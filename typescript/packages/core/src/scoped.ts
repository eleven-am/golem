import type {
  AliasedRawBuilder,
  CompiledQuery,
  JoinBuilder,
  Kysely,
  OperationNode,
  QueryCompiler,
  QueryCreator,
  RawBuilder,
  SelectQueryBuilder,
  Sql,
} from 'kysely';
import {
  PolicyDatamodel,
  SqlDialect,
  SqlNode,
  SqlRenderError,
  UNSUPPORTED_CONDITION_ERROR_NAME,
  createDatamodelSqlScope,
  postgresDialect,
  renderConstraintNode,
  sqliteDialect,
} from '@eleven-am/golem-policy';
import { AuthorizationProvider, isConditionalConstraint } from './authorization';
import { DatamodelField, DatamodelModel } from './datamodel';
import { GolemForbiddenError, GolemValidationError } from './errors';

export interface ScopedDatabase {
  [alias: string]: Record<string, any>;
}

export type ScopedBuilderEscape =
  | 'modifyFront'
  | 'modifyEnd'
  | 'innerJoin'
  | 'leftJoin'
  | 'rightJoin'
  | 'fullJoin'
  | 'crossJoin'
  | 'innerJoinLateral'
  | 'leftJoinLateral'
  | 'crossJoinLateral'
  | 'union'
  | 'unionAll'
  | 'intersect'
  | 'intersectAll'
  | 'except'
  | 'exceptAll'
  | 'withPlugin'
  | 'compile'
  | 'execute'
  | 'executeTakeFirst'
  | 'executeTakeFirstOrThrow'
  | 'stream'
  | 'explain';

export type ScopedSelectBuilder<O = {}> = Omit<
  SelectQueryBuilder<ScopedDatabase, string, O>,
  ScopedBuilderEscape
>;

export type ScopedJoinKind = 'inner' | 'left';

export type ScopedJoinCondition = (
  join: JoinBuilder<ScopedDatabase, string>,
) => JoinBuilder<ScopedDatabase, string>;

export interface CompiledScopedQuery {
  readonly sql: string;
  readonly parameters: readonly unknown[];
}

export interface ScopedStatement<O> {
  compile(): Promise<CompiledScopedQuery>;
  execute(): Promise<O[]>;
}

export type ScopedQueryCreator = Pick<QueryCreator<ScopedDatabase>, 'with' | 'selectFrom'>;

export type ScopedQueryBuilder<O> = (
  builder: ScopedSelectBuilder,
  creator: ScopedQueryCreator,
) => SelectQueryBuilder<ScopedDatabase, string, O>;

export interface ScopedQuery {
  join(
    kind: ScopedJoinKind,
    model: string,
    alias: string,
    on: ScopedJoinCondition,
  ): ScopedQuery;
  query<O>(build: ScopedQueryBuilder<O>): ScopedStatement<O>;
}

export interface ScopedRequest {
  context?: unknown;
  model: string;
  alias?: string;
}

export interface ScopedFieldPolicy {
  readonly access: 'conditional' | 'never';
  readonly dischargedByConstraint?: boolean;
  readonly condition?: unknown;
}

export interface ScopedHost {
  readonly models: readonly DatamodelModel[];
  readonly provider?: string;
  hiddenFields(model: string): ReadonlySet<string>;
  constraint(model: string): Promise<unknown>;
  fieldPolicy?(
    model: string,
    fields: readonly string[],
  ): Promise<ReadonlyMap<string, ScopedFieldPolicy>>;
  execute(model: string, sql: string, parameters: readonly unknown[]): Promise<unknown[]>;
}

export async function resolveScopedFieldPolicy(
  provider: AuthorizationProvider | undefined,
  model: string,
  fields: readonly string[],
  context: unknown,
): Promise<ReadonlyMap<string, ScopedFieldPolicy>> {
  const policies = new Map<string, ScopedFieldPolicy>();
  if (provider?.classifyFields === undefined || fields.length === 0) {
    return policies;
  }
  const classification = await provider.classifyFields('read', model, fields, context);
  for (const field of fields) {
    const entry = classification[field];
    if (entry === undefined || entry.access === 'always') {
      continue;
    }
    if (entry.access === 'never') {
      policies.set(field, { access: 'never' });
      continue;
    }
    const condition =
      provider.constrainField === undefined
        ? undefined
        : await provider.constrainField('read', model, field, context);
    policies.set(field, {
      access: 'conditional',
      dischargedByConstraint: entry.dischargedByConstraint === true,
      condition,
    });
  }
  return policies;
}

interface ScopedRootPlan {
  readonly model: string;
  readonly alias: string;
  readonly kind: ScopedJoinKind | 'root';
  readonly on?: ScopedJoinCondition;
}

export interface ScopedDialect {
  readonly policy: SqlDialect;
  readonly provider: 'sqlite' | 'postgres';
  createKysely(kysely: KyselyModule): Kysely<ScopedDatabase>;
  createCompiler(kysely: KyselyModule): QueryCompiler;
}

export type KyselyModule = typeof import('kysely');

let loaded: Promise<KyselyModule> | undefined;

async function loadKysely(): Promise<KyselyModule> {
  try {
    return await import('kysely');
  } catch (dynamic) {
    try {
      return require('kysely') as KyselyModule;
    } catch (required) {
      throw new Error(
        `golem could not load kysely, which compiles scoped queries: dynamic import failed with "${
          (dynamic as Error).message
        }" and require failed with "${
          (required as Error).message
        }". Kysely 0.29 is ESM only; under jest either run node with --experimental-vm-modules or transform kysely by setting transformIgnorePatterns to ['/node_modules/(?!kysely/)']`,
      );
    }
  }
}

export function kyselyModule(): Promise<KyselyModule> {
  if (loaded === undefined) {
    loaded = loadKysely();
  }
  return loaded;
}

const PHYSICAL_NAME_HINT =
  'regenerate the golem client so the datamodel carries physical names; falling back to the Prisma name would silently target the wrong table when the schema uses @map';

const PROVIDER_HINT =
  'regenerate the golem client so the datamodel carries the datasource provider; without it golem cannot tell which SQL dialect the connection speaks, and rendering the wrong one would either fail loudly or, worse, compile a predicate the engine reads differently';

function refuse(message: string): never {
  throw new GolemValidationError(message);
}

function refuseRead(message: string): never {
  throw new GolemForbiddenError(message);
}

function sqliteScopedDialect(): ScopedDialect {
  return {
    policy: sqliteDialect,
    provider: 'sqlite',
    createKysely: (kysely) =>
      new kysely.Kysely<ScopedDatabase>({
        dialect: {
          createAdapter: () => new kysely.SqliteAdapter(),
          createDriver: () => new kysely.DummyDriver(),
          createIntrospector: (db) => new kysely.SqliteIntrospector(db),
          createQueryCompiler: () => new kysely.SqliteQueryCompiler(),
        },
      }),
    createCompiler: (kysely) => new kysely.SqliteQueryCompiler(),
  };
}

function postgresScopedDialect(): ScopedDialect {
  return {
    policy: postgresDialect,
    provider: 'postgres',
    createKysely: (kysely) =>
      new kysely.Kysely<ScopedDatabase>({
        dialect: {
          createAdapter: () => new kysely.PostgresAdapter(),
          createDriver: () => new kysely.DummyDriver(),
          createIntrospector: (db) => new kysely.PostgresIntrospector(db),
          createQueryCompiler: () => new kysely.PostgresQueryCompiler(),
        },
      }),
    createCompiler: (kysely) => new kysely.PostgresQueryCompiler(),
  };
}

export function resolveScopedDialect(provider: string | undefined): ScopedDialect {
  if (provider === undefined || provider === '') {
    refuse(`a scoped query needs the datasource provider: ${PROVIDER_HINT}`);
  }
  if (provider === 'sqlite') {
    return sqliteScopedDialect();
  }
  if (provider === 'postgresql' || provider === 'postgres') {
    return postgresScopedDialect();
  }
  refuse(
    `a scoped query cannot be compiled for datasource provider "${provider}": golem renders policy predicates for sqlite and postgresql only`,
  );
}

export function sqlNodeToRaw(node: SqlNode, dialect: SqlDialect, sql: Sql): RawBuilder<unknown> {
  switch (node.kind) {
    case 'text':
      return sql.raw(node.text);
    case 'identifier':
      return sql.id(...node.path);
    case 'parameter':
      return sql.val(node.value);
    case 'sequence':
      return sql.join(
        node.nodes.map((child) => sqlNodeToRaw(child, dialect, sql)),
        sql.raw(''),
      );
    case 'dialectal':
      return sqlNodeToRaw(node.select(dialect), dialect, sql);
  }
}

function nodeKind(node: unknown): string {
  if (node && typeof node === 'object' && typeof (node as { kind?: unknown }).kind === 'string') {
    return (node as { kind: string }).kind;
  }
  return typeof node;
}

function walkNodes(root: unknown, visit: (node: OperationNode) => boolean): void {
  const seen = new Set<unknown>();
  const step = (value: unknown): void => {
    if (Array.isArray(value)) {
      value.forEach(step);
      return;
    }
    if (!value || typeof value !== 'object') {
      return;
    }
    if (seen.has(value)) {
      return;
    }
    seen.add(value);
    if (typeof (value as { kind?: unknown }).kind === 'string') {
      if (!visit(value as OperationNode)) {
        return;
      }
    }
    for (const child of Object.values(value as Record<string, unknown>)) {
      step(child);
    }
  };
  step(root);
}

function identifierName(node: unknown): string | undefined {
  if (node && typeof node === 'object' && nodeKind(node) === 'IdentifierNode') {
    const name = (node as { name?: unknown }).name;
    return typeof name === 'string' ? name : undefined;
  }
  return undefined;
}

function readProp(node: unknown, key: string): unknown {
  return node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined;
}

function tableIdentifier(node: unknown): { name?: string; schema?: string } {
  const table = readProp(node, 'table');
  return {
    name: identifierName(readProp(table, 'identifier')),
    schema: identifierName(readProp(table, 'schema')),
  };
}

const MODIFYING_KINDS = new Set([
  'InsertQueryNode',
  'UpdateQueryNode',
  'DeleteQueryNode',
  'MergeQueryNode',
]);

export interface ScopedValidationInput {
  readonly node: unknown;
  readonly roots: ReadonlyMap<string, unknown>;
  readonly rawNodes: ReadonlySet<unknown>;
  readonly physicalTables: ReadonlyMap<string, string>;
  readonly withheldColumns?: ReadonlyMap<string, ReadonlyMap<string, string>>;
}

function foldedName(name: string): string {
  return name.toLowerCase();
}

function commonTableNames(withNode: unknown): readonly (string | undefined)[] {
  const expressions = (readProp(withNode, 'expressions') ?? []) as readonly unknown[];
  return expressions.map((expression) =>
    tableIdentifier(readProp(readProp(expression, 'name'), 'table')).name,
  );
}

function walkScopedNodes(
  root: unknown,
  rawNodes: ReadonlySet<unknown>,
  visit: (node: OperationNode, visible: ReadonlySet<string>) => void,
): void {
  const seen = new WeakMap<object, Set<string>>();
  const descendWith = (withNode: unknown, outer: ReadonlySet<string>): ReadonlySet<string> => {
    const expressions = (readProp(withNode, 'expressions') ?? []) as readonly unknown[];
    const names = commonTableNames(withNode);
    const recursive = readProp(withNode, 'recursive') === true;
    const visible = new Set(outer);
    if (recursive) {
      for (const name of names) {
        if (name !== undefined) {
          visible.add(name);
        }
      }
    }
    expressions.forEach((expression, index) => {
      step(expression, new Set(visible));
      const name = names[index];
      if (!recursive && name !== undefined) {
        visible.add(name);
      }
    });
    return visible;
  };
  const step = (value: unknown, visible: ReadonlySet<string>): void => {
    if (Array.isArray(value)) {
      for (const entry of value) {
        step(entry, visible);
      }
      return;
    }
    if (!value || typeof value !== 'object') {
      return;
    }
    if (rawNodes.has(value)) {
      return;
    }
    const reached = [...visible].sort().join(' ');
    let scopes = seen.get(value);
    if (scopes === undefined) {
      scopes = new Set<string>();
      seen.set(value, scopes);
    }
    if (scopes.has(reached)) {
      return;
    }
    scopes.add(reached);
    if (typeof (value as { kind?: unknown }).kind === 'string') {
      visit(value as OperationNode, visible);
    }
    const withNode =
      nodeKind(value) === 'SelectQueryNode' ? readProp(value, 'with') : undefined;
    const inner =
      withNode === undefined || withNode === null ? visible : descendWith(withNode, visible);
    for (const [property, child] of Object.entries(value as Record<string, unknown>)) {
      if (property === 'with' && inner !== visible) {
        continue;
      }
      step(child, inner);
    }
  };
  step(root, new Set<string>());
}

function assertSingleSelect(input: ScopedValidationInput): void {
  if (nodeKind(input.node) !== 'SelectQueryNode') {
    refuse(
      `a scoped query must compile to a single SELECT, but the builder produced a ${nodeKind(input.node)}`,
    );
  }
}

function assertNoSetOperations(input: ScopedValidationInput): void {
  walkNodes(input.node, (node) => {
    if (nodeKind(node) === 'SetOperationNode') {
      const operator = (node as { operator?: unknown }).operator;
      refuse(
        `a scoped query must be a single SELECT, but it carries a set operation (${
          typeof operator === 'string' ? operator : 'union'
        }); the operand is not scoped by any policy predicate`,
      );
    }
    return true;
  });
  const setOperations = (input.node as { setOperations?: readonly unknown[] }).setOperations;
  if (setOperations && setOperations.length > 0) {
    refuse('a scoped query must be a single SELECT, but it carries a set operation');
  }
}

function unwrapAlias(node: unknown): unknown {
  return nodeKind(node) === 'AliasNode' ? readProp(node, 'node') : node;
}

function assertCommonTableExpressionNames(input: ScopedValidationInput): void {
  const aliases = new Map<string, string>();
  for (const alias of input.roots.keys()) {
    aliases.set(foldedName(alias), alias);
  }
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) !== 'CommonTableExpressionNameNode') {
      return true;
    }
    const name = tableIdentifier(readProp(node, 'table')).name;
    if (name === undefined) {
      return true;
    }
    const model = input.physicalTables.get(foldedName(name));
    if (model !== undefined) {
      refuse(
        `a scoped query may not name a common table expression "${name}", which is the physical table of model "${model}": the name would shadow the table its own body reads, which sqlite rejects as a circular reference and postgres silently resolves the other way`,
      );
    }
    const alias = aliases.get(foldedName(name));
    if (alias !== undefined) {
      refuse(
        `a scoped query may not name a common table expression "${name}", which is already the alias of the scoped root "${alias}": a read of that name would resolve to one or the other depending on where it stands`,
      );
    }
    return true;
  });
}

function assertScopedRootsIntact(input: ScopedValidationInput): void {
  const registered = new Set(input.roots.values());
  const declared = new Set(
    commonTableNames(readProp(input.node, 'with')).filter(
      (name): name is string => name !== undefined,
    ),
  );
  const namesDeclaredTable = (candidate: unknown): boolean => {
    const unwrapped = unwrapAlias(candidate);
    if (nodeKind(unwrapped) !== 'TableNode') {
      return false;
    }
    const name = tableIdentifier(unwrapped).name;
    return name !== undefined && declared.has(name);
  };
  const froms = (readProp(readProp(input.node, 'from'), 'froms') ?? []) as readonly OperationNode[];
  if (froms.length === 0) {
    refuse('a scoped query must read from a scoped root, but its FROM clause is empty');
  }
  for (const from of froms) {
    if (registered.has(from) || namesDeclaredTable(from)) {
      continue;
    }
    if (nodeKind(unwrapAlias(from)) === 'SelectQueryNode') {
      continue;
    }
    refuse(
      `the FROM clause of a scoped query must be a scoped root golem created, a subquery over one, or a common table expression it declares, but it carries a ${nodeKind(
        from,
      )} golem did not create; the policy predicate is not applied to it`,
    );
  }
  const joins = (readProp(input.node, 'joins') ?? []) as readonly OperationNode[];
  for (const join of joins) {
    const joined = readProp(join, 'table');
    if (registered.has(joined) || namesDeclaredTable(joined)) {
      continue;
    }
    refuse(
      `a scoped query may only join scoped roots golem created and the common table expressions it declares, but it joins a ${nodeKind(
        joined,
      )} golem did not create; the policy predicate is not applied to it`,
    );
  }
}

function assertNoTableRead(input: ScopedValidationInput): void {
  const registered = new Set(input.roots.values());
  const check = (candidate: unknown, clause: string, visible: ReadonlySet<string>): void => {
    if (registered.has(candidate)) {
      return;
    }
    const unwrapped = unwrapAlias(candidate);
    if (nodeKind(unwrapped) !== 'TableNode') {
      return;
    }
    const name = tableIdentifier(unwrapped).name;
    if (name !== undefined && visible.has(name)) {
      return;
    }
    refuse(
      `a scoped query may not read the table "${
        name ?? 'unknown'
      }" in a ${clause} clause; only the scoped roots golem created carry a policy predicate`,
    );
  };
  walkScopedNodes(input.node, input.rawNodes, (node, visible) => {
    if (nodeKind(node) === 'FromNode') {
      for (const from of (readProp(node, 'froms') ?? []) as readonly unknown[]) {
        check(from, 'FROM', visible);
      }
    }
    if (nodeKind(node) === 'JoinNode') {
      check(readProp(node, 'table'), 'JOIN', visible);
    }
  });
}

function collectDeclaredNames(input: ScopedValidationInput): Set<string> {
  const names = new Set<string>(input.roots.keys());
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) === 'AliasNode') {
      const alias = identifierName((node as { alias?: unknown }).alias);
      if (alias !== undefined) {
        names.add(alias);
      }
    }
    if (nodeKind(node) === 'CommonTableExpressionNameNode') {
      const name = tableIdentifier(readProp(node, 'table')).name;
      if (name !== undefined) {
        names.add(name);
      }
    }
    return true;
  });
  return names;
}

function assertNoForeignTable(input: ScopedValidationInput): void {
  const declared = collectDeclaredNames(input);
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) !== 'TableNode') {
      return true;
    }
    const { name, schema } = tableIdentifier(node);
    if (schema !== undefined) {
      refuse(
        `a scoped query may not name a schema, but it references schema "${schema}"; only the scoped roots golem created may be read`,
      );
    }
    if (name === undefined || !declared.has(name)) {
      refuse(
        `a scoped query may only reference the scoped roots golem created, but it references the table "${
          name ?? 'unknown'
        }", which carries no policy predicate`,
      );
    }
    return true;
  });
}

function assertNoForeignRaw(input: ScopedValidationInput): void {
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) === 'RawNode') {
      const fragments = (node as { sqlFragments?: readonly string[] }).sqlFragments ?? [];
      refuse(
        `a scoped query may not carry raw SQL golem did not create, but it carries the fragment ${JSON.stringify(
          fragments.join('?'),
        )}; raw SQL is not checked against any policy predicate`,
      );
    }
    return true;
  });
}

function assertNoDataModification(input: ScopedValidationInput): void {
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    const kind = nodeKind(node);
    if (MODIFYING_KINDS.has(kind)) {
      refuse(
        `a scoped query may only read, but it carries a ${kind}; on postgres a data-modifying common table expression is still a SELECT statement`,
      );
    }
    if (kind === 'CommonTableExpressionNode') {
      const expression = (node as { expression?: unknown }).expression;
      if (!input.rawNodes.has(expression) && nodeKind(expression) !== 'SelectQueryNode') {
        refuse(
          `a common table expression in a scoped query must be a SELECT, but "${
            tableIdentifier(readProp(readProp(node, 'name'), 'table')).name ?? 'unknown'
          }" is a ${nodeKind(expression)}`,
        );
      }
    }
    return true;
  });
}

function assertNoWithheldColumn(input: ScopedValidationInput): void {
  const withheldColumns = input.withheldColumns;
  if (withheldColumns === undefined || withheldColumns.size === 0) {
    return;
  }
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) !== 'ReferenceNode') {
      return true;
    }
    const alias = tableIdentifier(readProp(node, 'table')).name;
    if (alias === undefined) {
      return true;
    }
    const withheld = withheldColumns.get(alias);
    if (withheld === undefined) {
      return true;
    }
    const column = identifierName(readProp(readProp(node, 'column'), 'column'));
    if (column === undefined) {
      return true;
    }
    const reason = withheld.get(column);
    if (reason !== undefined) {
      refuseRead(`a scoped query may not reference "${alias}"."${column}": ${reason}`);
    }
    return true;
  });
}

export function validateScopedQuery(input: ScopedValidationInput): void {
  assertSingleSelect(input);
  assertNoSetOperations(input);
  assertNoDataModification(input);
  assertCommonTableExpressionNames(input);
  assertScopedRootsIntact(input);
  assertNoForeignRaw(input);
  assertNoTableRead(input);
  assertNoForeignTable(input);
  assertNoWithheldColumn(input);
}

function policyDatamodel(models: readonly DatamodelModel[]): PolicyDatamodel {
  return { models };
}

function resolveModel(models: readonly DatamodelModel[], name: string): DatamodelModel {
  const model = models.find((candidate) => candidate.name === name);
  if (model === undefined) {
    refuse(`a scoped query cannot be rooted at "${name}", which is not a model in the datamodel`);
  }
  return model;
}

const SQLITE_MASKABLE_TYPES = new Set(['String', 'Float', 'BigInt']);

function isUnsupported(error: unknown): boolean {
  return (
    error instanceof SqlRenderError ||
    (error as Error | undefined)?.name === UNSUPPORTED_CONDITION_ERROR_NAME
  );
}

interface ScopedColumn {
  readonly name: string;
  readonly dbName: string;
  readonly mask?: RawBuilder<unknown>;
}

interface ScopedProjection {
  readonly columns: readonly ScopedColumn[];
  readonly withheld: ReadonlyMap<string, string>;
}

type ScopedSqlScope = ReturnType<typeof createDatamodelSqlScope>;

function renderedMask(
  field: DatamodelField,
  policy: ScopedFieldPolicy,
  dialect: ScopedDialect,
  sql: Sql,
  scope: ScopedSqlScope,
): RawBuilder<unknown> | string {
  if (!isConditionalConstraint(policy.condition)) {
    return 'its readability is conditional and the authorization provider hands golem no condition for it, and an absent condition must not be read as a grant';
  }
  if (
    dialect.provider === 'sqlite' &&
    (field.kind !== 'scalar' || !SQLITE_MASKABLE_TYPES.has(field.type))
  ) {
    return `its readability is conditional and masking a ${field.type} column strips the declared type sqlite hands Prisma to decode the value by`;
  }
  try {
    return sqlNodeToRaw(
      renderConstraintNode(policy.condition, { scope, absent: 'deny-all' }),
      dialect.policy,
      sql,
    );
  } catch (error) {
    if (!isUnsupported(error)) {
      throw error;
    }
    return `its readability is conditional and golem cannot render the condition as SQL: ${
      (error as Error).message
    }`;
  }
}

function projectedColumns(
  model: DatamodelModel,
  hidden: ReadonlySet<string>,
  policies: ReadonlyMap<string, ScopedFieldPolicy>,
  dialect: ScopedDialect,
  sql: Sql,
  scope: ScopedSqlScope,
): ScopedProjection {
  const columns: ScopedColumn[] = [];
  const withheld = new Map<string, string>();
  for (const field of model.fields) {
    if (field.kind === 'object') {
      continue;
    }
    const dbName = field.dbName ?? field.name;
    if (hidden.has(field.name)) {
      withheld.set(field.name, 'it is hidden on the model');
      continue;
    }
    const policy = policies.get(field.name);
    if (policy === undefined) {
      columns.push({ name: field.name, dbName });
      continue;
    }
    if (policy.access === 'never') {
      withheld.set(field.name, 'the caller may not read it');
      continue;
    }
    const mask = renderedMask(field, policy, dialect, sql, scope);
    if (typeof mask !== 'string') {
      columns.push({ name: field.name, dbName, mask });
      continue;
    }
    if (policy.dischargedByConstraint === true) {
      columns.push({ name: field.name, dbName });
      continue;
    }
    withheld.set(field.name, mask);
  }
  if (columns.length === 0) {
    refuse(
      `a scoped query on "${model.name}" would expose no columns; every column of the model is either a relation, hidden, or one the caller may not read`,
    );
  }
  return { columns, withheld };
}

function physicalTable(model: DatamodelModel): string {
  if (model.dbName == null) {
    refuse(`model "${model.name}" carries no physical table name: ${PHYSICAL_NAME_HINT}`);
  }
  return model.dbName;
}

interface ScopedRoot {
  readonly root: AliasedRawBuilder<unknown, string>;
  readonly withheld: ReadonlyMap<string, string>;
}

function buildScopedRoot(
  host: ScopedHost,
  dialect: ScopedDialect,
  sql: Sql,
  model: DatamodelModel,
  alias: string,
  innerAlias: string,
  constraint: unknown,
  policies: ReadonlyMap<string, ScopedFieldPolicy>,
): ScopedRoot {
  const scope = createDatamodelSqlScope({
    datamodel: policyDatamodel(host.models),
    model: model.name,
    alias: innerAlias,
  });
  const predicate = renderConstraintNode(constraint, {
    scope,
    absent: constraint === null ? 'deny-all' : 'grant-all',
  });
  const projected = projectedColumns(
    model,
    host.hiddenFields(model.name),
    policies,
    dialect,
    sql,
    scope,
  );
  const projection = sql.join(
    projected.columns.map((column) =>
      column.mask === undefined
        ? sql`${sql.id(innerAlias, column.dbName)} as ${sql.id(column.name)}`
        : sql`case when (${column.mask}) then ${sql.id(innerAlias, column.dbName)} else null end as ${sql.id(
            column.name,
          )}`,
    ),
    sql.raw(', '),
  );
  const root = sql`(select ${projection} from ${sql.id(physicalTable(model))} as ${sql.id(
    innerAlias,
  )} where ${sqlNodeToRaw(predicate, dialect.policy, sql)})`.as(alias) as AliasedRawBuilder<
    unknown,
    string
  >;
  return { root, withheld: projected.withheld };
}

class ScopedStatementImpl<O> implements ScopedStatement<O> {
  constructor(
    private readonly host: ScopedHost,
    private readonly plans: readonly ScopedRootPlan[],
    private readonly build: ScopedQueryBuilder<O>,
  ) {}

  private async fieldPolicies(
    model: DatamodelModel,
  ): Promise<ReadonlyMap<string, ScopedFieldPolicy>> {
    if (this.host.fieldPolicy === undefined) {
      return new Map();
    }
    const hidden = this.host.hiddenFields(model.name);
    const fields = model.fields
      .filter((field) => field.kind !== 'object' && !hidden.has(field.name))
      .map((field) => field.name);
    if (fields.length === 0) {
      return new Map();
    }
    return this.host.fieldPolicy(model.name, fields);
  }

  private async prepare(): Promise<CompiledScopedQuery> {
    const kysely = await kyselyModule();
    const dialect = resolveScopedDialect(this.host.provider);
    const db = dialect.createKysely(kysely);
    const sql = kysely.sql;
    const claimed = new Set<string>();
    const built: AliasedRawBuilder<unknown, string>[] = [];
    const withheldColumns = new Map<string, ReadonlyMap<string, string>>();
    for (const [index, plan] of this.plans.entries()) {
      if (plan.alias.length === 0) {
        refuse('a scoped root needs a non-empty alias');
      }
      if (claimed.has(plan.alias)) {
        refuse(
          `a scoped query cannot declare "${plan.alias}" twice; give each scoped root its own alias`,
        );
      }
      claimed.add(plan.alias);
      const model = resolveModel(this.host.models, plan.model);
      const constraint = await this.host.constraint(plan.model);
      const policies = await this.fieldPolicies(model);
      const scoped = buildScopedRoot(
        this.host,
        dialect,
        sql,
        model,
        plan.alias,
        `g${index}`,
        constraint,
        policies,
      );
      built.push(scoped.root);
      if (scoped.withheld.size > 0) {
        withheldColumns.set(plan.alias, scoped.withheld);
      }
    }
    let rooted = db.selectFrom(built[0]!) as unknown as SelectQueryBuilder<
      ScopedDatabase,
      string,
      {}
    >;
    for (const [index, plan] of this.plans.entries()) {
      if (index === 0) {
        continue;
      }
      const joiner = plan.kind === 'left' ? 'leftJoin' : 'innerJoin';
      rooted = (rooted as unknown as Record<string, (...args: unknown[]) => unknown>)[joiner]!(
        built[index]!,
        plan.on,
      ) as SelectQueryBuilder<ScopedDatabase, string, {}>;
    }
    const rootNode = rooted.toOperationNode();
    const rawNodes = new Set<unknown>();
    walkNodes(rootNode, (node) => {
      if (nodeKind(node) === 'RawNode') {
        rawNodes.add(node);
        return false;
      }
      return true;
    });
    const roots = new Map<string, unknown>();
    for (const from of (readProp(readProp(rootNode, 'from'), 'froms') ?? []) as readonly unknown[]) {
      roots.set(identifierName(readProp(from, 'alias')) ?? '', from);
    }
    for (const join of (readProp(rootNode, 'joins') ?? []) as readonly unknown[]) {
      const joined = readProp(join, 'table');
      roots.set(identifierName(readProp(joined, 'alias')) ?? '', joined);
    }
    const shaped = this.build(rooted as ScopedSelectBuilder, db as ScopedQueryCreator);
    if (
      !shaped ||
      typeof (shaped as unknown as { toOperationNode?: unknown }).toOperationNode !== 'function'
    ) {
      refuse(
        'a scoped query must return the query builder golem handed it; the callback returned something that is not a Kysely query builder',
      );
    }
    const node = shaped.toOperationNode() as unknown as OperationNode;
    const physicalTables = new Map<string, string>();
    for (const model of this.host.models) {
      if (model.dbName != null) {
        physicalTables.set(foldedName(model.dbName), model.name);
      }
    }
    validateScopedQuery({ node, roots, rawNodes, physicalTables, withheldColumns });
    const compiled: CompiledQuery = dialect
      .createCompiler(kysely)
      .compileQuery(node as never, kysely.createQueryId());
    return { sql: compiled.sql, parameters: compiled.parameters };
  }

  async compile(): Promise<CompiledScopedQuery> {
    return this.prepare();
  }

  async execute(): Promise<O[]> {
    const compiled = await this.prepare();
    const rows = await this.host.execute(
      this.plans[0]!.model,
      compiled.sql,
      compiled.parameters,
    );
    return rows as O[];
  }
}

class ScopedQueryImpl implements ScopedQuery {
  constructor(
    private readonly host: ScopedHost,
    private readonly plans: readonly ScopedRootPlan[],
  ) {}

  join(
    kind: ScopedJoinKind,
    model: string,
    alias: string,
    on: ScopedJoinCondition,
  ): ScopedQuery {
    return new ScopedQueryImpl(this.host, [...this.plans, { model, alias, kind, on }]);
  }

  query<O>(build: ScopedQueryBuilder<O>): ScopedStatement<O> {
    return new ScopedStatementImpl(this.host, this.plans, build);
  }
}

export function createScopedQuery(host: ScopedHost, request: ScopedRequest): ScopedQuery {
  const alias = request.alias ?? request.model;
  return new ScopedQueryImpl(host, [{ model: request.model, alias, kind: 'root' }]);
}

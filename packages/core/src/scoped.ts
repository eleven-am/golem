import type {
  AliasedRawBuilder,
  CompiledQuery,
  JoinBuilder,
  Kysely,
  OperationNode,
  QueryCompiler,
  RawBuilder,
  SelectQueryBuilder,
  Sql,
} from 'kysely';
import {
  PolicyDatamodel,
  SqlDialect,
  SqlNode,
  createDatamodelSqlScope,
  postgresDialect,
  renderConstraintNode,
  sqliteDialect,
} from '@eleven-am/golem-policy';
import { DatamodelModel } from './datamodel';
import { GolemValidationError } from './errors';

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

export interface ScopedQuery {
  join(
    kind: ScopedJoinKind,
    model: string,
    alias: string,
    on: ScopedJoinCondition,
  ): ScopedQuery;
  query<O>(
    build: (
      builder: ScopedSelectBuilder,
    ) => SelectQueryBuilder<ScopedDatabase, string, O>,
  ): ScopedStatement<O>;
}

export interface ScopedRequest {
  context?: unknown;
  model: string;
  alias?: string;
}

export interface ScopedHost {
  readonly models: readonly DatamodelModel[];
  readonly provider?: string;
  hiddenFields(model: string): ReadonlySet<string>;
  constraint(model: string): Promise<unknown>;
  execute(model: string, sql: string, parameters: readonly unknown[]): Promise<unknown[]>;
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

function assertScopedRootsIntact(input: ScopedValidationInput): void {
  const registered = new Set(input.roots.values());
  const froms = (readProp(readProp(input.node, 'from'), 'froms') ?? []) as readonly OperationNode[];
  if (froms.length === 0) {
    refuse('a scoped query must read from a scoped root, but its FROM clause is empty');
  }
  for (const from of froms) {
    if (!registered.has(from)) {
      refuse(
        `the FROM clause of a scoped query must be a scoped root golem created, but it carries a ${nodeKind(
          from,
        )} golem did not create; the policy predicate is not applied to it`,
      );
    }
  }
  const joins = (readProp(input.node, 'joins') ?? []) as readonly OperationNode[];
  for (const join of joins) {
    const joined = readProp(join, 'table');
    if (!registered.has(joined)) {
      refuse(
        `a scoped query may only join scoped roots golem created, but it joins a ${nodeKind(
          joined,
        )} golem did not create; the policy predicate is not applied to it`,
      );
    }
  }
  if (readProp(input.node, 'with') !== undefined) {
    refuse(
      'a scoped query cannot carry a common table expression: a CTE named after the model would shadow the table its own body reads, which sqlite rejects as a circular reference and postgres silently resolves the other way',
    );
  }
}

function assertNoTableRead(input: ScopedValidationInput): void {
  const registered = new Set(input.roots.values());
  const check = (candidate: unknown, clause: string): void => {
    if (registered.has(candidate)) {
      return;
    }
    const unwrapped = nodeKind(candidate) === 'AliasNode' ? readProp(candidate, 'node') : candidate;
    if (nodeKind(unwrapped) === 'TableNode') {
      refuse(
        `a scoped query may not read the table "${
          tableIdentifier(unwrapped).name ?? 'unknown'
        }" in a ${clause} clause; only the scoped roots golem created carry a policy predicate`,
      );
    }
  };
  walkNodes(input.node, (node) => {
    if (input.rawNodes.has(node)) {
      return false;
    }
    if (nodeKind(node) === 'FromNode') {
      for (const from of (readProp(node, 'froms') ?? []) as readonly unknown[]) {
        check(from, 'FROM');
      }
    }
    if (nodeKind(node) === 'JoinNode') {
      check(readProp(node, 'table'), 'JOIN');
    }
    return true;
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

export function validateScopedQuery(input: ScopedValidationInput): void {
  assertSingleSelect(input);
  assertNoSetOperations(input);
  assertNoDataModification(input);
  assertScopedRootsIntact(input);
  assertNoForeignRaw(input);
  assertNoTableRead(input);
  assertNoForeignTable(input);
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

function projectedColumns(
  model: DatamodelModel,
  hidden: ReadonlySet<string>,
): readonly { name: string; dbName: string }[] {
  const columns = model.fields
    .filter((field) => field.kind !== 'object' && !hidden.has(field.name))
    .map((field) => ({ name: field.name, dbName: field.dbName ?? field.name }));
  if (columns.length === 0) {
    refuse(
      `a scoped query on "${model.name}" would expose no columns; every column of the model is either a relation or hidden`,
    );
  }
  return columns;
}

function physicalTable(model: DatamodelModel): string {
  if (model.dbName == null) {
    refuse(`model "${model.name}" carries no physical table name: ${PHYSICAL_NAME_HINT}`);
  }
  return model.dbName;
}

function buildScopedRoot(
  host: ScopedHost,
  dialect: ScopedDialect,
  sql: Sql,
  model: DatamodelModel,
  alias: string,
  innerAlias: string,
  constraint: unknown,
): AliasedRawBuilder<unknown, string> {
  const scope = createDatamodelSqlScope({
    datamodel: policyDatamodel(host.models),
    model: model.name,
    alias: innerAlias,
  });
  const predicate = renderConstraintNode(constraint, {
    scope,
    absent: constraint === null ? 'deny-all' : 'grant-all',
  });
  const columns = projectedColumns(model, host.hiddenFields(model.name));
  const projection = sql.join(
    columns.map(
      (column) => sql`${sql.id(innerAlias, column.dbName)} as ${sql.id(column.name)}`,
    ),
    sql.raw(', '),
  );
  return sql`(select ${projection} from ${sql.id(physicalTable(model))} as ${sql.id(
    innerAlias,
  )} where ${sqlNodeToRaw(predicate, dialect.policy, sql)})`.as(alias) as AliasedRawBuilder<
    unknown,
    string
  >;
}

class ScopedStatementImpl<O> implements ScopedStatement<O> {
  constructor(
    private readonly host: ScopedHost,
    private readonly plans: readonly ScopedRootPlan[],
    private readonly build: (
      builder: ScopedSelectBuilder,
    ) => SelectQueryBuilder<ScopedDatabase, string, O>,
  ) {}

  private async prepare(): Promise<CompiledScopedQuery> {
    const kysely = await kyselyModule();
    const dialect = resolveScopedDialect(this.host.provider);
    const db = dialect.createKysely(kysely);
    const sql = kysely.sql;
    const claimed = new Set<string>();
    const built: AliasedRawBuilder<unknown, string>[] = [];
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
      built.push(
        buildScopedRoot(this.host, dialect, sql, model, plan.alias, `g${index}`, constraint),
      );
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
    const shaped = this.build(rooted as ScopedSelectBuilder);
    if (
      !shaped ||
      typeof (shaped as unknown as { toOperationNode?: unknown }).toOperationNode !== 'function'
    ) {
      refuse(
        'a scoped query must return the query builder golem handed it; the callback returned something that is not a Kysely query builder',
      );
    }
    const node = shaped.toOperationNode() as unknown as OperationNode;
    validateScopedQuery({ node, roots, rawNodes });
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

  query<O>(
    build: (
      builder: ScopedSelectBuilder,
    ) => SelectQueryBuilder<ScopedDatabase, string, O>,
  ): ScopedStatement<O> {
    return new ScopedStatementImpl(this.host, this.plans, build);
  }
}

export function createScopedQuery(host: ScopedHost, request: ScopedRequest): ScopedQuery {
  const alias = request.alias ?? request.model;
  return new ScopedQueryImpl(host, [{ model: request.model, alias, kind: 'root' }]);
}

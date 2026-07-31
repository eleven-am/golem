import type { AliasedRawBuilder, RawBuilder, SelectQueryBuilder } from 'kysely';
import {
  UNSUPPORTED_CONDITION_ERROR_NAME,
  SqlRenderError,
  createDatamodelSqlScope,
  renderConstraintNode,
} from '@eleven-am/golem-policy';
import { mergeConstraint } from './authorization';
import {
  CompiledReadNestedProjection,
  CompiledReadScalarKind,
  DecimalConstructor,
  prismaDecimal,
} from './compiled-read-decode';
import { DatamodelField, DatamodelModel } from './datamodel';
import { ModelMetadataIndex } from './model-meta';
import { PreparedReadTree } from './readtree';
import {
  ScopedDatabase,
  ScopedDialect,
  KyselyModule,
  kyselyModule,
  resolveScopedDialect,
  sqlNodeToRaw,
} from './scoped';

const ROOT_ALIAS = 't0';

const COMPILABLE_PROVIDERS = new Set(['sqlite', 'postgresql', 'postgres']);

const RELATION_ENTRY_KEYS = new Set(['where', 'select', 'include', 'omit']);

const PAGING_ENTRY_REASONS: Readonly<Record<string, CompiledReadFallbackReason>> = {
  take: 'take',
  skip: 'take',
  orderBy: 'orderBy',
  cursor: 'cursor',
  distinct: 'distinct',
};

export type CompiledReadOperation = 'findOne' | 'findMany' | 'aggregate' | 'groupBy';

export type CompiledReadFallbackReason =
  | 'client'
  | 'provider'
  | 'relation'
  | 'projection'
  | 'cursor'
  | 'decoder'
  | 'distinct'
  | 'where'
  | 'orderBy'
  | 'take'
  | 'measure'
  | 'group'
  | 'having';

export interface CompiledReadColumn {
  readonly name: string;
  readonly dbName: string;
}

export interface CompiledReadEvent {
  readonly model: string;
  readonly operation: CompiledReadOperation;
  readonly outcome: 'compiled' | 'fallback';
  readonly reason?: CompiledReadFallbackReason;
  readonly detail?: string;
  readonly sql?: string;
}

export interface CompiledReadInput {
  readonly model: DatamodelModel;
  readonly models: readonly DatamodelModel[];
  readonly metadata: ModelMetadataIndex;
  readonly prepared: PreparedReadTree;
  readonly provider?: string;
  readonly where?: unknown;
  readonly constraint?: unknown;
  readonly orderBy?: unknown;
  readonly take?: number;
  readonly skip?: number;
  readonly cursor?: unknown;
  readonly distinct?: unknown;
  readonly single?: boolean;
}

export interface CompiledReadStatement {
  readonly kind: 'compiled';
  readonly sql: string;
  readonly parameters: readonly unknown[];
  readonly reversed: boolean;
  readonly columns: readonly CompiledReadColumn[];
  readonly relations: readonly CompiledReadNestedProjection[];
  readonly decimal: DecimalConstructor | null;
}

export interface CompiledReadFallback {
  readonly kind: 'fallback';
  readonly reason: CompiledReadFallbackReason;
  readonly detail: string;
}

export type CompiledReadPlan = CompiledReadStatement | CompiledReadFallback;

export interface JsonAggregationHelpers {
  jsonArrayFrom(expression: unknown): RawBuilder<unknown>;
  jsonObjectFrom(expression: unknown): RawBuilder<unknown>;
}

interface OrderTerm {
  readonly dbName: string;
  readonly direction: 'asc' | 'desc';
}

interface Projection {
  readonly select?: Record<string, unknown>;
  readonly include?: Record<string, unknown>;
  readonly omit?: Record<string, unknown>;
}

interface PlannedColumn extends CompiledReadColumn {
  readonly kind?: CompiledReadScalarKind;
}

interface Correlation {
  readonly child: string;
  readonly parent: string;
}

interface PlannedRelation {
  readonly name: string;
  readonly list: boolean;
  readonly table: string;
  readonly alias: string;
  readonly correlation: readonly Correlation[];
  readonly where?: unknown;
  readonly model: DatamodelModel;
  readonly node: PlannedNode;
}

interface PlannedNode {
  readonly columns: readonly PlannedColumn[];
  readonly relations: readonly PlannedRelation[];
}

interface PlanContext {
  readonly models: readonly DatamodelModel[];
  readonly metadata: ModelMetadataIndex;
  aliases: number;
}

const helpers = new Map<string, Promise<JsonAggregationHelpers>>();

async function loadHelpers(provider: 'sqlite' | 'postgres'): Promise<JsonAggregationHelpers> {
  const specifier = provider === 'sqlite' ? 'kysely/helpers/sqlite' : 'kysely/helpers/postgres';
  try {
    const module =
      provider === 'sqlite'
        ? await import('kysely/helpers/sqlite')
        : await import('kysely/helpers/postgres');
    return module as unknown as JsonAggregationHelpers;
  } catch (dynamic) {
    try {
      return require(specifier) as JsonAggregationHelpers;
    } catch (required) {
      throw new Error(
        `golem could not load ${specifier}, which aggregates a relation into JSON: dynamic import failed with "${
          (dynamic as Error).message
        }" and require failed with "${
          (required as Error).message
        }". Kysely 0.29 is ESM only; under jest either run node with --experimental-vm-modules or transform kysely by setting transformIgnorePatterns to ['/node_modules/(?!kysely/)']`,
      );
    }
  }
}

export function jsonAggregationHelpers(
  provider: 'sqlite' | 'postgres',
): Promise<JsonAggregationHelpers> {
  const loaded = helpers.get(provider);
  if (loaded !== undefined) {
    return loaded;
  }
  const loading = loadHelpers(provider);
  helpers.set(provider, loading);
  return loading;
}

function fallback(reason: CompiledReadFallbackReason, detail: string): CompiledReadFallback {
  return { kind: 'fallback', reason, detail };
}

function isFallback(value: unknown): value is CompiledReadFallback {
  return (value as CompiledReadFallback | undefined)?.kind === 'fallback';
}

function isUnsupported(error: unknown): boolean {
  return (
    error instanceof SqlRenderError ||
    (error as Error | undefined)?.name === UNSUPPORTED_CONDITION_ERROR_NAME
  );
}

function describePath(path: readonly string[]): string {
  return path.length === 0 ? 'the read' : path.join('.');
}

function nestedScalarKind(field: DatamodelField): CompiledReadScalarKind | undefined {
  if (field.kind === 'enum') {
    return 'plain';
  }
  switch (field.type) {
    case 'String':
    case 'Int':
    case 'Float':
      return 'plain';
    case 'BigInt':
      return 'bigint';
    case 'Decimal':
      return 'decimal';
    case 'DateTime':
      return 'datetime';
    case 'Boolean':
      return 'boolean';
    default:
      return undefined;
  }
}

function scalarColumn(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  name: string,
  nested: boolean,
): PlannedColumn | CompiledReadFallback {
  const field = metadata.get(model.name)?.fieldsByName.get(name);
  if (field === undefined) {
    return fallback(
      'projection',
      `${model.name}.${name} is not a field of the datamodel golem compiled the read against`,
    );
  }
  if (field.kind === 'object') {
    return fallback('relation', `${model.name}.${name} is a relation`);
  }
  if (field.isList) {
    return fallback('projection', `${model.name}.${name} is a list column`);
  }
  if (field.dbName == null) {
    return fallback(
      'projection',
      `${model.name}.${name} carries no physical column name; regenerate the golem client`,
    );
  }
  if (!nested) {
    return { name, dbName: field.dbName };
  }
  const kind = nestedScalarKind(field);
  if (kind === undefined) {
    return fallback(
      'projection',
      `${model.name}.${name} is a ${field.type} read through a relation, and golem does not carry ${field.type} through a JSON aggregate`,
    );
  }
  return { name, dbName: field.dbName, kind };
}

function correlate(
  metadata: ModelMetadataIndex,
  parent: DatamodelModel,
  target: DatamodelModel,
  field: DatamodelField,
): readonly Correlation[] | CompiledReadFallback {
  const owning =
    (field.relationFromFields?.length ?? 0) > 0
      ? { holder: parent, referenced: target, field }
      : undefined;
  const candidates = owning
    ? []
    : (metadata.get(target.name)?.relations ?? []).filter(
        (candidate) =>
          candidate.type === parent.name &&
          candidate.relationName === field.relationName &&
          (candidate.relationFromFields?.length ?? 0) > 0,
      );
  if (owning === undefined && candidates.length === 0) {
    return fallback(
      'relation',
      `${parent.name}.${field.name} carries no foreign key golem can correlate a subquery on`,
    );
  }
  if (candidates.length > 1) {
    return fallback(
      'relation',
      `${parent.name}.${field.name} matches ${candidates.length} foreign keys on ${target.name}, and golem cannot tell which one holds it`,
    );
  }
  const back = candidates[0];
  const source = owning?.field ?? back!;
  const from = source.relationFromFields ?? [];
  const to = source.relationToFields ?? [];
  if (from.length === 0 || from.length !== to.length) {
    return fallback(
      'relation',
      `${parent.name}.${field.name} carries ${from.length} foreign key columns against ${to.length} referenced columns`,
    );
  }
  const holder = owning ? parent : target;
  const referenced = owning ? target : parent;
  const pairs: Correlation[] = [];
  for (const [index, name] of from.entries()) {
    const holderColumn = metadata.get(holder.name)?.fieldsByName.get(name)?.dbName;
    const referencedColumn = metadata.get(referenced.name)?.fieldsByName.get(to[index]!)?.dbName;
    if (holderColumn == null || referencedColumn == null) {
      return fallback(
        'relation',
        `${parent.name}.${field.name} correlates on columns golem has no physical name for; regenerate the golem client`,
      );
    }
    pairs.push(
      owning
        ? { child: referencedColumn, parent: holderColumn }
        : { child: holderColumn, parent: referencedColumn },
    );
  }
  return pairs;
}

function planRelation(
  context: PlanContext,
  parent: DatamodelModel,
  field: DatamodelField,
  entry: unknown,
  path: readonly string[],
): PlannedRelation | CompiledReadFallback {
  const target = context.metadata.get(field.type)?.model;
  if (target === undefined) {
    return fallback(
      'relation',
      `${parent.name}.${field.name} points at ${field.type}, which is not a model in the datamodel`,
    );
  }
  if (target.dbName == null) {
    return fallback(
      'relation',
      `model ${target.name} carries no physical table name; regenerate the golem client`,
    );
  }
  if (entry !== true && (!entry || typeof entry !== 'object' || Array.isArray(entry))) {
    return fallback(
      'relation',
      `${describePath(path)} is read with ${JSON.stringify(entry)}`,
    );
  }
  const projection = (entry === true ? {} : entry) as Record<string, unknown>;
  if (!field.isList && projection.where !== undefined) {
    return fallback(
      'relation',
      `${describePath(path)} is a to-one narrowed by a where, which Prisma rejects rather than filtering`,
    );
  }
  for (const [key, value] of Object.entries(projection)) {
    if (value === undefined || RELATION_ENTRY_KEYS.has(key)) {
      continue;
    }
    const reason = PAGING_ENTRY_REASONS[key];
    return fallback(
      reason ?? 'relation',
      `${describePath(path)} is read with ${key}`,
    );
  }
  const correlation = correlate(context.metadata, parent, target, field);
  if (isFallback(correlation)) {
    return correlation;
  }
  const alias = `t${context.aliases++}`;
  const node = planNode(context, target, projection as Projection, path, true);
  if (isFallback(node)) {
    return node;
  }
  return {
    name: field.name,
    list: field.isList,
    table: target.dbName,
    alias,
    correlation,
    where: projection.where,
    model: target,
    node,
  };
}

function planNode(
  context: PlanContext,
  model: DatamodelModel,
  projection: Projection,
  path: readonly string[],
  nested: boolean,
): PlannedNode | CompiledReadFallback {
  const columns: PlannedColumn[] = [];
  const relations: PlannedRelation[] = [];
  const fields = context.metadata.get(model.name);
  if (fields === undefined) {
    return fallback(
      'projection',
      `${model.name} is not a model of the datamodel golem compiled the read against`,
    );
  }
  if (projection.select !== undefined && projection.include !== undefined) {
    return fallback(
      'projection',
      `${describePath(path)} is read with select and include together`,
    );
  }
  if (projection.select !== undefined) {
    for (const [name, value] of Object.entries(projection.select)) {
      if (value === false || value === undefined) {
        continue;
      }
      const field = fields.fieldsByName.get(name);
      if (field?.kind === 'object') {
        const relation = planRelation(context, model, field, value, [...path, name]);
        if (isFallback(relation)) {
          return relation;
        }
        relations.push(relation);
        continue;
      }
      if (value !== true) {
        return fallback(
          'projection',
          `${model.name}.${name} is selected as a nested projection but is not a relation`,
        );
      }
      const column = scalarColumn(model, context.metadata, name, nested);
      if (isFallback(column)) {
        return column;
      }
      columns.push(column);
    }
  } else {
    for (const field of fields.scalarFields) {
      if (projection.omit?.[field.name] === true) {
        continue;
      }
      const column = scalarColumn(model, context.metadata, field.name, nested);
      if (isFallback(column)) {
        return column;
      }
      columns.push(column);
    }
    for (const [name, value] of Object.entries(projection.include ?? {})) {
      if (value === false || value === undefined) {
        continue;
      }
      const field = fields.fieldsByName.get(name);
      if (field?.kind !== 'object') {
        return fallback(
          'projection',
          `${model.name}.${name} is included but is not a relation of the model`,
        );
      }
      const relation = planRelation(context, model, field, value, [...path, name]);
      if (isFallback(relation)) {
        return relation;
      }
      relations.push(relation);
    }
  }
  if (columns.length === 0 && relations.length === 0) {
    return fallback('projection', `${describePath(path)} is read with an empty projection`);
  }
  return { columns, relations };
}

function nestedProjection(relation: PlannedRelation): CompiledReadNestedProjection {
  return {
    name: relation.name,
    list: relation.list,
    fields: relation.node.columns.map((column) => ({
      name: column.name,
      kind: column.kind!,
    })),
    relations: relation.node.relations.map(nestedProjection),
  };
}

const SQLITE_JSON_OBJECT_ENTRIES = 63;

function tooWideForSqlite(relations: readonly PlannedRelation[]): PlannedRelation | undefined {
  for (const relation of relations) {
    if (relation.node.columns.length + relation.node.relations.length > SQLITE_JSON_OBJECT_ENTRIES) {
      return relation;
    }
    const nested = tooWideForSqlite(relation.node.relations);
    if (nested !== undefined) {
      return nested;
    }
  }
  return undefined;
}

function relationsNeedDecimal(relations: readonly PlannedRelation[]): boolean {
  return relations.some(
    (relation) =>
      relation.node.columns.some((column) => column.kind === 'decimal') ||
      relationsNeedDecimal(relation.node.relations),
  );
}

export function orderTerms(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  orderBy: unknown,
): readonly OrderTerm[] | CompiledReadFallback {
  if (orderBy === undefined || orderBy === null) {
    return [];
  }
  const entries = Array.isArray(orderBy) ? orderBy : [orderBy];
  const terms: OrderTerm[] = [];
  for (const entry of entries) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      return fallback('orderBy', `${model.name} is ordered by ${JSON.stringify(entry)}`);
    }
    for (const [name, direction] of Object.entries(entry as Record<string, unknown>)) {
      if (direction === undefined) {
        continue;
      }
      if (direction !== 'asc' && direction !== 'desc') {
        return fallback(
          'orderBy',
          `${model.name}.${name} is ordered by ${JSON.stringify(direction)}`,
        );
      }
      const column = scalarColumn(model, metadata, name, false);
      if (isFallback(column)) {
        return { ...column, reason: 'orderBy' };
      }
      terms.push({ dbName: column.dbName, direction });
    }
  }
  return terms;
}

function pageBounds(
  model: DatamodelModel,
  take: number | undefined,
  skip: number | undefined,
  ordered: boolean,
): { limit?: number; offset?: number; reversed: boolean } | CompiledReadFallback {
  if (take !== undefined && !Number.isInteger(take)) {
    return fallback('take', `take ${take} on ${model.name} is not an integer`);
  }
  if (skip !== undefined && (!Number.isInteger(skip) || skip < 0)) {
    return fallback('take', `skip ${skip} on ${model.name} is not a non-negative integer`);
  }
  const reversed = take !== undefined && take < 0;
  if (reversed && !ordered) {
    return fallback(
      'take',
      `take ${take} on ${model.name} walks backwards from an order the query does not define`,
    );
  }
  return {
    limit: take === undefined ? undefined : Math.abs(take),
    offset: skip,
    reversed,
  };
}

class NestedBuilder {
  constructor(
    private readonly kysely: KyselyModule,
    private readonly dialect: ScopedDialect,
    private readonly db: ReturnType<ScopedDialect['createKysely']>,
    private readonly aggregation: JsonAggregationHelpers,
    private readonly models: readonly DatamodelModel[],
  ) {}

  private predicate(model: DatamodelModel, alias: string, where: unknown): RawBuilder<unknown> {
    return sqlNodeToRaw(
      renderConstraintNode(where, {
        scope: createDatamodelSqlScope({
          datamodel: { models: this.models },
          model: model.name,
          alias,
        }),
        absent: 'grant-all',
      }),
      this.dialect.policy,
      this.kysely.sql,
    );
  }

  column(alias: string, column: PlannedColumn): AliasedRawBuilder<unknown, string> {
    const sql = this.kysely.sql;
    const reference = sql`${sql.id(alias, column.dbName)}`;
    const expression =
      column.kind === 'bigint' || column.kind === 'decimal'
        ? sql`cast(${reference} as text)`
        : reference;
    return expression.as(column.name) as AliasedRawBuilder<unknown, string>;
  }

  aggregate(relation: PlannedRelation, parentAlias: string): RawBuilder<unknown> {
    const sql = this.kysely.sql;
    let child = this.db.selectFrom(
      sql`${sql.id(relation.table)}`.as(relation.alias) as never,
    ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    for (const column of relation.node.columns) {
      child = child.select(this.column(relation.alias, column) as never);
    }
    for (const nested of relation.node.relations) {
      child = child.select(
        this.aggregate(nested, relation.alias).as(nested.name) as never,
      );
    }
    for (const pair of relation.correlation) {
      child = child.where(
        sql`${sql.id(relation.alias, pair.child)} = ${sql.id(parentAlias, pair.parent)}` as never,
      );
    }
    if (relation.where !== undefined) {
      child = child.where(
        sql`(${this.predicate(relation.model, relation.alias, relation.where)})` as never,
      );
    }
    return relation.list
      ? this.aggregation.jsonArrayFrom(child)
      : this.aggregation.jsonObjectFrom(child);
  }

  root(relation: PlannedRelation): AliasedRawBuilder<unknown, string> {
    const sql = this.kysely.sql;
    return sql`cast(${this.aggregate(relation, ROOT_ALIAS)} as text)`.as(
      relation.name,
    ) as AliasedRawBuilder<unknown, string>;
  }
}

export async function planCompiledRead(input: CompiledReadInput): Promise<CompiledReadPlan> {
  if (input.provider === undefined || !COMPILABLE_PROVIDERS.has(input.provider)) {
    return fallback(
      'provider',
      `golem compiles reads for sqlite and postgresql, not ${input.provider ?? 'an unknown provider'}`,
    );
  }
  if (input.model.dbName == null) {
    return fallback(
      'projection',
      `model ${input.model.name} carries no physical table name; regenerate the golem client`,
    );
  }
  if (input.cursor !== undefined) {
    return fallback('cursor', `${input.model.name} is read with a cursor`);
  }
  if (input.distinct !== undefined) {
    return fallback('distinct', `${input.model.name} is read with distinct`);
  }
  if (input.where === null) {
    return fallback('where', `${input.model.name} is read with a null where`);
  }
  if (input.constraint === null) {
    return fallback(
      'where',
      `the read policy on ${input.model.name} is null, which Prisma rejects rather than reading as a denial`,
    );
  }

  const context: PlanContext = { models: input.models, metadata: input.metadata, aliases: 1 };
  const node = planNode(context, input.model, input.prepared, [], false);
  if (isFallback(node)) {
    return node;
  }
  if (input.provider === 'sqlite') {
    const wide = tooWideForSqlite(node.relations);
    if (wide !== undefined) {
      return fallback(
        'projection',
        `${input.model.name}.${wide.name} projects more than ${SQLITE_JSON_OBJECT_ENTRIES} fields through a relation, and a sqlite build guarantees json_object only ${SQLITE_JSON_OBJECT_ENTRIES * 2} arguments`,
      );
    }
  }
  const decimal = prismaDecimal();
  if (decimal === null && relationsNeedDecimal(node.relations)) {
    return fallback(
      'decoder',
      `${input.model.name} is read with a Decimal through a relation, and golem could not load the Decimal class Prisma decodes one into`,
    );
  }
  const terms = orderTerms(input.model, input.metadata, input.orderBy);
  if (isFallback(terms)) {
    return terms;
  }
  const bounds = input.single
    ? { limit: 1, offset: undefined, reversed: false }
    : pageBounds(input.model, input.take, input.skip, terms.length > 0);
  if (isFallback(bounds)) {
    return bounds;
  }

  const kysely = await kyselyModule();
  const dialect = resolveScopedDialect(input.provider);
  const aggregation = await jsonAggregationHelpers(dialect.provider);
  const sql = kysely.sql;
  const db = dialect.createKysely(kysely);
  const builder = new NestedBuilder(kysely, dialect, db, aggregation, input.models);
  const merged = mergeConstraint(input.where, input.constraint);

  let query = db.selectFrom(
    sql`${sql.id(input.model.dbName)}`.as(ROOT_ALIAS) as never,
  ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
  try {
    for (const column of node.columns) {
      query = query.select(sql`${sql.id(ROOT_ALIAS, column.dbName)}`.as(column.name) as never);
    }
    for (const relation of node.relations) {
      query = query.select(builder.root(relation) as never);
    }
    if (merged !== undefined) {
      query = query.where(
        sql`(${sqlNodeToRaw(
          renderConstraintNode(merged, {
            scope: createDatamodelSqlScope({
              datamodel: { models: input.models },
              model: input.model.name,
              alias: ROOT_ALIAS,
            }),
            absent: 'grant-all',
          }),
          dialect.policy,
          sql,
        )})` as never,
      );
    }
  } catch (error) {
    if (isUnsupported(error)) {
      return fallback('where', (error as Error).message);
    }
    throw error;
  }
  for (const term of terms) {
    const direction = bounds.reversed
      ? term.direction === 'asc' ? 'desc' : 'asc'
      : term.direction;
    query = query.orderBy(sql`${sql.id(ROOT_ALIAS, term.dbName)}` as never, direction);
  }
  if (bounds.limit !== undefined) {
    query = query.limit(bounds.limit);
  } else if (bounds.offset !== undefined && dialect.provider === 'sqlite') {
    query = query.limit(-1);
  }
  if (bounds.offset !== undefined) {
    query = query.offset(bounds.offset);
  }

  const compiled = dialect
    .createCompiler(kysely)
    .compileQuery(query.toOperationNode() as never, kysely.createQueryId());
  return {
    kind: 'compiled',
    sql: compiled.sql,
    parameters: compiled.parameters,
    reversed: bounds.reversed,
    columns: node.columns.map((column) => ({ name: column.name, dbName: column.dbName })),
    relations: node.relations.map(nestedProjection),
    decimal,
  };
}

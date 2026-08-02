import type { AliasedRawBuilder, RawBuilder, SelectQueryBuilder } from 'kysely';
import {
  UNSUPPORTED_CONDITION_ERROR_NAME,
  SqlRenderError,
  createDatamodelSqlScope,
  renderConstraintNode,
} from '@eleven-am/golem-policy';
import { isConditionalConstraint, mergeConstraint } from './authorization';
import {
  CompiledReadCount,
  CompiledReadNestedProjection,
  CompiledReadScalarKind,
  DecimalConstructor,
  RELATION_COUNT_ALIAS,
  prismaDecimal,
} from './compiled-read-decode';
import { DatamodelField, DatamodelModel, isEqualityIndexed } from './datamodel';
import { ModelMetadataIndex } from './model-meta';
import { InjectedField, PreparedReadTree } from './readtree';
import { RELATION_COUNT_FIELD } from './select';
import {
  ScopedDatabase,
  ScopedDialect,
  KyselyModule,
  kyselyModule,
  resolveScopedDialect,
  sqlNodeToRaw,
} from './scoped';

const ROOT_ALIAS = 't0';

const CURSOR_ALIAS = 'c0';

const COUNT_COLUMN = '_golem_count';

const MAX_IDENTIFIER = 63;

const COMPILABLE_PROVIDERS = new Set(['sqlite', 'postgresql', 'postgres']);

const SQLITE_MASKABLE_TYPES = new Set(['String', 'Float', 'BigInt']);

const RELATION_ENTRY_KEYS = new Set(['where', 'select', 'include', 'omit']);

const RELATION_PAGING_KEYS = new Set(['take', 'skip', 'orderBy']);

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

export type CompiledReadDeferredReason =
  | 'relation'
  | 'unconstrained'
  | 'unprojected'
  | 'unrenderable'
  | 'correlated'
  | 'distinct'
  | 'decoder';

export interface CompiledReadDeferredMask {
  readonly path: string;
  readonly field: string;
  readonly reason: CompiledReadDeferredReason;
  readonly detail: string;
}

export interface CompiledReadEvent {
  readonly model: string;
  readonly operation: CompiledReadOperation;
  readonly outcome: 'compiled' | 'fallback';
  readonly reason?: CompiledReadFallbackReason;
  readonly detail?: string;
  readonly sql?: string;
  readonly statements?: readonly string[];
  readonly batched?: readonly string[];
  readonly masked?: readonly string[];
  readonly deferred?: readonly CompiledReadDeferredMask[];
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

export interface CompiledReadQuery {
  readonly sql: string;
  readonly parameters: readonly unknown[];
}

export interface CompiledReadBatch {
  readonly path: string;
  readonly name: string;
  readonly parentKey: string;
  readonly childKey: string;
  readonly relations: readonly CompiledReadNestedProjection[];
  readonly batches: readonly CompiledReadBatch[];
  readonly drop: readonly string[];
  readonly limit?: number;
  readonly offset?: number;
  readonly reversed: boolean;
  build(values: readonly unknown[]): CompiledReadQuery;
}

export interface CompiledReadDistinct {
  readonly keys: readonly string[];
  readonly limit?: number;
  readonly offset?: number;
}

export interface CompiledReadStatement {
  readonly kind: 'compiled';
  readonly sql: string;
  readonly parameters: readonly unknown[];
  readonly reversed: boolean;
  readonly columns: readonly CompiledReadColumn[];
  readonly counts: readonly CompiledReadCount[];
  readonly relations: readonly CompiledReadNestedProjection[];
  readonly batches: readonly CompiledReadBatch[];
  readonly drop: readonly string[];
  readonly distinct?: CompiledReadDistinct;
  readonly decimal: DecimalConstructor | null;
  readonly masked: readonly string[];
  readonly deferred: readonly CompiledReadDeferredMask[];
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

export interface OrderTerm {
  readonly alias: string;
  readonly dbName: string;
  readonly direction: 'asc' | 'desc';
  readonly nulls?: 'first' | 'last';
  readonly counted?: boolean;
  readonly field?: string;
  readonly nullable?: boolean;
}

interface OrderJoin {
  readonly alias: string;
  readonly table: string;
  readonly parentAlias: string;
  readonly pairs: readonly Correlation[];
  readonly counted: boolean;
}

interface OrderPlan {
  readonly terms: readonly OrderTerm[];
  readonly joins: readonly OrderJoin[];
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
  readonly childField: string;
  readonly parentField: string;
}

type RelationShape = 'row' | 'json';

interface PlannedRelation {
  readonly name: string;
  readonly path: string;
  readonly list: boolean;
  readonly table: string;
  readonly alias: string;
  readonly correlation: readonly Correlation[];
  readonly where?: unknown;
  readonly model: DatamodelModel;
  readonly node: PlannedNode;
  readonly shape: RelationShape;
  readonly order: readonly OrderTerm[];
  readonly limit?: number;
  readonly offset?: number;
  readonly reversed: boolean;
}

interface PlannedCount {
  readonly name: string;
  readonly column: string;
  readonly table: string;
  readonly alias: string;
  readonly correlation: readonly Correlation[];
  readonly where?: unknown;
  readonly model: DatamodelModel;
}

interface PlannedNode {
  readonly columns: PlannedColumn[];
  readonly counts: PlannedCount[];
  readonly relations: PlannedRelation[];
  readonly drop: string[];
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

function ensureColumn(
  node: PlannedNode,
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  name: string,
  nested: boolean,
): PlannedColumn | CompiledReadFallback {
  const existing = node.columns.find((column) => column.name === name);
  if (existing !== undefined) {
    return existing;
  }
  const column = scalarColumn(model, metadata, name, nested);
  if (isFallback(column)) {
    return column;
  }
  node.columns.push(column);
  node.drop.push(name);
  return column;
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
        ? {
            child: referencedColumn,
            parent: holderColumn,
            childField: to[index]!,
            parentField: name,
          }
        : {
            child: holderColumn,
            parent: referencedColumn,
            childField: name,
            parentField: to[index]!,
          },
    );
  }
  return pairs;
}

function primaryOrder(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  alias: string,
): readonly OrderTerm[] | CompiledReadFallback {
  const keys = metadata.get(model.name)?.primaryKeys ?? [];
  if (keys.length === 0) {
    return fallback(
      'orderBy',
      `${model.name} is paged without an order and carries no primary key golem can order it by`,
    );
  }
  const terms: OrderTerm[] = [];
  for (const key of keys) {
    if (key.dbName == null) {
      return fallback(
        'orderBy',
        `${model.name}.${key.name} carries no physical column name; regenerate the golem client`,
      );
    }
    terms.push({
      alias,
      dbName: key.dbName,
      direction: 'asc',
      field: key.name,
      nullable: !key.isRequired,
    });
  }
  return terms;
}

function planOrder(
  context: PlanContext,
  model: DatamodelModel,
  alias: string,
  orderBy: unknown,
  relations: boolean,
): OrderPlan | CompiledReadFallback {
  if (orderBy === undefined || orderBy === null) {
    return { terms: [], joins: [] };
  }
  const entries = Array.isArray(orderBy) ? orderBy : [orderBy];
  const terms: OrderTerm[] = [];
  const joins: OrderJoin[] = [];

  const walk = (
    current: DatamodelModel,
    currentAlias: string,
    entry: Record<string, unknown>,
    trail: readonly string[],
  ): CompiledReadFallback | undefined => {
    for (const [name, value] of Object.entries(entry)) {
      if (value === undefined) {
        continue;
      }
      const field = context.metadata.get(current.name)?.fieldsByName.get(name);
      if (field?.kind === 'object') {
        if (!relations) {
          return fallback(
            'orderBy',
            `${describePath([...trail, name])} orders by a relation, which golem compiles only at the top level of a read`,
          );
        }
        const nested = value as Record<string, unknown>;
        if (!nested || typeof nested !== 'object' || Array.isArray(nested)) {
          return fallback('orderBy', `${current.name}.${name} is ordered by ${JSON.stringify(value)}`);
        }
        const target = context.metadata.get(field.type)?.model;
        if (target?.dbName == null) {
          return fallback(
            'orderBy',
            `${current.name}.${name} orders by ${field.type}, which golem has no physical table name for`,
          );
        }
        const pairs = correlate(context.metadata, current, target, field);
        if (isFallback(pairs)) {
          return { ...pairs, reason: 'orderBy' };
        }
        const joinAlias = `o${context.aliases++}`;
        const count = nested._count;
        if (count !== undefined) {
          if (!field.isList) {
            return fallback(
              'orderBy',
              `${current.name}.${name} is a to-one ordered by _count, which Prisma offers only on a list`,
            );
          }
          if (Object.keys(nested).length > 1) {
            return fallback(
              'orderBy',
              `${current.name}.${name} orders by _count alongside ${Object.keys(nested).join(', ')}`,
            );
          }
          if (count !== 'asc' && count !== 'desc') {
            return fallback(
              'orderBy',
              `${current.name}.${name} orders by a _count of ${JSON.stringify(count)}`,
            );
          }
          if (pairs.length !== 1) {
            return fallback(
              'orderBy',
              `${current.name}.${name} orders by _count across ${pairs.length} foreign key columns`,
            );
          }
          joins.push({
            alias: joinAlias,
            table: target.dbName,
            parentAlias: currentAlias,
            pairs,
            counted: true,
          });
          terms.push({
            alias: joinAlias,
            dbName: COUNT_COLUMN,
            direction: count,
            counted: true,
          });
          continue;
        }
        if (field.isList) {
          return fallback(
            'orderBy',
            `${current.name}.${name} is a list ordered by ${JSON.stringify(value)}`,
          );
        }
        joins.push({
          alias: joinAlias,
          table: target.dbName,
          parentAlias: currentAlias,
          pairs,
          counted: false,
        });
        const nestedFallback = walk(target, joinAlias, nested, [...trail, name]);
        if (nestedFallback !== undefined) {
          return nestedFallback;
        }
        continue;
      }
      let direction: unknown = value;
      let nulls: 'first' | 'last' | undefined;
      if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        const shape = value as Record<string, unknown>;
        const extra = Object.keys(shape).filter((key) => key !== 'sort' && key !== 'nulls');
        if (extra.length > 0) {
          return fallback(
            'orderBy',
            `${current.name}.${name} is ordered by ${JSON.stringify(value)}`,
          );
        }
        direction = shape.sort;
        if (shape.nulls !== undefined) {
          if (shape.nulls !== 'first' && shape.nulls !== 'last') {
            return fallback(
              'orderBy',
              `${current.name}.${name} places nulls ${JSON.stringify(shape.nulls)}`,
            );
          }
          nulls = shape.nulls;
        }
      }
      if (direction !== 'asc' && direction !== 'desc') {
        return fallback(
          'orderBy',
          `${current.name}.${name} is ordered by ${JSON.stringify(direction)}`,
        );
      }
      const column = scalarColumn(current, context.metadata, name, false);
      if (isFallback(column)) {
        return { ...column, reason: 'orderBy' };
      }
      terms.push({
        alias: currentAlias,
        dbName: column.dbName,
        direction,
        nulls,
        field: currentAlias === alias ? name : undefined,
        nullable: field?.isRequired === false,
      });
    }
    return undefined;
  };

  for (const entry of entries) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      return fallback('orderBy', `${model.name} is ordered by ${JSON.stringify(entry)}`);
    }
    const refused = walk(model, alias, entry as Record<string, unknown>, []);
    if (refused !== undefined) {
      return refused;
    }
  }
  return { terms, joins };
}

export function orderTerms(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  orderBy: unknown,
): readonly OrderTerm[] | CompiledReadFallback {
  const plan = planOrder({ models: [], metadata, aliases: 1 }, model, ROOT_ALIAS, orderBy, false);
  return isFallback(plan) ? plan : plan.terms;
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

function batchable(
  metadata: ModelMetadataIndex,
  target: DatamodelModel,
  field: DatamodelField,
  correlation: readonly Correlation[],
): boolean {
  if (!field.isList || correlation.length !== 1) {
    return false;
  }
  const pair = correlation[0]!;
  const childField = metadata.get(target.name)?.fieldsByName.get(pair.childField);
  if (childField === undefined || nestedScalarKind(childField) !== 'plain') {
    return false;
  }
  return !isEqualityIndexed(target, pair.childField);
}

function planRelation(
  context: PlanContext,
  parent: DatamodelModel,
  field: DatamodelField,
  entry: unknown,
  path: readonly string[],
  parentShape: RelationShape,
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
    if (field.isList && RELATION_PAGING_KEYS.has(key)) {
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
  const order = planOrder(context, target, alias, projection.orderBy, false);
  if (isFallback(order)) {
    return order;
  }
  const paged = projection.take !== undefined || projection.skip !== undefined;
  let terms = order.terms;
  if (terms.length === 0 && paged) {
    const implied = primaryOrder(target, context.metadata, alias);
    if (isFallback(implied)) {
      return implied;
    }
    terms = implied;
  }
  const bounds = pageBounds(
    target,
    projection.take as number | undefined,
    projection.skip as number | undefined,
    terms.length > 0,
  );
  if (isFallback(bounds)) {
    return bounds;
  }
  const shape: RelationShape =
    parentShape === 'json' || !batchable(context.metadata, target, field, correlation)
      ? 'json'
      : 'row';
  const node = planNode(context, target, projection as Projection, path, shape);
  if (isFallback(node)) {
    return node;
  }
  return {
    name: field.name,
    path: describePath(path),
    list: field.isList,
    table: target.dbName,
    alias,
    correlation,
    where: projection.where,
    model: target,
    node,
    shape,
    order: terms,
    limit: bounds.limit,
    offset: bounds.offset,
    reversed: bounds.reversed,
  };
}

function planCounts(
  context: PlanContext,
  model: DatamodelModel,
  entry: unknown,
  path: readonly string[],
): readonly PlannedCount[] | CompiledReadFallback {
  if (path.length > 0) {
    return fallback(
      'relation',
      `${describePath([...path, RELATION_COUNT_FIELD])} counts a relation of a relation, which golem compiles only at the top level of a read`,
    );
  }
  const requested = (entry as { select?: unknown } | null)?.select;
  if (!requested || typeof requested !== 'object' || Array.isArray(requested)) {
    return fallback(
      'relation',
      `${model.name} counts relations with ${JSON.stringify(entry)} rather than with a select`,
    );
  }
  const counts: PlannedCount[] = [];
  for (const [name, value] of Object.entries(requested as Record<string, unknown>)) {
    if (value === false || value === undefined) {
      continue;
    }
    const field = context.metadata.get(model.name)?.fieldsByName.get(name);
    if (field === undefined || field.kind !== 'object') {
      return fallback(
        'relation',
        `${model.name}.${name} is counted but is not a relation of the model`,
      );
    }
    if (!field.isList) {
      return fallback(
        'relation',
        `${model.name}.${name} is counted but is a to-one relation, which Prisma counts only on a list`,
      );
    }
    const target = context.metadata.get(field.type)?.model;
    if (target === undefined || target.dbName == null) {
      return fallback(
        'relation',
        `${model.name}.${name} counts ${field.type}, which golem has no physical table name for`,
      );
    }
    let where: unknown;
    if (value !== true) {
      if (typeof value !== 'object' || Array.isArray(value)) {
        return fallback('relation', `${model.name}.${name} is counted with ${JSON.stringify(value)}`);
      }
      const extra = Object.entries(value as Record<string, unknown>)
        .filter(([key, held]) => key !== 'where' && held !== undefined)
        .map(([key]) => key);
      if (extra.length > 0) {
        return fallback('relation', `${model.name}.${name} is counted with ${extra.join(', ')}`);
      }
      where = (value as Record<string, unknown>).where;
      if (where === null) {
        return fallback(
          'where',
          `the read policy on ${target.name} counted through ${model.name}.${name} is null, which Prisma rejects rather than reading as a denial`,
        );
      }
    }
    const correlation = correlate(context.metadata, model, target, field);
    if (isFallback(correlation)) {
      return correlation;
    }
    const column = `${RELATION_COUNT_ALIAS}${name}`;
    if (column.length > MAX_IDENTIFIER) {
      return fallback(
        'projection',
        `${model.name}.${name} is counted into the result column "${column}", which is longer than the ${MAX_IDENTIFIER} characters postgres keeps before it truncates an identifier`,
      );
    }
    counts.push({
      name,
      column,
      table: target.dbName,
      alias: `t${context.aliases++}`,
      correlation,
      where,
      model: target,
    });
  }
  return counts;
}

function planNode(
  context: PlanContext,
  model: DatamodelModel,
  projection: Projection,
  path: readonly string[],
  shape: RelationShape,
): PlannedNode | CompiledReadFallback {
  const nested = shape === 'json';
  const columns: PlannedColumn[] = [];
  const counts: PlannedCount[] = [];
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
      if (name === RELATION_COUNT_FIELD) {
        const planned = planCounts(context, model, value, path);
        if (isFallback(planned)) {
          return planned;
        }
        counts.push(...planned);
        continue;
      }
      const field = fields.fieldsByName.get(name);
      if (field?.kind === 'object') {
        const relation = planRelation(context, model, field, value, [...path, name], shape);
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
      if (name === RELATION_COUNT_FIELD) {
        const planned = planCounts(context, model, value, path);
        if (isFallback(planned)) {
          return planned;
        }
        counts.push(...planned);
        continue;
      }
      const field = fields.fieldsByName.get(name);
      if (field?.kind !== 'object') {
        return fallback(
          'projection',
          `${model.name}.${name} is included but is not a relation of the model`,
        );
      }
      const relation = planRelation(context, model, field, value, [...path, name], shape);
      if (isFallback(relation)) {
        return relation;
      }
      relations.push(relation);
    }
  }
  if (columns.length === 0 && relations.length === 0 && counts.length === 0) {
    return fallback('projection', `${describePath(path)} is read with an empty projection`);
  }
  return { columns, counts, relations, drop: [] };
}

function correlateBatches(
  node: PlannedNode,
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  nested: boolean,
): CompiledReadFallback | undefined {
  for (const relation of node.relations) {
    if (relation.shape === 'row') {
      const pair = relation.correlation[0]!;
      const onParent = ensureColumn(node, model, metadata, pair.parentField, nested);
      if (isFallback(onParent)) {
        return onParent;
      }
      const onChild = ensureColumn(
        relation.node,
        relation.model,
        metadata,
        pair.childField,
        false,
      );
      if (isFallback(onChild)) {
        return onChild;
      }
      const refused = correlateBatches(relation.node, relation.model, metadata, false);
      if (refused !== undefined) {
        return refused;
      }
    }
  }
  return undefined;
}

function nestedProjection(relation: PlannedRelation): CompiledReadNestedProjection {
  return {
    name: relation.name,
    list: relation.list,
    ...(relation.reversed ? { reversed: true } : {}),
    fields: relation.node.columns.map((column) => ({
      name: column.name,
      kind: column.kind!,
    })),
    relations: relation.node.relations
      .filter((child) => child.shape === 'json')
      .map(nestedProjection),
  };
}

const SQLITE_JSON_OBJECT_ENTRIES = 63;

function tooWideForSqlite(relations: readonly PlannedRelation[]): PlannedRelation | undefined {
  for (const relation of relations) {
    if (
      relation.shape === 'json' &&
      relation.node.columns.length + relation.node.relations.length > SQLITE_JSON_OBJECT_ENTRIES
    ) {
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
      (relation.shape === 'json' &&
        relation.node.columns.some((column) => column.kind === 'decimal')) ||
      relationsNeedDecimal(relation.node.relations),
  );
}

interface CursorSelector {
  readonly dbName: string;
  readonly value: unknown;
}

function cursorSelectors(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  cursor: unknown,
): readonly CursorSelector[] | CompiledReadFallback {
  if (!cursor || typeof cursor !== 'object' || Array.isArray(cursor)) {
    return fallback('cursor', `${model.name} is read from the cursor ${JSON.stringify(cursor)}`);
  }
  const meta = metadata.get(model.name);
  const selectors: CursorSelector[] = [];
  const take = (name: string, value: unknown): CompiledReadFallback | undefined => {
    const field = meta?.fieldsByName.get(name);
    if (field === undefined || field.kind === 'object' || field.dbName == null) {
      return fallback('cursor', `${model.name}.${name} is not a column golem can position a cursor on`);
    }
    if (value === null || (typeof value === 'object' && !(value instanceof Date))) {
      return fallback(
        'cursor',
        `${model.name}.${name} is positioned at ${JSON.stringify(value)}, which golem cannot bind as a single value`,
      );
    }
    selectors.push({ dbName: field.dbName, value });
    return undefined;
  };
  for (const [name, value] of Object.entries(cursor as Record<string, unknown>)) {
    if (value === undefined) {
      continue;
    }
    const compound = meta?.compoundUniqueSelectors.get(name);
    if (compound !== undefined) {
      if (!value || typeof value !== 'object' || Array.isArray(value)) {
        return fallback(
          'cursor',
          `${model.name} is read from the compound cursor ${name} of ${JSON.stringify(value)}`,
        );
      }
      for (const part of compound) {
        const refused = take(part, (value as Record<string, unknown>)[part]);
        if (refused !== undefined) {
          return refused;
        }
      }
      continue;
    }
    const refused = take(name, value);
    if (refused !== undefined) {
      return refused;
    }
  }
  if (selectors.length === 0) {
    return fallback('cursor', `${model.name} is read from a cursor that names no column`);
  }
  return selectors;
}

function distinctKeys(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  distinct: unknown,
): readonly string[] | CompiledReadFallback {
  const names = Array.isArray(distinct) ? distinct : [distinct];
  const keys: string[] = [];
  for (const name of names) {
    if (typeof name !== 'string') {
      return fallback('distinct', `${model.name} is read distinct on ${JSON.stringify(name)}`);
    }
    const field = metadata.get(model.name)?.fieldsByName.get(name);
    if (field === undefined || field.kind === 'object' || field.isList) {
      return fallback(
        'distinct',
        `${model.name}.${name} is not a scalar column golem can read distinct on`,
      );
    }
    keys.push(name);
  }
  if (keys.length === 0) {
    return fallback('distinct', `${model.name} is read distinct on no column at all`);
  }
  return keys;
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

  order<T>(query: T, terms: readonly OrderTerm[], reversed: boolean): T {
    const sql = this.kysely.sql;
    let built = query as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    for (const term of terms) {
      const direction = reversed
        ? term.direction === 'asc'
          ? 'desc'
          : 'asc'
        : term.direction;
      const reference = term.counted
        ? sql`coalesce(${sql.id(term.alias, term.dbName)}, 0)`
        : sql`${sql.id(term.alias, term.dbName)}`;
      if (term.nulls !== undefined) {
        const first = (term.nulls === 'first') !== reversed;
        built = built.orderBy(
          sql`case when ${sql.id(term.alias, term.dbName)} is null then ${sql.lit(
            first ? 0 : 1,
          )} else ${sql.lit(first ? 1 : 0)} end` as never,
        );
      }
      built = built.orderBy(reference as never, direction);
    }
    return built as unknown as T;
  }

  private bound<T>(query: T, limit: number | undefined, offset: number | undefined): T {
    let built = query as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    if (limit !== undefined) {
      built = built.limit(limit);
    } else if (offset !== undefined && this.dialect.provider === 'sqlite') {
      built = built.limit(-1);
    }
    if (offset !== undefined) {
      built = built.offset(offset);
    }
    return built as unknown as T;
  }

  private child(
    relation: PlannedRelation,
  ): SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>> {
    const sql = this.kysely.sql;
    let child = this.db.selectFrom(
      sql`${sql.id(relation.table)}`.as(relation.alias) as never,
    ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    for (const column of relation.node.columns) {
      child = child.select(this.column(relation.alias, column) as never);
    }
    for (const nested of relation.node.relations) {
      if (nested.shape !== 'json') {
        continue;
      }
      child = child.select(
        this.aggregate(nested, relation.alias).as(nested.name) as never,
      );
    }
    if (relation.where !== undefined) {
      child = child.where(
        sql`(${this.predicate(relation.model, relation.alias, relation.where)})` as never,
      );
    }
    return child;
  }

  aggregate(relation: PlannedRelation, parentAlias: string): RawBuilder<unknown> {
    const sql = this.kysely.sql;
    let child = this.child(relation);
    for (const pair of relation.correlation) {
      child = child.where(
        sql`${sql.id(relation.alias, pair.child)} = ${sql.id(parentAlias, pair.parent)}` as never,
      );
    }
    child = this.order(child, relation.order, relation.reversed);
    child = this.bound(child, relation.limit, relation.offset);
    return relation.list
      ? this.aggregation.jsonArrayFrom(child)
      : this.aggregation.jsonObjectFrom(child);
  }

  count(count: PlannedCount, parentAlias: string): AliasedRawBuilder<unknown, string> {
    const sql = this.kysely.sql;
    const conditions: RawBuilder<unknown>[] = count.correlation.map(
      (pair) => sql`${sql.id(count.alias, pair.child)} = ${sql.id(parentAlias, pair.parent)}`,
    );
    if (count.where !== undefined) {
      conditions.push(sql`(${this.predicate(count.model, count.alias, count.where)})`);
    }
    return sql`(select count(*) from ${sql.id(count.table)} as ${sql.id(
      count.alias,
    )} where ${sql.join(conditions, sql.raw(' and '))})`.as(
      count.column,
    ) as AliasedRawBuilder<unknown, string>;
  }

  root(relation: PlannedRelation, parentAlias: string): AliasedRawBuilder<unknown, string> {
    const sql = this.kysely.sql;
    return sql`cast(${this.aggregate(relation, parentAlias)} as text)`.as(
      relation.name,
    ) as AliasedRawBuilder<unknown, string>;
  }

  join<T>(query: T, joins: readonly OrderJoin[]): T {
    const sql = this.kysely.sql;
    let built = query as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    for (const join of joins) {
      const pair = join.pairs[0]!;
      const source = join.counted
        ? sql`(select ${sql.id(pair.child)}, count(*) as ${sql.id(COUNT_COLUMN)} from ${sql.id(
            join.table,
          )} group by ${sql.id(pair.child)})`
        : sql`${sql.id(join.table)}`;
      const condition = sql.join(
        join.pairs.map(
          (entry) => sql`${sql.id(join.alias, entry.child)} = ${sql.id(join.parentAlias, entry.parent)}`,
        ),
        sql.raw(' and '),
      );
      built = built.leftJoin(source.as(join.alias) as never, (on) =>
        (on as unknown as { on(expression: unknown): unknown }).on(condition) as never,
      ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
    }
    return built as unknown as T;
  }

  cursor(
    table: string,
    selectors: readonly CursorSelector[],
    terms: readonly OrderTerm[],
    reversed: boolean,
  ): RawBuilder<unknown> {
    const sql = this.kysely.sql;
    const anchor = (term: OrderTerm): RawBuilder<unknown> =>
      sql`(select ${sql.id(CURSOR_ALIAS, term.dbName)} from ${sql.id(table)} as ${sql.id(
        CURSOR_ALIAS,
      )} where (${sql.join(
        selectors.map((selector) => sql`${sql.id(CURSOR_ALIAS, selector.dbName)}`),
        sql.raw(', '),
      )}) = (${sql.join(
        selectors.map((selector) => sql`${sql.val(selector.value)}`),
        sql.raw(', '),
      )}))`;
    const compare = (term: OrderTerm, operator: string): RawBuilder<unknown> =>
      sql`(${sql.id(ROOT_ALIAS, term.dbName)} ${sql.raw(operator)} ${anchor(term)})`;
    const direction = (term: OrderTerm): 'asc' | 'desc' =>
      reversed ? (term.direction === 'asc' ? 'desc' : 'asc') : term.direction;
    const branches: RawBuilder<unknown>[] = [];
    for (let depth = terms.length; depth > 0; depth -= 1) {
      const parts: RawBuilder<unknown>[] = [];
      for (let index = 0; index < depth - 1; index += 1) {
        parts.push(compare(terms[index]!, '='));
      }
      const last = terms[depth - 1]!;
      const strict = direction(last) === 'asc' ? '>' : '<';
      parts.push(compare(last, depth === terms.length ? `${strict}=` : strict));
      branches.push(sql`(${sql.join(parts, sql.raw(' and '))})`);
    }
    return sql`(${sql.join(branches, sql.raw(' or '))})`;
  }

  batch(relation: PlannedRelation, dialect: ScopedDialect): CompiledReadBatch {
    const sql = this.kysely.sql;
    const pair = relation.correlation[0]!;
    const nested = relation.node.relations
      .filter((child) => child.shape === 'row')
      .map((child) => this.batch(child, dialect));
    return {
      path: relation.path,
      name: relation.name,
      parentKey: pair.parentField,
      childKey: pair.childField,
      relations: relation.node.relations
        .filter((child) => child.shape === 'json')
        .map(nestedProjection),
      batches: nested,
      drop: relation.node.drop,
      limit: relation.limit,
      offset: relation.offset,
      reversed: relation.reversed,
      build: (values) => {
        let query = this.child(relation);
        query = query.where(
          sql`${sql.id(relation.alias, pair.child)} in (${sql.join(
            values.map((value) => sql`${sql.val(value)}`),
            sql.raw(', '),
          )})` as never,
        );
        query = this.order(query, relation.order, relation.reversed);
        const compiled = dialect
          .createCompiler(this.kysely)
          .compileQuery(query.toOperationNode() as never, this.kysely.createQueryId());
        return { sql: compiled.sql, parameters: compiled.parameters };
      },
    };
  }
}

interface RootMasks {
  readonly rendered: ReadonlyMap<string, RawBuilder<unknown>>;
  readonly deferred: readonly CompiledReadDeferredMask[];
}

function planRootNode(
  context: PlanContext,
  input: CompiledReadInput,
  projection: Projection,
  keys: readonly string[] | undefined,
): PlannedNode | CompiledReadFallback {
  const node = planNode(context, input.model, projection, [], 'row');
  if (isFallback(node)) {
    return node;
  }
  if (keys !== undefined) {
    for (const key of keys) {
      const column = ensureColumn(node, input.model, input.metadata, key, false);
      if (isFallback(column)) {
        return { ...column, reason: 'distinct' };
      }
    }
  }
  const correlated = correlateBatches(node, input.model, input.metadata, false);
  if (correlated !== undefined) {
    return correlated;
  }
  return node;
}

function planRootMasks(
  input: CompiledReadInput,
  node: PlannedNode,
  keys: readonly string[] | undefined,
  dialect: ScopedDialect,
  sql: KyselyModule['sql'],
): RootMasks {
  const rendered = new Map<string, RawBuilder<unknown>>();
  const deferred: CompiledReadDeferredMask[] = [];
  if (input.prepared.maskChecks.length === 0) {
    return { rendered, deferred };
  }
  const correlated = new Set(
    node.relations
      .filter((relation) => relation.shape === 'row')
      .map((relation) => relation.correlation[0]!.parentField),
  );
  const distinct = new Set(keys ?? []);
  const scope = createDatamodelSqlScope({
    datamodel: { models: input.models },
    model: input.model.name,
    alias: ROOT_ALIAS,
  });
  for (const check of input.prepared.maskChecks) {
    const defer = (reason: CompiledReadDeferredReason, detail: string): void => {
      deferred.push({ path: describePath(check.path), field: check.field, reason, detail });
    };
    if (check.path.length > 0) {
      defer(
        'relation',
        `${check.model}.${check.field} is masked through ${describePath(check.path)}, and golem masks in SQL only at the top level of a read`,
      );
      continue;
    }
    if (!isConditionalConstraint(check.constraint)) {
      defer(
        'unconstrained',
        `${check.model}.${check.field} is masked by an authorization provider that hands golem no condition for the field, and an absent condition must not be read as a grant`,
      );
      continue;
    }
    if (!node.columns.some((column) => column.name === check.field)) {
      defer(
        'unprojected',
        `${check.model}.${check.field} is masked but the compiled read projects no column for it`,
      );
      continue;
    }
    if (correlated.has(check.field)) {
      defer(
        'correlated',
        `${check.model}.${check.field} is masked but carries the key golem batches a relation of ${check.model} on`,
      );
      continue;
    }
    if (distinct.has(check.field)) {
      defer(
        'distinct',
        `${check.model}.${check.field} is masked but the read is distinct on it, and Prisma reads distinct on the value before the mask nulls it`,
      );
      continue;
    }
    const column = input.metadata.get(check.model)?.fieldsByName.get(check.field);
    if (
      dialect.provider === 'sqlite' &&
      (column === undefined || column.kind !== 'scalar' || !SQLITE_MASKABLE_TYPES.has(column.type))
    ) {
      defer(
        'decoder',
        `${check.model}.${check.field} is a ${column?.type ?? 'column golem cannot type'} sqlite carries no declared type for once it is wrapped in a case expression, and Prisma decodes such a column by its declared type`,
      );
      continue;
    }
    try {
      rendered.set(
        check.field,
        sqlNodeToRaw(
          renderConstraintNode(check.constraint, { scope, absent: 'deny-all' }),
          dialect.policy,
          sql,
        ),
      );
    } catch (error) {
      if (!isUnsupported(error)) {
        throw error;
      }
      defer('unrenderable', (error as Error).message);
    }
  }
  return { rendered, deferred };
}

function droppableHydration(
  injected: readonly InjectedField[],
  masked: ReadonlyMap<string, RawBuilder<unknown>>,
): readonly InjectedField[] {
  return injected.filter(
    (entry) =>
      entry.path.length === 0 &&
      entry.masks !== undefined &&
      entry.masks.length > 0 &&
      entry.masks.every((field) => masked.has(field)),
  );
}

function withoutHydration(
  prepared: PreparedReadTree,
  drop: readonly InjectedField[],
): { projection: Projection; dropped: readonly string[] } {
  const select = prepared.select === undefined ? undefined : { ...prepared.select };
  const include = prepared.include === undefined ? undefined : { ...prepared.include };
  const omit = prepared.omit === undefined ? undefined : { ...prepared.omit };
  const dropped: string[] = [];
  for (const entry of drop) {
    if (select !== undefined && entry.field in select) {
      delete select[entry.field];
      dropped.push(entry.field);
      continue;
    }
    if (include !== undefined && entry.field in include) {
      delete include[entry.field];
      dropped.push(entry.field);
      continue;
    }
    if (omit !== undefined && select === undefined) {
      omit[entry.field] = true;
      dropped.push(entry.field);
    }
  }
  return { projection: { select, include, omit }, dropped };
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
  if (input.where === null) {
    return fallback('where', `${input.model.name} is read with a null where`);
  }
  if (input.constraint === null) {
    return fallback(
      'where',
      `the read policy on ${input.model.name} is null, which Prisma rejects rather than reading as a denial`,
    );
  }

  const keys =
    input.distinct === undefined
      ? undefined
      : distinctKeys(input.model, input.metadata, input.distinct);
  if (isFallback(keys)) {
    return keys;
  }

  const kysely = await kyselyModule();
  const dialect = resolveScopedDialect(input.provider);

  let context: PlanContext = { models: input.models, metadata: input.metadata, aliases: 1 };
  let node = planRootNode(context, input, input.prepared, keys);
  if (isFallback(node)) {
    return node;
  }
  const masks = planRootMasks(input, node, keys, dialect, kysely.sql);
  const hydration = withoutHydration(
    input.prepared,
    droppableHydration(input.prepared.injected, masks.rendered),
  );
  if (hydration.dropped.length > 0) {
    context = { models: input.models, metadata: input.metadata, aliases: 1 };
    const reduced = planRootNode(context, input, hydration.projection, keys);
    if (isFallback(reduced)) {
      return reduced;
    }
    node = reduced;
  }
  const order = planOrder(context, input.model, ROOT_ALIAS, input.orderBy, true);
  if (isFallback(order)) {
    return order;
  }
  let terms = order.terms;

  let selectors: readonly CursorSelector[] | undefined;
  if (input.cursor !== undefined) {
    const resolved = cursorSelectors(input.model, input.metadata, input.cursor);
    if (isFallback(resolved)) {
      return resolved;
    }
    selectors = resolved;
    if (terms.length === 0) {
      const implied = primaryOrder(input.model, input.metadata, ROOT_ALIAS);
      if (isFallback(implied)) {
        return { ...implied, reason: 'cursor' };
      }
      terms = implied;
    }
    const foreign = terms.find((term) => term.field === undefined);
    if (foreign !== undefined) {
      return fallback(
        'cursor',
        `${input.model.name} is read from a cursor against an order that leaves the table golem positions the cursor in`,
      );
    }
    const nullable = terms.find((term) => term.nullable === true);
    if (nullable !== undefined) {
      return fallback(
        'cursor',
        `${input.model.name} is read from a cursor against the nullable column ${nullable.dbName}, where Prisma's own predicate keeps rows on either side of the cursor and it settles the page by finding the cursor row among the rows it read back`,
      );
    }
  }

  const bounds = input.single
    ? { limit: 1, offset: undefined, reversed: false }
    : pageBounds(input.model, input.take, input.skip, terms.length > 0);
  if (isFallback(bounds)) {
    return bounds;
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

  const aggregation = await jsonAggregationHelpers(dialect.provider);
  const sql = kysely.sql;
  const db = dialect.createKysely(kysely);
  const builder = new NestedBuilder(kysely, dialect, db, aggregation, input.models);
  const merged = mergeConstraint(input.where, input.constraint);

  let query = db.selectFrom(
    sql`${sql.id(input.model.dbName)}`.as(ROOT_ALIAS) as never,
  ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
  query = builder.join(query, order.joins);
  try {
    for (const column of node.columns) {
      const mask = masks.rendered.get(column.name);
      const reference = sql`${sql.id(ROOT_ALIAS, column.dbName)}`;
      const expression =
        mask === undefined
          ? reference
          : sql`case when (${mask}) then ${reference} else null end`;
      query = query.select(expression.as(column.name) as never);
    }
    for (const count of node.counts) {
      query = query.select(builder.count(count, ROOT_ALIAS) as never);
    }
    for (const relation of node.relations) {
      if (relation.shape !== 'json') {
        continue;
      }
      query = query.select(builder.root(relation, ROOT_ALIAS) as never);
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
  if (selectors !== undefined) {
    query = query.where(
      builder.cursor(input.model.dbName, selectors, terms, bounds.reversed) as never,
    );
  }
  query = builder.order(query, terms, bounds.reversed);
  const pushed = keys === undefined;
  if (pushed) {
    if (bounds.limit !== undefined) {
      query = query.limit(bounds.limit);
    } else if (bounds.offset !== undefined && dialect.provider === 'sqlite') {
      query = query.limit(-1);
    }
    if (bounds.offset !== undefined) {
      query = query.offset(bounds.offset);
    }
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
    counts: node.counts.map((count) => ({ name: count.name, column: count.column })),
    relations: node.relations.filter((relation) => relation.shape === 'json').map(nestedProjection),
    batches: node.relations
      .filter((relation) => relation.shape === 'row')
      .map((relation) => builder.batch(relation, dialect)),
    drop: node.drop,
    distinct:
      keys === undefined
        ? undefined
        : { keys, limit: bounds.limit, offset: bounds.offset },
    decimal,
    masked: [...masks.rendered.keys()],
    deferred: masks.deferred,
  };
}

export function compiledReadBatchPaths(plan: CompiledReadStatement): readonly string[] {
  const paths: string[] = [];
  const walk = (batches: readonly CompiledReadBatch[]): void => {
    for (const batch of batches) {
      paths.push(batch.path);
      walk(batch.batches);
    }
  };
  walk(plan.batches);
  return paths;
}

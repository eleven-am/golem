import type { SelectQueryBuilder } from 'kysely';
import {
  UNSUPPORTED_CONDITION_ERROR_NAME,
  SqlRenderError,
  createDatamodelSqlScope,
  renderConstraintNode,
} from '@eleven-am/golem-policy';
import { mergeConstraint } from './authorization';
import { DatamodelModel } from './datamodel';
import { ModelMetadataIndex } from './model-meta';
import { PreparedReadTree } from './readtree';
import {
  ScopedDatabase,
  kyselyModule,
  resolveScopedDialect,
  sqlNodeToRaw,
} from './scoped';

const ROOT_ALIAS = 't0';

const COMPILABLE_PROVIDERS = new Set(['sqlite', 'postgresql', 'postgres']);

export type CompiledReadOperation = 'findOne' | 'findMany';

export type CompiledReadFallbackReason =
  | 'client'
  | 'provider'
  | 'relation'
  | 'projection'
  | 'cursor'
  | 'distinct'
  | 'where'
  | 'orderBy'
  | 'take';

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
}

export interface CompiledReadFallback {
  readonly kind: 'fallback';
  readonly reason: CompiledReadFallbackReason;
  readonly detail: string;
}

export type CompiledReadPlan = CompiledReadStatement | CompiledReadFallback;

interface OrderTerm {
  readonly dbName: string;
  readonly direction: 'asc' | 'desc';
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

function scalarColumn(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  name: string,
): CompiledReadColumn | CompiledReadFallback {
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
  return { name, dbName: field.dbName };
}

export function projectionColumns(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  prepared: PreparedReadTree,
): readonly CompiledReadColumn[] | CompiledReadFallback {
  const columns: CompiledReadColumn[] = [];
  if (prepared.select !== undefined) {
    for (const [name, value] of Object.entries(prepared.select)) {
      if (value === false || value === undefined) {
        continue;
      }
      if (value !== true) {
        return fallback(
          'relation',
          `${model.name}.${name} is selected as a nested projection`,
        );
      }
      const column = scalarColumn(model, metadata, name);
      if (isFallback(column)) {
        return column;
      }
      columns.push(column);
    }
  } else {
    if (prepared.include !== undefined && Object.keys(prepared.include).length > 0) {
      return fallback(
        'relation',
        `${model.name} is read with include ${Object.keys(prepared.include).join(', ')}`,
      );
    }
    for (const field of metadata.get(model.name)?.scalarFields ?? []) {
      if (prepared.omit?.[field.name] === true) {
        continue;
      }
      const column = scalarColumn(model, metadata, field.name);
      if (isFallback(column)) {
        return column;
      }
      columns.push(column);
    }
  }
  if (columns.length === 0) {
    return fallback('projection', `${model.name} is read with an empty projection`);
  }
  return columns;
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
      const column = scalarColumn(model, metadata, name);
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

  const columns = projectionColumns(input.model, input.metadata, input.prepared);
  if (isFallback(columns)) {
    return columns;
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
  const sql = kysely.sql;
  const merged = mergeConstraint(input.where, input.constraint);
  let predicate;
  if (merged !== undefined) {
    try {
      predicate = sqlNodeToRaw(
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
      );
    } catch (error) {
      if (isUnsupported(error)) {
        return fallback('where', (error as Error).message);
      }
      throw error;
    }
  }

  const db = dialect.createKysely(kysely);
  let query = db.selectFrom(
    sql`${sql.id(input.model.dbName)}`.as(ROOT_ALIAS) as never,
  ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
  for (const column of columns) {
    query = query.select(sql`${sql.id(ROOT_ALIAS, column.dbName)}`.as(column.name) as never);
  }
  if (predicate !== undefined) {
    query = query.where(sql`(${predicate})` as never);
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
    columns,
  };
}

import type { AliasedRawBuilder, RawBuilder, SelectQueryBuilder } from 'kysely';
import {
  UNSUPPORTED_CONDITION_ERROR_NAME,
  SqlRenderError,
  createDatamodelSqlScope,
  renderConstraintNode,
} from '@eleven-am/golem-policy';
import { mergeConstraint } from './authorization';
import {
  AggregateDecodeKind,
  AggregateGroup,
  CompiledMeasure,
} from './compiled-aggregate-decode';
import {
  CompiledReadFallback,
  CompiledReadFallbackReason,
} from './compiled-read';
import {
  DecimalConstructor,
  prismaDecimal,
  unsafeIntegerError,
} from './compiled-read-decode';
import { DatamodelField, DatamodelModel } from './datamodel';
import { ModelMetadataIndex } from './model-meta';
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

const MEASURE_GROUPS = ['_count', '_sum', '_avg', '_min', '_max'] as const;

const COUNT_ALL = '_all';

const MAX_IDENTIFIER = 63;

const ORDERED_FILTERS = new Set(['equals', 'not', 'in', 'notIn', 'lt', 'lte', 'gt', 'gte']);

export interface CompiledAggregateInput {
  readonly model: DatamodelModel;
  readonly models: readonly DatamodelModel[];
  readonly metadata: ModelMetadataIndex;
  readonly provider?: string;
  readonly where?: unknown;
  readonly constraint?: unknown;
  readonly by?: readonly string[];
  readonly having?: unknown;
  readonly orderBy?: unknown;
  readonly take?: number;
  readonly skip?: number;
  readonly cursor?: unknown;
  readonly _count?: unknown;
  readonly _sum?: unknown;
  readonly _avg?: unknown;
  readonly _min?: unknown;
  readonly _max?: unknown;
}

export interface CompiledAggregateStatement {
  readonly kind: 'compiled';
  readonly sql: string;
  readonly parameters: readonly unknown[];
  readonly measures: readonly CompiledMeasure[];
  readonly keys: readonly string[];
  readonly decimal: DecimalConstructor | null;
}

export type CompiledAggregatePlan = CompiledAggregateStatement | CompiledReadFallback;

interface PlannedMeasure extends CompiledMeasure {
  readonly column?: string;
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

function measureDecode(
  group: AggregateGroup,
  field: DatamodelField,
): AggregateDecodeKind | undefined {
  if (group === '_count') {
    return field.kind === 'object' ? undefined : 'safeInteger';
  }
  if (field.kind !== 'scalar') {
    return undefined;
  }
  if (group === '_sum' || group === '_avg') {
    switch (field.type) {
      case 'Int':
        return group === '_sum' ? 'safeInteger' : 'float';
      case 'Float':
        return 'float';
      case 'BigInt':
        return group === '_sum' ? 'bigint' : 'float';
      case 'Decimal':
        return 'decimal';
      default:
        return undefined;
    }
  }
  switch (field.type) {
    case 'Int':
      return 'safeInteger';
    case 'Float':
      return 'float';
    case 'BigInt':
      return 'bigint';
    case 'Decimal':
      return 'decimal';
    case 'String':
      return 'string';
    case 'DateTime':
      return 'datetime';
    default:
      return undefined;
  }
}

function measurableField(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  group: AggregateGroup,
  name: string,
): { field: DatamodelField; decode: AggregateDecodeKind } | CompiledReadFallback {
  const field = metadata.get(model.name)?.fieldsByName.get(name);
  if (field === undefined) {
    return fallback(
      'measure',
      `${model.name}.${name} is not a field of the datamodel golem compiled the aggregate against`,
    );
  }
  if (field.kind === 'object') {
    return fallback('measure', `${model.name}.${name} is a relation, not a column to aggregate`);
  }
  if (field.isList) {
    return fallback('measure', `${model.name}.${name} is a list column`);
  }
  if (field.dbName == null) {
    return fallback(
      'measure',
      `${model.name}.${name} carries no physical column name; regenerate the golem client`,
    );
  }
  const decode = measureDecode(group, field);
  if (decode === undefined) {
    return fallback(
      'measure',
      `golem does not compile ${group} over ${model.name}.${name}, which is a ${field.type}`,
    );
  }
  return { field, decode };
}

function planMeasures(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  input: CompiledAggregateInput,
): readonly PlannedMeasure[] | CompiledReadFallback {
  const measures: PlannedMeasure[] = [];
  for (const group of MEASURE_GROUPS) {
    const requested = input[group];
    if (requested === undefined) {
      continue;
    }
    if (requested === true) {
      if (group !== '_count') {
        return fallback('measure', `${group} on ${model.name} is asked for as true`);
      }
      measures.push({ alias: `_count$${COUNT_ALL}`, group, decode: 'safeInteger' });
      continue;
    }
    if (!requested || typeof requested !== 'object' || Array.isArray(requested)) {
      return fallback(
        'measure',
        `${group} on ${model.name} is asked for as ${JSON.stringify(requested)}`,
      );
    }
    for (const [name, wanted] of Object.entries(requested as Record<string, unknown>)) {
      if (wanted === false || wanted === undefined) {
        continue;
      }
      if (wanted !== true) {
        return fallback(
          'measure',
          `${group}.${name} on ${model.name} is asked for as ${JSON.stringify(wanted)}`,
        );
      }
      if (name === COUNT_ALL) {
        if (group !== '_count') {
          return fallback('measure', `${group} on ${model.name} names ${COUNT_ALL}`);
        }
        measures.push({ alias: `_count$${COUNT_ALL}`, group, field: COUNT_ALL, decode: 'safeInteger' });
        continue;
      }
      const resolved = measurableField(model, metadata, group, name);
      if (isFallback(resolved)) {
        return resolved;
      }
      const alias = `${group}$${resolved.field.dbName}`;
      if (alias.length > MAX_IDENTIFIER) {
        return fallback(
          'measure',
          `${group}.${name} on ${model.name} needs the result column "${alias}", which is longer than the ${MAX_IDENTIFIER} characters postgres keeps before it truncates an identifier`,
        );
      }
      measures.push({
        alias,
        group,
        field: name,
        decode: resolved.decode,
        column: resolved.field.dbName,
      });
    }
  }
  if (measures.length === 0 && (input.by ?? []).length === 0) {
    return fallback('measure', `${model.name} is aggregated without a single measure`);
  }
  return measures;
}

function planKeys(
  model: DatamodelModel,
  metadata: ModelMetadataIndex,
  by: readonly string[],
): readonly { name: string; dbName: string }[] | CompiledReadFallback {
  const keys: { name: string; dbName: string }[] = [];
  const seen = new Set<string>();
  for (const name of by) {
    if (seen.has(name)) {
      return fallback('group', `${model.name} is grouped by ${name} twice`);
    }
    seen.add(name);
    const field = metadata.get(model.name)?.fieldsByName.get(name);
    if (field === undefined) {
      return fallback(
        'group',
        `${model.name}.${name} is not a field of the datamodel golem compiled the grouping against`,
      );
    }
    if (field.kind === 'object' || field.isList) {
      return fallback('group', `${model.name}.${name} is not a column golem can group by`);
    }
    if (field.dbName == null) {
      return fallback(
        'group',
        `${model.name}.${name} carries no physical column name; regenerate the golem client`,
      );
    }
    if (name.startsWith('_')) {
      return fallback(
        'group',
        `${model.name}.${name} is grouped by under a name golem reserves for a measure`,
      );
    }
    if (name.length > MAX_IDENTIFIER) {
      return fallback(
        'group',
        `${model.name}.${name} needs the result column "${name}", which is longer than the ${MAX_IDENTIFIER} characters postgres keeps before it truncates an identifier`,
      );
    }
    keys.push({ name, dbName: field.dbName });
  }
  return keys;
}

class AggregateBuilder {
  constructor(
    private readonly kysely: KyselyModule,
    private readonly model: DatamodelModel,
    private readonly metadata: ModelMetadataIndex,
  ) {}

  expression(group: AggregateGroup, column: string | undefined): RawBuilder<unknown> {
    const sql = this.kysely.sql;
    const call = group.slice(1);
    return column === undefined
      ? sql`${sql.raw(call)}(*)`
      : sql`${sql.raw(call)}(${sql.id(ROOT_ALIAS, column)})`;
  }

  projection(measure: PlannedMeasure): AliasedRawBuilder<unknown, string> {
    const sql = this.kysely.sql;
    const expression = this.expression(measure.group, measure.column);
    const carried =
      measure.decode === 'bigint' || measure.decode === 'decimal'
        ? sql`cast(${expression} as text)`
        : expression;
    return carried.as(measure.alias) as AliasedRawBuilder<unknown, string>;
  }

  measure(
    group: AggregateGroup,
    name: string,
  ): RawBuilder<unknown> | CompiledReadFallback {
    if (name === COUNT_ALL) {
      if (group !== '_count') {
        return fallback('having', `${group} on ${this.model.name} names ${COUNT_ALL}`);
      }
      return this.expression(group, undefined);
    }
    const resolved = measurableField(this.model, this.metadata, group, name);
    if (isFallback(resolved)) {
      return { ...resolved, reason: 'having' };
    }
    return this.expression(group, resolved.field.dbName);
  }
}

function havingFilter(
  kysely: KyselyModule,
  expression: RawBuilder<unknown>,
  filter: unknown,
  label: string,
): RawBuilder<unknown> | CompiledReadFallback {
  const sql = kysely.sql;
  if (filter === null) {
    return sql`${expression} is null`;
  }
  if (typeof filter !== 'object' || Array.isArray(filter) || filter instanceof Date) {
    return sql`${expression} = ${sql.val(filter)}`;
  }
  const clauses: RawBuilder<unknown>[] = [];
  for (const [operator, operand] of Object.entries(filter as Record<string, unknown>)) {
    if (operand === undefined) {
      continue;
    }
    if (!ORDERED_FILTERS.has(operator)) {
      return fallback('having', `${label} is filtered by ${operator}, which golem does not compile`);
    }
    if (operator === 'in' || operator === 'notIn') {
      if (!Array.isArray(operand)) {
        return fallback('having', `${label} is filtered by ${operator} against a non-list`);
      }
      if (operand.some((entry) => entry === null)) {
        return fallback('having', `${label} is filtered by ${operator} against a list holding null`);
      }
      if (operand.length === 0) {
        clauses.push(operator === 'in' ? sql`1 = 0` : sql`1 = 1`);
        continue;
      }
      const list = sql.join(operand.map((entry) => sql.val(entry)));
      clauses.push(
        operator === 'in'
          ? sql`${expression} in (${list})`
          : sql`${expression} not in (${list})`,
      );
      continue;
    }
    if (operand === null) {
      if (operator === 'equals') {
        clauses.push(sql`${expression} is null`);
        continue;
      }
      if (operator === 'not') {
        clauses.push(sql`${expression} is not null`);
        continue;
      }
      return fallback('having', `${label} is filtered by ${operator} against null`);
    }
    if (operator === 'equals') {
      clauses.push(sql`${expression} = ${sql.val(operand)}`);
      continue;
    }
    if (operator === 'not') {
      clauses.push(sql`${expression} <> ${sql.val(operand)}`);
      continue;
    }
    const comparison = operator === 'lt' ? '<' : operator === 'lte' ? '<=' : operator === 'gt' ? '>' : '>=';
    clauses.push(sql`${expression} ${sql.raw(comparison)} ${sql.val(operand)}`);
  }
  if (clauses.length === 0) {
    return sql`1 = 1`;
  }
  return sql.join(clauses, sql` and `) as RawBuilder<unknown>;
}

function havingNode(
  kysely: KyselyModule,
  builder: AggregateBuilder,
  model: DatamodelModel,
  having: unknown,
): RawBuilder<unknown> | CompiledReadFallback {
  const sql = kysely.sql;
  if (having === null || having === undefined) {
    return sql`1 = 1`;
  }
  if (typeof having !== 'object' || Array.isArray(having)) {
    return fallback('having', `${model.name} is grouped with having ${JSON.stringify(having)}`);
  }
  const clauses: RawBuilder<unknown>[] = [];
  for (const [key, value] of Object.entries(having as Record<string, unknown>)) {
    if (value === undefined) {
      continue;
    }
    if (key === 'AND' || key === 'OR') {
      const entries = Array.isArray(value) ? value : [value];
      const rendered: RawBuilder<unknown>[] = [];
      for (const entry of entries) {
        const nested = havingNode(kysely, builder, model, entry);
        if (isFallback(nested)) {
          return nested;
        }
        rendered.push(sql`(${nested})`);
      }
      if (rendered.length === 0) {
        clauses.push(key === 'AND' ? sql`1 = 1` : sql`1 = 0`);
        continue;
      }
      clauses.push(
        sql`(${sql.join(rendered, key === 'AND' ? sql` and ` : sql` or `)})`,
      );
      continue;
    }
    if (key === 'NOT') {
      const entries = Array.isArray(value) ? value : [value];
      for (const entry of entries) {
        const nested = havingNode(kysely, builder, model, entry);
        if (isFallback(nested)) {
          return nested;
        }
        clauses.push(sql`not (${nested})`);
      }
      continue;
    }
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      return fallback(
        'having',
        `${model.name}.${key} is filtered in having by ${JSON.stringify(value)} rather than by a measure`,
      );
    }
    for (const [group, filter] of Object.entries(value as Record<string, unknown>)) {
      if (filter === undefined) {
        continue;
      }
      if (!(MEASURE_GROUPS as readonly string[]).includes(group)) {
        return fallback(
          'having',
          `${model.name}.${key} is filtered in having by ${group}, which is not a measure`,
        );
      }
      const expression = builder.measure(group as AggregateGroup, key);
      if (isFallback(expression)) {
        return expression;
      }
      const rendered = havingFilter(kysely, expression, filter, `${group} of ${model.name}.${key}`);
      if (isFallback(rendered)) {
        return rendered;
      }
      clauses.push(sql`(${rendered})`);
    }
  }
  if (clauses.length === 0) {
    return sql`1 = 1`;
  }
  return sql.join(clauses, sql` and `) as RawBuilder<unknown>;
}

interface GroupOrderTerm {
  readonly expression: RawBuilder<unknown>;
  readonly direction: 'asc' | 'desc';
}

function groupOrderTerms(
  kysely: KyselyModule,
  builder: AggregateBuilder,
  model: DatamodelModel,
  keys: readonly { name: string; dbName: string }[],
  orderBy: unknown,
): readonly GroupOrderTerm[] | CompiledReadFallback {
  if (orderBy === undefined || orderBy === null) {
    return [];
  }
  const sql = kysely.sql;
  const byName = new Map(keys.map((key) => [key.name, key.dbName] as const));
  const entries = Array.isArray(orderBy) ? orderBy : [orderBy];
  const terms: GroupOrderTerm[] = [];
  for (const entry of entries) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      return fallback('orderBy', `${model.name} is grouped and ordered by ${JSON.stringify(entry)}`);
    }
    for (const [key, value] of Object.entries(entry as Record<string, unknown>)) {
      if (value === undefined) {
        continue;
      }
      if ((MEASURE_GROUPS as readonly string[]).includes(key)) {
        if (!value || typeof value !== 'object' || Array.isArray(value)) {
          return fallback(
            'orderBy',
            `${model.name} is ordered by ${key} ${JSON.stringify(value)}`,
          );
        }
        for (const [name, direction] of Object.entries(value as Record<string, unknown>)) {
          if (direction === undefined) {
            continue;
          }
          if (direction !== 'asc' && direction !== 'desc') {
            return fallback(
              'orderBy',
              `${model.name} is ordered by ${key}.${name} ${JSON.stringify(direction)}`,
            );
          }
          const expression = builder.measure(key as AggregateGroup, name);
          if (isFallback(expression)) {
            return { ...expression, reason: 'orderBy' };
          }
          terms.push({ expression, direction });
        }
        continue;
      }
      if (value !== 'asc' && value !== 'desc') {
        return fallback(
          'orderBy',
          `${model.name}.${key} is ordered by ${JSON.stringify(value)}`,
        );
      }
      const dbName = byName.get(key);
      if (dbName === undefined) {
        return fallback(
          'orderBy',
          `${model.name} is ordered by ${key}, which is not one of the columns it groups by`,
        );
      }
      terms.push({ expression: sql`${sql.id(ROOT_ALIAS, dbName)}`, direction: value });
    }
  }
  return terms;
}

export async function planCompiledAggregate(
  input: CompiledAggregateInput,
): Promise<CompiledAggregatePlan> {
  const grouped = input.by !== undefined;
  const model = input.model;
  if (input.provider === undefined || !COMPILABLE_PROVIDERS.has(input.provider)) {
    return fallback(
      'provider',
      `golem compiles aggregates for sqlite and postgresql, not ${input.provider ?? 'an unknown provider'}`,
    );
  }
  if (model.dbName == null) {
    return fallback(
      'measure',
      `model ${model.name} carries no physical table name; regenerate the golem client`,
    );
  }
  if (input.cursor !== undefined) {
    return fallback('cursor', `${model.name} is aggregated from a cursor`);
  }
  if (input.where === null) {
    return fallback('where', `${model.name} is aggregated with a null where`);
  }
  if (input.constraint === null) {
    return fallback(
      'where',
      `the read policy on ${model.name} is null, which Prisma rejects rather than reading as a denial`,
    );
  }
  if (!grouped && (input.take !== undefined || input.skip !== undefined || input.orderBy !== undefined)) {
    return fallback(
      'take',
      `${model.name} is aggregated over a window Prisma fences off with a subquery, which golem does not compile`,
    );
  }
  if (grouped) {
    for (const [name, value] of [['take', input.take], ['skip', input.skip]] as const) {
      if (value !== undefined && (!Number.isInteger(value) || value < 0)) {
        return fallback('take', `${name} ${value} on ${model.name} is not a non-negative integer`);
      }
    }
  } else if (input.having !== undefined) {
    return fallback('having', `${model.name} is aggregated with a having but no grouping`);
  }

  const measures = planMeasures(model, input.metadata, input);
  if (isFallback(measures)) {
    return measures;
  }
  const keys = grouped ? planKeys(model, input.metadata, input.by!) : [];
  if (isFallback(keys)) {
    return keys;
  }
  if (grouped && keys.length === 0) {
    return fallback('group', `${model.name} is grouped by nothing`);
  }
  const decimal = prismaDecimal();
  if (decimal === null && measures.some((measure) => measure.decode === 'decimal')) {
    return fallback(
      'decoder',
      `${model.name} is aggregated over a Decimal, and golem could not load the Decimal class Prisma decodes one into`,
    );
  }
  if (
    unsafeIntegerError() === null &&
    measures.some((measure) => measure.decode === 'safeInteger')
  ) {
    return fallback(
      'decoder',
      `${model.name} is aggregated into a JavaScript number, and golem could not load the error Prisma raises when that number would lose precision`,
    );
  }

  const kysely = await kyselyModule();
  const dialect: ScopedDialect = resolveScopedDialect(input.provider);
  const sql = kysely.sql;
  const db = dialect.createKysely(kysely);
  const builder = new AggregateBuilder(kysely, model, input.metadata);
  const merged = mergeConstraint(input.where, input.constraint);

  let query = db.selectFrom(
    sql`${sql.id(model.dbName)}`.as(ROOT_ALIAS) as never,
  ) as unknown as SelectQueryBuilder<ScopedDatabase, string, Record<string, unknown>>;
  for (const key of keys) {
    query = query.select(
      sql`${sql.id(ROOT_ALIAS, key.dbName)}`.as(key.name) as never,
    );
  }
  for (const measure of measures) {
    query = query.select(builder.projection(measure) as never);
  }
  try {
    if (merged !== undefined) {
      query = query.where(
        sql`(${sqlNodeToRaw(
          renderConstraintNode(merged, {
            scope: createDatamodelSqlScope({
              datamodel: { models: input.models },
              model: model.name,
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
  for (const key of keys) {
    query = query.groupBy(sql`${sql.id(ROOT_ALIAS, key.dbName)}` as never);
  }
  if (grouped && input.having !== undefined) {
    const predicate = havingNode(kysely, builder, model, input.having);
    if (isFallback(predicate)) {
      return predicate;
    }
    query = query.having(sql`(${predicate})` as never);
  }
  if (grouped) {
    const terms = groupOrderTerms(kysely, builder, model, keys, input.orderBy);
    if (isFallback(terms)) {
      return terms;
    }
    for (const term of terms) {
      query = query.orderBy(term.expression as never, term.direction);
    }
    if (input.take !== undefined) {
      query = query.limit(input.take);
    } else if (input.skip !== undefined && dialect.provider === 'sqlite') {
      query = query.limit(-1);
    }
    if (input.skip !== undefined) {
      query = query.offset(input.skip);
    }
  }

  const compiled = dialect
    .createCompiler(kysely)
    .compileQuery(query.toOperationNode() as never, kysely.createQueryId());
  return {
    kind: 'compiled',
    sql: compiled.sql,
    parameters: compiled.parameters,
    measures: measures.map((measure) => ({
      alias: measure.alias,
      group: measure.group,
      field: measure.field,
      decode: measure.decode,
    })),
    keys: keys.map((key) => key.name),
    decimal,
  };
}

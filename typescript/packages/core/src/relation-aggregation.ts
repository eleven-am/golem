import { canonicalToken } from './canonical';
import type {
  AggregationsConfig,
  DatamodelField,
  DatamodelModel,
  RelationDimensionConfig,
} from './datamodel';
import { GolemValidationError } from './errors';

export const DEFAULT_MAX_INTERMEDIATE_GROUPS = 10_000;
export const DEFAULT_RELATION_MAX_GROUPS = 100;

export interface RelationAggregationStep {
  sourceModel: string;
  relation: string;
  targetModel: string;
  fromFields: readonly string[];
  toFields: readonly string[];
}

export interface ResolvedRelationDimension {
  name: string;
  path: readonly string[];
  field: DatamodelField;
  steps: readonly RelationAggregationStep[];
}

export interface RelationAggregationPlan {
  model: string;
  dimensions: readonly DatamodelField[];
  measures: readonly DatamodelField[];
  relationDimensions: ReadonlyMap<string, ResolvedRelationDimension>;
  path: readonly RelationAggregationStep[];
  maxIntermediateGroups: number;
  maxGroups: number;
}

export type RelationAggregateSelection<TField extends string = string> =
  | boolean
  | Readonly<Record<TField | '_all', boolean>>;

export interface RelationGroupByRequest<
  TDimension extends string = string,
  TMeasure extends string = string,
> {
  context?: unknown;
  model: string;
  by: readonly TDimension[];
  where?: unknown;
  having?: RelationHaving<TMeasure> | PrismaRelationHaving<TMeasure>;
  orderBy?:
    | RelationOrderBy<TDimension, TMeasure>
    | readonly RelationOrderBy<TDimension, TMeasure>[];
  take?: number;
  skip?: number;
  _sum?: Readonly<Record<TMeasure, boolean>>;
  _avg?: Readonly<Record<TMeasure, boolean>>;
  _min?: Readonly<Record<TMeasure, boolean>>;
  _max?: Readonly<Record<TMeasure, boolean>>;
  _count?: RelationAggregateSelection<TMeasure>;
}

export type RelationGroupByRow<
  TDimension extends string = string,
  TMeasure extends string = string,
> = Record<TDimension, unknown> & {
  _count?: number | Partial<Record<TMeasure | '_all', number>>;
  _sum?: Partial<Record<TMeasure, unknown>>;
  _avg?: Partial<Record<TMeasure, unknown>>;
  _min?: Partial<Record<TMeasure, unknown>>;
  _max?: Partial<Record<TMeasure, unknown>>;
};

export interface RelationHaving<TMeasure extends string = string> {
  count?: Readonly<Record<string, unknown>>;
  sum?: Partial<Record<TMeasure, Readonly<Record<string, unknown>>>>;
  avg?: Partial<Record<TMeasure, Readonly<Record<string, unknown>>>>;
  min?: Partial<Record<TMeasure, Readonly<Record<string, unknown>>>>;
  max?: Partial<Record<TMeasure, Readonly<Record<string, unknown>>>>;
}

export type PrismaRelationHaving<TMeasure extends string = string> = Partial<
  Record<
    TMeasure,
    Partial<Record<'_sum' | '_avg' | '_min' | '_max', Readonly<Record<string, unknown>>>>
  >
>;

export interface RelationOrderBy<
  TDimension extends string = string,
  TMeasure extends string = string,
> {
  key?: Partial<Record<TDimension, 'asc' | 'desc'>>;
  count?: 'asc' | 'desc';
  sum?: Partial<Record<TMeasure, 'asc' | 'desc'>>;
  avg?: Partial<Record<TMeasure, 'asc' | 'desc'>>;
  min?: Partial<Record<TMeasure, 'asc' | 'desc'>>;
  max?: Partial<Record<TMeasure, 'asc' | 'desc'>>;
}

function positiveLimit(model: string, name: string, value: number): number {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`${name} for relation aggregation on ${model} must be a positive safe integer`);
  }
  return value;
}

function scalarField(model: DatamodelModel, name: string, purpose: string): DatamodelField {
  const field = model.fields.find((candidate) => candidate.name === name);
  if (!field || field.kind === 'object' || field.isList) {
    throw new Error(
      `Relation aggregation ${purpose} ${model.name}.${name} must be a scalar or enum field`,
    );
  }
  return field;
}

function resolvePath(
  root: DatamodelModel,
  alias: string,
  definition: RelationDimensionConfig,
  models: ReadonlyMap<string, DatamodelModel>,
): { steps: RelationAggregationStep[]; terminal: DatamodelModel } {
  if (!Array.isArray(definition.path) || definition.path.length === 0) {
    throw new Error(`Relation dimension ${root.name}.${alias} requires a non-empty path`);
  }
  const steps: RelationAggregationStep[] = [];
  let source = root;
  for (const relationName of definition.path) {
    const relation = source.fields.find((field) => field.name === relationName);
    if (!relation || relation.kind !== 'object') {
      throw new Error(
        `Relation dimension ${root.name}.${alias} references unknown relation ${source.name}.${relationName}`,
      );
    }
    if (relation.isList) {
      throw new Error(
        `Relation dimension ${root.name}.${alias} cannot traverse to-many relation ${source.name}.${relationName}`,
      );
    }
    const fromFields = relation.relationFromFields ?? [];
    const toFields = relation.relationToFields ?? [];
    if (fromFields.length === 0 || fromFields.length !== toFields.length) {
      throw new Error(
        `Relation dimension ${root.name}.${alias} requires explicit forward relation keys on ${source.name}.${relationName}`,
      );
    }
    for (const field of fromFields) scalarField(source, field, 'relation key');
    const target = models.get(relation.type);
    if (!target) {
      throw new Error(
        `Relation dimension ${root.name}.${alias} targets unknown model ${relation.type}`,
      );
    }
    for (const field of toFields) scalarField(target, field, 'target key');
    steps.push({
      sourceModel: source.name,
      relation: relationName,
      targetModel: target.name,
      fromFields: [...fromFields],
      toFields: [...toFields],
    });
    source = target;
  }
  return { steps, terminal: source };
}

export function buildRelationAggregationPlan(
  root: DatamodelModel,
  config: AggregationsConfig,
  models: ReadonlyMap<string, DatamodelModel>,
  defaults: { maxGroups?: number; maxIntermediateGroups?: number } = {},
): RelationAggregationPlan | undefined {
  const configured = Object.entries(config.relationDimensions ?? {});
  if (configured.length === 0) return undefined;

  const localNames = config.dimensions ?? [];
  const dimensions = localNames.map((name) => scalarField(root, name, 'dimension'));
  const measureNames = config.measures ?? [];
  const measures = measureNames.map((name) => scalarField(root, name, 'measure'));
  const relationDimensions = new Map<string, ResolvedRelationDimension>();
  let commonPathToken: string | undefined;
  let commonPath: readonly RelationAggregationStep[] = [];
  for (const [name, definition] of configured) {
    if (root.fields.some((field) => field.name === name)) {
      throw new Error(
        `Relation dimension ${root.name}.${name} collides with a model field`,
      );
    }
    const { steps, terminal } = resolvePath(root, name, definition, models);
    const token = canonicalToken(steps.map((step) => step.relation));
    if (commonPathToken !== undefined && token !== commonPathToken) {
      throw new Error(
        `Relation aggregation on ${root.name} cannot configure multiple relation paths`,
      );
    }
    commonPathToken = token;
    commonPath = steps;
    relationDimensions.set(name, {
      name,
      path: [...definition.path],
      field: scalarField(terminal, definition.field, 'terminal field'),
      steps,
    });
  }

  return {
    model: root.name,
    dimensions,
    measures,
    relationDimensions,
    path: commonPath,
    maxIntermediateGroups: positiveLimit(
      root.name,
      'maxIntermediateGroups',
      config.maxIntermediateGroups ??
        defaults.maxIntermediateGroups ??
        DEFAULT_MAX_INTERMEDIATE_GROUPS,
    ),
    maxGroups: positiveLimit(
      root.name,
      'maxGroups',
      config.maxGroups ?? defaults.maxGroups ?? DEFAULT_RELATION_MAX_GROUPS,
    ),
  };
}

export function selectedTrueFields(value: unknown): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [];
  return Object.entries(value as Record<string, unknown>)
    .filter(([, selected]) => selected === true)
    .map(([field]) => field);
}

export function relationTuple(row: Record<string, unknown>, fields: readonly string[]): unknown[] {
  return fields.map((field) => row[field]);
}

export function relationTupleToken(values: readonly unknown[]): string {
  return canonicalToken(values);
}

export function relationTupleWhere(
  fields: readonly string[],
  values: readonly unknown[],
): Record<string, unknown> {
  return Object.fromEntries(fields.map((field, index) => [field, values[index]]));
}

function decimalMethod(value: unknown, method: string): ((other: unknown) => unknown) | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const candidate = (value as Record<string, unknown>)[method];
  return typeof candidate === 'function'
    ? (candidate as (other: unknown) => unknown).bind(value)
    : undefined;
}

interface DecimalParts {
  coefficient: bigint;
  scale: number;
}

function decimalParts(value: unknown): DecimalParts | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const rendered = String(value);
  const match = /^([+-]?)(\d+)(?:\.(\d*))?(?:e([+-]?\d+))?$/i.exec(rendered);
  if (!match) return undefined;
  const fraction = match[3] ?? '';
  const exponent = Number(match[4] ?? 0);
  let coefficient = BigInt(`${match[1] === '-' ? '-' : ''}${match[2]}${fraction}`);
  let scale = fraction.length - exponent;
  if (scale < 0) {
    coefficient *= 10n ** BigInt(-scale);
    scale = 0;
  }
  return { coefficient, scale };
}

function renderDecimal({ coefficient, scale }: DecimalParts): string {
  const negative = coefficient < 0n;
  const digits = (negative ? -coefficient : coefficient).toString().padStart(scale + 1, '0');
  const rendered = scale === 0
    ? digits
    : `${digits.slice(0, -scale)}.${digits.slice(-scale)}`;
  return negative ? `-${rendered}` : rendered;
}

function exactDecimalSum(left: unknown, right: unknown): unknown | undefined {
  const a = decimalParts(left);
  const b = decimalParts(right);
  const Constructor = (left as { constructor?: unknown } | null)?.constructor;
  if (!a || !b || typeof Constructor !== 'function') return undefined;
  const scale = Math.max(a.scale, b.scale);
  const coefficient =
    a.coefficient * 10n ** BigInt(scale - a.scale) +
    b.coefficient * 10n ** BigInt(scale - b.scale);
  return new (Constructor as new (value: string) => unknown)(
    renderDecimal({ coefficient, scale }),
  );
}

export function addAggregationValues(left: unknown, right: unknown): unknown {
  if (left === null || left === undefined) return right;
  if (right === null || right === undefined) return left;
  if (typeof left === 'bigint' && typeof right === 'bigint') return left + right;
  if (typeof left === 'number' && typeof right === 'number') return left + right;
  const exact = exactDecimalSum(left, right);
  if (exact !== undefined) return exact;
  const plus = decimalMethod(left, 'plus') ?? decimalMethod(left, 'add');
  if (plus) return plus(right);
  throw new GolemValidationError('Cannot merge an unsupported aggregate numeric value');
}

function decimalConstructor(value: unknown): (new (input: string | number) => unknown) | undefined {
  const Constructor = (value as { constructor?: unknown } | null)?.constructor;
  return typeof Constructor === 'function'
    ? Constructor as new (input: string | number) => unknown
    : undefined;
}

function postgresDecimalScale(field: DatamodelField | undefined): number | undefined {
  if (field?.type !== 'Decimal') return undefined;
  const native = field.nativeType;
  if (native && ['Decimal', 'Numeric'].includes(native[0])) {
    const scale = Number(native[1][1]);
    if (Number.isSafeInteger(scale) && scale >= 0) return scale;
  }
  // Prisma's PostgreSQL default mapping for Decimal is decimal(65, 30).
  return 30;
}

function exactDecimalQuotient(
  sum: unknown,
  count: number,
  scale: number,
): unknown | undefined {
  const parts = decimalParts(sum);
  const Constructor = decimalConstructor(sum);
  if (!parts || !Constructor) return undefined;
  let numerator = parts.coefficient;
  let denominator = BigInt(count);
  if (scale >= parts.scale) {
    numerator *= 10n ** BigInt(scale - parts.scale);
  } else {
    denominator *= 10n ** BigInt(parts.scale - scale);
  }
  let coefficient = numerator / denominator;
  const remainder = numerator % denominator;
  if ((remainder < 0n ? -remainder : remainder) * 2n >= denominator) {
    coefficient += numerator < 0n ? -1n : 1n;
  }
  return new Constructor(renderDecimal({ coefficient, scale }));
}

export function divideAggregationValue(
  sum: unknown,
  count: number,
  options: { provider?: string; field?: DatamodelField } = {},
): unknown {
  if (sum === null || sum === undefined || count === 0) return null;
  if (typeof sum === 'number') return sum / count;
  if (typeof sum === 'bigint') return Number(sum) / count;
  if (options.provider === 'sqlite') {
    const Constructor = decimalConstructor(sum);
    if (Constructor) return new Constructor(Number(String(sum)) / count);
  }
  if (options.provider === 'postgresql') {
    const scale = postgresDecimalScale(options.field);
    if (scale !== undefined) {
      const quotient = exactDecimalQuotient(sum, count, scale);
      if (quotient !== undefined) return quotient;
    }
  }
  const divide =
    decimalMethod(sum, 'dividedBy') ??
    decimalMethod(sum, 'div') ??
    decimalMethod(sum, 'dividedToIntegerBy');
  if (divide) return divide(count);
  throw new GolemValidationError('Cannot average an unsupported aggregate numeric value');
}

export function compareAggregationValues(left: unknown, right: unknown): number {
  if (left === null || left === undefined) return right === null || right === undefined ? 0 : -1;
  if (right === null || right === undefined) return 1;
  const compared = decimalMethod(left, 'comparedTo') ?? decimalMethod(left, 'cmp');
  if (compared) return Number(compared(right));
  if (left instanceof Date && right instanceof Date) return left.getTime() - right.getTime();
  if (typeof left === 'bigint' && typeof right === 'bigint') return left < right ? -1 : left > right ? 1 : 0;
  if (typeof left === 'number' && typeof right === 'number') return left - right;
  if (typeof left === 'string' && typeof right === 'string') return left.localeCompare(right);
  const a = canonicalToken(left);
  const b = canonicalToken(right);
  return a < b ? -1 : a > b ? 1 : 0;
}

export function matchesAggregationFilter(value: unknown, filter: unknown): boolean {
  if (!filter || typeof filter !== 'object' || Array.isArray(filter)) return true;
  for (const [operator, expected] of Object.entries(filter as Record<string, unknown>)) {
    if (!['equals', 'in', 'notIn', 'lt', 'lte', 'gt', 'gte', 'not'].includes(operator)) {
      throw new GolemValidationError(`Unsupported relation aggregation filter operator ${operator}`);
    }
    const comparison = compareAggregationValues(value, expected);
    if (operator === 'equals' && comparison !== 0) return false;
    if (operator === 'in' && (!Array.isArray(expected) || !expected.some((item) => compareAggregationValues(value, item) === 0))) return false;
    if (operator === 'notIn' && Array.isArray(expected) && expected.some((item) => compareAggregationValues(value, item) === 0)) return false;
    if (operator === 'lt' && comparison >= 0) return false;
    if (operator === 'lte' && comparison > 0) return false;
    if (operator === 'gt' && comparison <= 0) return false;
    if (operator === 'gte' && comparison < 0) return false;
    if (operator === 'not') {
      if (expected && typeof expected === 'object') {
        if (matchesAggregationFilter(value, expected)) return false;
      } else if (comparison === 0) return false;
    }
  }
  return true;
}

import {
  GraphQLBoolean,
  GraphQLEnumType,
  GraphQLError,
  GraphQLFloat,
  GraphQLInputObjectType,
  GraphQLInt,
  GraphQLList,
  GraphQLNonNull,
  GraphQLObjectType,
  GraphQLScalarType,
  Kind,
  type GraphQLInputFieldConfigMap,
  type GraphQLFieldConfigMap,
  type GraphQLInputType,
} from 'graphql';
import type { DatamodelField, DatamodelModel } from './datamodel';

const NUMERIC_TYPES = new Set(['Int', 'Float', 'BigInt', 'Decimal']);

function coerceSafeInt(value: unknown): number {
  if (typeof value === 'number' && Number.isSafeInteger(value)) {
    return value;
  }
  throw new GraphQLError(`SafeInt cannot represent non-integer value: ${String(value)}`);
}

const SafeIntScalar = new GraphQLScalarType({
  name: 'SafeInt',
  description: 'A signed integer that is exactly representable as a JavaScript number.',
  serialize: coerceSafeInt,
  parseValue: coerceSafeInt,
  parseLiteral: (ast) => {
    if (ast.kind === Kind.INT) {
      return coerceSafeInt(Number(ast.value));
    }
    throw new GraphQLError(
      `SafeInt cannot represent non-integer literal: ${'value' in ast ? String(ast.value) : ast.kind}`,
      { nodes: ast },
    );
  },
});

export const MEASURE_KINDS = ['sum', 'avg', 'min', 'max'] as const;

export type MeasureKind = (typeof MEASURE_KINDS)[number];

export interface AggregationFieldSets {
  dimensions: readonly DatamodelField[];
  measures: readonly DatamodelField[];
}

export interface AggregationTypeDeps {
  model: DatamodelModel;
  fields: AggregationFieldSets;
  sortOrder: GraphQLEnumType;
  dimensionType: (
    model: DatamodelModel,
    field: DatamodelField,
  ) => GraphQLScalarType | GraphQLEnumType;
  filterTypeFor: (
    name: string,
    type: GraphQLInputType,
    operators: readonly string[],
  ) => GraphQLInputObjectType;
}

export interface AggregationTypes {
  dimensionEnum?: GraphQLEnumType;
  measuresInput: GraphQLInputObjectType;
  havingInput?: GraphQLInputObjectType;
  orderByInput?: GraphQLInputObjectType;
  aggregateOutput: GraphQLObjectType;
  groupOutput?: GraphQLObjectType;
}

export function isMeasurable(field: DatamodelField): boolean {
  return (
    field.kind === 'scalar' && !field.isList && NUMERIC_TYPES.has(field.type)
  );
}

export function isGroupable(field: DatamodelField): boolean {
  return field.kind !== 'object' && !field.isList;
}

const NUMBER_OPERATORS = ['equals', 'in', 'lt', 'lte', 'gt', 'gte', 'not'];

export function buildAggregationTypes(
  deps: AggregationTypeDeps,
): AggregationTypes {
  const { model, fields, sortOrder, dimensionType, filterTypeFor } = deps;
  const measureEnum =
    fields.measures.length > 0
      ? new GraphQLEnumType({
          name: `${model.name}MeasureField`,
          values: Object.fromEntries(
            fields.measures.map((field) => [field.name, { value: field.name }]),
          ),
        })
      : undefined;

  const dimensionEnum =
    fields.dimensions.length > 0
      ? new GraphQLEnumType({
          name: `${model.name}GroupField`,
          values: Object.fromEntries(
            fields.dimensions.map((field) => [field.name, { value: field.name }]),
          ),
        })
      : undefined;

  const measuresInput = new GraphQLInputObjectType({
    name: `${model.name}MeasuresInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: GraphQLBoolean },
      };
      if (measureEnum) {
        for (const kind of MEASURE_KINDS) {
          config[kind] = {
            type: new GraphQLList(new GraphQLNonNull(measureEnum)),
          };
        }
      }
      return config;
    },
  });

  const hasMeasures = fields.measures.length > 0;

  const measureOutputScalar = (field: DatamodelField, kind: MeasureKind) => {
    if (kind === 'avg') {
      return field.type === 'Decimal'
        ? dimensionType(model, field)
        : GraphQLFloat;
    }
    if (kind === 'sum' && field.type === 'Int') {
      // A sum can exceed GraphQL Int's 32-bit range even though each source value cannot.
      return GraphQLFloat;
    }
    return dimensionType(model, field);
  };

  const measureInputScalar = (field: DatamodelField, kind: MeasureKind) => {
    if (kind === 'avg') {
      return field.type === 'Decimal'
        ? dimensionType(model, field)
        : GraphQLFloat;
    }
    if (kind === 'sum' && field.type === 'Int') {
      return SafeIntScalar;
    }
    return dimensionType(model, field);
  };

  const measureValues = Object.fromEntries(
    MEASURE_KINDS.map((kind) => [
      kind,
      new GraphQLObjectType({
        name: `${model.name}${kind[0].toUpperCase()}${kind.slice(1)}Values`,
        fields: () => {
          const config: GraphQLFieldConfigMap<unknown, unknown> = {};
          for (const field of fields.measures) {
            config[field.name] = { type: measureOutputScalar(field, kind) };
          }
          return config;
        },
      }),
    ]),
  ) as Record<MeasureKind, GraphQLObjectType>;

  const measureFieldsFor = (
    prefix: string,
  ): GraphQLFieldConfigMap<unknown, unknown> => {
    const config: GraphQLFieldConfigMap<unknown, unknown> = {
      count: { type: GraphQLInt },
    };
    if (hasMeasures) {
      for (const kind of MEASURE_KINDS) {
        config[kind] = { type: measureValues[kind] };
      }
    }
    void prefix;
    return config;
  };

  const aggregateOutput = new GraphQLObjectType({
    name: `${model.name}Aggregate`,
    fields: () => measureFieldsFor('aggregate'),
  });

  if (!dimensionEnum) {
    return { measuresInput, aggregateOutput };
  }

  const groupKey = new GraphQLObjectType({
    name: `${model.name}GroupKey`,
    fields: () => {
      const config: GraphQLFieldConfigMap<unknown, unknown> = {};
      for (const field of fields.dimensions) {
        config[field.name] = { type: dimensionType(model, field) };
      }
      return config;
    },
  });

  const groupOutput = new GraphQLObjectType({
    name: `${model.name}Group`,
    fields: () => ({
      key: { type: new GraphQLNonNull(groupKey) },
      ...measureFieldsFor('group'),
    }),
  });

  const intFilter = filterTypeFor('IntFilter', GraphQLInt, NUMBER_OPERATORS);

  const measureFilters = hasMeasures
    ? Object.fromEntries(
        MEASURE_KINDS.map((kind) => [
          kind,
          new GraphQLInputObjectType({
            name: `${model.name}${kind[0].toUpperCase()}${kind.slice(1)}FilterInput`,
            fields: () => {
              const config: GraphQLInputFieldConfigMap = {};
              for (const field of fields.measures) {
                const scalar = measureInputScalar(field, kind);
                config[field.name] = {
                  type: filterTypeFor(`${scalar.name}Filter`, scalar, NUMBER_OPERATORS),
                };
              }
              return config;
            },
          }),
        ]),
      ) as Record<MeasureKind, GraphQLInputObjectType>
    : undefined;

  const havingInput = new GraphQLInputObjectType({
    name: `${model.name}HavingInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: intFilter },
      };
      if (measureFilters) {
        for (const kind of MEASURE_KINDS) {
          config[kind] = { type: measureFilters[kind] };
        }
      }
      return config;
    },
  });

  const measureOrder = hasMeasures
    ? new GraphQLInputObjectType({
        name: `${model.name}MeasureOrderInput`,
        fields: () => {
          const config: GraphQLInputFieldConfigMap = {};
          for (const field of fields.measures) {
            config[field.name] = { type: sortOrder };
          }
          return config;
        },
      })
    : undefined;

  const orderByInput = new GraphQLInputObjectType({
    name: `${model.name}GroupOrderByInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: sortOrder },
      };
      if (measureOrder) {
        for (const kind of MEASURE_KINDS) {
          config[kind] = { type: measureOrder };
        }
      }
      return config;
    },
  });

  return {
    dimensionEnum,
    measuresInput,
    havingInput,
    orderByInput,
    aggregateOutput,
    groupOutput,
  };
}

export function byFieldsOrder(by: readonly string[]): unknown {
  return by.map((field) => ({ [field]: 'asc' }));
}

export interface MeasuresArg {
  count?: boolean | null;
  sum?: readonly string[] | null;
  avg?: readonly string[] | null;
  min?: readonly string[] | null;
  max?: readonly string[] | null;
}

export interface PrismaMeasures {
  _count?: unknown;
  _sum?: unknown;
  _avg?: unknown;
  _min?: unknown;
  _max?: unknown;
}

function trueMap(fields: readonly string[] | null | undefined): unknown {
  if (!fields || fields.length === 0) {
    return undefined;
  }
  return Object.fromEntries(fields.map((field) => [field, true]));
}

export function toPrismaMeasures(measures: MeasuresArg | null | undefined): PrismaMeasures {
  const result: PrismaMeasures = {};
  if (!measures) {
    return result;
  }
  if (measures.count) {
    result._count = true;
  }
  for (const kind of MEASURE_KINDS) {
    const mapped = trueMap(measures[kind]);
    if (mapped !== undefined) {
      result[`_${kind}` as keyof PrismaMeasures] = mapped;
    }
  }
  return result;
}

export function toPrismaHaving(
  having: Record<string, unknown> | null | undefined,
  countKey: string,
): unknown {
  if (!having) {
    return undefined;
  }
  const clauses: Record<string, unknown>[] = [];
  if (having.count !== undefined && having.count !== null) {
    clauses.push({ [countKey]: { _count: having.count } });
  }
  for (const kind of MEASURE_KINDS) {
    const entry = having[kind] as Record<string, unknown> | undefined | null;
    if (!entry) {
      continue;
    }
    for (const [field, filter] of Object.entries(entry)) {
      if (filter !== undefined && filter !== null) {
        clauses.push({ [field]: { [`_${kind}`]: filter } });
      }
    }
  }
  if (clauses.length === 0) {
    return undefined;
  }
  return clauses.length === 1 ? clauses[0] : { AND: clauses };
}

export function toPrismaGroupOrderBy(
  orderBy: Record<string, unknown> | null | undefined,
  countKey: string,
): unknown {
  if (!orderBy) {
    return undefined;
  }
  const entries: Record<string, unknown>[] = [];
  if (orderBy.count) {
    entries.push({ _count: { [countKey]: orderBy.count } });
  }
  for (const kind of MEASURE_KINDS) {
    const entry = orderBy[kind] as Record<string, unknown> | undefined | null;
    if (entry && Object.keys(entry).length > 0) {
      entries.push({ [`_${kind}`]: entry });
    }
  }
  if (entries.length === 0) {
    return undefined;
  }
  return entries;
}

export function toAggregateResult(
  result: Record<string, unknown>,
): Record<string, unknown> {
  return {
    count: typeof result._count === 'number' ? result._count : undefined,
    sum: result._sum,
    avg: result._avg,
    min: result._min,
    max: result._max,
  };
}

export function toGroupResults(
  rows: readonly Record<string, unknown>[],
  by: readonly string[],
): Record<string, unknown>[] {
  return rows.map((row) => {
    const key: Record<string, unknown> = {};
    for (const field of by) {
      key[field] = row[field];
    }
    return { key, ...toAggregateResult(row) };
  });
}

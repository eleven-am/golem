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
const ORDERABLE_TYPES = new Set([...NUMERIC_TYPES, 'DateTime', 'String']);

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
  orderables?: readonly DatamodelField[];
  countables?: readonly DatamodelField[];
}

export interface AggregationTypeDeps {
  model: DatamodelModel;
  fields: AggregationFieldSets;
  includeKeyOrder?: boolean;
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

export function isOrderable(field: DatamodelField): boolean {
  return (
    field.kind === 'scalar' && !field.isList && ORDERABLE_TYPES.has(field.type)
  );
}

export function isGroupable(field: DatamodelField): boolean {
  return field.kind !== 'object' && !field.isList;
}

const ORDERED_OPERATORS = ['lt', 'lte', 'gt', 'gte'];
const STRING_OPERATORS = ['contains', 'startsWith', 'endsWith'];

export function scalarFilterOperators(scalar: string): readonly string[] {
  return scalar === 'String'
    ? [...ORDERED_OPERATORS, ...STRING_OPERATORS]
    : ORDERED_OPERATORS;
}

const NUMERIC_KINDS: readonly MeasureKind[] = ['sum', 'avg'];

function sameFields(
  a: readonly DatamodelField[],
  b: readonly DatamodelField[],
): boolean {
  if (a.length !== b.length) {
    return false;
  }
  const names = new Set(b.map((field) => field.name));
  return a.every((field) => names.has(field.name));
}

function enumOf(
  name: string,
  entries: readonly DatamodelField[],
): GraphQLEnumType | undefined {
  if (entries.length === 0) {
    return undefined;
  }
  return new GraphQLEnumType({
    name,
    values: Object.fromEntries(
      entries.map((field) => [field.name, { value: field.name }]),
    ),
  });
}

export function buildAggregationTypes(
  deps: AggregationTypeDeps,
): AggregationTypes {
  const { model, fields, sortOrder, dimensionType, filterTypeFor } = deps;
  const orderables = fields.orderables ?? fields.measures;
  const countables = fields.countables ?? fields.dimensions;
  const orderablesMatchMeasures = sameFields(orderables, fields.measures);

  const measureEnum = enumOf(`${model.name}MeasureField`, fields.measures);
  const orderableEnum = orderablesMatchMeasures
    ? measureEnum
    : enumOf(`${model.name}OrderableField`, orderables);
  const dimensionEnum = enumOf(`${model.name}GroupField`, fields.dimensions);
  const countEnum = sameFields(countables, fields.dimensions)
    ? dimensionEnum
    : enumOf(`${model.name}CountField`, countables);

  const fieldsForKind = (kind: MeasureKind): readonly DatamodelField[] =>
    NUMERIC_KINDS.includes(kind) ? fields.measures : orderables;
  const enumForKind = (kind: MeasureKind): GraphQLEnumType | undefined =>
    NUMERIC_KINDS.includes(kind) ? measureEnum : orderableEnum;

  const measuresInput = new GraphQLInputObjectType({
    name: `${model.name}MeasuresInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: GraphQLBoolean },
      };
      if (countEnum) {
        config.countFields = {
          type: new GraphQLList(new GraphQLNonNull(countEnum)),
        };
      }
      for (const kind of MEASURE_KINDS) {
        const kindEnum = enumForKind(kind);
        if (kindEnum) {
          config[kind] = {
            type: new GraphQLList(new GraphQLNonNull(kindEnum)),
          };
        }
      }
      return config;
    },
  });

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

  const measureValues: Partial<Record<MeasureKind, GraphQLObjectType>> = {};
  for (const kind of MEASURE_KINDS) {
    const kindFields = fieldsForKind(kind);
    if (kindFields.length === 0) {
      continue;
    }
    measureValues[kind] = new GraphQLObjectType({
      name: `${model.name}${kind[0].toUpperCase()}${kind.slice(1)}Values`,
      fields: () => {
        const config: GraphQLFieldConfigMap<unknown, unknown> = {};
        for (const field of kindFields) {
          config[field.name] = { type: measureOutputScalar(field, kind) };
        }
        return config;
      },
    });
  }

  const countValues =
    countables.length > 0
      ? new GraphQLObjectType({
          name: `${model.name}CountValues`,
          fields: () => {
            const config: GraphQLFieldConfigMap<unknown, unknown> = {};
            for (const field of countables) {
              config[field.name] = { type: GraphQLInt };
            }
            return config;
          },
        })
      : undefined;

  const measureFieldsFor = (): GraphQLFieldConfigMap<unknown, unknown> => {
    const config: GraphQLFieldConfigMap<unknown, unknown> = {
      count: { type: GraphQLInt },
    };
    if (countValues) {
      config.countBy = { type: countValues };
    }
    for (const kind of MEASURE_KINDS) {
      const values = measureValues[kind];
      if (values) {
        config[kind] = { type: values };
      }
    }
    return config;
  };

  const aggregateOutput = new GraphQLObjectType({
    name: `${model.name}Aggregate`,
    fields: () => measureFieldsFor(),
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
      ...measureFieldsFor(),
    }),
  });

  const intFilter = filterTypeFor('IntFilter', GraphQLInt, ORDERED_OPERATORS);

  const measureFilters: Partial<Record<MeasureKind, GraphQLInputObjectType>> = {};
  for (const kind of MEASURE_KINDS) {
    const kindFields = fieldsForKind(kind);
    if (kindFields.length === 0) {
      continue;
    }
    measureFilters[kind] = new GraphQLInputObjectType({
      name: `${model.name}${kind[0].toUpperCase()}${kind.slice(1)}FilterInput`,
      fields: () => {
        const config: GraphQLInputFieldConfigMap = {};
        for (const field of kindFields) {
          const scalar = measureInputScalar(field, kind);
          config[field.name] = {
            type: filterTypeFor(
              `${scalar.name}Filter`,
              scalar,
              scalarFilterOperators(scalar.name),
            ),
          };
        }
        return config;
      },
    });
  }

  const havingInput = new GraphQLInputObjectType({
    name: `${model.name}HavingInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: intFilter },
      };
      for (const kind of MEASURE_KINDS) {
        const filter = measureFilters[kind];
        if (filter) {
          config[kind] = { type: filter };
        }
      }
      return config;
    },
  });

  const orderInputOf = (name: string, entries: readonly DatamodelField[]) =>
    entries.length > 0
      ? new GraphQLInputObjectType({
          name,
          fields: () => {
            const config: GraphQLInputFieldConfigMap = {};
            for (const field of entries) {
              config[field.name] = { type: sortOrder };
            }
            return config;
          },
        })
      : undefined;

  const measureOrder = orderInputOf(
    `${model.name}MeasureOrderInput`,
    fields.measures,
  );
  const orderableOrder = orderablesMatchMeasures
    ? measureOrder
    : orderInputOf(`${model.name}OrderableOrderInput`, orderables);

  const orderByInput = new GraphQLInputObjectType({
    name: `${model.name}GroupOrderByInput`,
    fields: () => {
      const config: GraphQLInputFieldConfigMap = {
        count: { type: sortOrder },
      };
      if (deps.includeKeyOrder) {
        const keyOrder = orderInputOf(`${model.name}GroupKeyOrderInput`, fields.dimensions);
        if (keyOrder) config.key = { type: keyOrder };
      }
      for (const kind of MEASURE_KINDS) {
        const order = NUMERIC_KINDS.includes(kind) ? measureOrder : orderableOrder;
        if (order) {
          config[kind] = { type: order };
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
  countFields?: readonly string[] | null;
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
  const countFields = trueMap(measures.countFields) as
    | Record<string, true>
    | undefined;
  if (countFields) {
    result._count = measures.count ? { _all: true, ...countFields } : countFields;
  } else if (measures.count) {
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

function splitCount(value: unknown): {
  count?: number;
  countBy?: Record<string, unknown>;
} {
  if (typeof value === 'number') {
    return { count: value };
  }
  if (!value || typeof value !== 'object') {
    return {};
  }
  const entries = Object.entries(value as Record<string, unknown>);
  const all = entries.find(([key]) => key === '_all')?.[1];
  const rest = entries.filter(([key]) => key !== '_all');
  return {
    count: typeof all === 'number' ? all : undefined,
    countBy: rest.length > 0 ? Object.fromEntries(rest) : undefined,
  };
}

export function toAggregateResult(
  result: Record<string, unknown>,
): Record<string, unknown> {
  const { count, countBy } = splitCount(result._count);
  return {
    count,
    countBy,
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

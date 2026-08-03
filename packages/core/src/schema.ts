import {
  GraphQLBoolean,
  GraphQLEnumType,
  GraphQLError,
  GraphQLFieldConfigArgumentMap,
  GraphQLFieldConfigMap,
  GraphQLFieldResolver,
  GraphQLFloat,
  GraphQLID,
  GraphQLInputFieldConfigMap,
  GraphQLInputObjectType,
  GraphQLInputType,
  GraphQLInt,
  GraphQLList,
  GraphQLNonNull,
  GraphQLObjectType,
  GraphQLOutputType,
  GraphQLScalarType,
  GraphQLSchema,
  GraphQLString,
  GraphQLType,
  Kind,
} from 'graphql';
import { AuthorizationProvider } from './authorization';
import { BatchScope, clearBatchCaches } from './batch';
import {
  GolemDefaults,
  DatamodelDocument,
  DatamodelField,
  DatamodelModel,
  ModelConfig,
  ModelsConfig,
} from './datamodel';
import { GolemError, GolemValidationError } from './errors';
import { GolemEventBus, eventTopic } from './events';
import {
  ComputedFieldSpec,
  CustomOperationSpec,
  buildComputedRequiresMap,
  computedFieldRequires,
  computedFieldResolver,
} from './extensions';
import { ALL_OPERATIONS, GolemOperation, HookRegistry } from './hooks';
import { InputTypeRegistry } from './inputs';
import {
  aggregateFieldName,
  createFieldName,
  deleteFieldName,
  deleteManyFieldName,
  eventsFieldName,
  findManyFieldName,
  findOneFieldName,
  groupByFieldName,
  relationCountTypeName,
  updateFieldName,
  updateManyFieldName,
} from './naming';
import {
  buildAggregationTypes,
  byFieldsOrder,
  isGroupable,
  isMeasurable,
  isOrderable,
  scalarFilterOperators,
  toAggregateResult,
  toGroupResults,
  toPrismaGroupOrderBy,
  toPrismaHaving,
  toPrismaMeasures,
  type MeasuresArg,
} from './aggregations';
import { GolemEngine } from './operations';
import {
  RELATION_COUNT_FIELD,
  buildEventEntitySelect,
  buildSelect,
  primaryKeySelect,
} from './select';

export const DateTimeScalar = new GraphQLScalarType({
  name: 'DateTime',
  serialize: (value) => (value instanceof Date ? value.toISOString() : value),
  parseValue: (value) => new Date(value as string),
  parseLiteral: (ast) => (ast.kind === Kind.STRING ? new Date(ast.value) : null),
});

const SUBSCRIPTION_ABANDONED = Symbol('golem.subscription.abandoned');

const BIGINT_STRING = /^-?\d+$/;

function coerceBigInt(value: unknown): bigint {
  if (typeof value === 'bigint') {
    return value;
  }
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) {
      throw new GraphQLError(`BigInt cannot represent non-integer value: ${value}`);
    }
    return BigInt(value);
  }
  if (typeof value === 'string' && BIGINT_STRING.test(value)) {
    return BigInt(value);
  }
  throw new GraphQLError(
    `BigInt cannot represent value: ${typeof value === 'string' ? JSON.stringify(value) : String(value)}`,
  );
}

export const BigIntScalar = new GraphQLScalarType({
  name: 'BigInt',
  serialize: (value) => coerceBigInt(value).toString(),
  parseValue: (value) => coerceBigInt(value),
  parseLiteral: (ast) => {
    if (ast.kind === Kind.INT) {
      return BigInt(ast.value);
    }
    if (ast.kind === Kind.STRING && BIGINT_STRING.test(ast.value)) {
      return BigInt(ast.value);
    }
    throw new GraphQLError(
      `BigInt cannot represent literal: ${'value' in ast ? String(ast.value) : ast.kind}`,
      { nodes: ast },
    );
  },
});

const DECIMAL_STRING = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;

function coerceDecimal(value: unknown): string {
  const serialized =
    typeof value === 'string'
      ? value
      : value && typeof value === 'object' && typeof (value as { toString?: unknown }).toString === 'function'
        ? String(value)
        : undefined;
  if (serialized !== undefined && DECIMAL_STRING.test(serialized)) {
    return serialized;
  }
  throw new GraphQLError(
    `Decimal cannot represent value: ${typeof value === 'string' ? JSON.stringify(value) : String(value)}`,
  );
}

export const DecimalScalar = new GraphQLScalarType({
  name: 'Decimal',
  description: 'An arbitrary-precision decimal serialized as a string.',
  serialize: (value) => coerceDecimal(value),
  parseValue: (value) => coerceDecimal(value),
  parseLiteral: (ast) => {
    if (
      (ast.kind === Kind.STRING || ast.kind === Kind.INT || ast.kind === Kind.FLOAT) &&
      DECIMAL_STRING.test(ast.value)
    ) {
      return ast.value;
    }
    throw new GraphQLError(
      `Decimal cannot represent literal: ${'value' in ast ? String(ast.value) : ast.kind}`,
      { nodes: ast },
    );
  },
});

const SCALAR_MAP: Record<string, GraphQLScalarType> = {
  String: GraphQLString as unknown as GraphQLScalarType,
  Int: GraphQLInt as unknown as GraphQLScalarType,
  Float: GraphQLFloat as unknown as GraphQLScalarType,
  Boolean: GraphQLBoolean as unknown as GraphQLScalarType,
  DateTime: DateTimeScalar,
  BigInt: BigIntScalar,
  Decimal: DecimalScalar,
};

export interface BuildGolemSchemaOptions<TModels = Record<string, string>> {
  datamodel: DatamodelDocument<TModels>;
  client: Record<string, any>;
  models?: ModelsConfig<TModels>;
  defaults?: GolemDefaults;
  eventBus?: GolemEventBus;
  hooks?: HookRegistry;
  engine?: GolemEngine;
  computedFields?: readonly ComputedFieldSpec[];
  customOperations?: readonly CustomOperationSpec[];
  authorization?: AuthorizationProvider;
}

interface ResolvedModelSettings {
  operations: ReadonlySet<GolemOperation>;
  hidden: ReadonlySet<string>;
  immutable: ReadonlySet<string>;
  readOnly: ReadonlySet<string>;
  writeOnly: ReadonlySet<string>;
  maxTake?: number;
  subscriptions: boolean;
  aggregations: boolean;
  dimensions?: ReadonlySet<string>;
  measures?: ReadonlySet<string>;
  maxGroups?: number;
}

function resolveModelSettings(
  model: DatamodelModel,
  config: ModelConfig | undefined,
  defaults: GolemDefaults,
): ResolvedModelSettings {
  const fieldNames = new Set(model.fields.map((f) => f.name));
  const hidden = new Set(config?.hidden ?? []);
  const immutable = new Set(config?.immutable ?? []);
  const readOnly = new Set(config?.readOnly ?? []);
  const writeOnly = new Set(config?.writeOnly ?? []);
  for (const name of [...hidden, ...immutable, ...readOnly, ...writeOnly]) {
    if (!fieldNames.has(name)) {
      throw new Error(`Unknown field ${name} in configuration for model ${model.name}`);
    }
  }
  for (const field of model.fields) {
    if (field.isId && hidden.has(field.name)) {
      throw new Error(`Cannot hide primary key ${model.name}.${field.name}`);
    }
    if (writeOnly.has(field.name) && field.isId) {
      throw new Error(`Cannot make primary key ${model.name}.${field.name} write-only`);
    }
    if (writeOnly.has(field.name) && field.kind === 'object') {
      throw new Error(`Cannot make relation field ${model.name}.${field.name} write-only`);
    }
    if (writeOnly.has(field.name) && field.isReadOnly) {
      throw new Error(`Cannot make Prisma read-only field ${model.name}.${field.name} write-only`);
    }
  }
  const accessModes = [
    ['hidden', hidden],
    ['immutable', immutable],
    ['readOnly', readOnly],
    ['writeOnly', writeOnly],
  ] as const;
  for (const field of model.fields) {
    const configured = accessModes
      .filter(([, fields]) => fields.has(field.name))
      .map(([name]) => name);
    const allowed =
      configured.length <= 1 ||
      (configured.length === 2 &&
        configured.includes('immutable') &&
        configured.includes('writeOnly'));
    if (!allowed) {
      throw new Error(
        `Conflicting field configuration for ${model.name}.${field.name}: ${configured.join(', ')}`,
      );
    }
  }
  const operations = config?.operations ?? defaults.operations ?? ALL_OPERATIONS;
  for (const operation of operations) {
    if (!ALL_OPERATIONS.includes(operation)) {
      throw new Error(`Unknown operation ${operation} in configuration for model ${model.name}`);
    }
  }
  return {
    operations: new Set(operations),
    hidden,
    immutable,
    readOnly,
    writeOnly,
    maxTake: config?.maxTake ?? defaults.maxTake,
    subscriptions: config?.subscriptions ?? defaults.subscriptions ?? false,
    ...resolveAggregations(config?.aggregations, defaults),
  };
}

function resolveAggregations(
  config: ModelConfig['aggregations'],
  defaults: GolemDefaults,
): Pick<
  ResolvedModelSettings,
  'aggregations' | 'dimensions' | 'measures' | 'maxGroups'
> {
  if (config === undefined) {
    return {
      aggregations: defaults.aggregations ?? false,
      maxGroups: defaults.maxGroups,
    };
  }
  if (typeof config === 'boolean') {
    return { aggregations: config, maxGroups: defaults.maxGroups };
  }
  return {
    aggregations: true,
    dimensions: config.dimensions ? new Set(config.dimensions) : undefined,
    measures: config.measures ? new Set(config.measures) : undefined,
    maxGroups: config.maxGroups ?? defaults.maxGroups,
  };
}

function written<T>(ctx: unknown, scope: BatchScope, result: Promise<T>): Promise<T> {
  return result.then((value) => {
    clearBatchCaches(ctx, scope);
    return value;
  });
}

function wrapResolve(
  resolve: GraphQLFieldResolver<unknown, unknown>,
): GraphQLFieldResolver<unknown, unknown> {
  return async (root, args, ctx, info) => {
    try {
      return await resolve(root, args, ctx, info);
    } catch (error) {
      if (error instanceof GolemError) {
        throw new GraphQLError(error.message, { extensions: { code: error.code } });
      }
      throw error;
    }
  };
}

interface ResolvedGolem {
  models: readonly DatamodelModel[];
  engineModels: readonly DatamodelModel[];
  modelsByName: Map<string, DatamodelModel>;
  settings: Map<string, ResolvedModelSettings>;
  subscribable: Set<string>;
  takeLimits: Map<string, number>;
  groupLimits: Map<string, number>;
}

function resolveGolem<TModels>(options: BuildGolemSchemaOptions<TModels>): ResolvedGolem {
  const excluded = new Set<string>(
    Object.entries(options.models ?? {})
      .filter(([, value]) => value === false)
      .map(([key]) => key),
  );
  const models = options.datamodel.models.filter((m) => !excluded.has(m.name));
  const modelsByName = new Map(models.map((m) => [m.name, m]));

  const settings = new Map<string, ResolvedModelSettings>(
    models.map((m) => [
      m.name,
      resolveModelSettings(
        m,
        (options.models?.[m.name as keyof TModels] || undefined) as ModelConfig | undefined,
        options.defaults ?? {},
      ),
    ]),
  );

  const subscribable = new Set<string>(
    models.filter((m) => settings.get(m.name)!.subscriptions).map((m) => m.name),
  );

  const takeLimits = new Map<string, number>();
  const groupLimits = new Map<string, number>();
  for (const [name, resolved] of settings) {
    if (resolved.maxTake !== undefined) {
      takeLimits.set(name, resolved.maxTake);
    }
    if (resolved.maxGroups !== undefined) {
      groupLimits.set(name, resolved.maxGroups);
    }
  }

  return {
    models,
    engineModels: options.datamodel.models,
    modelsByName,
    settings,
    subscribable,
    takeLimits,
    groupLimits,
  };
}

export function createGolemEngine<TModels>(options: BuildGolemSchemaOptions<TModels>): GolemEngine {
  const { engineModels, settings, takeLimits, groupLimits } = resolveGolem(options);
  const hiddenFields = new Map<string, ReadonlySet<string>>();
  for (const [name, resolved] of settings) {
    if (resolved.hidden.size > 0) {
      hiddenFields.set(name, resolved.hidden);
    }
  }
  return new GolemEngine(options.client, engineModels, {
    hooks: options.hooks,
    takeLimits,
    groupLimits,
    authorization: options.authorization,
    maxDepth: options.defaults?.maxDepth,
    checkWriteResults: options.defaults?.checkWriteResults,
    checkReadFields: options.defaults?.checkReadFields,
    provider: options.datamodel.provider,
    hiddenFields,
  });
}

export function subscribableModels<TModels>(
  options: Pick<BuildGolemSchemaOptions<TModels>, 'datamodel' | 'models' | 'defaults'>,
): Set<string> {
  return resolveGolem({ ...options, client: {} }).subscribable;
}

export function buildGolemSchema<TModels>(options: BuildGolemSchemaOptions<TModels>): GraphQLSchema {
  const { models, engineModels, modelsByName, settings, subscribable, takeLimits, groupLimits } =
    resolveGolem(options);
  if (subscribable.size > 0 && !options.eventBus) {
    throw new Error(
      `Subscriptions are enabled for ${[...subscribable].join(', ')} but no event bus was provided`,
    );
  }
  for (const model of models) {
    if (!model.primaryKey || model.primaryKey.fields.length <= 1) {
      continue;
    }
    if (subscribable.has(model.name)) {
      throw new Error(
        `Model ${model.name} has a composite primary key and cannot enable subscriptions; Golem event identifiers require a single-column primary key`,
      );
    }
    throw new Error(
      `Model ${model.name} has a composite primary key and cannot be exposed on the generated GraphQL surface; set models.${model.name} to false and use forContext for composite-key access`,
    );
  }

  const engine =
    options.engine ??
    new GolemEngine(options.client, engineModels, {
      hooks: options.hooks,
      takeLimits,
      groupLimits,
      authorization: options.authorization,
      maxDepth: options.defaults?.maxDepth,
      checkWriteResults: options.defaults?.checkWriteResults,
      checkReadFields: options.defaults?.checkReadFields,
      provider: options.datamodel.provider,
    });

  const hiddenFor = (name: string): ReadonlySet<string> => settings.get(name)?.hidden ?? new Set();
  const immutableFor = (name: string): ReadonlySet<string> =>
    settings.get(name)?.immutable ?? new Set();
  const readOnlyFor = (name: string): ReadonlySet<string> =>
    settings.get(name)?.readOnly ?? new Set();
  const visibleFields = (model: DatamodelModel): DatamodelField[] =>
    model.fields.filter(
      (f) =>
        !hiddenFor(model.name).has(f.name) &&
        !settings.get(model.name)?.writeOnly.has(f.name),
    );

  const computedSpecs = options.computedFields ?? [];
  for (const spec of computedSpecs) {
    const model = modelsByName.get(spec.model);
    if (!model) {
      throw new Error(`Computed field ${spec.name} targets unknown model ${spec.model}`);
    }
    if (model.fields.some((f) => f.name === spec.name)) {
      throw new Error(`Computed field ${spec.model}.${spec.name} collides with an existing field`);
    }
    const fieldNames = new Set(model.fields.map((f) => f.name));
    for (const required of computedFieldRequires(spec)) {
      if (!fieldNames.has(required)) {
        throw new Error(
          `Computed field ${spec.model}.${spec.name} requires unknown field ${required}`,
        );
      }
    }
  }
  const computedByModel = new Map<string, ComputedFieldSpec[]>();
  for (const spec of computedSpecs) {
    const list = computedByModel.get(spec.model) ?? [];
    list.push(spec);
    computedByModel.set(spec.model, list);
  }
  const computedResolvers = new Map(
    computedSpecs.map((spec) => [spec, computedFieldResolver(spec)] as const),
  );
  const computedRequires = buildComputedRequiresMap(computedSpecs);

  const enumTypes = new Map<string, GraphQLEnumType>(
    options.datamodel.enums.map((e) => [
      e.name,
      new GraphQLEnumType({
        name: e.name,
        values: Object.fromEntries(e.values.map((v) => [v, {}])),
      }),
    ]),
  );

  const sortOrder = new GraphQLEnumType({
    name: 'SortOrder',
    values: { asc: {}, desc: {} },
  });

  const batchPayload = new GraphQLObjectType({
    name: 'BatchPayload',
    fields: { count: { type: new GraphQLNonNull(GraphQLInt) } },
  });

  const filterTypes = new Map<string, GraphQLInputObjectType>();

  function scalarType(model: DatamodelModel, field: DatamodelField): GraphQLScalarType {
    const mapped = SCALAR_MAP[field.type];
    if (!mapped) {
      throw new Error(`Unsupported scalar type ${field.type} on ${model.name}.${field.name}`);
    }
    return mapped;
  }

  function dimensionType(
    model: DatamodelModel,
    field: DatamodelField,
  ): GraphQLScalarType | GraphQLEnumType {
    if (field.kind === 'enum') {
      const enumType = enumTypes.get(field.type);
      if (!enumType) {
        throw new Error(
          `Cannot group ${model.name} by ${field.name}: unknown enum ${field.type}`,
        );
      }
      return enumType;
    }
    return scalarType(model, field);
  }

  function filterTypeFor(name: string, type: GraphQLInputType, operators: readonly string[]): GraphQLInputObjectType {
    const existing = filterTypes.get(name);
    if (existing) {
      return existing;
    }
    const filter = new GraphQLInputObjectType({
      name,
      fields: () => {
        const fields: GraphQLInputFieldConfigMap = {
          equals: { type },
          in: { type: new GraphQLList(new GraphQLNonNull(type)) },
          notIn: { type: new GraphQLList(new GraphQLNonNull(type)) },
          not: { type },
        };
        for (const op of operators) {
          fields[op] = { type };
        }
        return fields;
      },
    });
    filterTypes.set(name, filter);
    return filter;
  }

  function filterFor(model: DatamodelModel, field: DatamodelField): GraphQLInputObjectType {
    if (field.kind === 'enum') {
      const enumType = enumTypes.get(field.type);
      if (!enumType) {
        throw new Error(`Unknown enum ${field.type} on ${model.name}.${field.name}`);
      }
      return filterTypeFor(`${field.type}EnumFilter`, enumType, []);
    }
    const type = scalarType(model, field);
    if (field.type === 'Boolean') {
      return filterTypeFor('BoolFilter', type, []);
    }
    return filterTypeFor(
      `${field.type}Filter`,
      type,
      scalarFilterOperators(field.type),
    );
  }

  const objectTypes = new Map<string, GraphQLObjectType>();
  const countOutputTypes = new Map<string, GraphQLObjectType>();

  function relationCountType(
    model: DatamodelModel,
    counted: readonly DatamodelField[],
  ): GraphQLObjectType {
    const existing = countOutputTypes.get(model.name);
    if (existing) {
      return existing;
    }
    const type = new GraphQLObjectType({
      name: relationCountTypeName(model.name),
      fields: () =>
        Object.fromEntries(
          counted.map((field) => [field.name, { type: new GraphQLNonNull(GraphQLInt) }]),
        ),
    });
    countOutputTypes.set(model.name, type);
    return type;
  }

  const whereInputs = new Map<string, GraphQLInputObjectType>();
  const whereUniqueInputs = new Map<string, GraphQLInputObjectType>();
  const orderByInputs = new Map<string, GraphQLInputObjectType>();

  for (const model of models) {
    objectTypes.set(
      model.name,
      new GraphQLObjectType({
        name: model.name,
        fields: () => {
          const fields: GraphQLFieldConfigMap<unknown, unknown> = {};
          for (const field of visibleFields(model)) {
            if (field.kind === 'object') {
              const target = objectTypes.get(field.type);
              if (!target) {
                continue;
              }
              const type: GraphQLOutputType = field.isList
                ? new GraphQLNonNull(new GraphQLList(new GraphQLNonNull(target)))
                : target;
              fields[field.name] = { type };
            } else {
              const base: GraphQLOutputType =
                field.kind === 'enum' ? enumTypes.get(field.type)! : scalarType(model, field);
              fields[field.name] = {
                type: field.isRequired ? new GraphQLNonNull(base) : base,
              };
            }
          }
          const counted = visibleFields(model).filter(
            (field) => field.kind === 'object' && field.isList && objectTypes.has(field.type),
          );
          if (counted.length > 0) {
            fields[RELATION_COUNT_FIELD] = { type: relationCountType(model, counted) };
          }
          for (const spec of computedByModel.get(model.name) ?? []) {
            const args: GraphQLFieldConfigArgumentMap = {};
            for (const [argName, argRef] of Object.entries(spec.args ?? {})) {
              args[argName] = {
                type: resolveTypeRef(argRef, inputTypeByName) as GraphQLInputType,
              };
            }
            fields[spec.name] = {
              type: resolveTypeRef(spec.type, outputTypeByName) as GraphQLOutputType,
              args,
              resolve: wrapResolve(computedResolvers.get(spec)!),
            };
          }
          return fields;
        },
      }),
    );

    whereInputs.set(
      model.name,
      new GraphQLInputObjectType({
        name: `${model.name}WhereInput`,
        fields: () => {
          const self = whereInputs.get(model.name)!;
          const fields: GraphQLInputFieldConfigMap = {
            AND: { type: new GraphQLList(new GraphQLNonNull(self)) },
            OR: { type: new GraphQLList(new GraphQLNonNull(self)) },
            NOT: { type: new GraphQLList(new GraphQLNonNull(self)) },
          };
          for (const field of visibleFields(model)) {
            if (field.kind === 'object' || field.isList) {
              continue;
            }
            fields[field.name] = { type: filterFor(model, field) };
          }
          return fields;
        },
      }),
    );

    whereUniqueInputs.set(
      model.name,
      new GraphQLInputObjectType({
        name: `${model.name}WhereUniqueInput`,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of visibleFields(model)) {
            if (field.kind === 'scalar' && (field.isId || field.isUnique)) {
              fields[field.name] = { type: scalarType(model, field) };
            }
          }
          return fields;
        },
      }),
    );

    orderByInputs.set(
      model.name,
      new GraphQLInputObjectType({
        name: `${model.name}OrderByInput`,
        fields: () => {
          const fields: GraphQLInputFieldConfigMap = {};
          for (const field of visibleFields(model)) {
            if (field.kind === 'object' || field.isList) {
              continue;
            }
            fields[field.name] = { type: sortOrder };
          }
          return fields;
        },
      }),
    );
  }

  const inputs = new InputTypeRegistry({
    modelsByName,
    enumTypes,
    whereUniqueInputs,
    scalarType,
    hiddenFor,
    immutableFor,
    readOnlyFor,
  });

  const namedScalars: Record<string, GraphQLScalarType> = {
    ...SCALAR_MAP,
    ID: GraphQLID as unknown as GraphQLScalarType,
  };

  function resolveTypeRef(ref: string, lookup: (name: string) => GraphQLType | undefined): GraphQLType {
    const trimmed = ref.trim();
    if (trimmed.endsWith('!')) {
      return new GraphQLNonNull(resolveTypeRef(trimmed.slice(0, -1), lookup) as never);
    }
    if (trimmed.startsWith('[') && trimmed.endsWith(']')) {
      return new GraphQLList(resolveTypeRef(trimmed.slice(1, -1), lookup) as never);
    }
    const named = lookup(trimmed);
    if (!named) {
      throw new Error(`Unknown type ${trimmed} in extension type reference`);
    }
    return named;
  }

  function outputTypeByName(name: string): GraphQLType | undefined {
    if (name === 'BatchPayload') {
      return batchPayload;
    }
    return namedScalars[name] ?? enumTypes.get(name) ?? objectTypes.get(name);
  }

  function inputTypeByName(name: string): GraphQLType | undefined {
    const direct = namedScalars[name] ?? enumTypes.get(name) ?? filterTypes.get(name) ?? inputs.find(name);
    if (direct) {
      return direct;
    }
    const suffixes: Array<[string, (model: DatamodelModel) => GraphQLInputObjectType | undefined]> = [
      ['WhereUniqueInput', (m) => whereUniqueInputs.get(m.name)],
      ['WhereInput', (m) => whereInputs.get(m.name)],
      ['OrderByInput', (m) => orderByInputs.get(m.name)],
      ['CreateInput', (m) => inputs.createInput(m)],
      ['UpdateManyInput', (m) => inputs.updateManyInput(m)],
      ['UpdateInput', (m) => inputs.updateInput(m)],
    ];
    for (const [suffix, get] of suffixes) {
      if (name.endsWith(suffix)) {
        const model = modelsByName.get(name.slice(0, -suffix.length));
        if (model) {
          return get(model);
        }
      }
    }
    return undefined;
  }

  const queryFields: GraphQLFieldConfigMap<unknown, unknown> = {};
  const mutationFields: GraphQLFieldConfigMap<unknown, unknown> = {};
  const subscriptionFields: GraphQLFieldConfigMap<unknown, unknown> = {};

  const golemEventType = new GraphQLEnumType({
    name: 'GolemEventType',
    values: { CREATED: {}, UPDATED: {}, DELETED: {} },
  });

  for (const model of models) {
    const objectType = objectTypes.get(model.name)!;
    const whereInput = whereInputs.get(model.name)!;
    const whereUniqueInput = whereUniqueInputs.get(model.name)!;
    const orderByInput = orderByInputs.get(model.name)!;
    const operations = settings.get(model.name)!.operations;

    if (operations.has('findOne')) {
      queryFields[findOneFieldName(model.name)] = {
        type: objectType,
        args: {
          where: { type: new GraphQLNonNull(whereUniqueInput) },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          engine.findOne({
            model: model.name,
            where: args.where,
            select: buildSelect(info, model, modelsByName, computedRequires),
            context: ctx,
            compiled: true,
          }),
        ),
      };
    }

    if (operations.has('findMany')) {
      queryFields[findManyFieldName(model.name)] = {
        type: new GraphQLNonNull(new GraphQLList(new GraphQLNonNull(objectType))),
        args: {
          where: { type: whereInput },
          orderBy: { type: new GraphQLList(new GraphQLNonNull(orderByInput)) },
          take: { type: GraphQLInt },
          skip: { type: GraphQLInt },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          engine.findMany({
            model: model.name,
            where: args.where ?? undefined,
            orderBy: args.orderBy ?? undefined,
            take: args.take ?? undefined,
            skip: args.skip ?? undefined,
            select: buildSelect(info, model, modelsByName, computedRequires),
            context: ctx,
            compiled: true,
          }),
        ),
      };
    }

    const modelSettings = settings.get(model.name)!;
    if (modelSettings.aggregations) {
      const groupable = visibleFields(model).filter(
        (field) =>
          isGroupable(field) &&
          (!modelSettings.dimensions || modelSettings.dimensions.has(field.name)),
      );
      const measurable = visibleFields(model).filter(
        (field) =>
          isMeasurable(field) &&
          (!modelSettings.measures || modelSettings.measures.has(field.name)),
      );
      const orderable = visibleFields(model).filter(
        (field) =>
          isOrderable(field) &&
          (!modelSettings.measures || modelSettings.measures.has(field.name)),
      );
      const countable = visibleFields(model).filter(isGroupable);
      const types = buildAggregationTypes({
        model,
        fields: {
          dimensions: groupable,
          measures: measurable,
          orderables: orderable,
          countables: countable,
        },
        sortOrder,
        dimensionType,
        filterTypeFor,
      });

      queryFields[aggregateFieldName(model.name)] = {
        type: new GraphQLNonNull(types.aggregateOutput),
        args: {
          where: { type: whereInput },
          measures: { type: types.measuresInput },
        },
        resolve: wrapResolve(async (_root, args, ctx) => {
          const measures = toPrismaMeasures(args.measures as MeasuresArg);
          const result = (await engine.aggregate({
            model: model.name,
            where: args.where ?? undefined,
            ...measures,
            context: ctx,
            compiled: true,
          })) as Record<string, unknown>;
          return toAggregateResult(result);
        }),
      };

      if (types.dimensionEnum && types.groupOutput) {
        queryFields[groupByFieldName(model.name)] = {
          type: new GraphQLNonNull(
            new GraphQLList(new GraphQLNonNull(types.groupOutput)),
          ),
          args: {
            where: { type: whereInput },
            by: {
              type: new GraphQLNonNull(
                new GraphQLList(new GraphQLNonNull(types.dimensionEnum)),
              ),
            },
            measures: { type: types.measuresInput },
            ...(types.havingInput ? { having: { type: types.havingInput } } : {}),
            ...(types.orderByInput ? { orderBy: { type: types.orderByInput } } : {}),
            take: { type: GraphQLInt },
            skip: { type: GraphQLInt },
          },
          resolve: wrapResolve(async (_root, args, ctx) => {
            const by = (args.by ?? []) as string[];
            const groupLimit = groupLimits.get(model.name);
            const requestedTake = (args.take ?? undefined) as number | undefined;
            if (
              groupLimit !== undefined &&
              requestedTake !== undefined &&
              Math.abs(requestedTake) > groupLimit
            ) {
              throw new GolemValidationError(
                `${groupByFieldName(model.name)} requires an explicit take of at most ${groupLimit}`,
              );
            }
            const take =
              requestedTake ??
              (groupLimit === undefined ? undefined : groupLimit + 1);
            const countKey = by[0];
            const measures = toPrismaMeasures(args.measures as MeasuresArg);
            const orderBy =
              toPrismaGroupOrderBy(
                args.orderBy as Record<string, unknown>,
                countKey,
              ) ?? (take === undefined ? undefined : byFieldsOrder(by));
            const rows = (await engine.groupBy({
              model: model.name,
              by,
              where: args.where ?? undefined,
              having: toPrismaHaving(
                args.having as Record<string, unknown>,
                countKey,
              ),
              orderBy,
              take,
              skip: args.skip ?? undefined,
              ...measures,
              context: ctx,
              compiled: true,
            })) as Record<string, unknown>[];
            if (
              groupLimit !== undefined &&
              requestedTake === undefined &&
              rows.length > groupLimit
            ) {
              throw new GolemValidationError(
                `${groupByFieldName(model.name)} matched more than ${groupLimit} groups; narrow the query or pass an explicit take of at most ${groupLimit}`,
              );
            }
            return toGroupResults(rows, by);
          }),
        };
      }
    }

    if (operations.has('create')) {
      mutationFields[createFieldName(model.name)] = {
        type: new GraphQLNonNull(objectType),
        args: {
          data: { type: new GraphQLNonNull(inputs.createInput(model)) },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          written(
            ctx,
            info,
            engine.create({
              model: model.name,
              data: args.data,
              select: buildSelect(info, model, modelsByName, computedRequires),
              context: ctx,
            }),
          ),
        ),
      };
    }

    if (operations.has('update')) {
      mutationFields[updateFieldName(model.name)] = {
        type: new GraphQLNonNull(objectType),
        args: {
          where: { type: new GraphQLNonNull(whereUniqueInput) },
          data: { type: new GraphQLNonNull(inputs.updateInput(model)) },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          written(
            ctx,
            info,
            engine.update({
              model: model.name,
              where: args.where,
              data: args.data,
              select: buildSelect(info, model, modelsByName, computedRequires),
              context: ctx,
            }),
          ),
        ),
      };
    }

    if (operations.has('delete')) {
      mutationFields[deleteFieldName(model.name)] = {
        type: new GraphQLNonNull(objectType),
        args: {
          where: { type: new GraphQLNonNull(whereUniqueInput) },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          written(
            ctx,
            info,
            engine.delete({
              model: model.name,
              where: args.where,
              select: buildSelect(info, model, modelsByName, computedRequires),
              context: ctx,
            }),
          ),
        ),
      };
    }

    if (operations.has('updateMany')) {
      mutationFields[updateManyFieldName(model.name)] = {
        type: new GraphQLNonNull(batchPayload),
        args: {
          where: { type: whereInput },
          data: { type: new GraphQLNonNull(inputs.updateManyInput(model)) },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          written(
            ctx,
            info,
            engine.updateMany({
              model: model.name,
              where: args.where ?? undefined,
              data: args.data,
              context: ctx,
            }),
          ),
        ),
      };
    }

    if (operations.has('deleteMany')) {
      mutationFields[deleteManyFieldName(model.name)] = {
        type: new GraphQLNonNull(batchPayload),
        args: {
          where: { type: whereInput },
        },
        resolve: wrapResolve((_root, args, ctx, info) =>
          written(
            ctx,
            info,
            engine.deleteMany({
              model: model.name,
              where: args.where ?? undefined,
              context: ctx,
            }),
          ),
        ),
      };
    }

    if (subscribable.has(model.name)) {
      const eventBus = options.eventBus!;
      const pkField = model.fields.find((f) => f.isId);
      if (!pkField) {
        throw new Error(`Model ${model.name} has no primary key field and cannot be subscribable`);
      }
      const eventTypeName = `${model.name}Event`;
      const eventType = new GraphQLObjectType({
        name: eventTypeName,
        fields: {
          type: { type: new GraphQLNonNull(golemEventType) },
          id: { type: new GraphQLNonNull(scalarType(model, pkField)) },
          entity: { type: objectType },
        },
      });

      subscriptionFields[eventsFieldName(model.name)] = {
        type: new GraphQLNonNull(eventType),
        args: {
          where: { type: whereInput },
        },
        subscribe: (_root, args, ctx, info) => {
          const authz = options.authorization;
          const eventContext = () => (authz ? authz.freshContext?.(ctx) ?? ctx : undefined);
          let abandon!: () => void;
          const abandoned = new Promise<typeof SUBSCRIPTION_ABANDONED>((resolve) => {
            abandon = () => resolve(SUBSCRIPTION_ABANDONED);
          });
          const events = (async function* () {
            if (authz) {
              await authz.authorize('read', model.name, eventContext());
            }
            const entitySelect = buildEventEntitySelect(info, eventTypeName, model, modelsByName, computedRequires);
            const source = eventBus.iterate(eventTopic(model.name));
            try {
              while (true) {
                const step = await Promise.race([source.next(), abandoned]);
                if (step === SUBSCRIPTION_ABANDONED || step.done) break;
                const payload = step.value;
                if (payload.type === 'DELETED') {
                  // A deleted row can no longer be queried safely. Filtered deletion events are
                  // therefore suppressed, and authorized subscriptions require a pre-delete
                  // snapshot that passes a fresh instance check.
                  if (args.where) continue;
                  if (authz) {
                    if (!payload.entity || !authz.check) continue;
                    try {
                      const allowed = await authz.check('read', model.name, payload.entity, eventContext());
                      if (!allowed) continue;
                    } catch (error) {
                      if (
                        error instanceof GolemError &&
                        (error.code === 'FORBIDDEN' || error.code === 'UNAUTHENTICATED')
                      ) continue;
                      throw error;
                    }
                  }
                  yield { type: payload.type, id: payload.id, entity: null };
                  continue;
                }
                if (!args.where && !entitySelect && !authz) {
                  yield { type: payload.type, id: payload.id, entity: null };
                  continue;
                }
                const where = args.where
                  ? { AND: [{ [pkField.name]: payload.id }, args.where] }
                  : { [pkField.name]: payload.id };
                let entity: unknown;
                try {
                  entity = await engine.findFirst({
                    model: model.name,
                    where,
                    select: entitySelect ?? primaryKeySelect(model),
                    context: eventContext(),
                  });
                } catch (error) {
                  if (
                    error instanceof GolemError &&
                    (error.code === 'FORBIDDEN' || error.code === 'UNAUTHENTICATED')
                  ) {
                    continue;
                  }
                  throw error;
                }
                if (!entity && (args.where || authz)) {
                  continue;
                }
                yield { type: payload.type, id: payload.id, entity: entitySelect ? entity : null };
              }
            } finally {
              void Promise.resolve(source.return?.()).catch(() => undefined);
            }
          })();
          return {
            next: () => events.next(),
            return: (value?: unknown) => {
              abandon();
              return events.return(value as never);
            },
            throw: (error?: unknown) => events.throw(error),
            [Symbol.asyncIterator]() {
              return this;
            },
          };
        },
        resolve: (payload: unknown) => payload,
      };
    }
  }

  for (const spec of options.customOperations ?? []) {
    const target = spec.kind === 'query' ? queryFields : mutationFields;
    if (target[spec.name]) {
      throw new Error(`Custom ${spec.kind} ${spec.name} collides with an existing field`);
    }
    const args: GraphQLFieldConfigArgumentMap = {};
    for (const [argName, argRef] of Object.entries(spec.args ?? {})) {
      args[argName] = { type: resolveTypeRef(argRef, inputTypeByName) as GraphQLInputType };
    }
    target[spec.name] = {
      type: resolveTypeRef(spec.type, outputTypeByName) as GraphQLOutputType,
      args,
      resolve: wrapResolve((_root, resolvedArgs, ctx, info) => spec.resolve(resolvedArgs, ctx, info)),
    };
  }

  return new GraphQLSchema({
    query: new GraphQLObjectType({
      name: 'Query',
      fields: queryFields,
    }),
    mutation:
      Object.keys(mutationFields).length > 0
        ? new GraphQLObjectType({
            name: 'Mutation',
            fields: mutationFields,
          })
        : undefined,
    subscription:
      Object.keys(subscriptionFields).length > 0
        ? new GraphQLObjectType({
            name: 'Subscription',
            fields: subscriptionFields,
          })
        : undefined,
  });
}

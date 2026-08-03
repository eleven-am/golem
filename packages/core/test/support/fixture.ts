import { AuthorizationProvider, FieldClassification } from '../../src/authorization';
import { DatamodelModel } from '../../src/datamodel';
import { GolemEngine, GolemEngineOptions } from '../../src/operations';
import {
  ScopedHost,
  ScopedQuery,
  ScopedRequest,
  createScopedQuery,
  resolveScopedFieldPolicy,
} from '../../src/scoped';
import { field } from '../../src/testing';

export const scopedModels: readonly DatamodelModel[] = [
  {
    name: 'Post',
    dbName: 'posts',
    indexes: [{ kind: 'normal', name: 'posts_author_id_idx', fields: ['authorId'] }],
    fields: [
      field({ name: 'id', dbName: 'post_id', type: 'Int', isId: true }),
      field({ name: 'title', dbName: 'title', type: 'String' }),
      field({ name: 'authorId', dbName: 'author_id', type: 'Int' }),
      field({ name: 'published', dbName: 'published', type: 'Boolean' }),
      field({ name: 'views', dbName: 'views', type: 'Int' }),
      field({ name: 'secretNote', dbName: 'secret_note', type: 'String' }),
      field({
        name: 'author',
        kind: 'object',
        type: 'User',
        relationName: 'PostToUser',
        relationFromFields: ['authorId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'User',
    dbName: 'users',
    fields: [
      field({ name: 'id', dbName: 'user_id', type: 'Int', isId: true }),
      field({ name: 'name', dbName: 'name', type: 'String' }),
      field({ name: 'tenantId', dbName: 'tenant_id', type: 'Int' }),
      field({
        name: 'posts',
        kind: 'object',
        type: 'Post',
        isList: true,
        isRequired: false,
        relationName: 'PostToUser',
      }),
      field({
        name: 'metrics',
        kind: 'object',
        type: 'Metric',
        isList: true,
        isRequired: false,
        relationName: 'MetricToUser',
      }),
      field({
        name: 'profile',
        kind: 'object',
        type: 'Profile',
        isRequired: false,
        relationName: 'ProfileToUser',
      }),
    ],
  },
  {
    name: 'Profile',
    dbName: 'profiles',
    fields: [
      field({ name: 'id', dbName: 'profile_id', type: 'Int', isId: true }),
      field({ name: 'bio', dbName: 'bio', type: 'String' }),
      field({ name: 'userId', dbName: 'user_id', type: 'Int', isUnique: true }),
      field({
        name: 'user',
        kind: 'object',
        type: 'User',
        relationName: 'ProfileToUser',
        relationFromFields: ['userId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'Secret',
    dbName: 'secrets',
    fields: [
      field({ name: 'id', dbName: 'id', type: 'Int', isId: true }),
      field({ name: 'value', dbName: 'value', type: 'String' }),
    ],
  },
  {
    name: 'Metric',
    dbName: 'metrics',
    fields: [
      field({ name: 'id', dbName: 'metric_id', type: 'Int', isId: true }),
      field({ name: 'label', dbName: 'label', type: 'String' }),
      field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
      field({ name: 'note', dbName: 'note', type: 'String', isRequired: false }),
      field({ name: 'rank', dbName: 'rank_value', type: 'Int', isRequired: false }),
      field({ name: 'score', dbName: 'score', type: 'Decimal', isRequired: false }),
      field({ name: 'hits', dbName: 'hits', type: 'BigInt' }),
      field({ name: 'ratio', dbName: 'ratio', type: 'Float' }),
      field({ name: 'active', dbName: 'active', type: 'Boolean' }),
      field({ name: 'recordedAt', dbName: 'recorded_at', type: 'DateTime' }),
      field({
        name: 'owner',
        kind: 'object',
        type: 'User',
        relationName: 'MetricToUser',
        relationFromFields: ['ownerId'],
        relationToFields: ['id'],
      }),
    ],
  },
];

export const playModel: DatamodelModel = {
  name: 'Play',
  dbName: 'plays',
  fields: [
    field({ name: 'id', dbName: 'play_id', type: 'Int', isId: true }),
    field({ name: 'userId', dbName: 'user_id', type: 'Int' }),
    field({ name: 'ts', dbName: 'ts', type: 'DateTime' }),
    field({ name: 'msPlayed', dbName: 'ms_played', type: 'Int' }),
    field({ name: 'reasonStart', dbName: 'reason_start', type: 'String' }),
    field({ name: 'reasonEnd', dbName: 'reason_end', type: 'String' }),
    field({ name: 'trackUri', dbName: 'track_uri', type: 'String' }),
    field({ name: 'trackName', dbName: 'track_name', type: 'String' }),
    field({ name: 'artistName', dbName: 'artist_name', type: 'String' }),
  ],
};

export const playModels: readonly DatamodelModel[] = [...scopedModels, playModel];

export interface ScopedEngineOptions extends Omit<GolemEngineOptions, 'authorization'> {
  constraints?: Record<string, unknown>;
  client?: Record<string, any>;
}

export const scopedContext = { user: 'caller' };

export function scopedEngine(options: ScopedEngineOptions = {}): GolemEngine {
  const constraints = options.constraints;
  const authorization: AuthorizationProvider | undefined =
    constraints === undefined
      ? undefined
      : {
          authorize: async () => undefined,
          constrain: async (_action, model) => constraints[model],
        };
  return new GolemEngine(options.client ?? {}, scopedModels, {
    provider: 'sqlite',
    ...options,
    authorization,
    checkWriteResults: false,
    checkReadFields: false,
  });
}

export interface ScopedFieldSpec {
  readonly model: string;
  readonly field: string;
  readonly access?: 'conditional' | 'never';
  readonly condition?: unknown;
  readonly discharged?: boolean;
}

export interface ScopedFieldHostOptions {
  readonly provider?: string;
  readonly models?: readonly DatamodelModel[];
  readonly constraints?: Record<string, unknown>;
  readonly hiddenFields?: ReadonlyMap<string, ReadonlySet<string>>;
  readonly fields?: readonly ScopedFieldSpec[];
  readonly client?: Record<string, any>;
  readonly context?: unknown;
}

export function scopedFieldAuthorization(
  options: ScopedFieldHostOptions = {},
): AuthorizationProvider {
  const specs = options.fields ?? [];
  const specFor = (model: string, name: string): ScopedFieldSpec | undefined =>
    specs.find((spec) => spec.model === model && spec.field === name);
  return {
    authorize: async () => undefined,
    constrain: async (_action, model) => options.constraints?.[model],
    check: async () => true,
    checkField: async () => true,
    classifyFields: async (_action, model, names) => {
      const classification: Record<string, FieldClassification> = {};
      for (const name of names) {
        const spec = specFor(model, name);
        classification[name] =
          spec === undefined
            ? { access: 'always' }
            : {
                access: spec.access ?? 'conditional',
                dischargedByConstraint: spec.discharged === true,
              };
      }
      return classification;
    },
    constrainField: async (_action, model, name) => specFor(model, name)?.condition,
  };
}

export function scopedFieldEngine(
  options: ScopedFieldHostOptions = {},
  checkReadFields = true,
): GolemEngine {
  return new GolemEngine(options.client ?? {}, options.models ?? scopedModels, {
    provider: options.provider ?? 'sqlite',
    authorization: scopedFieldAuthorization(options),
    hiddenFields: options.hiddenFields,
    checkWriteResults: false,
    checkReadFields,
  });
}

export function scopedFieldHost(options: ScopedFieldHostOptions = {}): ScopedHost {
  const models = options.models ?? scopedModels;
  const context = options.context ?? scopedContext;
  const authorization = scopedFieldAuthorization(options);
  return {
    models,
    provider: options.provider ?? 'sqlite',
    hiddenFields: (model) => options.hiddenFields?.get(model) ?? new Set(),
    constraint: (model) => authorization.constrain('read', model, context),
    fieldPolicy: (model, names) =>
      resolveScopedFieldPolicy(authorization, model, names, context),
    execute: async (_model, sql, parameters) => {
      const runner = options.client?.$queryRawUnsafe;
      if (typeof runner !== 'function') {
        throw new Error('the scoped field fixture needs a client exposing $queryRawUnsafe');
      }
      return (await runner.call(options.client, sql, ...parameters)) as unknown[];
    },
  };
}

export function scopedFieldQuery(
  options: ScopedFieldHostOptions,
  request: ScopedRequest,
): ScopedQuery {
  return createScopedQuery(scopedFieldHost(options), request);
}

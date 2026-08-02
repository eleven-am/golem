import { AuthorizationProvider } from '../../src/authorization';
import { DatamodelModel } from '../../src/datamodel';
import { GolemEngine, GolemEngineOptions } from '../../src/operations';
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

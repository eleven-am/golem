import { AuthorizationProvider } from '../../src/authorization';
import { DatamodelModel } from '../../src/datamodel';
import { GolemEngine, GolemEngineOptions } from '../../src/operations';
import { field } from '../../src/testing';

export const scopedModels: readonly DatamodelModel[] = [
  {
    name: 'Post',
    dbName: 'posts',
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
    ],
  },
];

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

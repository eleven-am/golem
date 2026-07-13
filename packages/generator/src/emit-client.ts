import { lcFirst } from './names';

export function emitClientModule(modelNames: readonly string[], clientImport: string): string {
  const modelMap = modelNames.map((name) => `  ${lcFirst(name)}: '${name}',`).join('\n');
  const delegateUnion = modelNames.map((name) => `'${lcFirst(name)}'`).join(' | ');

  return `import { PrismaClient } from '${clientImport}';
import type { GolemEngineRef, GolemQueryInterceptor } from '@eleven-am/golem-core';

export type GolemClientOptions = ConstructorParameters<typeof PrismaClient>[0];

const GOLEM_MODELS = {
${modelMap}
} as const;

const POLICY_OPS = {
  findUnique: 'findOne',
  findFirst: 'findFirst',
  findMany: 'findMany',
  create: 'create',
  update: 'update',
  updateMany: 'updateMany',
  delete: 'delete',
  deleteMany: 'deleteMany',
  upsert: 'upsert',
} as const;

function createBaseClient(options: GolemClientOptions, interceptor: GolemQueryInterceptor) {
  const raw = new PrismaClient(options);
  return raw.$extends({
    query: {
      $allModels: {
        $allOperations({ model, operation, args, query }) {
          const delegate = model ? raw[model.charAt(0).toLowerCase() + model.slice(1) as keyof typeof raw] : undefined;
          return interceptor({
            model,
            operation,
            args,
            query: query as (value: unknown) => Promise<unknown>,
            findExisting: model
              ? (where, select) => (delegate as unknown as { findUnique(args: unknown): Promise<unknown> }).findUnique({ where, select })
              : undefined,
          });
        },
      },
    },
  });
}

export type GolemBaseClient = ReturnType<typeof createBaseClient>;

export type ContextBoundClient = {
  [K in ${delegateUnion}]: Pick<GolemBaseClient[K], keyof typeof POLICY_OPS & keyof GolemBaseClient[K]>;
};

function bindContext(engineRef: GolemEngineRef, context: unknown): ContextBoundClient {
  return new Proxy({} as Record<string, unknown>, {
    get: (_target, delegateName) => {
      const model = GOLEM_MODELS[delegateName as keyof typeof GOLEM_MODELS];
      if (!model) {
        return undefined;
      }
      return new Proxy({} as Record<string, unknown>, {
        get: (_inner, operation) => {
          const engineOp = POLICY_OPS[operation as keyof typeof POLICY_OPS];
          if (!engineOp) {
            return undefined;
          }
          return (args: Record<string, unknown> = {}) => {
            const engine = engineRef.current;
            if (!engine) {
              throw new Error('GolemModule is not initialized: forContext is unavailable before bootstrap');
            }
            return (engine as unknown as Record<string, (request: unknown) => Promise<unknown>>)[engineOp]({
              model,
              ...args,
              context,
            });
          };
        },
      });
    },
  }) as unknown as ContextBoundClient;
}

export function createGolemClient(
  options: GolemClientOptions,
  interceptor: GolemQueryInterceptor,
  engineRef: GolemEngineRef,
) {
  return createBaseClient(options, interceptor).$extends({
    client: {
      forContext(context: unknown): ContextBoundClient {
        return bindContext(engineRef, context);
      },
    },
  });
}

export type GolemClient = ReturnType<typeof createGolemClient>;

const GolemPrismaBase = class {
  constructor(options: GolemClientOptions, interceptor: GolemQueryInterceptor, engineRef: GolemEngineRef) {
    return createGolemClient(options, interceptor, engineRef);
  }
} as new (
  options: GolemClientOptions,
  interceptor: GolemQueryInterceptor,
  engineRef: GolemEngineRef,
) => GolemClient;

export class GolemPrismaService extends GolemPrismaBase {}
`;
}

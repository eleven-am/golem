import { lcFirst } from './names';

export function emitClientModule(modelNames: readonly string[], clientImport: string): string {
  const modelMap = modelNames.map((name) => `  ${lcFirst(name)}: '${name}',`).join('\n');
  const delegateUnion = modelNames.map((name) => `'${lcFirst(name)}'`).join(' | ');

  return `import { Prisma, PrismaClient } from '${clientImport}';
import { withBufferedEvents } from '@eleven-am/golem-core';
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
  count: 'count',
  aggregate: 'aggregate',
  groupBy: 'groupBy',
} as const;

function createBaseClient(options: GolemClientOptions, interceptor: GolemQueryInterceptor) {
  const raw = new PrismaClient(options);
  const instrumented = raw.$extends({
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
  const transaction = instrumented.$transaction.bind(instrumented);
  const commitAwareTransaction = ((...args: unknown[]) =>
    withBufferedEvents(() =>
      (transaction as unknown as (...transactionArgs: unknown[]) => Promise<unknown>)(...args),
    )) as typeof instrumented.$transaction;
  return instrumented.$extends({
    client: {
      $transaction: commitAwareTransaction,
    },
  });
}

export type GolemBaseClient = ReturnType<typeof createBaseClient>;

type SupportedArgs<
  TDelegate,
  TOperation extends keyof typeof POLICY_OPS,
  TKeys extends PropertyKey,
> = Pick<
  Prisma.Args<TDelegate, TOperation>,
  Extract<keyof Prisma.Args<TDelegate, TOperation>, TKeys>
>;

type ContextBoundDelegate<TDelegate> = {
  findUnique<TArgs extends SupportedArgs<TDelegate, 'findUnique', 'where' | 'select' | 'include' | 'omit'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'findUnique', 'where' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'findUnique'>>;
  findFirst<TArgs extends SupportedArgs<TDelegate, 'findFirst', 'where' | 'orderBy' | 'take' | 'skip' | 'cursor' | 'distinct' | 'select' | 'include' | 'omit'>>(
    args?: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'findFirst', 'where' | 'orderBy' | 'take' | 'skip' | 'cursor' | 'distinct' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'findFirst'>>;
  findMany<TArgs extends SupportedArgs<TDelegate, 'findMany', 'where' | 'orderBy' | 'take' | 'skip' | 'cursor' | 'distinct' | 'select' | 'include' | 'omit'>>(
    args?: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'findMany', 'where' | 'orderBy' | 'take' | 'skip' | 'cursor' | 'distinct' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'findMany'>>;
  create<TArgs extends SupportedArgs<TDelegate, 'create', 'data' | 'select' | 'include' | 'omit'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'create', 'data' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'create'>>;
  update<TArgs extends SupportedArgs<TDelegate, 'update', 'where' | 'data' | 'select' | 'include' | 'omit'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'update', 'where' | 'data' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'update'>>;
  updateMany<TArgs extends SupportedArgs<TDelegate, 'updateMany', 'where' | 'data'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'updateMany', 'where' | 'data'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'updateMany'>>;
  delete<TArgs extends SupportedArgs<TDelegate, 'delete', 'where' | 'select' | 'include' | 'omit'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'delete', 'where' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'delete'>>;
  deleteMany<TArgs extends SupportedArgs<TDelegate, 'deleteMany', 'where'>>(
    args?: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'deleteMany', 'where'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'deleteMany'>>;
  upsert<TArgs extends SupportedArgs<TDelegate, 'upsert', 'where' | 'create' | 'update' | 'select' | 'include' | 'omit'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'upsert', 'where' | 'create' | 'update' | 'select' | 'include' | 'omit'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'upsert'>>;
  count<TArgs extends SupportedArgs<TDelegate, 'count', 'where'>>(
    args?: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'count', 'where'>>,
  ): Promise<number>;
  aggregate<TArgs extends SupportedArgs<TDelegate, 'aggregate', 'where' | 'orderBy' | 'cursor' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'aggregate', 'where' | 'orderBy' | 'cursor' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'aggregate'>>;
  groupBy<TArgs extends SupportedArgs<TDelegate, 'groupBy', 'where' | 'orderBy' | 'by' | 'having' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'>>(
    args: Prisma.SelectSubset<TArgs, SupportedArgs<TDelegate, 'groupBy', 'where' | 'orderBy' | 'by' | 'having' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'>>,
  ): Promise<Prisma.Result<TDelegate, TArgs, 'groupBy'>>;
};

export type ContextBoundDelegates = {
  [K in ${delegateUnion}]: ContextBoundDelegate<GolemBaseClient[K]>;
};

export interface ContextBoundTransactionOptions {
  timeout?: number;
  isolationLevel?: unknown;
}

export type ContextBoundClient = ContextBoundDelegates & {
  $transaction<T>(
    fn: (tx: ContextBoundDelegates) => Promise<T>,
    options?: ContextBoundTransactionOptions,
  ): Promise<T>;
};

function bindContext(engineRef: GolemEngineRef, context: unknown): ContextBoundClient {
  const requireEngine = (): Record<string, (request: unknown) => Promise<unknown>> => {
    const engine = engineRef.current;
    if (!engine) {
      throw new Error('GolemModule is not initialized: forContext is unavailable before bootstrap');
    }
    return engine as unknown as Record<string, (request: unknown) => Promise<unknown>>;
  };
  const boundDelegates = (
    resolve: () => Record<string, (request: unknown) => Promise<unknown>>,
  ): ContextBoundDelegates =>
    new Proxy({} as Record<string, unknown>, {
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
            return (args: Record<string, unknown> = {}) =>
              resolve()[engineOp]({ model, ...args, context });
          },
        });
      },
    }) as unknown as ContextBoundDelegates;
  const engineDelegates = boundDelegates(requireEngine);
  return new Proxy({} as Record<string, unknown>, {
    get: (_target, prop, receiver) => {
      if (prop === '$transaction') {
        return (
          fn: (tx: ContextBoundDelegates) => Promise<unknown>,
          options?: ContextBoundTransactionOptions,
        ) =>
          (
            requireEngine() as unknown as {
              transaction(
                context: unknown,
                run: (tx: Record<string, (request: unknown) => Promise<unknown>>) => Promise<unknown>,
                options?: ContextBoundTransactionOptions,
              ): Promise<unknown>;
            }
          ).transaction(
            context,
            (txView) => fn(boundDelegates(() => txView)),
            options,
          );
      }
      return Reflect.get(engineDelegates as object, prop, receiver);
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

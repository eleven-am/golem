import { AsyncLocalStorage } from 'node:async_hooks';
import { withBufferedEvents } from '../../src/event-buffer';
import { GolemBatchDelegate, GolemBatchTransaction, GolemQueryInterceptor } from '../../src/publisher';

type Client = Record<string, any>;

interface TransactionState {
  readonly client: Client;
  readonly suppressed: boolean;
}

export function golemClient(raw: Client, interceptor: GolemQueryInterceptor, wrap: (tx: Client) => Client = (tx) => tx): Client {
  const context = new AsyncLocalStorage<TransactionState>();
  const delegateFor = (client: Client, model: string) =>
    client[model.charAt(0).toLowerCase() + model.slice(1)] as GolemBatchDelegate;
  const transactionFor = (client: Client): GolemBatchTransaction => ({
    scope: client,
    delegate: (model) => delegateFor(client, model),
    queryRaw: (sql, ...values) => client.$queryRawUnsafe(sql, ...values),
  });
  const instrumented = raw.$extends({
    query: {
      $allModels: {
        $allOperations({ model, operation, args, query }: { model: string; operation: string; args: unknown; query: (args: unknown) => Promise<unknown> }) {
          const state = context.getStore();
          const active = state?.client ?? raw;
          return interceptor({
            model,
            operation,
            args,
            query,
            findExisting: (where, select) => delegateFor(active, model).findUnique({ where, select }),
            batch: {
              suppressed: state?.suppressed ?? false,
              run: async (work) => {
                const current = context.getStore();
                const execute = (client: Client) =>
                  context.run({ client, suppressed: true }, () => work(delegateFor(client, model), transactionFor(client)));
                if (current) return execute(current.client);
                return withBufferedEvents(() => raw.$transaction((tx: Client) => execute(wrap(tx))));
              },
            },
          });
        },
      },
    },
  });
  const transaction = instrumented.$transaction.bind(instrumented);
  return new Proxy(instrumented, {
    get: (target, property, receiver) => {
      if (property !== '$transaction') return Reflect.get(target, property, receiver);
      return (work: (tx: Client) => Promise<unknown>, ...rest: unknown[]) =>
        withBufferedEvents(() => transaction((tx: Client) => {
          const wrapped = wrap(tx);
          return context.run({ client: wrapped, suppressed: false }, () => work(wrapped));
        }, ...rest));
    },
  });
}

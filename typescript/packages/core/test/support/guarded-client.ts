type Client = Record<string, any>;

export function guardedClient<T extends Client>(delegates: T): T & { golemUpsertGuard: Client; $transaction: jest.Mock } {
  const client = {
    ...delegates,
    golemUpsertGuard: {
      upsert: jest.fn().mockResolvedValue({ stripe: 1 }),
      createMany: jest.fn().mockResolvedValue({ count: 0 }),
      findFirst: jest.fn().mockResolvedValue(null),
    },
  } as unknown as T & { golemUpsertGuard: Client; $transaction: jest.Mock };
  client.$transaction = jest.fn(async (run: (tx: Client) => Promise<unknown>) => run(client));
  return client;
}

import { AuthorizationProvider } from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemForbiddenError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const post: DatamodelModel = {
  name: 'Post',
  fields: [
    field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
    field({ name: 'title', type: 'String' }),
    field({ name: 'type', type: 'PostType', kind: 'enum', hasDefaultValue: true }),
    field({ name: 'published', type: 'Boolean', hasDefaultValue: true }),
    field({ name: 'views', type: 'Int', hasDefaultValue: true }),
    field({ name: 'author', type: 'User', kind: 'object', relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
    field({ name: 'authorId', type: 'String', isReadOnly: true }),
  ],
};

const user: DatamodelModel = {
  name: 'User',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'email', type: 'String', isUnique: true }),
    field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
  ],
};

const models = [post, user];
const ctx = { req: {} };

function verifyingProvider(
  overrides: Partial<AuthorizationProvider> = {},
): AuthorizationProvider & { check: jest.Mock; checkField: jest.Mock } {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => ({})),
    check: jest.fn(async () => true),
    checkField: jest.fn(async () => true),
    ...overrides,
  } as never;
}

function guardTxClient(delegates: Record<string, unknown>) {
  return new Proxy(delegates, {
    get(target, prop) {
      if (prop === '$transaction') {
        throw new Error('nested $transaction must not be opened inside an ambient transaction');
      }
      return (target as Record<string | symbol, unknown>)[prop];
    },
  });
}

describe('engine interactive transaction', () => {
  it('routes ops to the transaction client and forwards options', async () => {
    const txDelegates = {
      post: {
        findMany: jest.fn().mockResolvedValue([{ id: 'p1' }]),
        create: jest.fn().mockResolvedValue({ id: 'p2' }),
        count: jest.fn().mockResolvedValue(7),
      },
    };
    const client = {
      post: {
        findMany: jest.fn().mockResolvedValue([]),
        create: jest.fn().mockResolvedValue({}),
        count: jest.fn().mockResolvedValue(0),
      },
      $transaction: jest.fn(async (fn: (tx: unknown) => Promise<unknown>) => fn(txDelegates)),
    };
    const engine = new GolemEngine(client, models);

    const rows = await engine.transaction(
      ctx,
      async (tx) => {
        const found = await tx.findMany({ model: 'Post' });
        await tx.create({ model: 'Post', data: { title: 'x' } });
        await tx.count({ model: 'Post' });
        return found;
      },
      { timeout: 5000, isolationLevel: 'Serializable' },
    );

    expect(rows).toEqual([{ id: 'p1' }]);
    expect(txDelegates.post.findMany).toHaveBeenCalled();
    expect(txDelegates.post.create).toHaveBeenCalled();
    expect(txDelegates.post.count).toHaveBeenCalled();
    expect(client.post.findMany).not.toHaveBeenCalled();
    expect(client.post.create).not.toHaveBeenCalled();
    expect(client.$transaction).toHaveBeenCalledWith(expect.any(Function), {
      timeout: 5000,
      isolationLevel: 'Serializable',
    });
  });

  it('runs a verified write directly on the ambient client without a nested transaction', async () => {
    const row = { id: 'cuid-real', title: 'x', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const txDelegates = {
      post: {
        create: jest.fn().mockResolvedValue(row),
        findUnique: jest.fn().mockResolvedValue({ id: 'cuid-real' }),
      },
    };
    const txClient = guardTxClient(txDelegates);
    const client = {
      post: { create: jest.fn(), findUnique: jest.fn() },
      $transaction: jest.fn(async (fn: (tx: unknown) => Promise<unknown>) => fn(txClient)),
    };
    const authz = verifyingProvider();
    const engine = new GolemEngine(client, models, { authorization: authz, checkReadFields: false });

    await engine.transaction(ctx, async (tx) => {
      await tx.create({ model: 'Post', data: { title: 'x' } });
    });

    expect(txDelegates.post.create).toHaveBeenCalled();
    expect(authz.check).toHaveBeenCalledWith('create', 'Post', row, ctx);
    expect(client.$transaction).toHaveBeenCalledTimes(1);
  });

  it('aborts the transaction when an ambient verified write is denied', async () => {
    const row = { id: 'cuid-real', title: 'x', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const txDelegates = {
      post: {
        create: jest.fn().mockResolvedValue(row),
        findUnique: jest.fn().mockResolvedValue({ id: 'cuid-real' }),
      },
    };
    const txClient = guardTxClient(txDelegates);
    const client = {
      post: { create: jest.fn(), findUnique: jest.fn() },
      $transaction: jest.fn(async (fn: (tx: unknown) => Promise<unknown>) => fn(txClient)),
    };
    const authz = verifyingProvider({ check: jest.fn(async () => false) });
    const engine = new GolemEngine(client, models, { authorization: authz, checkReadFields: false });

    await expect(
      engine.transaction(ctx, async (tx) => {
        await tx.create({ model: 'Post', data: { title: 'x' } });
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.$transaction).toHaveBeenCalledTimes(1);
  });

  it('keeps the context-aware upsert branch probe and write inside the ambient transaction', async () => {
    const txDelegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue(null),
        create: jest.fn().mockResolvedValue({ id: 'p2', title: 'new' }),
      },
    };
    const txClient = guardTxClient(txDelegates);
    const client = {
      post: { findFirst: jest.fn(), create: jest.fn() },
      $transaction: jest.fn(async (run: (tx: unknown) => Promise<unknown>) => run(txClient)),
    };
    const engine = new GolemEngine(client, models);

    await engine.transaction(ctx, (tx) => tx.upsert({
      model: 'Post',
      where: { id: 'p2' },
      create: { title: 'new' },
      update: { title: 'updated' },
    }));

    expect(txDelegates.post.findFirst).toHaveBeenCalledTimes(1);
    expect(txDelegates.post.create).toHaveBeenCalledTimes(1);
    expect(client.post.findFirst).not.toHaveBeenCalled();
    expect(client.post.create).not.toHaveBeenCalled();
    expect(client.$transaction).toHaveBeenCalledTimes(1);
  });
});

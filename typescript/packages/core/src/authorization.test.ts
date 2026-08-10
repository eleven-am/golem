import { parse, subscribe } from 'graphql';
import { AuthorizationProvider } from './authorization';
import { DatamodelDocument } from './datamodel';
import { GolemForbiddenError, GolemNotFoundError } from './errors';
import { GolemEventBus, GolemEventPayload } from './events';
import { GolemEngine } from './operations';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
      field({ name: 'posts', type: 'Post', kind: 'object' as const, isList: true, relationName: 'PostToUser' }),
    ],
  },
  {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'author', type: 'User', kind: 'object' as const, relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
      field({ name: 'authorId', type: 'String', isReadOnly: true }),
    ],
  },
];

const datamodel: DatamodelDocument = { models, enums: [] };

function fakeClient() {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      findFirst: jest.fn().mockResolvedValue(null),
      create: jest.fn().mockResolvedValue({ id: 'u1' }),
      update: jest.fn().mockResolvedValue({ id: 'u1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: 'u1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    },
    post: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      findFirst: jest.fn().mockResolvedValue(null),
      create: jest.fn().mockResolvedValue({ id: 'p1' }),
      update: jest.fn().mockResolvedValue({ id: 'p1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: 'p1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    },
  };
}

function fakeProvider(constraint: unknown = { ownerId: 'me' }): AuthorizationProvider & {
  authorizeCalls: Array<[string, string]>;
} {
  const authorizeCalls: Array<[string, string]> = [];
  return {
    authorizeCalls,
    authorize: jest.fn(async (action: string, model: string) => {
      authorizeCalls.push([action, model]);
    }),
    constrain: jest.fn(async () => constraint),
  };
}

function rowPolicy(provider: AuthorizationProvider) {
  return { authorization: provider, checkWriteResults: false, checkReadFields: false };
}

const ctx = { req: {} };

describe('engine authorization', () => {
  it('merges read constraints into findMany and switches findOne to findFirst', async () => {
    const client = fakeClient();
    const provider = fakeProvider({ authorId: 'me' });
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await engine.findMany({ model: 'Post', where: { title: { contains: 'x' } }, context: ctx });
    expect(client.post.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        where: { AND: [{ title: { contains: 'x' } }, { authorId: 'me' }] },
      }),
    );

    await engine.findOne({ model: 'Post', where: { id: 'p1' }, context: ctx });
    expect(client.post.findUnique).not.toHaveBeenCalled();
    expect(client.post.findFirst).toHaveBeenCalledWith(
      expect.objectContaining({ where: { AND: [{ id: 'p1' }, { authorId: 'me' }] } }),
    );
  });

  it('uses fetch-then-mutate for update and hides constrained rows as NOT_FOUND', async () => {
    const client = fakeClient();
    client.post.findFirst.mockResolvedValueOnce({ id: 'p1' });
    const provider = fakeProvider({ authorId: 'me' });
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx });
    expect(client.post.findFirst).toHaveBeenCalledWith({
      where: { AND: [{ id: 'p1' }, { authorId: 'me' }] },
      select: { id: true },
    });
    expect(client.post.update).toHaveBeenCalledWith(
      expect.objectContaining({ where: { id: 'p1' } }),
    );

    client.post.findFirst.mockResolvedValueOnce(null);
    await expect(
      engine.update({ model: 'Post', where: { id: 'p2' }, data: { title: 'x' }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);
    expect(client.post.update).toHaveBeenCalledTimes(1);
  });

  it('merges constraints into batch operations directly', async () => {
    const client = fakeClient();
    const provider = fakeProvider({ authorId: 'me' });
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await engine.deleteMany({ model: 'Post', where: { title: { contains: 'x' } }, context: ctx });
    expect(client.post.deleteMany).toHaveBeenCalledWith({
      where: { AND: [{ title: { contains: 'x' } }, { authorId: 'me' }] },
    });
  });

  it('gates create and walks nested writes per touched model', async () => {
    const client = fakeClient();
    const provider = fakeProvider();
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await engine.create({
      model: 'User',
      data: {
        email: 'a@b.c',
        posts: { create: [{ title: 'p' }], connect: [{ id: 'p9' }] },
      },
      context: ctx,
    });
    expect(provider.authorizeCalls).toEqual([
      ['create', 'User'],
      ['create', 'Post'],
      ['update', 'Post'],
    ]);
  });

  it('propagates provider denials and skips the database call', async () => {
    const client = fakeClient();
    const provider = fakeProvider();
    (provider.authorize as jest.Mock).mockRejectedValue(new GolemForbiddenError('no'));
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await expect(
      engine.create({ model: 'Post', data: { title: 'x' }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.post.create).not.toHaveBeenCalled();
  });

  it('skips enforcement for internal calls without context', async () => {
    const client = fakeClient();
    const provider = fakeProvider();
    const engine = new GolemEngine(client, models, rowPolicy(provider));

    await engine.findMany({ model: 'Post' });
    expect(provider.constrain).not.toHaveBeenCalled();
    expect(client.post.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ where: undefined }),
    );
  });

  it('memoizes model constraints for repeated work in one request context', async () => {
    const client = fakeClient();
    const provider = fakeProvider({ published: true });
    const engine = new GolemEngine(client, models, rowPolicy(provider));
    const requestContext = { req: {} };

    await engine.findMany({ model: 'Post', context: requestContext });
    await engine.findMany({ model: 'Post', context: requestContext });
    expect(provider.constrain).toHaveBeenCalledTimes(1);

    await engine.findMany({ model: 'Post', context: { req: {} } });
    expect(provider.constrain).toHaveBeenCalledTimes(2);
  });
});

describe('subscription authorization', () => {
  class FakeBus implements GolemEventBus {
    private buffer: GolemEventPayload[] = [];
    private notify: (() => void) | null = null;

    async publish(): Promise<void> {}

    push(payload: GolemEventPayload): void {
      this.buffer.push(payload);
      this.notify?.();
      this.notify = null;
    }

    async *iterate(): AsyncIterableIterator<GolemEventPayload> {
      while (true) {
        while (this.buffer.length > 0) {
          yield this.buffer.shift()!;
        }
        await new Promise<void>((resolve) => {
          this.notify = resolve;
        });
      }
    }
  }

  it('gates subscribe, uses fresh contexts per event and skips unauthorized rows', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.post.findFirst.mockResolvedValueOnce(null).mockResolvedValueOnce({ title: 'mine' });
    const freshContexts: unknown[] = [];
    const provider = fakeProvider({ authorId: 'me' });
    provider.freshContext = (context: unknown) => {
      const wrapped = { fresh: true, inner: context };
      freshContexts.push(wrapped);
      return wrapped;
    };
    const schema = buildGolemSchema({
      datamodel: { models, enums: [] },
      client,
      models: { Post: { subscriptions: true } },
      eventBus: bus,
      authorization: provider,
      defaults: { checkWriteResults: false, checkReadFields: false },
    });

    const iterator = (await subscribe({
      schema,
      document: parse('subscription { postEvents { type entity { title } } }'),
      contextValue: ctx,
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({ type: 'UPDATED', model: 'Post', id: 'p1' });
    bus.push({ type: 'UPDATED', model: 'Post', id: 'p2' });
    const { value } = await next;
    expect(value.data.postEvents.entity).toEqual({ title: 'mine' });
    expect(provider.authorize).toHaveBeenCalledWith('read', 'Post', expect.objectContaining({ fresh: true }));
    expect(freshContexts.length).toBeGreaterThanOrEqual(3);
    expect(client.post.findFirst).toHaveBeenCalledWith(
      expect.objectContaining({
        where: { AND: [{ id: 'p2' }, { authorId: 'me' }] },
      }),
    );
    await iterator.return?.();
  });
});

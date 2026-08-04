import { graphql, parse, subscribe } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { GolemEventBus, GolemEventPayload } from './events';
import { ComputedFieldSpec } from './extensions';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{ User: 'id' | 'email'; Post: 'id' | 'title' }> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'teamId', type: 'String', isRequired: false }),
      ],
    },
    {
      name: 'Post',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'title', type: 'String' }),
        field({ name: 'authorId', type: 'String', isReadOnly: true }),
      ],
    },
  ],
  enums: [],
};

const users = [
  { id: 'u1', email: 'roy@example.com', teamId: 't1' },
  { id: 'u2', email: 'ada@example.com', teamId: 't1' },
  { id: 'u3', email: 'guest@example.com', teamId: null },
];

function clientReturning(rows: unknown[]) {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue(rows),
      findFirst: jest.fn().mockResolvedValue(rows[0]),
    },
    post: { groupBy: jest.fn().mockResolvedValue([]) },
  };
}

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

type BatchLoad = NonNullable<ComputedFieldSpec['batch']>['load'];

function postCountSpec(load: BatchLoad): ComputedFieldSpec {
  return {
    model: 'User',
    name: 'postCount',
    type: 'Int',
    requires: [],
    batch: { key: 'id', load },
    resolve: () => {
      throw new Error('a batched computed field must not resolve per row');
    },
  };
}

describe('batched computed fields', () => {
  it('issues one load for every parent in the result', async () => {
    const client = clientReturning(users);
    const load = jest.fn(async (keys: readonly string[], ctx: unknown) => {
      await (ctx as { client: ReturnType<typeof clientReturning> }).client.post.groupBy({
        by: ['authorId'],
        where: { authorId: { in: [...keys] } },
      });
      return new Map(keys.map((key, index) => [key, index + 1]));
    });
    const schema = buildGolemSchema({ datamodel, client, computedFields: [postCountSpec(load)] });

    const result = await graphql({
      schema,
      source: '{ users { email postCount } }',
      contextValue: { client },
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({
      users: [
        { email: 'roy@example.com', postCount: 1 },
        { email: 'ada@example.com', postCount: 2 },
        { email: 'guest@example.com', postCount: 3 },
      ],
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(client.post.groupBy).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['u1', 'u2', 'u3']);
  });

  it('folds the batch key into the columns the main query selects', async () => {
    const client = clientReturning(users);
    const schema = buildGolemSchema({
      datamodel,
      client,
      computedFields: [postCountSpec(async (keys: readonly string[]) => keys.map(() => 0))],
    });

    await graphql({ schema, source: '{ users { postCount } }', contextValue: {} });

    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { id: true } }),
    );
  });

  it('gives every waiting field the failure when the batch fails', async () => {
    const client = clientReturning(users);
    const schema = buildGolemSchema({
      datamodel,
      client,
      computedFields: [
        postCountSpec(async () => {
          throw new Error('post count unavailable');
        }),
      ],
    });

    const result = await graphql({
      schema,
      source: '{ users { email postCount } }',
      contextValue: {},
    });

    expect(result.errors).toHaveLength(3);
    for (const error of result.errors ?? []) {
      expect(error.message).toBe('post count unavailable');
    }
    expect(result.data).toEqual({
      users: [
        { email: 'roy@example.com', postCount: null },
        { email: 'ada@example.com', postCount: null },
        { email: 'guest@example.com', postCount: null },
      ],
    });
  });

  it('answers a parent whose key is null without dropping the rest of the batch', async () => {
    const client = clientReturning(users);
    const load = jest.fn(
      async (keys: readonly string[]) => new Map(keys.map((key) => [key, key.length])),
    );
    const schema = buildGolemSchema({
      datamodel,
      client,
      computedFields: [
        {
          model: 'User',
          name: 'teamSize',
          type: 'Int',
          requires: [],
          batch: { key: 'teamId', load },
          resolve: () => {
            throw new Error('unused');
          },
        },
      ],
    });

    const result = await graphql({
      schema,
      source: '{ users { email teamSize } }',
      contextValue: {},
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({
      users: [
        { email: 'roy@example.com', teamSize: 2 },
        { email: 'ada@example.com', teamSize: 2 },
        { email: 'guest@example.com', teamSize: null },
      ],
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['t1']);
  });

  it('keeps two requests in flight from sharing a batch', async () => {
    const client = clientReturning(users);
    const load = jest.fn(
      async (keys: readonly string[], ctx: unknown) =>
        new Map(keys.map((key) => [key, (ctx as { visible: number }).visible])),
    );
    const schema = buildGolemSchema({ datamodel, client, computedFields: [postCountSpec(load)] });
    const source = '{ users { email postCount } }';

    const [wide, narrow] = await Promise.all([
      graphql({ schema, source, contextValue: { visible: 9 } }),
      graphql({ schema, source, contextValue: { visible: 1 } }),
    ]);

    expect(wide.data).toEqual({
      users: [
        { email: 'roy@example.com', postCount: 9 },
        { email: 'ada@example.com', postCount: 9 },
        { email: 'guest@example.com', postCount: 9 },
      ],
    });
    expect(narrow.data).toEqual({
      users: [
        { email: 'roy@example.com', postCount: 1 },
        { email: 'ada@example.com', postCount: 1 },
        { email: 'guest@example.com', postCount: 1 },
      ],
    });
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('leaves a computed field without a batch resolving once per row', async () => {
    const client = clientReturning(users);
    const resolve = jest.fn((parent: { email: string }) => parent.email.split('@')[1]);
    const schema = buildGolemSchema({
      datamodel,
      client,
      computedFields: [
        {
          model: 'User',
          name: 'emailDomain',
          type: 'String!',
          requires: ['email'],
          resolve,
        },
      ],
    });

    const result = await graphql({
      schema,
      source: '{ users { emailDomain } }',
      contextValue: {},
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({
      users: [
        { emailDomain: 'example.com' },
        { emailDomain: 'example.com' },
        { emailDomain: 'example.com' },
      ],
    });
    expect(resolve).toHaveBeenCalledTimes(3);
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { email: true } }),
    );
  });

  it('reads the batch again for every subscription event', async () => {
    const bus = new FakeBus();
    const client = clientReturning(users);
    client.user.findFirst.mockResolvedValue({ id: 'u1' });
    const stored = new Map([['u1', 1]]);
    const load = jest.fn(async (keys: readonly string[]) =>
      new Map(keys.map((key) => [key, stored.get(key) ?? 0])),
    );
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { User: { subscriptions: true } },
      eventBus: bus,
      defaults: { checkWriteResults: false, checkReadFields: false },
      computedFields: [postCountSpec(load)],
    });
    const iterator = (await subscribe({
      schema,
      document: parse('subscription { userEvents { type entity { postCount } } }'),
      contextValue: {},
    })) as AsyncIterableIterator<any>;

    const first = iterator.next();
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    expect((await first).value.data.userEvents.entity).toEqual({ postCount: 1 });

    stored.set('u1', 7);

    const second = iterator.next();
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    expect((await second).value.data.userEvents.entity).toEqual({ postCount: 7 });
    expect(load).toHaveBeenCalledTimes(2);

    await iterator.return?.();
  });

  it('answers a read that follows a write in the same request from after the write', async () => {
    let stored = 2;
    const client = {
      ...clientReturning(users),
      user: {
        findMany: jest.fn().mockResolvedValue(users),
        findFirst: jest.fn().mockResolvedValue(users[0]),
        update: jest.fn().mockResolvedValue({ id: 'u1' }),
      },
      post: {
        groupBy: jest.fn().mockResolvedValue([]),
        create: jest.fn().mockImplementation(async () => {
          stored += 1;
          return { id: 'p9' };
        }),
      },
    };
    const load = jest.fn(async (keys: readonly string[]) =>
      new Map(keys.map((key) => [key, stored])),
    );
    const schema = buildGolemSchema({ datamodel, client, computedFields: [postCountSpec(load)] });

    const result = await graphql({
      schema,
      source: `mutation {
        before: updateUser(where: { id: "u1" }, data: { teamId: "t2" }) { postCount }
        made: createPost(data: { title: "mid-request" }) { id }
        after: updateUser(where: { id: "u1" }, data: { teamId: "t3" }) { postCount }
      }`,
      contextValue: {},
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({
      before: { postCount: 2 },
      made: { id: 'p9' },
      after: { postCount: 3 },
    });
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('answers the payload of a delete from after the delete', async () => {
    let stored = 2;
    const client = {
      user: {
        findMany: jest.fn().mockResolvedValue(users),
        findFirst: jest.fn().mockResolvedValue(users[0]),
        update: jest.fn().mockResolvedValue({ id: 'u1' }),
        delete: jest.fn().mockImplementation(async () => {
          stored -= 1;
          return { id: 'u1' };
        }),
      },
      post: { groupBy: jest.fn().mockResolvedValue([]) },
    };
    const load = jest.fn(async (keys: readonly string[]) =>
      new Map(keys.map((key) => [key, stored])),
    );
    const schema = buildGolemSchema({ datamodel, client, computedFields: [postCountSpec(load)] });

    const result = await graphql({
      schema,
      source: `mutation {
        before: updateUser(where: { id: "u1" }, data: { teamId: "t2" }) { postCount }
        gone: deleteUser(where: { id: "u1" }) { postCount }
      }`,
      contextValue: {},
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ before: { postCount: 2 }, gone: { postCount: 1 } });
  });

  it('holds one batch across the rows a single read returns', async () => {
    const client = clientReturning(users);
    const load = jest.fn(async (keys: readonly string[]) => new Map(keys.map((key) => [key, 1])));
    const schema = buildGolemSchema({ datamodel, client, computedFields: [postCountSpec(load)] });

    const result = await graphql({
      schema,
      source: '{ first: users { postCount } second: users { postCount } }',
      contextValue: {},
    });

    expect(result.errors).toBeUndefined();
    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['u1', 'u2', 'u3']);
  });

  it('rejects a batch key that is not a field of the model', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: clientReturning(users),
        computedFields: [
          {
            model: 'User',
            name: 'postCount',
            type: 'Int',
            requires: [],
            batch: { key: 'ghost', load: async (keys) => keys.map(() => 0) },
            resolve: () => 0,
          },
        ],
      }),
    ).toThrow('Computed field User.postCount requires unknown field ghost');
  });
});

import { clearBatchCaches, createBatchLoader } from './batch';
import { COMPILED_READ_BATCH_CHUNK } from './compiled-read-run';

describe('createBatchLoader', () => {
  it('loads once for every key resolved in the same tick', async () => {
    const load = jest.fn(
      async (keys: readonly string[]) => new Map(keys.map((key) => [key, key.toUpperCase()])),
    );
    const loader = createBatchLoader<string, string>({ name: 'test', load });
    const ctx = {};

    const values = await Promise.all(['a', 'b', 'c'].map((key) => loader.load(ctx, key)));

    expect(values).toEqual(['A', 'B', 'C']);
    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['a', 'b', 'c']);
  });

  it('accepts an array aligned with the keys', async () => {
    const loader = createBatchLoader<number, number>({
      name: 'test',
      load: async (keys) => keys.map((key) => key * 2),
    });
    const ctx = {};

    await expect(Promise.all([loader.load(ctx, 1), loader.load(ctx, 2)])).resolves.toEqual([2, 4]);
  });

  it('hands the request context to the batch untouched', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const ctx = { user: 'roy' };

    await loader.load(ctx, 'a');

    expect(load.mock.calls[0][1]).toBe(ctx);
  });

  it('resolves a null key without putting it in the batch', async () => {
    const load = jest.fn(
      async (keys: readonly string[]) => new Map(keys.map((key) => [key, key.length])),
    );
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const ctx = {};

    const values = await Promise.all([
      loader.load(ctx, null),
      loader.load(ctx, 'abc'),
      loader.load(ctx, undefined),
    ]);

    expect(values).toEqual([null, 3, null]);
    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['abc']);
  });

  it('resolves a key the batch did not answer for as null', async () => {
    const loader = createBatchLoader<string, number>({
      name: 'test',
      load: async () => new Map([['a', 1]]),
    });
    const ctx = {};

    await expect(Promise.all([loader.load(ctx, 'a'), loader.load(ctx, 'b')])).resolves.toEqual([
      1,
      null,
    ]);
  });

  it('rejects every waiter when the batch fails', async () => {
    const failure = new Error('batch exploded');
    const loader = createBatchLoader<string, number>({
      name: 'test',
      load: async () => {
        throw failure;
      },
    });
    const ctx = {};

    const settled = await Promise.allSettled([
      loader.load(ctx, 'a'),
      loader.load(ctx, 'b'),
      loader.load(ctx, 'c'),
    ]);

    expect(settled.map((result) => result.status)).toEqual(['rejected', 'rejected', 'rejected']);
    for (const result of settled) {
      expect((result as PromiseRejectedResult).reason).toBe(failure);
    }
  });

  it('rejects every waiter when the batch returns the wrong number of results', async () => {
    const loader = createBatchLoader<string, number>({
      name: 'User.postCount',
      load: async () => [1],
    });
    const ctx = {};

    const settled = await Promise.allSettled([loader.load(ctx, 'a'), loader.load(ctx, 'b')]);

    expect(settled.map((result) => result.status)).toEqual(['rejected', 'rejected']);
    expect((settled[0] as PromiseRejectedResult).reason.message).toBe(
      'Batch loader User.postCount returned 1 results for 2 keys',
    );
  });

  it('rejects only the key the batch answered with an error', async () => {
    const loader = createBatchLoader<string, number>({
      name: 'test',
      load: async (keys) => keys.map((key) => (key === 'b' ? new Error('no b') : 1)),
    });
    const ctx = {};

    const settled = await Promise.allSettled([loader.load(ctx, 'a'), loader.load(ctx, 'b')]);

    expect(settled[0]).toEqual({ status: 'fulfilled', value: 1 });
    expect(settled[1].status).toBe('rejected');
  });

  it('keeps one request from seeing another request\'s values', async () => {
    const load = jest.fn(
      async (keys: readonly string[], ctx: unknown) =>
        new Map(keys.map((key) => [key, `${(ctx as { viewer: string }).viewer}:${key}`])),
    );
    const loader = createBatchLoader<string, string>({ name: 'test', load });
    const roy = { viewer: 'roy' };
    const ada = { viewer: 'ada' };

    const [royValue, adaValue] = await Promise.all([
      loader.load(roy, 'shared'),
      loader.load(ada, 'shared'),
    ]);

    expect(royValue).toBe('roy:shared');
    expect(adaValue).toBe('ada:shared');
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('keeps a later request from reading an earlier request\'s cache', async () => {
    let answer = 'first';
    const loader = createBatchLoader<string, string>({
      name: 'test',
      load: async (keys) => keys.map(() => answer),
    });

    const first = await loader.load({}, 'shared');
    answer = 'second';
    const second = await loader.load({}, 'shared');

    expect(first).toBe('first');
    expect(second).toBe('second');
  });

  it('reuses one loader for the same request', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const ctx = {};

    await loader.load(ctx, 'a');
    await loader.load(ctx, 'a');

    expect(load).toHaveBeenCalledTimes(1);
  });

  it('starts a new batch for each execution of the same request', async () => {
    let stored = 'first';
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => stored));
    const loader = createBatchLoader<string, string>({ name: 'test', load });
    const connection = {};

    const first = await loader.load(connection, 'shared', undefined, { rootValue: { event: 1 } });
    stored = 'second';
    const second = await loader.load(connection, 'shared', undefined, { rootValue: { event: 2 } });

    expect(first).toBe('first');
    expect(second).toBe('second');
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('keeps one batch for every key of a single execution', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map((key) => key.length));
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const ctx = {};
    const execution = { rootValue: { event: 1 } };

    await Promise.all([
      loader.load(ctx, 'a', undefined, execution),
      loader.load(ctx, 'bb', undefined, execution),
    ]);

    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0]).toEqual(['a', 'bb']);
  });

  it('keeps two requests apart even when they share an execution root', async () => {
    const load = jest.fn(
      async (keys: readonly string[], ctx: unknown) =>
        keys.map(() => (ctx as { viewer: string }).viewer),
    );
    const loader = createBatchLoader<string, string>({ name: 'test', load });
    const shared = { rootValue: { operation: 'events' } };

    const [royValue, adaValue] = await Promise.all([
      loader.load({ viewer: 'roy' }, 'shared', undefined, shared),
      loader.load({ viewer: 'ada' }, 'shared', undefined, shared),
    ]);

    expect(royValue).toBe('roy');
    expect(adaValue).toBe('ada');
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('batches each set of arguments separately', async () => {
    const load = jest.fn(
      async (keys: readonly string[], _ctx: unknown, args: { published: boolean }) =>
        keys.map(() => args.published),
    );
    const loader = createBatchLoader<string, boolean, { published: boolean }>({
      name: 'test',
      load,
    });
    const ctx = {};

    const values = await Promise.all([
      loader.load(ctx, 'a', { published: true }),
      loader.load(ctx, 'b', { published: false }),
      loader.load(ctx, 'c', { published: true }),
    ]);

    expect(values).toEqual([true, false, true]);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('treats argument objects that differ only in key order as one batch', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number, Record<string, unknown>>({
      name: 'test',
      load,
    });
    const ctx = {};

    await Promise.all([
      loader.load(ctx, 'a', { take: 1, skip: 2 }),
      loader.load(ctx, 'b', { skip: 2, take: 1 }),
    ]);

    expect(load).toHaveBeenCalledTimes(1);
  });

  it('groups keys through cacheKey when they are not primitives', async () => {
    const load = jest.fn(
      async (keys: readonly { id: string }[]) => new Map(keys.map((key) => [key, key.id])),
    );
    const loader = createBatchLoader<{ id: string }, string>({
      name: 'test',
      load,
      cacheKey: (key) => key.id,
    });
    const ctx = {};

    const values = await Promise.all([
      loader.load(ctx, { id: 'a' }),
      loader.load(ctx, { id: 'a' }),
      loader.load(ctx, { id: 'b' }),
    ]);

    expect(values).toEqual(['a', 'a', 'b']);
    expect(load.mock.calls[0][0]).toEqual([{ id: 'a' }, { id: 'b' }]);
  });

  it('reads again once the request clears what it cached', async () => {
    let stored = 2;
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => stored));
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const ctx = {};

    const before = await loader.load(ctx, 'a');
    stored = 3;
    clearBatchCaches(ctx);
    const after = await loader.load(ctx, 'a');

    expect([before, after]).toEqual([2, 3]);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('clears every argument variant of every loader the request holds', async () => {
    let stored = 2;
    const posts = createBatchLoader<string, number, { published: boolean } | undefined>({
      name: 'posts',
      load: async (keys) => keys.map(() => stored),
    });
    const comments = createBatchLoader<string, number>({
      name: 'comments',
      load: async (keys) => keys.map(() => stored),
    });
    const ctx = {};

    await Promise.all([
      posts.load(ctx, 'a'),
      posts.load(ctx, 'a', { published: true }),
      comments.load(ctx, 'a'),
    ]);
    stored = 3;
    clearBatchCaches(ctx);

    await expect(
      Promise.all([
        posts.load(ctx, 'a'),
        posts.load(ctx, 'a', { published: true }),
        comments.load(ctx, 'a'),
      ]),
    ).resolves.toEqual([3, 3, 3]);
  });

  it('clears only the request that wrote', async () => {
    let stored = 2;
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => stored));
    const loader = createBatchLoader<string, number>({ name: 'test', load });
    const writer = {};
    const reader = {};

    await Promise.all([loader.load(writer, 'a'), loader.load(reader, 'a')]);
    stored = 3;
    clearBatchCaches(writer);

    await expect(
      Promise.all([loader.load(writer, 'a'), loader.load(reader, 'a')]),
    ).resolves.toEqual([3, 2]);
  });

  it('clears only the execution that wrote', async () => {
    let stored = 2;
    const loader = createBatchLoader<string, number>({
      name: 'test',
      load: async (keys) => keys.map(() => stored),
    });
    const connection = {};
    const written = { rootValue: { event: 1 } };
    const untouched = { rootValue: { event: 2 } };

    await Promise.all([
      loader.load(connection, 'a', undefined, written),
      loader.load(connection, 'a', undefined, untouched),
    ]);
    stored = 3;
    clearBatchCaches(connection, written);

    await expect(
      Promise.all([
        loader.load(connection, 'a', undefined, written),
        loader.load(connection, 'a', undefined, untouched),
      ]),
    ).resolves.toEqual([3, 2]);
  });

  it('ignores a request that never batched anything', () => {
    expect(() => clearBatchCaches(undefined)).not.toThrow();
    expect(() => clearBatchCaches({})).not.toThrow();
  });

  it('refuses arguments it cannot tell apart by their own state', async () => {
    class Tenant {
      readonly #id: string;

      constructor(id: string) {
        this.#id = id;
      }

      id(): string {
        return this.#id;
      }
    }
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number, { tenant: Tenant }>({
      name: 'User.postCount',
      load,
    });

    await expect(loader.load({}, 'a', { tenant: new Tenant('a') })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by argument tenant: a Tenant instance cannot be serialized exactly',
    );
    expect(load).not.toHaveBeenCalled();
  });

  it('refuses arguments whose state hides behind symbol keys', async () => {
    const tenant = Symbol('tenant');
    const loader = createBatchLoader<string, number, Record<symbol, string>>({
      name: 'User.postCount',
      load: async (keys) => keys.map(() => 1),
    });

    await expect(loader.load({}, 'a', { [tenant]: 'A' })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by its arguments: it carries symbol keys, which cannot be serialized exactly',
    );
  });

  it('refuses an argument that refers back to itself instead of recursing', async () => {
    const cyclic: Record<string, unknown> = { name: 'a' };
    cyclic.self = cyclic;
    const loader = createBatchLoader<string, number, { filter: unknown }>({
      name: 'User.postCount',
      load: async (keys) => keys.map(() => 1),
    });

    await expect(loader.load({}, 'a', { filter: cyclic })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by argument filter.self: it holds a reference back to itself',
    );
  });

  it('keeps arguments that repeat a value without a cycle', async () => {
    const shared = { tenant: 'a' };
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number, Record<string, unknown>>({
      name: 'test',
      load,
    });
    const ctx = {};

    await Promise.all([
      loader.load(ctx, 'a', { left: shared, right: shared }),
      loader.load(ctx, 'b', { left: { tenant: 'a' }, right: { tenant: 'a' } }),
    ]);

    expect(load).toHaveBeenCalledTimes(1);
  });

  it('refuses a function or a symbol as an argument value', async () => {
    const loader = createBatchLoader<string, number, unknown>({
      name: 'User.postCount',
      load: async (keys) => keys.map(() => 1),
    });

    await expect(loader.load({}, 'a', { pick: () => 1 })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by argument pick: a function cannot be serialized exactly',
    );
    await expect(loader.load({}, 'a', { pick: Symbol('x') })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by argument pick: a symbol cannot be serialized exactly',
    );
  });

  it('refuses a Map argument rather than reading it as an empty object', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number, { where: Map<string, string> }>({
      name: 'User.postCount',
      load,
    });
    const ctx = {};

    await expect(loader.load(ctx, 'a', { where: new Map([['tenant', 'A']]) })).rejects.toThrow(
      'Batch loader User.postCount cannot tell one batch from another by argument where: a Map instance cannot be serialized exactly',
    );
    expect(load).not.toHaveBeenCalled();
  });

  it('tells an invalid date apart from a date it can render', async () => {
    const load = jest.fn(async (keys: readonly string[]) => keys.map(() => 1));
    const loader = createBatchLoader<string, number, { since: Date }>({ name: 'test', load });
    const ctx = {};

    await Promise.all([
      loader.load(ctx, 'a', { since: new Date('2024-01-01T00:00:00.000Z') }),
      loader.load(ctx, 'b', { since: new Date('nonsense') }),
      loader.load(ctx, 'c', { since: new Date('nonsense') }),
    ]);

    expect(load).toHaveBeenCalledTimes(2);
  });

  it('keeps a batch within the chunk the compiled reads use', async () => {
    const load = jest.fn(async (keys: readonly number[]) => keys.map(() => 1));
    const loader = createBatchLoader<number, number>({ name: 'test', load });
    const ctx = {};
    const keys = Array.from({ length: COMPILED_READ_BATCH_CHUNK + 100 }, (_, index) => index);

    await Promise.all(keys.map((key) => loader.load(ctx, key)));

    expect(load.mock.calls.map((call) => call[0].length)).toEqual([
      COMPILED_READ_BATCH_CHUNK,
      100,
    ]);
  });

  it('lets a loader ask for a smaller chunk than the default', async () => {
    const load = jest.fn(async (keys: readonly number[]) => keys.map(() => 1));
    const loader = createBatchLoader<number, number>({ name: 'test', load, maxBatchSize: 2 });
    const ctx = {};

    await Promise.all([1, 2, 3].map((key) => loader.load(ctx, key)));

    expect(load.mock.calls.map((call) => call[0].length)).toEqual([2, 1]);
  });

  it('refuses a batch whose answers do not identify like the keys it was given', async () => {
    const loader = createBatchLoader<{ id: string }, number, undefined>({
      name: 'User.postCount',
      load: async (keys) => new Map(keys.map((key) => [key.id as unknown as { id: string }, 1])),
      cacheKey: (key) => key.id,
    });

    await expect(loader.load({}, { id: 'a' })).rejects.toThrow(
      'Batch loader User.postCount answered for 1 key it was not given, starting with a; the keys it answers with must identify the same way as the keys it is given',
    );
  });

  it('refuses to batch without an object request context', async () => {
    const loader = createBatchLoader<string, number>({
      name: 'User.postCount',
      load: async (keys) => keys.map(() => 1),
    });

    await expect(loader.load(undefined, 'a')).rejects.toThrow(
      'Batch loader User.postCount needs an object request context to scope its batch to, but received undefined',
    );
  });
});

import { claimEngineWrite, engineDelegate, EngineDelegate } from './engine-writes';

type Write = { readonly operation: string; readonly args: unknown };

const cloned = (value: unknown): unknown => {
  if (value instanceof Date) return new Date(value.getTime());
  if (Array.isArray(value)) return value.map(cloned);
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, cloned(child)]));
  }
  return value;
};

const intercepting = (claims: boolean[], before: (raw: EngineDelegate) => Promise<void> = async () => undefined) => {
  const write = (operation: string) => async (args: unknown) => {
    claims.push(claimEngineWrite('Post', operation, cloned(args)));
    return { operation, args } satisfies Write;
  };
  const raw: EngineDelegate = { create: write('create'), update: write('update'), updateMany: write('updateMany') };
  return {
    ...raw,
    create: async (args: unknown) => {
      await before(raw);
      return raw.create(args);
    },
  } satisfies EngineDelegate;
};

describe('engine-issued writes', () => {
  const args = { data: { id: 1, title: 'a', at: new Date('2026-01-01T00:00:00.000Z') }, select: { id: true } };

  it('lets the interceptor claim the exact write the engine issued, once', async () => {
    const claims: boolean[] = [];

    await engineDelegate('Post', intercepting(claims)).create(args);

    expect(claims).toEqual([true]);
  });

  it('refuses the claim to a different write of the same model and operation issued inside the engine call', async () => {
    const claims: boolean[] = [];
    const delegate = intercepting(claims, async (inner) => {
      await inner.update({ where: { id: 1 }, data: { title: 'b' } });
      await inner.create({ data: { id: 2, title: 'foreign' }, select: { id: true } });
    });

    await engineDelegate('Post', delegate).create(args);

    expect(claims).toEqual([false, false, true]);
  });

  it('refuses the claim to a repeat of the engine write once it is claimed', async () => {
    const claims: boolean[] = [];
    const delegate = engineDelegate('Post', intercepting(claims));
    const twice: EngineDelegate = {
      create: async (request) => {
        const inner = intercepting(claims);
        await inner.create(request);
        return inner.create(request);
      },
    };

    await engineDelegate('Post', twice).create(args);
    await delegate.create(args);

    expect(claims).toEqual([true, false, true]);
  });

  it('refuses a claim for another model', async () => {
    const delegate: EngineDelegate = {
      create: async (request) => claimEngineWrite('Comment', 'create', request),
    };

    await expect(engineDelegate('Post', delegate).create(args)).resolves.toBe(false);
  });

  it('leaves a write the client substituted for the engine write unclaimed', async () => {
    const claims: boolean[] = [];
    const delegate: EngineDelegate = {
      create: async () => claims.push(claimEngineWrite('Post', 'create', { data: { id: 9 } })),
    };

    await engineDelegate('Post', delegate).create(args);

    expect(claims).toEqual([false]);
  });

  it('marks nothing for a write it cannot fingerprint exactly, so the interceptor guards it too', async () => {
    class Sentinel {}
    const claims: boolean[] = [];
    const delegate: EngineDelegate = {
      create: async (request) => claims.push(claimEngineWrite('Post', 'create', request)),
    };
    const outer: EngineDelegate = {
      create: async () => engineDelegate('Post', delegate).create({ data: { id: 1, meta: new Sentinel() } }),
    };

    await engineDelegate('Post', outer).create({ data: { id: 1, meta: {} } });

    expect(claims).toEqual([false]);
  });

  it('keeps concurrent engine writes apart', async () => {
    const claims: boolean[] = [];
    const delegate = engineDelegate('Post', intercepting(claims, () => new Promise((resolve) => setTimeout(resolve, 1))));

    await Promise.all([1, 2, 3, 4].map((id) => delegate.create({ data: { id } })));

    expect(claims).toEqual([true, true, true, true]);
  });

  it('marks nothing on reads', async () => {
    const delegate: EngineDelegate = {
      findMany: async () => claimEngineWrite('Post', 'findMany', {}),
    };

    await expect(engineDelegate('Post', delegate).findMany({})).resolves.toBe(false);
  });
});

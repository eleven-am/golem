import { PrismaJobStore } from './prisma-job-store';

function build() {
  const job = {
    create: jest.fn(),
    findMany: jest.fn().mockResolvedValue([]),
    findFirst: jest.fn(),
    updateMany: jest.fn(),
    deleteMany: jest.fn(),
    groupBy: jest.fn().mockResolvedValue([]),
  };
  return {
    job,
    store: new PrismaJobStore({ job }),
  };
}

describe('PrismaJobStore administrative queries', () => {
  it('counts statuses with database groupBy and maps absent statuses to zero', async () => {
    const { job, store } = build();
    job.groupBy.mockResolvedValue([
      { status: 'PENDING', _count: { _all: 4 } },
      { status: 'FAILED', _count: { _all: 2 } },
    ]);

    await expect(store.countByStatus({})).resolves.toEqual({
      PENDING: 4,
      RUNNING: 0,
      SUCCEEDED: 0,
      FAILED: 2,
    });
    expect(job.groupBy).toHaveBeenCalledWith({
      by: ['status'],
      where: {},
      _count: { _all: true },
    });
    expect(job.findMany).not.toHaveBeenCalled();
  });

  it('preserves scope, type and status filters in database status counts', async () => {
    const { job, store } = build();
    await store.countByStatus({
      scopeType: 'Article',
      scopeId: 'a1',
      types: ['extract', 'embed'],
      statuses: ['FAILED'],
    });

    expect(job.groupBy).toHaveBeenCalledWith(expect.objectContaining({
      where: {
        scopeType: 'Article',
        scopeId: 'a1',
        type: { in: ['extract', 'embed'] },
        status: { in: ['FAILED'] },
      },
    }));
  });

  it('finds all administrative ids when no explicit limit is supplied', async () => {
    const { job, store } = build();
    job.findMany.mockResolvedValue([{ id: 'a' }, { id: 'b' }]);

    await expect(store.findJobIds({ statuses: ['FAILED'] })).resolves.toEqual(['a', 'b']);
    expect(job.findMany).toHaveBeenCalledWith({
      where: { status: { in: ['FAILED'] } },
      orderBy: { runAt: 'desc' },
      skip: undefined,
      select: { id: true },
    });
  });

  it('passes through an explicit administrative page', async () => {
    const { job, store } = build();
    await store.findJobIds({ statuses: ['FAILED'], limit: 25, skip: 10 });
    expect(job.findMany).toHaveBeenCalledWith(expect.objectContaining({ take: 25, skip: 10 }));
  });
});

describe('PrismaJobStore lease renewal', () => {
  it('fences renewal on owner, running status, and an unexpired lease', async () => {
    const { job, store } = build();
    job.updateMany.mockResolvedValue({ count: 1 });
    const now = new Date('2026-01-01T00:00:00.000Z');
    const leaseExpiresAt = new Date('2026-01-01T00:01:00.000Z');

    const renewed = await store.renewLease({
      id: 'job-1',
      leaseOwner: 'worker-1',
      leaseExpiresAt,
      now,
    });

    expect(renewed).toBe(true);
    expect(job.updateMany).toHaveBeenCalledWith({
      where: {
        id: 'job-1',
        status: 'RUNNING',
        leaseOwner: 'worker-1',
        leaseExpiresAt: { gt: now },
      },
      data: { leaseExpiresAt },
    });
  });

  it('reports a lost lease when the fenced update matches nothing', async () => {
    const { job, store } = build();
    job.updateMany.mockResolvedValue({ count: 0 });

    await expect(
      store.renewLease({
        id: 'job-1',
        leaseOwner: 'worker-1',
        leaseExpiresAt: new Date(),
        now: new Date(),
      }),
    ).resolves.toBe(false);
  });
});

describe('PrismaJobStore guarded claims', () => {
  function client(options: { holders?: number; members?: { type: string }[] } = {}) {
    const calls: string[] = [];
    const job = {
      count: jest.fn(async () => {
        calls.push('job.count');
        return options.holders ?? 0;
      }),
      findMany: jest.fn(async () => {
        calls.push('job.findMany');
        return options.members ?? [];
      }),
      updateMany: jest.fn(async (args: { where: Where; data: Data }) => {
        calls.push('job.updateMany');
        lastUpdate = args;
        return { count: 1 };
      }),
    };
    const jobGuard = {
      create: jest.fn(async (args: { data: { key: string } }) => {
        calls.push(`guard.create:${args.data.key}`);
      }),
      update: jest.fn(async (args: { where: { key: string } }) => {
        calls.push(`guard.update:${args.where.key}`);
      }),
    };
    let lastUpdate: { where: Where; data: Data } | undefined;
    const prisma = {
      job,
      jobGuard,
      async $transaction<T>(fn: (tx: unknown) => Promise<T>): Promise<T> {
        calls.push('tx.begin');
        const out = await fn(prisma);
        calls.push('tx.commit');
        return out;
      },
    };
    return { calls, job, jobGuard, prisma, update: () => lastUpdate };
  }

  const base = {
    id: 'job-1',
    fromStatus: 'PENDING' as const,
    now: new Date('2026-01-01T00:00:00.000Z'),
    leaseOwner: 'worker-1',
    leaseExpiresAt: new Date('2026-01-01T00:01:00.000Z'),
  };

  it('claims without a transaction when no guard applies', async () => {
    const { calls, prisma } = client();
    const store = new PrismaJobStore(prisma as never);

    expect(await store.claim(base)).toBe(true);
    expect(calls).toEqual(['job.updateMany']);
  });

  it('writes every guard row before reading anything', async () => {
    const { calls, prisma } = client();
    const store = new PrismaJobStore(prisma as never);

    await store.claim({
      ...base,
      guardKeys: ['excl:a|b', 'pool:spotify'],
      excludeTypes: ['b'],
      pool: { types: ['a'], costs: {}, limit: 2, cost: 1 },
    });

    const begin = calls.indexOf('tx.begin');
    const lastGuardWrite = calls.lastIndexOf('guard.update:pool:spotify');
    const firstRead = calls.findIndex(
      (c, i) => i > begin && (c === 'job.count' || c === 'job.findMany'),
    );
    expect(begin).toBeGreaterThanOrEqual(0);
    expect(lastGuardWrite).toBeGreaterThan(begin);
    expect(firstRead).toBeGreaterThan(lastGuardWrite);
    expect(calls[calls.length - 1]).toBe('tx.commit');
  });

  it('acquires guard rows in the order given, so two guards cannot deadlock', async () => {
    const { calls, prisma } = client();
    const store = new PrismaJobStore(prisma as never);

    await store.claim({
      ...base,
      guardKeys: ['excl:a|b', 'pool:spotify', 'scope:Article'],
    });

    const acquired = calls.filter((c) => c.startsWith('guard.update:'));
    expect(acquired).toEqual([
      'guard.update:excl:a|b',
      'guard.update:pool:spotify',
      'guard.update:scope:Article',
    ]);
  });

  it('creates a guard row once and reuses it', async () => {
    const { jobGuard, prisma } = client();
    const store = new PrismaJobStore(prisma as never);

    await store.claim({ ...base, guardKeys: ['pool:spotify'] });
    await store.claim({ ...base, guardKeys: ['pool:spotify'] });

    expect(jobGuard.create).toHaveBeenCalledTimes(1);
    expect(jobGuard.update).toHaveBeenCalledTimes(2);
  });

  it('refuses the claim when a live lease holds the scope', async () => {
    const { job, prisma } = client({ holders: 1 });
    const store = new PrismaJobStore(prisma as never);

    expect(
      await store.claim({
        ...base,
        guardKeys: ['scope:Article'],
        serializeScope: true,
        scopeType: 'Article',
        scopeId: 'a1',
      }),
    ).toBe(false);

    expect(job.updateMany).not.toHaveBeenCalled();
    expect(job.count).toHaveBeenCalledWith({
      where: {
        id: { not: 'job-1' },
        scopeType: 'Article',
        scopeId: 'a1',
        status: 'RUNNING',
        leaseExpiresAt: { gt: base.now },
      },
    });
  });

  it('refuses the claim while an excluded type is pending or running', async () => {
    const { job, prisma } = client({ holders: 1 });
    const store = new PrismaJobStore(prisma as never);

    expect(
      await store.claim({
        ...base,
        guardKeys: ['excl:history-import|track-hydrate'],
        excludeTypes: ['history-import'],
      }),
    ).toBe(false);

    expect(job.count).toHaveBeenCalledWith({
      where: {
        type: { in: ['history-import'] },
        status: { in: ['PENDING', 'RUNNING'] },
      },
    });
  });

  it('weighs pool members by cost and refuses when the budget is spent', async () => {
    const { job, prisma } = client({
      members: [{ type: 'sync' }, { type: 'hydrate' }],
    });
    const store = new PrismaJobStore(prisma as never);

    expect(
      await store.claim({
        ...base,
        guardKeys: ['pool:spotify'],
        pool: { types: ['sync', 'hydrate'], costs: { sync: 3 }, limit: 4, cost: 1 },
      }),
    ).toBe(false);
    expect(job.updateMany).not.toHaveBeenCalled();
  });

  it('admits the claim when the pool still has room', async () => {
    const { prisma, update } = client({ members: [{ type: 'hydrate' }] });
    const store = new PrismaJobStore(prisma as never);

    expect(
      await store.claim({
        ...base,
        guardKeys: ['pool:spotify'],
        pool: { types: ['sync', 'hydrate'], costs: {}, limit: 4, cost: 1 },
      }),
    ).toBe(true);
    expect((update()?.data as { startedAt?: Date })?.startedAt).toEqual(base.now);
  });

  it('records when the job started so a rate window can be derived', async () => {
    const { prisma, update } = client();
    const store = new PrismaJobStore(prisma as never);

    await store.claim(base);

    expect((update()?.data as { startedAt?: Date })?.startedAt).toEqual(base.now);
  });

  it('names what is missing when the client cannot run a guarded claim', async () => {
    const store = new PrismaJobStore({ job: {} as never });

    await expect(
      store.claim({ ...base, guardKeys: ['pool:spotify'] }),
    ).rejects.toThrow(/\$transaction and a jobGuard delegate/);
  });
});

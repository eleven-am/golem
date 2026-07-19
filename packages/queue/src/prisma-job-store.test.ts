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

describe('PrismaJobStore scope serialization', () => {
  function client(affected: number) {
    const calls: Array<{ sql: string; values: unknown[] }> = [];
    return {
      calls,
      prisma: {
        job: {} as never,
        $executeRawUnsafe(sql: string, ...values: unknown[]) {
          calls.push({ sql, values });
          return Promise.resolve(affected);
        },
      },
    };
  }

  const input = {
    id: 'job-1',
    fromStatus: 'PENDING' as const,
    now: new Date('2026-01-01T00:00:00.000Z'),
    leaseOwner: 'worker-1',
    leaseExpiresAt: new Date('2026-01-01T00:01:00.000Z'),
    serializeScope: true,
  };

  it('claims and checks the scope in one statement', async () => {
    const { calls, prisma } = client(1);
    const store = new PrismaJobStore(prisma);

    expect(await store.claim(input)).toBe(true);
    expect(calls).toHaveLength(1);

    const sql = calls[0].sql.replace(/\s+/g, ' ');
    expect(sql).toMatch(/^UPDATE "Job" SET/);
    expect(sql).toContain('NOT EXISTS');
  });

  it('only treats a live lease as a blocker', async () => {
    const { calls, prisma } = client(1);
    await new PrismaJobStore(prisma).claim(input);

    const sql = calls[0].sql.replace(/\s+/g, ' ');
    expect(sql).toContain(`active."status" = 'RUNNING'`);
    expect(sql).toContain('active."leaseExpiresAt" > ?');
  });

  it('never blocks a candidate on its own row', async () => {
    const { calls, prisma } = client(1);
    await new PrismaJobStore(prisma).claim(input);

    expect(calls[0].sql.replace(/\s+/g, ' ')).toContain('active."id" <> "Job"."id"');
  });

  it('reports a lost race as a failed claim', async () => {
    const { prisma } = client(0);
    expect(await new PrismaJobStore(prisma).claim(input)).toBe(false);
  });

  it('leaves ordinary claims on the delegate path', async () => {
    const { calls, prisma } = client(1);
    const store = new PrismaJobStore({
      ...prisma,
      job: { updateMany: () => Promise.resolve({ count: 1 }) } as never,
    });

    expect(await store.claim({ ...input, serializeScope: undefined })).toBe(true);
    expect(calls).toHaveLength(0);
  });

  it('refuses serialization when the client cannot run a statement', async () => {
    const store = new PrismaJobStore({ job: {} as never });
    await expect(store.claim(input)).rejects.toThrow(/serializeByScope/);
  });

  it('honours a mapped table name', async () => {
    const { calls, prisma } = client(1);
    await new PrismaJobStore(prisma, { table: 'queue_jobs' }).claim(input);

    expect(calls[0].sql).toContain('"queue_jobs"');
  });
});

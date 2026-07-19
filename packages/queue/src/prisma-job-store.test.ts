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

import { PrismaJobStore, type PrismaClientLike } from './prisma-job-store';

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
    store: new PrismaJobStore({ job } as unknown as PrismaClientLike),
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

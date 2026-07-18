import { INestApplication } from '@nestjs/common';
import { GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('forContext count and aggregate (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  it('scopes count to the caller ability and resists a nested OR that would inflate it', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));
    const asGuest = prisma.forContext(ctxFor('guest@example.com'));

    expect(await asRoy.post.count({})).toBe(3);
    expect(await asGuest.post.count({})).toBe(2);

    const inflated = await asGuest.post.count({
      where: { OR: [{ published: true }, { published: false }] },
    });
    expect(inflated).toBe(2);
  });

  it('sums only the rows the where clause admits within the caller ability', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));

    const all = (await asRoy.post.aggregate({ _sum: { viewCount: true } })) as {
      _sum: { viewCount: bigint | null };
    };
    expect(all._sum.viewCount).toBe(9007199254740993n + 5n + 100n);

    const published = (await asRoy.post.aggregate({
      where: { published: true },
      _sum: { viewCount: true },
    })) as { _sum: { viewCount: bigint | null } };
    expect(published._sum.viewCount).toBe(9007199254740993n + 100n);
  });

  it('rejects aggregating a field the caller can never read, naming the field', async () => {
    const asGuest = prisma.forContext(ctxFor('guest@example.com'));
    await expect(
      asGuest.user.aggregate({ _max: { phone: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(asGuest.user.aggregate({ _max: { phone: true } })).rejects.toThrow(/phone/);
  });

  it('rejects aggregating a field the caller can only read conditionally', async () => {
    const asAda = prisma.forContext(ctxFor('ada@example.com'));
    await expect(
      asAda.user.aggregate({ _max: { phone: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('allows aggregating always-readable fields and field-free row counts', async () => {
    const asAda = prisma.forContext(ctxFor('ada@example.com'));

    const byField = (await asAda.user.aggregate({ _count: { email: true } })) as {
      _count: { email: number };
    };
    expect(byField._count.email).toBeGreaterThan(0);

    const rows = (await asAda.user.aggregate({ _count: true })) as { _count: number };
    expect(rows._count).toBeGreaterThan(0);
  });

  it('exposes count, aggregate and groupBy on the context-bound surface', async () => {
    const scoped = prisma.forContext(ctxFor('roy@example.com'));
    expect(typeof scoped.post.count).toBe('function');
    expect(typeof scoped.post.aggregate).toBe('function');
    expect(typeof scoped.post.groupBy).toBe('function');
  });

  it('merges the caller constraint into a grouped query', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));
    const asGuest = prisma.forContext(ctxFor('guest@example.com'));

    const royGroups = (await asRoy.post.groupBy({
      by: ['published'],
      _count: { _all: true },
    })) as { published: boolean; _count: { _all: number } }[];
    const guestGroups = (await asGuest.post.groupBy({
      by: ['published'],
      _count: { _all: true },
    })) as { published: boolean; _count: { _all: number } }[];

    const total = (groups: { _count: { _all: number } }[]) =>
      groups.reduce((sum, group) => sum + group._count._all, 0);

    expect(total(royGroups)).toBe(3);
    expect(total(guestGroups)).toBe(2);
  });

  it('keeps one caller out of another caller grouped sums', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));
    const asGuest = prisma.forContext(ctxFor('guest@example.com'));

    const sumFor = async (client: typeof asRoy) => {
      const groups = (await client.post.groupBy({
        by: ['published'],
        _sum: { viewCount: true },
      })) as { _sum: { viewCount: bigint | null } }[];
      return groups.reduce(
        (sum, group) => sum + (group._sum.viewCount ?? 0n),
        0n,
      );
    };

    const royTotal = await sumFor(asRoy);
    const guestTotal = await sumFor(asGuest);

    expect(royTotal).toBe(9007199254740993n + 5n + 100n);
    expect(guestTotal).toBe(9007199254740993n + 100n);
    expect(royTotal - guestTotal).toBe(5n);
  });

  it('contributes zero from a row the caller cannot read to any group', async () => {
    const asGuest = prisma.forContext(ctxFor('guest@example.com'));

    const groups = (await asGuest.post.groupBy({
      by: ['published'],
      _sum: { viewCount: true },
      _count: { _all: true },
    })) as {
      published: boolean;
      _sum: { viewCount: bigint | null };
      _count: { _all: number };
    }[];

    const unpublished = groups.find((group) => group.published === false);
    expect(unpublished).toBeUndefined();
    expect(groups).toHaveLength(1);
    expect(groups[0]._sum.viewCount).toBe(9007199254740993n + 100n);
  });

  it('orders by an aggregate measure and takes the top group', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));

    const top = (await asRoy.post.groupBy({
      by: ['published'],
      _sum: { viewCount: true },
      orderBy: { _sum: { viewCount: 'desc' } },
      take: 1,
    })) as { published: boolean; _sum: { viewCount: bigint | null } }[];

    expect(top).toHaveLength(1);
    expect(top[0]._sum.viewCount).toBe(9007199254740993n + 100n);
  });

  it('returns null rather than zero for a measure over zero rows', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));

    const empty = (await asRoy.post.aggregate({
      where: { title: '__no_such_post__' },
      _max: { viewCount: true },
    })) as { _max: { viewCount: bigint | null } };

    expect(empty._max.viewCount).toBeNull();
  });

  it('groups inside a caller-scoped transaction', async () => {
    const asRoy = prisma.forContext(ctxFor('roy@example.com'));

    const groups = await asRoy.$transaction(async (tx) => {
      return (await tx.post.groupBy({
        by: ['published'],
        _count: { _all: true },
      })) as { _count: { _all: number } }[];
    });

    expect(groups.reduce((sum, group) => sum + group._count._all, 0)).toBe(3);
  });
});

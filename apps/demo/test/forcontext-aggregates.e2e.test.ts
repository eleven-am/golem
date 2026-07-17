import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { GolemValidationError } from '@eleven-am/golem';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('forContext count and aggregate (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
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

  it('exposes count and aggregate but not groupBy on the context-bound surface', async () => {
    const scoped = prisma.forContext(ctxFor('roy@example.com'));
    expect(typeof scoped.post.count).toBe('function');
    expect(typeof scoped.post.aggregate).toBe('function');
    // @ts-expect-error groupBy is deliberately absent from the context-bound surface
    scoped.post.groupBy;
  });
});

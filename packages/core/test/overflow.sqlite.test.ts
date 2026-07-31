import { context, engineFor, seed, seedMetrics } from './support/analytics';
import { SqliteHandle, openSqlite } from './support/sqlite';

describe('a sum the sqlite engine cannot hold', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seed(handle.prisma, () => '?');
    await seedMetrics(handle.prisma);
    for (let index = 0; index < 4; index += 1) {
      await handle.prisma.$executeRawUnsafe(
        `INSERT INTO "metrics" ("metric_id","label","owner_id","note","rank_value","score","hits","ratio","active","recorded_at")` +
          ` VALUES (?, 'bulk', 1, NULL, NULL, NULL, 9223372036854775807, 0, 1, '2024-01-01T00:00:00.000+00:00')`,
        1000 + index,
      );
    }
  });

  afterAll(async () => {
    await handle.close();
  });

  it('raises the engine error on the compiled path exactly as it raises it through Prisma', async () => {
    const engine = engineFor(handle.prisma as unknown as Record<string, any>, 'sqlite', {});
    const compiled = await engine
      .aggregate({ model: 'Metric', _sum: { hits: true }, context, compiled: true })
      .then(() => null, (error: Error) => error);
    const prisma = await engine
      .aggregate({ model: 'Metric', _sum: { hits: true }, context })
      .then(() => null, (error: Error) => error);

    expect(compiled).toBeInstanceOf(Error);
    expect(prisma).toBeInstanceOf(Error);
    expect((compiled as Error).constructor).toBe((prisma as Error).constructor);
    expect((compiled as { code?: unknown }).code).toBe((prisma as { code?: unknown }).code);
    expect((compiled as Error).message).toContain('integer overflow');
  });

  it('still answers the sums the engine can hold', async () => {
    const engine = engineFor(handle.prisma as unknown as Record<string, any>, 'sqlite', {});
    const compiled = (await engine.aggregate({
      model: 'Metric',
      _sum: { rank: true },
      _count: { _all: true },
      context,
      compiled: true,
    })) as Record<string, any>;
    expect(compiled._count._all).toBe(9);
    expect(compiled._sum.rank).toBe(6);
  });
});

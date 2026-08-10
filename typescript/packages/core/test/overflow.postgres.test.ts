import { context, engineFor, seed, seedMetrics } from './support/analytics';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  ensureDatabase,
  openPostgres,
} from './support/postgres';

jest.setTimeout(120000);

const OVERFLOW_DATABASE = 'golem_core_overflow';

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('a sum postgres widens past what a JavaScript number holds', () => {
  let handle: PostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) {
        return;
      }
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openPostgres(await ensureDatabase(url, OVERFLOW_DATABASE));
    await seed(handle.prisma, (position) => `$${position}`);
    await seedMetrics(handle.prisma);
    await handle.prisma.$executeRawUnsafe(
      `ALTER TABLE "metrics" ALTER COLUMN "rank_value" TYPE bigint`,
    );
    await handle.prisma.$executeRawUnsafe(
      `UPDATE "metrics" SET "rank_value" = 9007199254740993 WHERE "metric_id" = 1`,
    );
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it.skip(`skipped: ${POSTGRES_URL_HINT}`, () => undefined);
  } else {
    it('refuses the sum with the error Prisma raises, naming the same column and value', async () => {
      const engine = engineFor(handle.prisma as unknown as Record<string, any>, 'postgresql', {});
      const compiled = await engine
        .aggregate({ model: 'Metric', _sum: { rank: true }, context, compiled: true })
        .then(() => null, (error: Error) => error);
      const prisma = await engine
        .aggregate({ model: 'Metric', _sum: { rank: true }, context })
        .then(() => null, (error: Error) => error);

      expect(compiled).toBeInstanceOf(Error);
      expect(prisma).toBeInstanceOf(Error);
      expect((compiled as Error).constructor).toBe((prisma as Error).constructor);
      expect((compiled as { code?: unknown }).code).toBe('P2023');
      expect((prisma as { code?: unknown }).code).toBe('P2023');
      expect((compiled as Error).message).toBe(
        `Integer value in column '_sum$rank_value' is too large to represent as a JavaScript number` +
          ` without loss of precision, got: 9007199254740998. Consider using BigInt type.`,
      );
      expect((prisma as Error).message).toContain((compiled as Error).message);
    });

    it('answers the same sum as a BigInt, which loses nothing, on the compiled path', async () => {
      const engine = engineFor(handle.prisma as unknown as Record<string, any>, 'postgresql', {});
      const compiled = (await engine.aggregate({
        model: 'Metric',
        _sum: { hits: true },
        context,
        compiled: true,
      })) as Record<string, any>;
      expect(compiled._sum.hits).toBe(43n);
    });
  }
});

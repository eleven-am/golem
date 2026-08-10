import { seed, seedMetrics } from './support/analytics';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  openPostgres,
  type PostgresHandle,
} from './support/postgres';
import { relationAggregationSuite } from './support/relation-aggregation';

jest.setTimeout(120000);

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('relation-aware aggregation against live postgres', () => {
  let handle: PostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) return;
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openPostgres(
      await ensureDatabase(url, 'golem_core_relation_aggregation'),
    );
    await seed(handle.prisma, (position) => `$${position}`);
    await seedMetrics(handle.prisma);
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it('is explicitly optional when no PostgreSQL verification server is configured', () => {
      expect(POSTGRES_OPTIONAL).toBe(true);
    });
  } else {
    relationAggregationSuite(() => ({
      provider: 'postgresql',
      client: handle.prisma as unknown as Record<string, any>,
    }));
  }
});

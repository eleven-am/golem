import { seed, seedMetrics } from './support/analytics';
import { relationAggregationSuite } from './support/relation-aggregation';
import { openSqlite, type SqliteHandle } from './support/sqlite';

describe('relation-aware aggregation against live sqlite', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seed(handle.prisma, () => '?');
    await seedMetrics(handle.prisma);
  });

  afterAll(async () => {
    await handle.close();
  });

  relationAggregationSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
  }));
});

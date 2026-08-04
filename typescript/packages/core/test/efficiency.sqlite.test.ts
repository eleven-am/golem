import { efficiencySuite } from './support/efficiency';
import { efficiencyRunner, seedEfficiencyPlays } from './support/efficiency-core';
import { SqliteHandle, openSqlite } from './support/sqlite';

describe('a scoped efficiency metric against a live sqlite database', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seedEfficiencyPlays(handle.prisma, 'sqlite');
  });

  afterAll(async () => {
    await handle.close();
  });

  efficiencySuite(() => ({
    provider: 'sqlite',
    userId: (key: number) => key,
    run: efficiencyRunner(handle.prisma as unknown as Record<string, any>, 'sqlite'),
  }));
});

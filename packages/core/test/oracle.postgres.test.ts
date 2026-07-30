import { seed, seedMetrics } from './support/analytics';
import { oracleSuite } from './support/oracle';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  ensureDatabase,
  openPostgres,
} from './support/postgres';

const ORACLE_DATABASE = 'golem_core_oracle';

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('the compiled read path against a live postgres database', () => {
  let handle: PostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) {
        return;
      }
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openPostgres(await ensureDatabase(url, ORACLE_DATABASE));
    await seed(handle.prisma, (position) => `$${position}`);
    await seedMetrics(handle.prisma);
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it.skip(`skipped: ${POSTGRES_URL_HINT}`, () => undefined);
  } else {
    oracleSuite(() => ({
      provider: 'postgresql',
      client: handle.prisma as unknown as Record<string, any>,
    }));
  }
});

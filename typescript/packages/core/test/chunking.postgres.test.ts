import { POSTGRES_CEILING_OWNERS, chunkingSuite, seedChunking } from './support/chunking';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  ensureDatabase,
  openPostgres,
} from './support/postgres';

jest.setTimeout(600000);

const CHUNKING_DATABASE = 'golem_core_chunking';

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('a batched relation over more parents than one statement can bind, on postgres', () => {
  let handle: PostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) {
        return;
      }
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openPostgres(await ensureDatabase(url, CHUNKING_DATABASE));
    await seedChunking(handle.prisma as never, POSTGRES_CEILING_OWNERS);
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it.skip(`skipped: ${POSTGRES_URL_HINT}`, () => undefined);
  } else {
    chunkingSuite(() => ({
      provider: 'postgresql',
      client: handle.prisma as unknown as Record<string, any>,
      owners: POSTGRES_CEILING_OWNERS,
    }));
  }
});

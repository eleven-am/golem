import { DEFAULT_UPSERT_GUARD_STRIPES, prepareUpsertGuard } from '../src/upsert-guard';
import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from './prisma-postgres/generated/client';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  openPostgres,
} from './support/postgres';
import { describeWritePath } from './support/write-path';

jest.setTimeout(120000);

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('write path on PostgreSQL', () => {
  if (url === '') {
    it('requires the PostgreSQL verification server unless explicitly optional', () => {
      if (!POSTGRES_OPTIONAL) throw new Error(POSTGRES_URL_HINT);
    });
    return;
  }
  describeWritePath('postgresql', async () => {
    const databaseUrl = await ensureDatabase(url, 'golem_core_write_path');
    const opened = await openPostgres(databaseUrl);
    await prepareUpsertGuard(opened.prisma as unknown as Record<string, unknown>, 'postgresql', DEFAULT_UPSERT_GUARD_STRIPES);
    const concurrent = new PrismaClient({ adapter: new PrismaPg({ connectionString: databaseUrl }) });
    return {
      prisma: opened.prisma,
      concurrent,
      close: async () => {
        await concurrent.$disconnect();
        await opened.close();
      },
    };
  });
});

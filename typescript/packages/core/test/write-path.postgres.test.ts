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
  describeWritePath('postgresql', async () =>
    openPostgres(await ensureDatabase(url, 'golem_core_write_path')));
});

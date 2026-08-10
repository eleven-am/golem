import { efficiencySuite } from './support/efficiency';
import {
  EfficiencyPostgresHandle,
  efficiencyRunner,
  openEfficiencyPostgres,
} from './support/efficiency-core';
import { POSTGRES_OPTIONAL, POSTGRES_URL_ENV, POSTGRES_URL_HINT } from './support/postgres';

jest.setTimeout(120000);

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('a scoped efficiency metric against a live postgres database', () => {
  let handle: EfficiencyPostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) {
        return;
      }
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openEfficiencyPostgres(url);
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it.skip(`skipped: ${POSTGRES_URL_HINT}`, () => undefined);
  } else {
    efficiencySuite(() => ({
      provider: 'postgresql',
      userId: (key: number) => key,
      run: efficiencyRunner(handle.prisma as unknown as Record<string, any>, 'postgresql'),
    }));
  }
});

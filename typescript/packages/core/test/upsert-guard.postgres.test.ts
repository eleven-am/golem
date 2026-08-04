import { PrismaPg } from '@prisma/adapter-pg';
import { AuthorizationProvider } from '../src/authorization';
import { DatamodelModel } from '../src/datamodel';
import { GolemEngine } from '../src/operations';
import { field } from '../src/testing';
import { PrismaClient } from './prisma-postgres/generated/client';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  openPostgres,
} from './support/postgres';

jest.setTimeout(120000);

const url = process.env[POSTGRES_URL_ENV] ?? '';
const database = 'golem_core_upsert_guard';

const model: DatamodelModel = {
  name: 'UpsertTarget',
  fields: [
    field({ name: 'id', type: 'Int', isId: true, hasDefaultValue: true }),
    field({ name: 'key', type: 'String', isUnique: true }),
    field({ name: 'value', type: 'String' }),
  ],
};

const authorization: AuthorizationProvider = {
  authorize: async () => undefined,
  constrain: async () => ({}),
  check: async () => true,
  checkField: async () => true,
};

describe('serialized context-aware upsert against live PostgreSQL', () => {
  if (url === '' && POSTGRES_OPTIONAL) {
    it('is explicitly optional when no PostgreSQL verification server is configured', () => {
      expect(POSTGRES_OPTIONAL).toBe(true);
    });
    return;
  }

  if (url === '') {
    it('requires the PostgreSQL verification server', () => {
      throw new Error(POSTGRES_URL_HINT);
    });
    return;
  }

  let first: PrismaClient;
  let second: PrismaClient;

  beforeAll(async () => {
    const databaseUrl = await ensureDatabase(url, database);
    const initialized = await openPostgres(databaseUrl);
    first = initialized.prisma;
    second = new PrismaClient({ adapter: new PrismaPg({ connectionString: databaseUrl }) });
    await second.$connect();
  });

  afterAll(async () => {
    await second?.$disconnect();
    await first?.$disconnect();
  });

  it('serializes concurrent creators across independent clients and connections', async () => {
    const engines = [first, second].map((client) => new GolemEngine(
      client as unknown as Record<string, any>,
      [model],
      {
        authorization,
        checkReadFields: false,
        checkWriteResults: false,
        provider: 'postgresql',
      },
    ));
    const attempts = Array.from({ length: 24 }, (_, index) =>
      engines[index % engines.length].upsert({
        model: 'UpsertTarget',
        where: { key: 'shared' },
        create: { key: 'shared', value: `created-${index}` },
        update: { value: `updated-${index}` },
        select: { id: true, value: true },
        context: { request: {} },
      }),
    );

    const results = await Promise.all(attempts) as Array<{ id: number; value: string }>;
    expect(new Set(results.map(({ id }) => id)).size).toBe(1);
    await expect(first.upsertTarget.count({ where: { key: 'shared' } })).resolves.toBe(1);
    await expect(first.golemUpsertGuard.count()).resolves.toBeLessThanOrEqual(4096);
  });
});

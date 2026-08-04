import { PrismaPg } from '@prisma/adapter-pg';
import type { DatamodelDocument } from '../src/datamodel';
import { withBufferedEvents } from '../src/event-buffer';
import type { GolemEventBus, GolemEventPayload } from '../src/events';
import { GolemConflictError } from '../src/errors';
import { createEventPublisher } from '../src/publisher';
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
const datamodel: DatamodelDocument = {
  models: [{
    name: 'Secret',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'value', type: 'String' }),
    ],
  }],
  enums: [],
  provider: 'postgresql',
};

function latch(): { promise: Promise<void>; release(): void } {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

describe('batch-event concurrency against live PostgreSQL', () => {
  if (url === '') {
    it('requires the PostgreSQL verification server unless explicitly optional', () => {
      if (!POSTGRES_OPTIONAL) throw new Error(POSTGRES_URL_HINT);
    });
    return;
  }

  let first: PrismaClient;
  let second: PrismaClient;

  beforeAll(async () => {
    const databaseUrl = await ensureDatabase(url, 'golem_core_batch_events');
    const initialized = await openPostgres(databaseUrl);
    first = initialized.prisma;
    second = new PrismaClient({ adapter: new PrismaPg({ connectionString: databaseUrl }) });
    await second.$connect();
  });

  afterAll(async () => {
    await second?.$disconnect();
    await first?.$disconnect();
  });

  it('rolls back with a stable conflict and emits nothing when a selected row is concurrently deleted', async () => {
    await first.secret.create({ data: { id: 1, value: 'selected' } });
    const selected = latch();
    const concurrentlyDeleted = latch();
    const published: GolemEventPayload[] = [];
    const bus: GolemEventBus = {
      publish: async (_topic, event) => { published.push(event); },
      publishMany: async (_topic, events) => { published.push(...events); },
      iterate: (async function* () {})() as never,
    };
    const publisher = createEventPublisher({
      datamodel,
      eventBus: bus,
      models: new Set(['Secret']),
    });

    const deleting = withBufferedEvents(() =>
      first.$transaction((tx) => publisher({
        model: 'Secret',
        operation: 'deleteMany',
        args: { where: { id: 1 } },
        query: async () => { throw new Error('the native batch query escaped interception'); },
        batch: {
          suppressed: false,
          run: (work) => work({
            findMany: async (args) => {
              const rows = await tx.secret.findMany(args as Parameters<typeof tx.secret.findMany>[0]);
              selected.release();
              await concurrentlyDeleted.promise;
              return rows;
            },
            updateManyAndReturn: (args) =>
              tx.secret.updateManyAndReturn(
                args as Parameters<typeof tx.secret.updateManyAndReturn>[0],
              ),
            deleteMany: (args) =>
              tx.secret.deleteMany(args as Parameters<typeof tx.secret.deleteMany>[0]),
          }),
        },
      })),
    );
    const outcome = deleting.then(
      () => undefined,
      (error: unknown) => error,
    );

    await selected.promise;
    await second.secret.delete({ where: { id: 1 } });
    concurrentlyDeleted.release();

    expect(await outcome).toBeInstanceOf(GolemConflictError);
    expect(published).toEqual([]);
    await expect(first.secret.count()).resolves.toBe(0);
  });
});

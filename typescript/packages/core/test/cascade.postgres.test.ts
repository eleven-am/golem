import { PrismaPg } from '@prisma/adapter-pg';
import type { DatamodelDocument } from '../src/datamodel';
import { withBufferedEvents } from '../src/event-buffer';
import type { GolemEventBus, GolemEventPayload } from '../src/events';
import { createEventPublisher, GolemBatchDelegate, GolemBatchRuntime } from '../src/publisher';
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
  provider: 'postgresql',
  enums: [],
  models: [
    {
      name: 'Thread',
      dbName: 'threads',
      fields: [
        field({ name: 'id', type: 'Int', isId: true }),
        field({ name: 'title', type: 'String' }),
        field({ name: 'replies', type: 'Reply', kind: 'object', isList: true, relationName: 'ReplyToThread' }),
        field({ name: 'watches', type: 'Watch', kind: 'object', isList: true, relationName: 'ThreadToWatch' }),
      ],
    },
    {
      name: 'Reply',
      dbName: 'replies',
      fields: [
        field({ name: 'id', type: 'Int', isId: true }),
        field({ name: 'threadId', type: 'Int', dbName: 'thread_id' }),
        field({ name: 'body', type: 'String' }),
        field({ name: 'amount', type: 'Decimal', isRequired: false }),
        field({ name: 'postedAt', type: 'DateTime', dbName: 'posted_at' }),
        field({
          name: 'thread', type: 'Thread', kind: 'object', relationName: 'ReplyToThread',
          relationFromFields: ['threadId'], relationToFields: ['id'], relationOnDelete: 'Cascade',
        }),
      ],
    },
    {
      name: 'Watch',
      dbName: 'watches',
      fields: [
        field({ name: 'id', type: 'Int', isId: true }),
        field({ name: 'threadId', type: 'Int', isRequired: false, dbName: 'thread_id' }),
        field({
          name: 'thread', type: 'Thread', kind: 'object', isRequired: false, relationName: 'ThreadToWatch',
          relationFromFields: ['threadId'], relationToFields: ['id'], relationOnDelete: 'SetNull',
        }),
      ],
    },
  ],
};

function latch(): { promise: Promise<void>; release(): void } {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

function runtimeOver(
  tx: Record<string, any>,
  model: string,
  afterStatement: (sql: string) => Promise<void> = async () => undefined,
): GolemBatchRuntime {
  const delegate = (name: string) => tx[name.charAt(0).toLowerCase() + name.slice(1)] as GolemBatchDelegate;
  return {
    suppressed: false,
    run: (work) => work(delegate(model), {
      scope: tx,
      delegate,
      queryRaw: async (sql, ...values) => {
        const rows = await tx.$queryRawUnsafe(sql, ...values);
        await afterStatement(sql);
        return rows;
      },
    }),
  };
}

describe('cascaded delete events against live PostgreSQL', () => {
  if (url === '') {
    it('requires the PostgreSQL verification server unless explicitly optional', () => {
      if (!POSTGRES_OPTIONAL) throw new Error(POSTGRES_URL_HINT);
    });
    return;
  }

  let first: PrismaClient;
  let second: PrismaClient;
  let published: GolemEventPayload[];
  let publisher: ReturnType<typeof createEventPublisher>;

  beforeAll(async () => {
    const databaseUrl = await ensureDatabase(url, 'golem_core_cascade_events');
    first = (await openPostgres(databaseUrl)).prisma;
    second = new PrismaClient({ adapter: new PrismaPg({ connectionString: databaseUrl }) });
    await second.$connect();
  });

  afterAll(async () => {
    await second?.$disconnect();
    await first?.$disconnect();
  });

  beforeEach(async () => {
    await first.reply.deleteMany();
    await first.watch.deleteMany();
    await first.thread.deleteMany();
    await first.thread.create({ data: { id: 1, title: 'parent' } });
    await first.reply.createMany({ data: [
      { id: 10, threadId: 1, body: 'a', amount: '1.50', postedAt: new Date('2026-01-01T00:00:00.000Z') },
      { id: 11, threadId: 1, body: 'b', amount: null, postedAt: new Date('2026-01-02T00:00:00.000Z') },
    ] });
    await first.watch.create({ data: { id: 20, threadId: 1 } });
    published = [];
    const bus: GolemEventBus = {
      publish: async (_topic, event) => { published.push(event); },
      publishMany: async (_topic, events) => { published.push(...events); },
      iterate: (async function* () {})() as never,
    };
    publisher = createEventPublisher({
      datamodel,
      eventBus: bus,
      models: new Set(['Thread', 'Reply', 'Watch']),
    });
  });

  it('reads the cascaded rows exactly as Prisma reads them and publishes after commit', async () => {
    const expectedReplies = await first.reply.findMany({ orderBy: { id: 'asc' } });

    await withBufferedEvents(() =>
      first.$transaction((tx) => publisher({
        model: 'Thread',
        operation: 'delete',
        args: { where: { id: 1 } },
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(tx, 'Thread'),
      })),
    );

    expect(published).toEqual([
      { type: 'DELETED', model: 'Thread', id: 1, entity: { id: 1, title: 'parent' } },
      { type: 'DELETED', model: 'Reply', id: 10, entity: expectedReplies[0] },
      { type: 'DELETED', model: 'Reply', id: 11, entity: expectedReplies[1] },
      { type: 'UPDATED', model: 'Watch', id: 20 },
    ]);
    await expect(first.reply.count()).resolves.toBe(0);
    await expect(first.watch.findUnique({ where: { id: 20 } })).resolves.toEqual({ id: 20, threadId: null });
  });

  it('holds a new dependent back once the delete holds its parent, so none is cascaded silently', async () => {
    const enumerated = latch();
    const proceed = latch();
    const deleting = withBufferedEvents(() =>
      first.$transaction((tx) => publisher({
        model: 'Thread',
        operation: 'delete',
        args: { where: { id: 1 } },
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(tx, 'Thread', async (sql) => {
          if (sql.includes('FROM "threads"')) {
            enumerated.release();
            await proceed.promise;
          }
        }),
      })),
    );

    await enumerated.promise;
    let inserted = false;
    const inserting = second.reply
      .create({ data: { id: 12, threadId: 1, body: 'late', postedAt: new Date() } })
      .then(() => { inserted = true; }, (error: unknown) => error);
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(inserted).toBe(false);

    proceed.release();
    await deleting;
    const outcome = await inserting;

    expect(inserted).toBe(false);
    expect(outcome).toBeInstanceOf(Error);
    expect(published.filter((event) => event.model === 'Reply').map((event) => event.id)).toEqual([10, 11]);
    await expect(first.reply.count()).resolves.toBe(0);
  });

  it('locks dependents in the schema their model declares, not a same-named table on the search path', async () => {
    await first.$executeRawUnsafe('DROP SCHEMA IF EXISTS "golem""other" CASCADE');
    await first.$executeRawUnsafe('CREATE SCHEMA "golem""other"');
    await first.$executeRawUnsafe(`CREATE TABLE "golem""other"."replies" ("id" INTEGER PRIMARY KEY)`);
    await first.$executeRawUnsafe(`INSERT INTO "golem""other"."replies" ("id") VALUES (10)`);
    const qualified: DatamodelDocument = {
      ...datamodel,
      models: datamodel.models.map((model) => (model.name === 'Reply' ? { ...model, schema: 'golem"other' } : model)),
    };
    const qualifiedPublisher = createEventPublisher({
      datamodel: qualified,
      eventBus: { publish: async () => undefined, iterate: (async function* () {})() as never },
      models: new Set(),
    });
    const holding = latch();
    const released = latch();
    const holder = second.$transaction(async (tx) => {
      await tx.$queryRawUnsafe('SELECT 1 FROM "golem""other"."replies" WHERE "id" = 10 FOR UPDATE');
      holding.release();
      await released.promise;
    });

    try {
      await holding.promise;
      let deleted = false;
      const deleting = withBufferedEvents(() =>
        first.$transaction((tx) => qualifiedPublisher({
          model: 'Thread',
          operation: 'delete',
          args: { where: { id: 1 } },
          query: async () => { throw new Error('the native delete escaped interception'); },
          batch: runtimeOver(tx, 'Thread'),
        })),
      ).then(() => { deleted = true; });
      await new Promise((resolve) => setTimeout(resolve, 300));
      expect(deleted).toBe(false);
      released.release();
      await holder;
      await deleting;
      expect(deleted).toBe(true);
    } finally {
      released.release();
      await first.$executeRawUnsafe('DROP SCHEMA IF EXISTS "golem""other" CASCADE');
    }
  });

});

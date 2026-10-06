import { PrismaPg } from '@prisma/adapter-pg';
import { AuthorizationProvider } from '../src/authorization';
import { DatamodelModel } from '../src/datamodel';
import { GolemEngine } from '../src/operations';
import { field } from '../src/testing';
import { DEFAULT_UPSERT_GUARD_STRIPES, prepareUpsertGuard } from '../src/upsert-guard';
import { PrismaClient } from './prisma-postgres/generated/client';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  openPostgres,
} from './support/postgres';
import { upsertGuardModel } from './support/upsert-guard-model';

jest.setTimeout(120000);

const url = process.env[POSTGRES_URL_ENV] ?? '';

const models: DatamodelModel[] = [
  {
    name: 'Thread',
    dbName: 'threads',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'replies', type: 'Reply', kind: 'object', isList: true, relationName: 'ReplyToThread' }),
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
    name: 'Person',
    dbName: 'people',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'buddyId', type: 'Int', isRequired: false, dbName: 'buddy_id' }),
      field({
        name: 'buddy', type: 'Person', kind: 'object', isRequired: false, relationName: 'Buddy',
        relationFromFields: ['buddyId'], relationToFields: ['id'], relationOnDelete: 'SetNull',
      }),
      field({ name: 'buddiedBy', type: 'Person', kind: 'object', isList: true, relationName: 'Buddy' }),
    ],
  },
];

const authorization: AuthorizationProvider = {
  authorize: async () => undefined,
  constrain: async () => ({}),
  check: async () => true,
  checkField: async () => true,
};

describe('the statements an uncontended write issues on PostgreSQL', () => {
  if (url === '') {
    it('requires the PostgreSQL verification server unless explicitly optional', () => {
      if (!POSTGRES_OPTIONAL) throw new Error(POSTGRES_URL_HINT);
    });
    return;
  }

  let prisma: PrismaClient;
  let statements: string[] = [];
  let close: () => Promise<void>;

  beforeAll(async () => {
    const databaseUrl = await ensureDatabase(url, 'golem_core_write_statements');
    const opened = await openPostgres(databaseUrl);
    await prepareUpsertGuard(opened.prisma as unknown as Record<string, unknown>, 'postgresql', DEFAULT_UPSERT_GUARD_STRIPES);
    await opened.close();
    prisma = new PrismaClient({
      adapter: new PrismaPg({ connectionString: databaseUrl }),
      log: [{ emit: 'event', level: 'query' }],
    });
    (prisma as unknown as { $on(event: 'query', listener: (event: { query: string }) => void): void })
      .$on('query', (event) => statements.push(event.query));
    close = () => prisma.$disconnect();
  });

  afterAll(async () => {
    await close?.();
  });

  beforeEach(async () => {
    await prisma.person.deleteMany();
    await prisma.reply.deleteMany();
    await prisma.thread.deleteMany();
    await prisma.thread.createMany({ data: [{ id: 1, title: 'one' }, { id: 2, title: 'two' }] });
    await prisma.person.createMany({ data: [{ id: 1 }, { id: 2 }] });
    statements = [];
  });

  const engine = (checkWriteResults: boolean) => new GolemEngine(prisma as unknown as Record<string, any>, models, {
    authorization,
    checkWriteResults,
    checkReadFields: false,
    provider: 'postgresql',
    upsertGuard: upsertGuardModel,
  });
  const context = () => ({ req: {} });
  const issued = async (write: () => Promise<unknown>): Promise<string[]> => {
    statements = [];
    await write();
    return statements.splice(0);
  };
  const locks = (sql: readonly string[]) => sql.filter((statement) => / FOR (SHARE|UPDATE)( NOWAIT)?$/.test(statement));

  describe.each([[true, 6, 9], [false, 5, 7]])('with checkWriteResults %s', (checkWriteResults, createCount, twiceCount) => {
    it(`creates a row linked by a foreign key in ${createCount} statements, locking the parent once`, async () => {
      const sql = await issued(() => engine(checkWriteResults).create({
        model: 'Reply',
        data: { id: 10, threadId: 1, body: 'a', postedAt: new Date('2026-01-01T00:00:00.000Z') },
        select: { id: true },
        context: context(),
      }));

      expect(locks(sql)).toEqual(['SELECT 1 FROM "threads" WHERE ("id") = ($1) FOR SHARE']);
      expect(sql).toHaveLength(createCount);
    });

    it(`reads, locks and rechecks a row the write links twice only once, in ${twiceCount} statements`, async () => {
      const sql = await issued(() => engine(checkWriteResults).create({
        model: 'Person',
        data: { id: 3, buddyId: 1, buddiedBy: { connect: [{ id: 1 }] } },
        select: { id: true },
        context: context(),
      }));

      expect(locks(sql)).toEqual(['SELECT 1 FROM "people" WHERE ("id") = ($1) FOR UPDATE']);
      expect(sql).toHaveLength(twiceCount);
    });
  });

  it.each([
    ['with no caller', undefined, true],
    ['with a caller and checkWriteResults false', context(), false],
  ])('creates a row that links nothing in one statement, %s', async (_label, caller, checkWriteResults) => {
    const sql = await issued(() => engine(checkWriteResults).create({
      model: 'Thread', data: { id: 3, title: 'three' }, select: { id: true }, context: caller,
    }));

    expect(sql).toEqual(['INSERT INTO "public"."threads" ("id","title") VALUES ($1,$2) RETURNING "public"."threads"."id"']);
  });

  it('locks the parent a create links by a foreign key even with no caller to check', async () => {
    const sql = await issued(() => engine(true).create({
      model: 'Reply',
      data: { id: 11, threadId: 1, body: 'a', postedAt: new Date('2026-01-01T00:00:00.000Z') },
      select: { id: true },
    }));

    expect(locks(sql)).toEqual(['SELECT 1 FROM "threads" WHERE ("id") = ($1) FOR SHARE']);
  });

  it('updates one row in 5 statements, locking it once', async () => {
    const sql = await issued(() => engine(true).update({
      model: 'Thread', where: { id: 1 }, data: { title: 'edited' }, select: { id: true }, context: context(),
    }));

    expect(locks(sql)).toEqual(['SELECT 1 FROM "threads" WHERE ("id") = ($1) FOR UPDATE']);
    expect(sql).toHaveLength(5);
  });

  it('locks the rows an updateMany touches in one statement per table, in the order every writer uses', async () => {
    await prisma.thread.createMany({ data: [{ id: 9, title: 'nine' }, { id: 10, title: 'ten' }] });

    const sql = await issued(() => engine(true).updateMany({
      model: 'Thread', where: { id: { in: [1, 2, 9, 10] } }, data: { title: 'edited' }, context: context(),
    }));

    expect(locks(sql)).toHaveLength(1);
  });
});

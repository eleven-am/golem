import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { AbilityBuilder } from '@casl/ability';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import {
  Authorizer,
  AuthorizationModule,
  ResolvedAbility,
  ResolvedUser,
  WillAuthorize,
} from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GolemModule } from '@eleven-am/golem';
import { CompiledReadEvent, GolemEngine } from '@eleven-am/golem-core';
import { DemoAuthenticator } from '../src/auth';
import { getDatamodel } from '../src/generated/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import {
  bootDemoApp,
  databaseFileFor,
  provisionDatabase,
  removeDatabaseFiles,
  shutdownDemoApp,
} from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

type Disclosure = { disclosed: true; rows: unknown } | { disclosed: false };

describe('filtering by an unreadable field through a nested clause or a batch write (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    engine = context.moduleRef.get<GolemEngine>('GOLEM_ENGINE');
    const royPost = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    await prisma.readingSession.createMany({
      data: [
        { postId: royPost.id, progress: 10, note: 'roy private note' },
        { postId: adaPost.id, progress: 30, note: 'ada private note' },
      ],
    });
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, email: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', `token-${email}`)
      .send({ query })
      .expect(200);
  }

  it('refuses a nested where on a field the caller reads only on their own rows', async () => {
    await expect(
      prisma.forContext(ctxFor('mod@example.com')).post.findMany({
        select: {
          title: true,
          readingSessions: {
            where: { note: { startsWith: 'ada' } },
            select: { progress: true },
          },
        },
      }),
    ).rejects.toThrow('Cannot filter or order by field "note" on ReadingSession');
  });

  it('refuses a nested orderBy on that field', async () => {
    await expect(
      prisma.forContext(ctxFor('mod@example.com')).post.findMany({
        select: {
          title: true,
          readingSessions: {
            orderBy: [{ note: 'asc' }],
            select: { progress: true },
          },
        },
      }),
    ).rejects.toThrow('Cannot filter or order by field "note" on ReadingSession');
  });

  it('refuses the same filter two relations deep', async () => {
    await expect(
      prisma.forContext(ctxFor('mod@example.com')).user.findMany({
        select: {
          email: true,
          posts: {
            select: {
              title: true,
              readingSessions: {
                where: { note: { startsWith: 'ada' } },
                select: { progress: true },
              },
            },
          },
        },
      }),
    ).rejects.toThrow('Cannot filter or order by field "note" on ReadingSession');
  });

  it('still answers a nested filter over a field the caller may read', async () => {
    const posts = await prisma.forContext(ctxFor('mod@example.com')).post.findMany({
      orderBy: [{ title: 'asc' }],
      select: {
        title: true,
        readingSessions: {
          where: { progress: { gte: 20 } },
          select: { progress: true },
        },
      },
    });

    expect(posts).toEqual([
      { title: 'Draft post', readingSessions: [] },
      { title: 'First post', readingSessions: [] },
      { title: 'Memory systems', readingSessions: [{ progress: 30 }] },
    ]);
  });

  it('leaves a caller whose rules carry no field lists able to filter the nested relation', async () => {
    const posts = await prisma.forContext(ctxFor('ada@example.com')).post.findMany({
      where: { title: 'Memory systems' },
      select: {
        title: true,
        readingSessions: {
          where: { note: { startsWith: 'ada' } },
          select: { note: true },
        },
      },
    });

    expect(posts).toEqual([
      { title: 'Memory systems', readingSessions: [{ note: 'ada private note' }] },
    ]);
  });

  it('refuses an updateMany filtered by a field the caller may not read everywhere', async () => {
    const response = await gql(
      'mutation { updateManyUsers(where: { phone: { startsWith: "+44" } }, data: { name: "taken" }) { count } }',
      'ada@example.com',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toContain(
      'Cannot filter or order by field "phone" on User',
    );
    const untouched = await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } });
    expect(untouched.name).toBe('Ada');
  });

  it('refuses a deleteMany that reaches the field through a relation', async () => {
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).post.deleteMany({
        where: { author: { is: { phone: { startsWith: '+44' } } } },
      }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
    expect(await prisma.post.count()).toBe(3);
  });

  it('still runs a batch write filtered by fields the caller may read', async () => {
    const updated = await gql(
      'mutation { updateManyUsers(where: { email: { equals: "ada@example.com" } }, data: { name: "Ada" }) { count } }',
      'ada@example.com',
    );
    expect(updated.body.errors).toBeUndefined();
    expect(updated.body.data.updateManyUsers.count).toBe(1);

    const deleted = await prisma.forContext(ctxFor('ada@example.com')).post.deleteMany({
      where: { title: 'no such post' },
    });
    expect(deleted).toEqual({ count: 0 });
  });

  describe('the compiled statement that renders a nested filter', () => {
    function compiledRead(where: Record<string, unknown>) {
      return engine.findMany({
        model: 'Post',
        compiled: true,
        orderBy: [{ title: 'asc' }],
        select: {
          title: true,
          readingSessions: { where, select: { progress: true } },
        },
        context: ctxFor('mod@example.com'),
      });
    }

    it('refuses the nested filter before anything is compiled', async () => {
      const events: CompiledReadEvent[] = [];
      const release = engine.observeCompiledRead((event) => events.push(event));

      await expect(compiledRead({ note: { startsWith: 'ada' } })).rejects.toThrow(
        'Cannot filter or order by field "note" on ReadingSession',
      );
      release();

      expect(events).toEqual([]);
    });

    it('compiles a nested filter over a field the caller may read', async () => {
      const events: CompiledReadEvent[] = [];
      const release = engine.observeCompiledRead((event) => events.push(event));

      const posts = await compiledRead({ progress: { gte: 20 } });
      release();

      expect(posts).toEqual([
        { title: 'Draft post', readingSessions: [] },
        { title: 'First post', readingSessions: [] },
        { title: 'Memory systems', readingSessions: [{ progress: 30 }] },
      ]);
      const rendered = events.flatMap((event) => [event.sql ?? '', ...(event.statements ?? [])]);
      expect(events.map((event) => event.outcome)).toContain('compiled');
      expect(rendered.some((statement) => statement.includes('progress'))).toBe(true);
    });
  });

  describe('a filter that hops a relation to reach a field the caller may not read', () => {
    async function probe(run: () => Promise<unknown>): Promise<Disclosure> {
      try {
        return { disclosed: true, rows: await run() };
      } catch {
        return { disclosed: false };
      }
    }

    function asMod() {
      return prisma.forContext(ctxFor('mod@example.com'));
    }

    it.each([
      ['some', (note: string) => ({ readingSessions: { some: { note: { startsWith: note } } } })],
      ['every', (note: string) => ({ readingSessions: { every: { note: { startsWith: note } } } })],
      ['none', (note: string) => ({ readingSessions: { none: { note: { startsWith: note } } } })],
    ] as const)('tells the caller nothing about a note through %s', async (_shape, where) => {
      const hit = await probe(() =>
        asMod().post.findMany({ where: where('ada'), select: { title: true } }),
      );
      const miss = await probe(() =>
        asMod().post.findMany({ where: where('zzz'), select: { title: true } }),
      );

      expect(hit).toEqual({ disclosed: false });
      expect(hit).toEqual(miss);
    });

    it.each([
      ['the bare to-one shorthand', (prefix: string) => ({ author: { phone: { startsWith: prefix } } })],
      ['is', (prefix: string) => ({ author: { is: { phone: { startsWith: prefix } } } })],
      ['isNot', (prefix: string) => ({ author: { isNot: { phone: { startsWith: prefix } } } })],
    ] as const)('tells the caller nothing about a phone through %s', async (_shape, where) => {
      const hit = await probe(() =>
        asMod().post.findMany({ where: where('+44'), select: { title: true } }),
      );
      const miss = await probe(() =>
        asMod().post.findMany({ where: where('+99'), select: { title: true } }),
      );

      expect(hit).toEqual({ disclosed: false });
      expect(hit).toEqual(miss);
    });

    it('tells the caller nothing about a note two relations away', async () => {
      const query = (note: string) =>
        asMod().user.findMany({
          where: { posts: { some: { readingSessions: { some: { note: { startsWith: note } } } } } },
          select: { email: true },
        });

      const hit = await probe(() => query('ada'));
      const miss = await probe(() => query('zzz'));

      expect(hit).toEqual({ disclosed: false });
      expect(hit).toEqual(miss);
    });

    it('tells the caller nothing about a note through the count a batch write reports', async () => {
      const update = (note: string) =>
        asMod().post.updateMany({
          where: { readingSessions: { some: { note: { startsWith: note } } } },
          data: { published: false },
        });

      const hit = await probe(() => update('ada'));
      const miss = await probe(() => update('zzz'));

      expect(hit).toEqual({ disclosed: false });
      expect(hit).toEqual(miss);
      const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
      expect(adaPost.published).toBe(true);
    });

    it('still answers a relation filter over a field the caller may read', async () => {
      const answered = await asMod().post.findMany({
        where: { readingSessions: { some: { progress: { gte: 20 } } } },
        select: { title: true },
      });

      expect(answered).toEqual([{ title: 'Memory systems' }]);
    });
  });

  describe('a distinct read counting rows a field the caller may not read partitions', () => {
    type Counted = { disclosed: true; count: number } | { disclosed: false };

    async function probe(run: () => Promise<{ length: number }>): Promise<Counted> {
      try {
        return { disclosed: true, count: (await run()).length };
      } catch {
        return { disclosed: false };
      }
    }

    function asMod() {
      return prisma.forContext(ctxFor('mod@example.com'));
    }

    beforeAll(async () => {
      const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
      await prisma.readingSession.createMany({
        data: [
          { postId: adaPost.id, progress: 60, note: 'shared note' },
          { postId: adaPost.id, progress: 70, note: 'shared note' },
          { postId: adaPost.id, progress: 80, note: 'other note' },
        ],
      });
    });

    it('reports the same row count either way for a root distinct', async () => {
      const query = (progress: number[]) =>
        asMod().readingSession.findMany({
          where: { progress: { in: progress } },
          distinct: ['note'],
          orderBy: [{ progress: 'asc' }],
          select: { progress: true, note: true },
        });

      const equal = await probe(() => query([60, 70]));
      const different = await probe(() => query([60, 80]));

      expect(equal).toEqual({ disclosed: false });
      expect(equal).toEqual(different);
    });

    it('reports the same row count either way for a distinct inside a relation entry', async () => {
      const query = async (progress: number[]) => {
        const posts = await asMod().post.findMany({
          where: { title: 'Memory systems' },
          select: {
            title: true,
            readingSessions: {
              where: { progress: { in: progress } },
              distinct: ['note'],
              orderBy: [{ progress: 'asc' }],
              select: { progress: true, note: true },
            },
          },
        });
        return posts[0]?.readingSessions ?? [];
      };

      const equal = await probe(() => query([60, 70]));
      const different = await probe(() => query([60, 80]));

      expect(equal).toEqual({ disclosed: false });
      expect(equal).toEqual(different);
    });

    it('reports the same row count either way through findFirst', async () => {
      const query = async (progress: number[]) => {
        const session = await asMod().readingSession.findFirst({
          where: { progress: { in: progress } },
          distinct: ['note'],
          orderBy: [{ progress: 'asc' }],
          skip: 1,
          select: { progress: true, note: true },
        });
        return session === null ? [] : [session];
      };

      const equal = await probe(() => query([60, 70]));
      const different = await probe(() => query([60, 80]));

      expect(equal).toEqual({ disclosed: false });
      expect(equal).toEqual(different);
    });

    it('still reads distinct for a caller whose row constraint discharges the field', async () => {
      const sessions = await prisma.forContext(ctxFor('ada@example.com')).readingSession.findMany({
        distinct: ['note'],
        orderBy: [{ progress: 'asc' }],
        select: { progress: true, note: true },
      });

      expect(sessions).toEqual([
        { progress: 30, note: 'ada private note' },
        { progress: 60, note: 'shared note' },
        { progress: 80, note: 'other note' },
      ]);
    });

    it('still reads distinct over a field the caller may always read', async () => {
      const sessions = await asMod().readingSession.findMany({
        where: { progress: { in: [60, 70, 80] } },
        distinct: ['progress'],
        orderBy: [{ progress: 'asc' }],
        select: { progress: true },
      });

      expect(sessions).toEqual([{ progress: 60 }, { progress: 70 }, { progress: 80 }]);
    });
  });
});

@Authorizer()
class NestedWriteProbeRules implements WillAuthorize {
  forUser(_user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can, cannot } = builder;
    can('read', 'User');
    can('update', 'User');
    can('read', 'Post');
    can('update', 'Post');
    can('read', 'ReadingSession');
    cannot('read', 'ReadingSession', ['note']);
    can('read', 'ReadingSession', ['note'], { postId: 'no-such-post' });
    can(['update', 'delete'], 'ReadingSession', { postId: 'no-such-post' });
  }
}

describe('a nested write filtered by a field the caller may not read (e2e)', () => {
  const suite = `${__filename}.nested-write`;
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let postId: string;
  let authorId: string;

  async function probe(run: () => Promise<unknown>): Promise<Disclosure> {
    try {
      return { disclosed: true, rows: await run() };
    } catch {
      return { disclosed: false };
    }
  }

  function asProbe() {
    return prisma.forContext(ctxFor('mod@example.com'));
  }

  beforeAll(async () => {
    const databaseFile = provisionDatabase(suite);
    const moduleRef = await Test.createTestingModule({
      providers: [NestedWriteProbeRules],
      imports: [
        AuthorizationModule.forRootAsync({
          inject: [GolemPrismaService],
          useFactory: (client: GolemPrismaService) => new DemoAuthenticator(client),
        }),
        GolemModule.forRoot({
          client: GolemPrismaService,
          prismaOptions: { adapter: new PrismaBetterSqlite3({ url: `file:${databaseFile}` }) },
          datamodel: getDatamodel(),
          models: { PostTag: false, Play: false },
          authorization: GolemAuthorizationAdapter,
        }),
      ],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    postId = post.id;
    authorId = post.authorId;
    await prisma.readingSession.createMany({
      data: [
        { postId, progress: 30, note: 'secret-bravo' },
        { postId, progress: 40, note: 'other note' },
      ],
    });
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  it('tells the caller nothing through a nested deleteMany', async () => {
    const before = await prisma.readingSession.count();
    const attack = (prefix: string) =>
      asProbe().post.update({
        where: { id: postId },
        data: { readingSessions: { deleteMany: { note: { startsWith: prefix } } } },
        select: { title: true },
      });

    const hit = await probe(() => attack('secret-b'));
    const miss = await probe(() => attack('zzz'));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
    expect(await prisma.readingSession.count()).toBe(before);
  });

  it('tells the caller nothing through a nested updateMany', async () => {
    const attack = (prefix: string) =>
      asProbe().post.update({
        where: { id: postId },
        data: {
          readingSessions: {
            updateMany: { where: { note: { startsWith: prefix } }, data: { progress: 99 } },
          },
        },
        select: { title: true },
      });

    const hit = await probe(() => attack('secret-b'));
    const miss = await probe(() => attack('zzz'));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
    const sessions = await prisma.readingSession.findMany({ select: { progress: true } });
    expect(sessions.map((session) => session.progress).sort()).toEqual([30, 40]);
  });

  it('tells the caller nothing through a nested write two relations deep', async () => {
    const before = await prisma.readingSession.count();
    const attack = (prefix: string) =>
      asProbe().user.update({
        where: { id: authorId },
        data: {
          posts: {
            update: {
              where: { id: postId },
              data: { readingSessions: { deleteMany: { note: { startsWith: prefix } } } },
            },
          },
        },
        select: { email: true },
      });

    const hit = await probe(() => attack('secret-b'));
    const miss = await probe(() => attack('zzz'));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
    expect(await prisma.readingSession.count()).toBe(before);
  });

  it('lets a nested filter over a readable field reach the row checks', async () => {
    const before = await prisma.readingSession.count();

    await expect(
      asProbe().post.update({
        where: { id: postId },
        data: { readingSessions: { deleteMany: { progress: { gte: 0 } } } },
        select: { title: true },
      }),
    ).rejects.toThrow('Cannot delete ReadingSession with the provided values');
    expect(await prisma.readingSession.count()).toBe(before);
  });
});

import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
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
import { DemoAuthenticator } from '../src/auth';
import { getDatamodel } from '../src/generated/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { databaseFileFor, provisionDatabase, removeDatabaseFiles } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

type Counted = { disclosed: true; count: number } | { disclosed: false };

async function probe(run: () => Promise<{ count: number }>): Promise<Counted> {
  try {
    return { disclosed: true, count: (await run()).count };
  } catch {
    return { disclosed: false };
  }
}

async function bootRules(suite: string, rules: unknown): Promise<{
  app: INestApplication;
  prisma: GolemPrismaService;
}> {
  const databaseFile = provisionDatabase(suite);
  const moduleRef = await Test.createTestingModule({
    providers: [rules as never],
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
  const app = moduleRef.createNestApplication();
  await app.init();
  const prisma = app.get(GolemPrismaService);
  await seed(prisma);
  return { app, prisma };
}

@Authorizer()
class BatchReachRules implements WillAuthorize {
  forUser(user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder;
    can('read', 'Post', { published: true });
    if ((user as { email: string }).email === 'mod@example.com') {
      can(['update', 'delete'], 'Post');
      return;
    }
    can(['update', 'delete'], 'Post', { published: true });
  }
}

@Authorizer()
class WriteOnlyRules implements WillAuthorize {
  forUser(_user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder;
    can(['update', 'delete'], 'Post');
  }
}

describe('a batch write whose write reach is wider than its read reach (e2e)', () => {
  const suite = `${__filename}.wider-write`;
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const booted = await bootRules(suite, BatchReachRules);
    app = booted.app;
    prisma = booted.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  function asWiderWriter() {
    return prisma.forContext(ctxFor('mod@example.com'));
  }

  function asMatchedWriter() {
    return prisma.forContext(ctxFor('ada@example.com'));
  }

  it('tells the caller nothing about an unpublished post through the count an updateMany reports', async () => {
    const update = (prefix: string) =>
      asWiderWriter().post.updateMany({
        where: { title: { startsWith: prefix } },
        data: { published: false },
      });

    const hit = await probe(() => update('Draft'));
    const miss = await probe(() => update('zzz'));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
  });

  it('tells the caller nothing about an unpublished post through the count a deleteMany reports', async () => {
    const remove = (prefix: string) =>
      asWiderWriter().post.deleteMany({ where: { title: { startsWith: prefix } } });

    const hit = await probe(() => remove('Draft'));
    const miss = await probe(() => remove('zzz'));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
    expect(await prisma.post.count()).toBe(3);
  });

  it('tells the caller nothing about an unpublished post through a batch write on another field', async () => {
    const update = (ceiling: bigint) =>
      asWiderWriter().post.updateMany({
        where: { viewCount: { lt: ceiling } },
        data: { published: false },
      });

    const hit = await probe(() => update(10n));
    const miss = await probe(() => update(1n));

    expect(hit).toEqual({ disclosed: false });
    expect(hit).toEqual(miss);
  });

  it('still runs a batch write for a caller whose write reach matches its read reach', async () => {
    const updated = await asMatchedWriter().post.updateMany({
      where: { title: { startsWith: 'Memory' } },
      data: { published: true },
    });

    expect(updated).toEqual({ count: 1 });
  });

  it('still reports an empty batch for a caller whose write reach matches its read reach', async () => {
    const deleted = await asMatchedWriter().post.deleteMany({
      where: { title: { startsWith: 'no such post' } },
    });

    expect(deleted).toEqual({ count: 0 });
    expect(await prisma.post.count()).toBe(3);
  });

  it('still reads the posts its read constraint admits', async () => {
    const posts = await asWiderWriter().post.findMany({
      orderBy: [{ title: 'asc' }],
      select: { title: true },
    });

    expect(posts).toEqual([{ title: 'First post' }, { title: 'Memory systems' }]);
  });
});

describe('a caller granted update without read (e2e)', () => {
  const suite = `${__filename}.write-only`;
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let postId: string;

  beforeAll(async () => {
    const booted = await bootRules(suite, WriteOnlyRules);
    app = booted.app;
    prisma = booted.prisma;
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'Draft post' } });
    postId = post.id;
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  function asWriter() {
    return prisma.forContext(ctxFor('mod@example.com'));
  }

  it('refuses to filter an updateMany even by the primary key', async () => {
    await expect(
      asWriter().post.updateMany({ where: { id: postId }, data: { published: true } }),
    ).rejects.toThrow('Cannot filter or order by field "id" on Post');
    const untouched = await prisma.post.findUniqueOrThrow({ where: { id: postId } });
    expect(untouched.published).toBe(false);
  });

  it('refuses to filter a deleteMany by any field', async () => {
    await expect(
      asWriter().post.deleteMany({ where: { title: { startsWith: 'Draft' } } }),
    ).rejects.toThrow('Cannot filter or order by field "title" on Post');
    expect(await prisma.post.count()).toBe(3);
  });
});

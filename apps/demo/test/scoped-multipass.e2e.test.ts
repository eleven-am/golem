import { INestApplication } from '@nestjs/common';
import { GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

interface ShareRow {
  readonly posts: unknown;
  readonly share: unknown;
}

describe('a multi-pass scoped query (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  const sharesFor = async (email: string): Promise<ShareRow[]> => {
    const rows = await prisma
      .forContext(ctxFor(email))
      .$scoped('Post')
      .query((qb, db) => {
        const passes = db
          .with('mine', () => qb.select(['Post.authorId', 'Post.title']))
          .with('perAuthor', (c) =>
            (c as never as typeof db)
              .selectFrom('mine')
              .select('mine.authorId as authorId' as never)
              .select((eb) => eb.fn.countAll().as('posts'))
              .groupBy('mine.authorId' as never) as never,
          )
          .with('across', (c) =>
            (c as never as typeof db)
              .selectFrom('perAuthor')
              .select((eb) => eb.fn.avg('perAuthor.posts' as never).as('mean')) as never,
          )
          .selectFrom('perAuthor') as any;
        return passes
          .crossJoin('across')
          .select('perAuthor.posts')
          .select((eb: any) =>
            eb(eb.ref('perAuthor.posts'), '/', eb.ref('across.mean')).as('share'),
          )
          .orderBy('perAuthor.posts') as never;
      })
      .execute();
    return rows as ShareRow[];
  };

  it('averages over the rows the caller may see, never over the rows they may not', async () => {
    const rows = await sharesFor('guest@example.com');

    expect(rows.map((row) => Number(row.posts))).toEqual([1, 1]);
    expect(rows.map((row) => Number(row.share))).toEqual([1, 1]);
  });

  it('gives an unrestricted caller the mean their larger set implies', async () => {
    const rows = await sharesFor('roy@example.com');

    expect(rows.map((row) => Number(row.posts))).toEqual([1, 2]);
    expect(Number(rows[0]!.share)).toBeCloseTo(2 / 3, 9);
    expect(Number(rows[1]!.share)).toBeCloseTo(4 / 3, 9);
  });

  it('refuses a common table expression named after the physical table', async () => {
    await expect(
      prisma
        .forContext(ctxFor('roy@example.com'))
        .$scoped('Post')
        .query((qb, db) =>
          db
            .with('Post', () => qb.select('Post.title'))
            .selectFrom('Post')
            .select('Post.title' as never) as never,
        )
        .execute(),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('refuses a common table expression whose body reads a table golem did not scope', async () => {
    await expect(
      prisma
        .forContext(ctxFor('roy@example.com'))
        .$scoped('Post')
        .query((qb, db) =>
          db
            .with('leak', (c) =>
              (c as never as typeof db)
                .selectFrom('User' as never)
                .select('User.email as email' as never) as never,
            )
            .selectFrom('leak')
            .select('leak.email' as never) as never,
        )
        .execute(),
    ).rejects.toThrow('may not read the table "User" in a FROM clause');
  });
});

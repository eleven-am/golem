import { INestApplication } from '@nestjs/common';
import { GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('the scoped query root (e2e)', () => {
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

  it('answers a window function over the rows the caller ability permits, undecoded', async () => {
    const rows = await prisma
      .forContext(ctxFor('guest@example.com'))
      .$scoped('Post')
      .query((qb) =>
        qb
          .select(['Post.title', 'Post.published'])
          .select((eb) =>
            eb.fn
              .countAll()
              .over((over) => over.partitionBy('Post.authorId'))
              .as('byAuthor'),
          )
          .orderBy('Post.title'),
      )
      .execute();

    expect(rows).toEqual([
      { title: 'First post', published: true, byAuthor: 1n },
      { title: 'Memory systems', published: true, byAuthor: 1n },
    ]);
  });

  it('gives an unrestricted caller the rows a restricted one cannot see', async () => {
    const rows = (await prisma
      .forContext(ctxFor('roy@example.com'))
      .$scoped('Post')
      .query((qb) => qb.select('Post.title').orderBy('Post.title'))
      .execute()) as { title: string }[];

    expect(rows.map((row) => row.title)).toEqual(['Draft post', 'First post', 'Memory systems']);
  });

  it('keeps hidden fields out of the scoped projection', async () => {
    const rows = (await prisma
      .forContext(ctxFor('roy@example.com'))
      .$scoped('User')
      .query((qb) => qb.selectAll())
      .execute()) as Record<string, unknown>[];

    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(Object.keys(row)).not.toContain('apiKey');
      expect(Object.keys(row)).toContain('email');
    }
  });

  it('refuses to select a field the caller may never read', async () => {
    await expect(
      prisma
        .forContext(ctxFor('guest@example.com'))
        .$scoped('User')
        .query((qb) => qb.select(['User.email', 'User.phone' as never]))
        .execute(),
    ).rejects.toThrow('may not reference "User"."phone"');
  });

  it('refuses to filter by a field the caller may never read', async () => {
    await expect(
      prisma
        .forContext(ctxFor('guest@example.com'))
        .$scoped('User')
        .query((qb) => qb.select('User.email').where('User.phone' as never, '=', '+1-555-0100' as never))
        .execute(),
    ).rejects.toThrow('may not reference "User"."phone"');
  });

  it('refuses to order by a field the caller may never read', async () => {
    await expect(
      prisma
        .forContext(ctxFor('guest@example.com'))
        .$scoped('User')
        .query((qb) => qb.select('User.email').orderBy('User.phone' as never))
        .execute(),
    ).rejects.toThrow('may not reference "User"."phone"');
  });

  it('keeps a field the caller may never read out of a scoped star projection', async () => {
    const rows = (await prisma
      .forContext(ctxFor('guest@example.com'))
      .$scoped('User')
      .query((qb) => qb.selectAll())
      .execute()) as Record<string, unknown>[];

    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(Object.keys(row)).not.toContain('phone');
      expect(Object.keys(row)).toContain('email');
    }
  });

  it('projects a conditionally readable field through its mask', async () => {
    const rows = await prisma
      .forContext(ctxFor('ada@example.com'))
      .$scoped('User')
      .query((qb) => qb.select(['User.email', 'User.phone' as never]).orderBy('User.email'))
      .execute();

    expect(rows).toEqual([
      { email: 'ada@example.com', phone: '+44-555-0200' },
      { email: 'guest@example.com', phone: null },
      { email: 'mod@example.com', phone: null },
      { email: 'roy@example.com', phone: null },
    ]);
  });

  it('cannot recover a masked value by filtering for it', async () => {
    const rows = await prisma
      .forContext(ctxFor('ada@example.com'))
      .$scoped('User')
      .query((qb) =>
        qb.select('User.email').where('User.phone' as never, '=', '+1-555-0100' as never),
      )
      .execute();

    expect(rows).toEqual([]);
  });

  it('refuses a join onto a table golem did not scope', async () => {
    await expect(
      prisma
        .forContext(ctxFor('roy@example.com'))
        .$scoped('Post')
        .query((qb) => (qb as any).innerJoin('User', 'User.id', 'Post.authorId'))
        .execute(),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('joins a second scoped root, applying both abilities', async () => {
    const rows = await prisma
      .forContext(ctxFor('guest@example.com'))
      .$scoped('Post')
      .join('inner', 'User', 'Author', (join) => join.onRef('Author.id', '=', 'Post.authorId'))
      .query((qb) => qb.select(['Post.title', 'Author.email']).orderBy('Post.title'))
      .execute();

    expect(rows).toEqual([
      { title: 'First post', email: 'roy@example.com' },
      { title: 'Memory systems', email: 'ada@example.com' },
    ]);
  });

  it('refuses a caller whose ability grants no read on the model at all', async () => {
    await expect(
      prisma
        .forContext(ctxFor('guest@example.com'))
        .$scoped('ReadingSession')
        .query((qb) => qb.select('ReadingSession.id'))
        .execute(),
    ).rejects.toThrow();
  });

  it('runs on the open transaction client', async () => {
    const rows = await prisma.forContext(ctxFor('guest@example.com')).$transaction((tx) =>
      tx
        .$scoped('Post')
        .query((qb) => qb.select('Post.title').orderBy('Post.title'))
        .execute(),
    );

    expect(rows).toEqual([{ title: 'First post' }, { title: 'Memory systems' }]);
  });
});

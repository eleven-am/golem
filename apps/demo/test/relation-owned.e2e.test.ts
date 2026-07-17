import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('relation-owned read hydration (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let adaPostId: string;
  let modPostId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
  });

  beforeEach(async () => {
    await seed(prisma);
    const modId = (await prisma.user.findUniqueOrThrow({ where: { email: 'mod@example.com' } })).id;
    adaPostId = (await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } })).id;
    modPostId = (await prisma.post.create({ data: { title: 'Mod post', authorId: modId } })).id;
    await prisma.readingSession.create({ data: { postId: adaPostId, progress: 10, note: 'ada note' } });
    await prisma.readingSession.create({ data: { postId: modPostId, progress: 20, note: 'mod note' } });
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, token: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', token).send({ query });
  }

  it('returns the full row from a forContext create for the relation owner', async () => {
    const created = await prisma.forContext(ctxFor('ada@example.com')).readingSession.create({
      data: { postId: adaPostId, progress: 3, note: 'fresh' },
      select: { progress: true, note: true },
    });
    expect(created).toEqual({ progress: 3, note: 'fresh' });
  });

  it('serves unmasked rows to the owner and hides other owners rows', async () => {
    const response = await gql('{ readingSessions { progress note } }', 'token-ada@example.com');
    expect(response.body.errors).toBeUndefined();
    const sessions = response.body.data.readingSessions as Array<{ progress: number; note: string | null }>;
    expect(sessions).toEqual([{ progress: 10, note: 'ada note' }]);
  });

  it('masks a relation-conditional field per row and blocks it entirely where unauthorized', async () => {
    const response = await gql('{ readingSessions { progress note } }', 'token-mod@example.com');
    expect(response.body.errors).toBeUndefined();
    const byProgress = Object.fromEntries(
      (response.body.data.readingSessions as Array<{ progress: number; note: string | null }>).map(
        (row) => [row.progress, row.note],
      ),
    );
    expect(byProgress[20]).toBe('mod note');
    expect(byProgress[10]).toBeNull();
  });

  it('keeps a user-selected relation shape without leaking the injected hydration column', async () => {
    const rows = await prisma.forContext(ctxFor('ada@example.com')).readingSession.findMany({
      select: { progress: true, post: { select: { title: true } } },
    });
    expect(rows).toHaveLength(1);
    expect(rows[0].post).toEqual({ title: 'Memory systems' });
    expect('authorId' in (rows[0].post as object)).toBe(false);
  });

  it('strips the wholly-injected relation from a scalar-only selection', async () => {
    const rows = await prisma.forContext(ctxFor('ada@example.com')).readingSession.findMany({
      select: { progress: true, note: true },
    });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toEqual({ progress: 10, note: 'ada note' });
    expect('post' in rows[0]).toBe(false);
  });
});

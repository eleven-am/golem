import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

const NUL_TITLE = 'before\u0000after';

describe('strings containing a NUL byte (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let royId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    royId = (await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } })).id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, variables: Record<string, unknown>) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .send({ query, variables });
  }

  async function nulRows(): Promise<number> {
    const rows = await prisma.post.findMany({ select: { title: true } });
    return rows.filter((row) => row.title.includes('\u0000')).length;
  }

  it('refuses a NUL byte in a GraphQL string argument before writing', async () => {
    const response = await gql(
      'mutation ($title: String!, $author: String!) { createPost(data: { title: $title, author: { connect: { id: $author } } }) { id } }',
      { title: NUL_TITLE, author: royId },
    );
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(response.body.data).toBeNull();
    await expect(nulRows()).resolves.toBe(0);
  });

  it('refuses a NUL byte in a GraphQL filter, including on the compiled read path', async () => {
    const response = await gql(
      'query ($title: String!) { posts(where: { title: { equals: $title } }) { id } }',
      { title: NUL_TITLE },
    );
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
  });

  it('refuses a NUL byte through a context-bound create', async () => {
    await expect(
      prisma.forContext(ctxFor('roy@example.com')).post.create({
        data: { title: NUL_TITLE, author: { connect: { id: royId } } },
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(nulRows()).resolves.toBe(0);
  });

  it('refuses a NUL byte nested inside an update', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { authorId: royId } });
    await expect(
      prisma.forContext(ctxFor('roy@example.com')).user.update({
        where: { id: royId },
        data: { posts: { update: [{ where: { id: post.id }, data: { title: NUL_TITLE } }] } },
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(nulRows()).resolves.toBe(0);
  });

  it('refuses a NUL byte on the unscoped client', async () => {
    await expect(
      prisma.post.create({ data: { title: NUL_TITLE, authorId: royId } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(nulRows()).resolves.toBe(0);
  });

  it('refuses a NUL byte bound into raw SQL', async () => {
    await expect(
      prisma.$queryRawUnsafe('SELECT id FROM Post WHERE title = ?', NUL_TITLE),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('still accepts ordinary strings', async () => {
    await expect(
      prisma.forContext(ctxFor('roy@example.com')).post.create({
        data: { title: 'no nul here', author: { connect: { id: royId } } },
        select: { title: true },
      }),
    ).resolves.toEqual({ title: 'no nul here' });
  });
});

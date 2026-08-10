import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('field-level write permissions (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let draftId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    const draft = await prisma.post.findFirst({ where: { title: 'Draft post' } });
    draftId = draft!.id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, token: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send({ query });
  }

  it('lets the moderator flip the permitted field on anyone\'s post', async () => {
    const response = await gql(
      `mutation { updatePost(where: { id: "${draftId}" }, data: { published: true }) { published } }`,
      'token-mod@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.updatePost.published).toBe(true);
  });

  it('refuses the moderator any other field, naming it', async () => {
    const response = await gql(
      `mutation { updatePost(where: { id: "${draftId}" }, data: { title: "renamed by mod" }) { id } }`,
      'token-mod@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
    expect(response.body.errors[0].message).toContain('field "title"');
    const untouched = await prisma.post.findUnique({ where: { id: draftId } });
    expect(untouched!.title).toBe('Draft post');
  });

  it('enforces the same rule through forContext', async () => {
    await expect(
      prisma.forContext(ctxFor('mod@example.com')).post.update({
        where: { id: draftId },
        data: { title: 'renamed via facade' },
      }),
    ).rejects.toThrow('Cannot update field "title" on Post');

    const flipped = await prisma.forContext(ctxFor('mod@example.com')).post.update({
      where: { id: draftId },
      data: { published: false },
    });
    expect(flipped.published).toBe(false);
  });

  it('leaves field-unrestricted users untouched', async () => {
    const post = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    const response = await gql(
      `mutation { updatePost(where: { id: "${post!.id}" }, data: { title: "Memory systems II" }) { title } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.updatePost.title).toBe('Memory systems II');
  });
});

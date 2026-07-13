import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

describe('golem mutations (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
  });

  function gql(query: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', 'token-roy@example.com').send({ query }).expect(200);
  }

  it('creates a user with nested posts and profile in one mutation', async () => {
    const response = await gql(`mutation {
      createUser(data: {
        email: "grace@example.com"
        name: "Grace"
        profile: { create: { bio: "compiler pioneer" } }
        posts: { create: [{ title: "Nested post", published: true }] }
      }) {
        email
        profile { bio }
        posts { title published }
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createUser).toEqual({
      email: 'grace@example.com',
      profile: { bio: 'compiler pioneer' },
      posts: [{ title: 'Nested post', published: true }],
    });
  });

  it('creates a post connected to an existing user', async () => {
    const response = await gql(`mutation {
      createPost(data: {
        title: "Connected post"
        author: { connect: { email: "grace@example.com" } }
      }) {
        title
        author { email }
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createPost).toEqual({
      title: 'Connected post',
      author: { email: 'grace@example.com' },
    });
  });

  it('updates scalars and relation connections', async () => {
    const post = await prisma.post.findFirst({ where: { title: 'Connected post' } });
    const response = await gql(`mutation {
      updateUser(where: { email: "ada@example.com" }, data: {
        name: "Ada L."
        posts: { connect: [{ id: "${post!.id}" }] }
      }) {
        name
        posts { title }
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.updateUser.name).toBe('Ada L.');
    expect(response.body.data.updateUser.posts.map((p: { title: string }) => p.title)).toContain(
      'Connected post',
    );
  });

  it('updates many and deletes many with batch counts', async () => {
    const drafts = await gql(`mutation {
      updateManyPosts(where: { published: { equals: false } }, data: { published: true }) { count }
    }`);
    expect(drafts.body.errors).toBeUndefined();
    expect(drafts.body.data.updateManyPosts.count).toBeGreaterThanOrEqual(1);

    const created = await gql(`mutation {
      createPost(data: { title: "doomed", author: { connect: { email: "grace@example.com" } } }) { id }
    }`);
    expect(created.body.errors).toBeUndefined();

    const deleted = await gql(`mutation {
      deleteManyPosts(where: { title: { equals: "doomed" } }) { count }
    }`);
    expect(deleted.body.errors).toBeUndefined();
    expect(deleted.body.data.deleteManyPosts.count).toBe(1);
  });

  it('deletes a childless user and returns the deleted record', async () => {
    await gql(`mutation { createUser(data: { email: "temp@example.com" }) { id } }`);
    const response = await gql(`mutation {
      deleteUser(where: { email: "temp@example.com" }) { email }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.deleteUser).toEqual({ email: 'temp@example.com' });
  });

  it('returns NOT_FOUND with no prisma internals for missing records', async () => {
    const response = await gql(`mutation {
      updateUser(where: { email: "ghost@example.com" }, data: { name: "x" }) { id }
    }`);
    expect(response.body.errors).toBeDefined();
    expect(response.body.errors[0].extensions.code).toBe('NOT_FOUND');
    expect(JSON.stringify(response.body.errors)).not.toContain('P2025');
    expect(JSON.stringify(response.body.errors)).not.toContain('prisma');
  });

  it('returns CONFLICT for unique constraint violations', async () => {
    const response = await gql(`mutation {
      createUser(data: { email: "roy@example.com" }) { id }
    }`);
    expect(response.body.errors).toBeDefined();
    expect(response.body.errors[0].extensions.code).toBe('CONFLICT');
  });

  it('returns BAD_USER_INPUT when disconnect would break a required relation', async () => {
    const response = await gql(`mutation {
      updateUser(where: { email: "roy@example.com" }, data: {
        profile: { disconnect: true }
      }) { id }
    }`);
    expect(response.body.errors).toBeDefined();
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
  });

  it('rejects foreign key scalars in write inputs at the schema level', async () => {
    const response = await request(app.getHttpServer())
      .post('/graphql')
      .send({
        query: 'mutation { createPost(data: { title: "x", authorId: "u1" }) { id } }',
      });
    expect(response.body.errors).toBeDefined();
    const messages = response.body.errors.map((e: { message: string }) => e.message).join('\n');
    expect(messages).toContain('"authorId" is not defined by type "PostCreateInput"');
  });
});

import { INestApplication } from '@nestjs/common';
import { TestingModule } from '@nestjs/testing';
import request from 'supertest';
import { CompiledReadEvent, GolemEngine } from '@eleven-am/golem-core';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

describe('counting a relation over GraphQL (e2e)', () => {
  let app: INestApplication;
  let moduleRef: TestingModule;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;
  let events: CompiledReadEvent[];
  let release: () => void;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    moduleRef = context.moduleRef;
    prisma = context.prisma;
    engine = moduleRef.get<GolemEngine>('GOLEM_ENGINE');
    const roy = await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } });
    const ada = await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } });
    await prisma.branch.createMany({
      data: [
        { name: 'roy-one', authorId: roy.id },
        { name: 'roy-two', authorId: roy.id },
        { name: 'ada-one', authorId: ada.id },
      ],
    });
    const royPost = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    await prisma.readingSession.createMany({
      data: [
        { postId: royPost.id, progress: 10 },
        { postId: royPost.id, progress: 20 },
        { postId: adaPost.id, progress: 30 },
      ],
    });
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  beforeEach(() => {
    events = [];
    release = engine.observeCompiledRead((event) => events.push(event));
  });

  afterEach(() => {
    release();
  });

  function gql(query: string, email = 'roy@example.com') {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', `token-${email}`)
      .send({ query })
      .expect(200);
  }

  const postCounts = `{
    users(orderBy: [{ email: asc }]) { email _count { posts branches } }
  }`;

  it('counts a relation for a caller who may read all of it', async () => {
    const response = await gql(postCounts);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', _count: { posts: 1, branches: 1 } },
      { email: 'guest@example.com', _count: { posts: 0, branches: 0 } },
      { email: 'mod@example.com', _count: { posts: 0, branches: 0 } },
      { email: 'roy@example.com', _count: { posts: 2, branches: 2 } },
    ]);
    expect(events.filter((event) => event.model === 'User').map((event) => event.outcome)).toEqual([
      'compiled',
    ]);
    expect(events[0].sql).toContain('count(*)');
  });

  it('counts only what a scoped caller may read, and zero where it may read nothing', async () => {
    const response = await gql(postCounts, 'guest@example.com');

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', _count: { posts: 1, branches: 0 } },
      { email: 'guest@example.com', _count: { posts: 0, branches: 0 } },
      { email: 'mod@example.com', _count: { posts: 0, branches: 0 } },
      { email: 'roy@example.com', _count: { posts: 1, branches: 0 } },
    ]);
  });

  it('agrees with the rows the same caller is allowed to read', async () => {
    const response = await gql(
      `{ users(orderBy: [{ email: asc }]) { email posts { id } _count { posts } } }`,
      'guest@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    for (const user of response.body.data.users) {
      expect(user._count.posts).toBe(user.posts.length);
    }
    expect(response.body.data.users.map((user: { _count: { posts: number } }) => user._count.posts))
      .toEqual([1, 0, 0, 1]);
  });

  it('counts a relation the policy narrows by a condition on another table', async () => {
    const mine = await gql(
      `{ posts(orderBy: [{ title: asc }]) { title _count { readingSessions } } }`,
      'ada@example.com',
    );

    expect(mine.body.errors).toBeUndefined();
    expect(mine.body.data.posts).toEqual([
      { title: 'Draft post', _count: { readingSessions: 0 } },
      { title: 'First post', _count: { readingSessions: 0 } },
      { title: 'Memory systems', _count: { readingSessions: 1 } },
    ]);

    expect(events[0].outcome).toBe('compiled');
    expect(events[0].sql).toContain('EXISTS (SELECT 1 FROM "Post"');

    const owner = await gql(`{ posts(orderBy: [{ title: asc }]) { title _count { readingSessions } } }`);
    expect(owner.body.data.posts).toEqual([
      { title: 'Draft post', _count: { readingSessions: 0 } },
      { title: 'First post', _count: { readingSessions: 2 } },
      { title: 'Memory systems', _count: { readingSessions: 1 } },
    ]);
  });

  it('refuses a count of a model the caller may not read at all', async () => {
    const response = await gql(
      `{ posts { title _count { readingSessions } } }`,
      'guest@example.com',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
  });

  it('counts a relation on the row a mutation hands back', async () => {
    const response = await gql(`mutation {
      createPost(data: { title: "Counted", author: { connect: { email: "roy@example.com" } } }) {
        title
        _count { readingSessions }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createPost).toEqual({
      title: 'Counted',
      _count: { readingSessions: 0 },
    });
  });

  it('counts no relation whose model is off the surface', async () => {
    const response = await request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .send({ query: '{ posts { _count { postTags } } }' });

    expect(response.body.errors[0].message).toContain('Cannot query field "postTags"');
    const counted = await gql(`{ __type(name: "PostCountOutputType") { fields { name } } }`);
    expect(counted.body.data.__type.fields.map((field: { name: string }) => field.name)).toEqual([
      'readingSessions',
    ]);
  });

  it('offers no count on a model holding no to-many relation', async () => {
    const response = await gql(`{ __type(name: "Branch") { fields { name } } }`);

    expect(response.body.data.__type.fields.map((field: { name: string }) => field.name)).not.toContain(
      '_count',
    );
    const counted = await gql(`{ __type(name: "UserCountOutputType") { fields { name } } }`);
    expect(counted.body.data.__type.fields.map((field: { name: string }) => field.name)).toEqual([
      'posts',
      'branches',
    ]);
  });
});

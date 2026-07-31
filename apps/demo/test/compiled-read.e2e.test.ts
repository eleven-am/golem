import { INestApplication } from '@nestjs/common';
import { TestingModule } from '@nestjs/testing';
import request from 'supertest';
import { CompiledReadEvent, GolemEngine } from '@eleven-am/golem-core';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('the compiled read path (e2e)', () => {
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

  function outcomes(model: string): CompiledReadEvent[] {
    return events.filter((event) => event.model === model);
  }

  it('compiles a scalar findMany rather than handing it to Prisma', async () => {
    const response = await gql(`{
      posts(orderBy: [{ title: asc }]) { title published viewCount }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'Draft post', published: false, viewCount: '5' },
      { title: 'First post', published: true, viewCount: '9007199254740993' },
      { title: 'Memory systems', published: true, viewCount: '100' },
    ]);
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
    expect(outcomes('Post')[0].sql).toContain('from "Post" as "t0"');
  });

  it('compiles a scalar findOne rather than handing it to Prisma', async () => {
    const target = await prisma.post.findFirst({ where: { title: 'First post' } });
    const response = await gql(`{
      post(where: { id: "${target!.id}" }) { title viewCount rating }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.post).toEqual({
      title: 'First post',
      viewCount: '9007199254740993',
      rating: '0.1',
    });
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('carries the policy predicate into the compiled statement', async () => {
    const response = await gql(
      `{ posts(orderBy: [{ title: asc }]) { title } }`,
      'guest@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'First post' },
      { title: 'Memory systems' },
    ]);
    const compiled = outcomes('Post')[0];
    expect(compiled.outcome).toBe('compiled');
    expect(compiled.sql).toContain('"published"');
  });

  it('compiles a selection set that reaches a to-one relation', async () => {
    const response = await gql(`{
      posts(orderBy: [{ title: asc }]) { title author { email } }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'Draft post', author: { email: 'roy@example.com' } },
      { title: 'First post', author: { email: 'roy@example.com' } },
      { title: 'Memory systems', author: { email: 'ada@example.com' } },
    ]);
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
    expect(outcomes('Post')[0].sql).toContain('from "User" as "t1"');
  });

  it('compiles a selection set that reaches a to-many relation', async () => {
    const response = await gql(`{
      users(orderBy: [{ email: asc }]) { email posts { title } }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', posts: [{ title: 'Memory systems' }] },
      { email: 'guest@example.com', posts: [] },
      { email: 'mod@example.com', posts: [] },
      {
        email: 'roy@example.com',
        posts: [{ title: 'First post' }, { title: 'Draft post' }],
      },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('compiles two levels of relation', async () => {
    const response = await gql(`{
      users(where: { email: { equals: "roy@example.com" } }) {
        email
        posts { title author { email } }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      {
        email: 'roy@example.com',
        posts: [
          { title: 'First post', author: { email: 'roy@example.com' } },
          { title: 'Draft post', author: { email: 'roy@example.com' } },
        ],
      },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('carries a BigInt and a Decimal out of a nested relation without losing either', async () => {
    const response = await gql(`{
      users(where: { email: { equals: "roy@example.com" } }) {
        posts { title viewCount rating createdAt published }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    const posts = response.body.data.users[0].posts;
    expect(posts[0]).toMatchObject({
      title: 'First post',
      viewCount: '9007199254740993',
      rating: '0.1',
      published: true,
    });
    expect(posts[1]).toMatchObject({
      title: 'Draft post',
      viewCount: '5',
      rating: null,
      published: false,
    });
    expect(Date.parse(posts[0].createdAt)).not.toBeNaN();
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('masks a field read through a relation exactly as it masks one at the top level', async () => {
    const response = await gql(
      `{ posts(orderBy: [{ title: asc }]) { title author { email phone } } }`,
      'ada@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'Draft post', author: { email: 'roy@example.com', phone: null } },
      { title: 'First post', author: { email: 'roy@example.com', phone: null } },
      {
        title: 'Memory systems',
        author: { email: 'ada@example.com', phone: '+44-555-0200' },
      },
    ]);
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('nulls a to-one the policy denies and strips the column that decided it', async () => {
    const response = await gql(
      `{ users(orderBy: [{ email: asc }]) { email profile { bio } } }`,
      'ada@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', profile: { bio: 'countess of computing' } },
      { email: 'guest@example.com', profile: null },
      { email: 'mod@example.com', profile: null },
      { email: 'roy@example.com', profile: null },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('pages a nested relation per parent rather than handing the read to Prisma', async () => {
    const found = await engine.findMany({
      model: 'User',
      select: { email: true, posts: { select: { title: true }, take: 1, orderBy: [{ title: 'asc' }] } },
      orderBy: [{ email: 'asc' }],
      context: ctxFor('roy@example.com'),
      compiled: true,
    });

    expect(found).toEqual([
      { email: 'ada@example.com', posts: [{ title: 'Memory systems' }] },
      { email: 'guest@example.com', posts: [] },
      { email: 'mod@example.com', posts: [] },
      { email: 'roy@example.com', posts: [{ title: 'Draft post' }] },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('compiles a cursor rather than handing the read to Prisma', async () => {
    const rows = await prisma.post.findMany({ select: { id: true }, orderBy: { id: 'asc' } });
    const found = await engine.findMany({
      model: 'Post',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: rows[1].id },
      compiled: true,
    });

    expect(found).toEqual([{ id: rows[1].id }, { id: rows[2].id }]);
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('compiles distinct rather than handing the read to Prisma', async () => {
    const found = (await engine.findMany({
      model: 'Post',
      select: { published: true },
      orderBy: [{ published: 'asc' }],
      distinct: ['published'],
      compiled: true,
    })) as { published: boolean }[];

    expect(found.map((row) => row.published)).toEqual([false, true]);
    expect(outcomes('Post').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('orders a read by a column of the relation it reaches', async () => {
    const found = await engine.findMany({
      model: 'Post',
      select: { title: true },
      orderBy: [{ author: { email: 'asc' } }, { title: 'asc' }],
      compiled: true,
    });

    expect(found).toEqual([
      { title: 'Memory systems' },
      { title: 'Draft post' },
      { title: 'First post' },
    ]);
    const compiled = outcomes('Post')[0];
    expect(compiled.outcome).toBe('compiled');
    expect(compiled.sql).toContain('left join');
  });

  it('orders a read by how many rows a to-many holds', async () => {
    const found = await engine.findMany({
      model: 'User',
      select: { email: true },
      orderBy: [{ posts: { _count: 'desc' } }, { email: 'asc' }],
      compiled: true,
    });

    expect(found).toEqual([
      { email: 'roy@example.com' },
      { email: 'ada@example.com' },
      { email: 'guest@example.com' },
      { email: 'mod@example.com' },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });

  it('reaches an unindexed foreign key in two statements and an indexed one in one', async () => {
    const owner = await prisma.user.findFirst({ where: { email: 'roy@example.com' } });
    await prisma.branch.createMany({
      data: [
        { name: 'main', authorId: owner!.id },
        { name: 'next', authorId: owner!.id },
      ],
    });

    const unindexed = await engine.findMany({
      model: 'User',
      select: { email: true, posts: { select: { title: true } } },
      where: { email: 'roy@example.com' },
      compiled: true,
    });
    const batched = outcomes('User')[0];
    expect(batched.outcome).toBe('compiled');
    expect(batched.batched).toEqual(['posts']);
    expect(batched.statements).toHaveLength(2);
    expect(batched.statements![1]).toContain('"authorId" in (');
    expect((unindexed[0] as { posts: unknown[] }).posts).toHaveLength(2);

    events = [];
    const indexed = await engine.findMany({
      model: 'User',
      select: { email: true, branches: { select: { name: true }, orderBy: [{ name: 'asc' }] } },
      where: { email: 'roy@example.com' },
      compiled: true,
    });
    const correlated = outcomes('User')[0];
    expect(correlated.outcome).toBe('compiled');
    expect(correlated.batched).toEqual([]);
    expect(correlated.statements).toHaveLength(1);
    expect(indexed).toEqual([
      { email: 'roy@example.com', branches: [{ name: 'main' }, { name: 'next' }] },
    ]);
  });

  it('answers a batched relation exactly as Prisma answers it', async () => {
    const request = {
      select: {
        email: true,
        posts: { select: { title: true, viewCount: true }, orderBy: [{ title: 'asc' }], take: 1 },
      },
      orderBy: [{ email: 'asc' }] as const,
    };
    const compiled = await engine.findMany({
      model: 'User',
      ...request,
      orderBy: [{ email: 'asc' }],
      compiled: true,
    });
    const plain = await engine.findMany({ model: 'User', ...request, orderBy: [{ email: 'asc' }] });

    expect(compiled).toStrictEqual(plain);
    expect(outcomes('User')[0].batched).toEqual(['posts']);
  });

  it('leaves the programmatic client on Prisma', async () => {
    const found = await prisma
      .forContext(ctxFor('roy@example.com'))
      .post.findMany({ select: { title: true }, orderBy: { title: 'asc' } });

    expect(found).toEqual([
      { title: 'Draft post' },
      { title: 'First post' },
      { title: 'Memory systems' },
    ]);
    expect(events).toEqual([]);
  });

  it('leaves the programmatic client on Prisma even when it asks for the compiled path', async () => {
    const found = await (
      prisma.forContext(ctxFor('roy@example.com')).post as unknown as {
        findMany(args: Record<string, unknown>): Promise<unknown>;
      }
    ).findMany({ select: { title: true }, orderBy: { title: 'asc' }, compiled: true });

    expect(found).toEqual([
      { title: 'Draft post' },
      { title: 'First post' },
      { title: 'Memory systems' },
    ]);
    expect(events).toEqual([]);
  });

  it('masks a field on a compiled read exactly as it masks one on a Prisma read', async () => {
    const response = await gql(
      `{ users(orderBy: [{ email: asc }]) { email phone } }`,
      'ada@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', phone: '+44-555-0200' },
      { email: 'guest@example.com', phone: null },
      { email: 'mod@example.com', phone: null },
      { email: 'roy@example.com', phone: null },
    ]);
    expect(outcomes('User').map((event) => event.outcome)).toEqual(['compiled']);
  });
});

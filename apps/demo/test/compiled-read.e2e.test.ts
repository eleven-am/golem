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

  it('falls back to Prisma when the selection set reaches a relation', async () => {
    const response = await gql(`{
      posts(orderBy: [{ title: asc }]) { title author { email } }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts[0]).toEqual({
      title: 'Draft post',
      author: { email: 'roy@example.com' },
    });
    expect(outcomes('Post')).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'relation' }),
    ]);
  });

  it('falls back to Prisma when a cursor is asked for', async () => {
    const rows = await prisma.post.findMany({ select: { id: true }, orderBy: { id: 'asc' } });
    const found = await engine.findMany({
      model: 'Post',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: rows[1].id },
      compiled: true,
    });

    expect(found).toEqual([{ id: rows[1].id }, { id: rows[2].id }]);
    expect(outcomes('Post')).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'cursor' }),
    ]);
  });

  it('falls back to Prisma when distinct is asked for', async () => {
    const found = (await engine.findMany({
      model: 'Post',
      select: { published: true },
      orderBy: [{ published: 'asc' }],
      distinct: ['published'],
      compiled: true,
    })) as { published: boolean }[];

    expect(found.map((row) => row.published)).toEqual([false, true]);
    expect(outcomes('Post')).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'distinct' }),
    ]);
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

import { INestApplication } from '@nestjs/common';
import { TestingModule } from '@nestjs/testing';
import request from 'supertest';
import { CompiledReadEvent, GolemEngine } from '@eleven-am/golem-core';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('masking a field in the compiled statement (e2e)', () => {
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
    await app.listen(0);
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

  function gql(query: string, email: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', `token-${email}`)
      .send({ query })
      .expect(200);
  }

  function compiledFor(model: string): CompiledReadEvent {
    const event = events.find((candidate) => candidate.model === model);
    expect(event).toBeDefined();
    expect(event!.outcome).toBe('compiled');
    return event!;
  }

  it('masks a scalar the caller may read only on their own row', async () => {
    const response = await gql('{ users(orderBy: [{ email: asc }]) { email phone } }', 'ada@example.com');

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', phone: '+44-555-0200' },
      { email: 'guest@example.com', phone: null },
      { email: 'mod@example.com', phone: null },
      { email: 'roy@example.com', phone: null },
    ]);
    const compiled = compiledFor('User');
    expect(compiled.masked).toEqual(['phone']);
    expect(compiled.deferred).toEqual([]);
    expect(compiled.sql).toContain('case when');
    expect(compiled.sql).toContain('else null end as "phone"');
  });

  it('asks the database for no column it only needed to check the field in memory', async () => {
    await gql('{ users(orderBy: [{ email: asc }]) { email phone } }', 'ada@example.com');

    const compiled = compiledFor('User');
    expect(compiled.sql).not.toContain('as "id"');
    expect(compiled.sql).toContain('"t0"."id"');
  });

  it('answers two callers of the same row differently', async () => {
    const mine = await gql(
      '{ users(where: { email: { equals: "ada@example.com" } }) { email phone } }',
      'ada@example.com',
    );
    const theirs = await gql(
      '{ users(where: { email: { equals: "ada@example.com" } }) { email phone } }',
      'mod@example.com',
    );

    expect(mine.body.data.users).toEqual([{ email: 'ada@example.com', phone: '+44-555-0200' }]);
    expect(theirs.body.data.users).toEqual([{ email: 'ada@example.com', phone: null }]);
    expect(events.filter((event) => event.model === 'User').map((event) => event.masked)).toEqual([
      ['phone'],
      ['phone'],
    ]);
  });

  it('answers exactly what the uncompiled path answers', async () => {
    const compiled = await gql(
      '{ users(orderBy: [{ email: asc }]) { email phone } }',
      'ada@example.com',
    );
    const uncompiled = await prisma.forContext(ctxFor('ada@example.com')).user.findMany({
      orderBy: { email: 'asc' },
      select: { email: true, phone: true },
    });

    expect(uncompiled).toEqual(compiled.body.data.users);
    expect(compiledFor('User').masked).toEqual(['phone']);
  });

  it('keeps the hydration a field it will not mask in SQL still needs', async () => {
    await seed(prisma);
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    await prisma.readingSession.create({ data: { postId: adaPost.id, progress: 10, note: 'ada note' } });

    const response = await gql('{ readingSessions { progress note } }', 'ada@example.com');

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.readingSessions).toEqual([{ progress: 10, note: 'ada note' }]);
    const compiled = compiledFor('ReadingSession');
    expect(compiled.masked).toEqual(['note']);
    expect(compiled.deferred).toEqual([
      expect.objectContaining({ field: 'progress', reason: 'decoder' }),
    ]);
    expect(compiled.sql).toContain('as "progress"');
    expect(compiled.sql).toContain('else null end as "note"');
  });

  it('masks a field of a relation the database reaches in one correlated statement', async () => {
    const response = await gql(
      '{ posts(orderBy: [{ title: asc }]) { title author { email phone } } }',
      'ada@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'Draft post', author: { email: 'roy@example.com', phone: null } },
      { title: 'First post', author: { email: 'roy@example.com', phone: null } },
      { title: 'Memory systems', author: { email: 'ada@example.com', phone: '+44-555-0200' } },
    ]);
    const compiled = compiledFor('Post');
    expect(compiled.batched).toEqual([]);
    expect(compiled.statements).toHaveLength(1);
    expect(compiled.masked).toEqual(['author.phone']);
    expect(compiled.deferred).toEqual([]);
    expect(compiled.sql).toContain('else null end as "phone"');
  });

  it('masks a field of a relation the database reaches in a second statement', async () => {
    await seed(prisma);
    const modId = (await prisma.user.findUniqueOrThrow({ where: { email: 'mod@example.com' } })).id;
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    const modPost = await prisma.post.create({ data: { title: 'Mod post', authorId: modId } });
    await prisma.readingSession.create({ data: { postId: adaPost.id, progress: 10, note: 'ada note' } });
    await prisma.readingSession.create({ data: { postId: modPost.id, progress: 20, note: 'mod note' } });

    const response = await gql(
      '{ posts(orderBy: [{ title: asc }]) { title readingSessions { progress note } } }',
      'mod@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([
      { title: 'Draft post', readingSessions: [] },
      { title: 'First post', readingSessions: [] },
      { title: 'Memory systems', readingSessions: [{ progress: 10, note: null }] },
      { title: 'Mod post', readingSessions: [{ progress: 20, note: 'mod note' }] },
    ]);
    const compiled = compiledFor('Post');
    expect(compiled.batched).toEqual(['readingSessions']);
    expect(compiled.statements).toHaveLength(2);
    expect(compiled.masked).toEqual(['readingSessions.note']);
    expect(compiled.sql).not.toContain('case when');
    expect(compiled.statements![1]).toContain('else null end as "note"');
  });

  it('answers a mask two relations down exactly as the uncompiled path answers it', async () => {
    await seed(prisma);
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    await prisma.readingSession.create({ data: { postId: adaPost.id, progress: 10, note: 'ada note' } });

    const response = await gql(
      '{ readingSessions { progress post { title author { email phone } } } }',
      'ada@example.com',
    );
    const uncompiled = await prisma.forContext(ctxFor('ada@example.com')).readingSession.findMany({
      select: {
        progress: true,
        post: { select: { title: true, author: { select: { email: true, phone: true } } } },
      },
    });

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.readingSessions).toEqual(uncompiled);
    expect(response.body.data.readingSessions).toEqual([
      {
        progress: 10,
        post: {
          title: 'Memory systems',
          author: { email: 'ada@example.com', phone: '+44-555-0200' },
        },
      },
    ]);
    const compiled = compiledFor('ReadingSession');
    expect(compiled.masked).toEqual(['post.author.phone']);
    expect(compiled.deferred).toEqual([
      expect.objectContaining({ path: 'the read', field: 'progress', reason: 'decoder' }),
    ]);
  });

  it('renders every mask against the aliases the dropped hydration leaves behind', async () => {
    await seed(prisma);
    const modId = (await prisma.user.findUniqueOrThrow({ where: { email: 'mod@example.com' } })).id;
    const modPost = await prisma.post.create({ data: { title: 'Alias post', authorId: modId } });
    await prisma.readingSession.create({ data: { postId: modPost.id, progress: 30, note: 'mod note' } });

    const response = await gql(
      '{ posts(orderBy: [{ title: asc }]) { title readingSessions { note } author { email phone } } }',
      'mod@example.com',
    );
    const uncompiled = await prisma.forContext(ctxFor('mod@example.com')).post.findMany({
      orderBy: { title: 'asc' },
      select: {
        title: true,
        readingSessions: { select: { note: true } },
        author: { select: { email: true, phone: true } },
      },
    });

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual(uncompiled);
    const compiled = compiledFor('Post');
    expect(compiled.masked).toEqual(['readingSessions.note', 'author.phone']);
    expect(compiled.sql).not.toContain('as "post"');
    for (const statement of compiled.statements ?? []) {
      const declared = [...statement.matchAll(/as "(t\d+)"/g)].map((match) => match[1]);
      const referenced = [...statement.matchAll(/"(t\d+)"\./g)].map((match) => match[1]);
      for (const alias of new Set(referenced)) {
        expect(declared).toContain(alias);
      }
    }
  });

  it('masks a field whose condition hops a relation', async () => {
    await seed(prisma);
    const modId = (await prisma.user.findUniqueOrThrow({ where: { email: 'mod@example.com' } })).id;
    const adaPost = await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } });
    const modPost = await prisma.post.create({ data: { title: 'Mod post', authorId: modId } });
    await prisma.readingSession.create({ data: { postId: adaPost.id, progress: 10, note: 'ada note' } });
    await prisma.readingSession.create({ data: { postId: modPost.id, progress: 20, note: 'mod note' } });

    const response = await gql(
      '{ readingSessions(orderBy: [{ progress: asc }]) { progress note } }',
      'mod@example.com',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.readingSessions).toEqual([
      { progress: 10, note: null },
      { progress: 20, note: 'mod note' },
    ]);
    const compiled = compiledFor('ReadingSession');
    expect(compiled.masked).toEqual(['note']);
    expect(compiled.sql).toContain('EXISTS (SELECT 1 FROM "Post"');
    expect(compiled.sql).not.toContain('as "post"');
    expect(compiled.statements).toHaveLength(1);
  });
});

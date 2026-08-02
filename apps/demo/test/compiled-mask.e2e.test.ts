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

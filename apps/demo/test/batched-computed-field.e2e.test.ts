import { INestApplication } from '@nestjs/common';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GOLEM_ENGINE, GolemEngine } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

const AUTHORS = ['nina', 'omar', 'pia', 'quinn', 'rafa', 'sena', 'tomas', 'uma'];

describe('batched computed fields (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;
  let groupBy: jest.SpyInstance;
  let count: jest.SpyInstance;
  let socket: Client;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    engine = app.get(GOLEM_ENGINE);
    await app.listen(0);
    const address = app.getHttpServer().address();
    socket = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
    for (const author of AUTHORS) {
      await prisma.user.create({
        data: {
          email: `${author}@example.com`,
          name: author,
          posts: {
            create: [
              { title: `${author} published`, published: true },
              { title: `${author} draft`, published: false },
            ],
          },
        },
      });
    }
  });

  beforeEach(() => {
    groupBy = jest.spyOn(engine, 'groupBy');
    count = jest.spyOn(engine, 'count');
  });

  afterEach(() => {
    groupBy.mockRestore();
    count.mockRestore();
  });

  afterAll(async () => {
    await socket.dispose();
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, token = 'token-roy@example.com') {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send({ query });
  }

  it('issues one query for the whole page instead of one per row', async () => {
    const perRow = await gql('{ users(orderBy: [{ email: asc }]) { email postCountPerRow } }');
    expect(perRow.body.errors).toBeUndefined();
    const rows = perRow.body.data.users as Array<{ email: string; postCountPerRow: number }>;
    expect(rows).toHaveLength(12);
    expect(count).toHaveBeenCalledTimes(12);

    count.mockClear();
    groupBy.mockClear();

    const batched = await gql('{ users(orderBy: [{ email: asc }]) { email postCount } }');
    expect(batched.body.errors).toBeUndefined();
    expect(groupBy).toHaveBeenCalledTimes(1);
    expect(count).toHaveBeenCalledTimes(0);
    expect(batched.body.data.users).toEqual(
      rows.map((row) => ({ email: row.email, postCount: row.postCountPerRow })),
    );
  });

  it('gives every row its own answer', async () => {
    const response = await gql(
      '{ users(where: { email: { in: ["roy@example.com", "ada@example.com", "guest@example.com", "nina@example.com"] } }, orderBy: [{ email: asc }]) { email postCount } }',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', postCount: 1 },
      { email: 'guest@example.com', postCount: 0 },
      { email: 'nina@example.com', postCount: 2 },
      { email: 'roy@example.com', postCount: 2 },
    ]);
    expect(groupBy).toHaveBeenCalledTimes(1);
  });

  it('does not let one request read another request\'s batch', async () => {
    const query =
      '{ users(where: { email: { in: ["roy@example.com", "nina@example.com"] } }, orderBy: [{ email: asc }]) { email postCount } }';

    const [privileged, restricted] = await Promise.all([
      gql(query, 'token-roy@example.com'),
      gql(query, 'token-guest@example.com'),
    ]);

    expect(privileged.body.errors).toBeUndefined();
    expect(restricted.body.errors).toBeUndefined();
    expect(privileged.body.data.users).toEqual([
      { email: 'nina@example.com', postCount: 2 },
      { email: 'roy@example.com', postCount: 2 },
    ]);
    expect(restricted.body.data.users).toEqual([
      { email: 'nina@example.com', postCount: 1 },
      { email: 'roy@example.com', postCount: 1 },
    ]);
    expect(groupBy).toHaveBeenCalledTimes(2);
  });

  it('batches each set of field arguments on its own', async () => {
    const response = await gql(`{
      users(where: { email: { in: ["roy@example.com", "nina@example.com"] } }, orderBy: [{ email: asc }]) {
        email
        live: postCountWhere(published: true)
        drafts: postCountWhere(published: false)
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'nina@example.com', live: 1, drafts: 1 },
      { email: 'roy@example.com', live: 1, drafts: 1 },
    ]);
    expect(groupBy).toHaveBeenCalledTimes(2);
  });

  it('fails every row when the batch fails', async () => {
    groupBy.mockRejectedValueOnce(new Error('grouping unavailable'));

    const response = await gql(
      '{ users(orderBy: [{ email: asc }]) { email drafts: postCountWhere(published: false) } }',
    );

    const errors = response.body.errors as Array<{ message: string; path: unknown[] }>;
    expect(errors).toHaveLength(12);
    for (const error of errors) {
      expect(error.message).toBe('grouping unavailable');
      expect(error.path[2]).toBe('drafts');
    }
    expect(new Set(errors.map((error) => error.path[1])).size).toBe(12);
    for (const user of response.body.data.users as Array<{ drafts: number | null }>) {
      expect(user.drafts).toBeNull();
    }
    expect(groupBy).toHaveBeenCalledTimes(1);
  });

  it('reads what the same mutation wrote a field earlier', async () => {
    await prisma.user.create({
      data: {
        email: 'stale@example.com',
        name: 'Stale',
        posts: {
          create: [
            { title: 'stale published', published: true },
            { title: 'stale draft', published: false },
          ],
        },
      },
    });

    const response = await gql(`mutation {
      before: updateUser(where: { email: "stale@example.com" }, data: { name: "First" }) { postCount }
      made: createPost(data: { title: "mid-request", author: { connect: { email: "stale@example.com" } } }) { author { postCount } }
      after: updateUser(where: { email: "stale@example.com" }, data: { name: "Second" }) { postCount }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.before.postCount).toBe(2);
    expect(response.body.data.made.author.postCount).toBe(3);
    expect(response.body.data.after.postCount).toBe(3);
    expect(groupBy).toHaveBeenCalledTimes(3);

    const follow = await gql(
      '{ users(where: { email: { equals: "stale@example.com" } }) { postCount } }',
    );
    expect(follow.body.data.users).toEqual([{ postCount: 3 }]);
  });

  it('recounts on every subscription event rather than holding the first answer', async () => {
    const events: any[] = [];
    const errors: unknown[] = [];
    const unsubscribe = socket.subscribe(
      { query: 'subscription { userEvents { type entity { email postCount } } }' },
      {
        next: (value) => events.push(value),
        error: (error) => errors.push(error),
        complete: () => undefined,
      },
    );
    await sleep(400);

    const subject = await prisma.user.findUniqueOrThrow({ where: { email: 'nina@example.com' } });
    await gql(`mutation { updateUser(where: { email: "nina@example.com" }, data: { name: "Nina" }) { id } }`);
    await sleep(400);

    await prisma.post.create({
      data: { title: 'nina second published', published: true, authorId: subject.id },
    });
    await gql(`mutation { updateUser(where: { email: "nina@example.com" }, data: { name: "Nina II" }) { id } }`);
    await sleep(400);
    unsubscribe();

    expect(errors).toEqual([]);
    expect(events.map((event) => event.data.userEvents.entity)).toEqual([
      { email: 'nina@example.com', postCount: 2 },
      { email: 'nina@example.com', postCount: 3 },
    ]);
    expect(groupBy).toHaveBeenCalledTimes(2);
  });

  it('leaves a pure computed field resolving from the row it was given', async () => {
    const response = await gql(
      '{ users(where: { email: { equals: "roy@example.com" } }) { emailDomain displayName } }',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { emailDomain: 'example.com', displayName: 'Roy' },
    ]);
    expect(groupBy).toHaveBeenCalledTimes(0);
    expect(count).toHaveBeenCalledTimes(0);
  });
});

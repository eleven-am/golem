import { INestApplication } from '@nestjs/common';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe('golem subscriptions (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let client: Client;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename, {
      golem: { postTagSubscriptions: true },
    });
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
    const address = app.getHttpServer().address();
    client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
  });

  afterAll(async () => {
    await client.dispose();
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', 'token-roy@example.com').send({ query }).expect(200);
  }

  function collect(query: string) {
    const events: any[] = [];
    const errors: unknown[] = [];
    const unsubscribe = client.subscribe(
      { query },
      {
        next: (value) => events.push(value),
        error: (error) => errors.push(error),
        complete: () => undefined,
      },
    );
    return { events, errors, unsubscribe };
  }

  it('delivers created events with the subscriber selection including relations', async () => {
    const sub = collect('subscription { userEvents { type id entity { email posts { title } } } }');
    await sleep(400);

    await gql(`mutation {
      createUser(data: {
        email: "sub@example.com"
        posts: { create: [{ title: "sub post" }] }
      }) { id }
    }`);
    await sleep(400);
    sub.unsubscribe();

    expect(sub.errors).toEqual([]);
    expect(sub.events).toHaveLength(1);
    const event = sub.events[0].data.userEvents;
    expect(event.type).toBe('CREATED');
    expect(event.entity).toEqual({ email: 'sub@example.com', posts: [{ title: 'sub post' }] });
  });

  it('applies where filters in the database and skips non-matching events', async () => {
    const sub = collect(
      'subscription { postEvents(where: { published: { equals: true } }) { type entity { title } } }',
    );
    await sleep(400);

    await gql(`mutation {
      createPost(data: { title: "invisible draft", author: { connect: { email: "sub@example.com" } } }) { id }
    }`);
    await gql(`mutation {
      createPost(data: { title: "visible post", published: true, author: { connect: { email: "sub@example.com" } } }) { id }
    }`);
    await sleep(400);
    sub.unsubscribe();

    expect(sub.errors).toEqual([]);
    expect(sub.events).toHaveLength(1);
    expect(sub.events[0].data.postEvents.entity).toEqual({ title: 'visible post' });
  });

  it('suppresses deleted events for filtered subscribers when the filter cannot be re-evaluated', async () => {
    const sub = collect(
      'subscription { userEvents(where: { email: { contains: "nobody" } }) { type id entity { email } } }',
    );
    await sleep(400);

    await gql('mutation { createUser(data: { email: "victim@example.com" }) { id } }');
    await gql(
      'mutation { deleteUser(where: { email: "victim@example.com" }) { id } }',
    );
    await sleep(400);
    sub.unsubscribe();

    expect(sub.errors).toEqual([]);
    expect(sub.events).toEqual([]);
  });

  it('does not disclose deleted row ids outside the subscriber ability', async () => {
    const address = app.getHttpServer().address();
    const guestClient = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-guest@example.com' },
    });
    const events: any[] = [];
    const unsubscribe = guestClient.subscribe(
      { query: 'subscription { postEvents { type id } }' },
      { next: (value) => events.push(value), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const hidden = await prisma.post.create({
      data: { title: 'deleted secret', published: false, author: { connect: { email: 'roy@example.com' } } },
    });
    const visible = await prisma.post.create({
      data: { title: 'deleted public', published: true, author: { connect: { email: 'roy@example.com' } } },
    });
    await sleep(200);
    events.length = 0;
    await prisma.post.delete({ where: { id: hidden.id } });
    await prisma.post.delete({ where: { id: visible.id } });
    await sleep(400);
    unsubscribe();
    await guestClient.dispose();

    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents).toEqual({ type: 'DELETED', id: visible.id });
  });

  it('executes composite-primary-key CRUD through the generated GraphQL schema', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const tag = await prisma.tag.findFirstOrThrow({ orderBy: { id: 'asc' } });
    await prisma.postTag.deleteMany({ where: { postId: post.id, tagId: tag.id } });
    const where = `postId_tagId: { postId: "${post.id}", tagId: "${tag.id}" }`;

    const created = await gql(`mutation {
      createPostTag(data: {
        post: { connect: { id: "${post.id}" } }
        tag: { connect: { id: "${tag.id}" } }
      }) { postId tagId }
    }`);
    expect(created.body.errors).toBeUndefined();
    expect(created.body.data.createPostTag).toEqual({ postId: post.id, tagId: tag.id });

    const found = await gql(`query {
      postTag(where: { ${where} }) { postId tagId }
    }`);
    expect(found.body.errors).toBeUndefined();
    expect(found.body.data.postTag).toEqual({ postId: post.id, tagId: tag.id });

    const updated = await gql(`mutation {
      updatePostTag(
        where: { ${where} }
        data: { addedAt: "2026-02-01T00:00:00.000Z" }
      ) { addedAt }
    }`);
    expect(updated.body.errors).toBeUndefined();
    expect(updated.body.data.updatePostTag.addedAt).toBe('2026-02-01T00:00:00.000Z');

    const upserted = await gql(`mutation {
      upsertPostTag(
        where: { ${where} }
        create: {
          post: { connect: { id: "${post.id}" } }
          tag: { connect: { id: "${tag.id}" } }
        }
        update: { addedAt: "2026-03-01T00:00:00.000Z" }
      ) { addedAt }
    }`);
    expect(upserted.body.errors).toBeUndefined();
    expect(upserted.body.data.upsertPostTag.addedAt).toBe('2026-03-01T00:00:00.000Z');

    const deleted = await gql(`mutation {
      deletePostTag(where: { ${where} }) { postId tagId }
    }`);
    expect(deleted.body.errors).toBeUndefined();
    expect(deleted.body.data.deletePostTag).toEqual({ postId: post.id, tagId: tag.id });
    await expect(prisma.postTag.findUnique({
      where: { postId_tagId: { postId: post.id, tagId: tag.id } },
    })).resolves.toBeNull();
  });

  it('emits bounded deterministic per-row events for batch mutations', async () => {
    const first = await gql(`mutation {
      createPost(data: { title: "batch-a", author: { connect: { email: "sub@example.com" } } }) { id }
    }`);
    const second = await gql(`mutation {
      createPost(data: { title: "batch-b", author: { connect: { email: "sub@example.com" } } }) { id }
    }`);
    const ids = [first.body.data.createPost.id, second.body.data.createPost.id].sort();

    const sub = collect('subscription { postEvents { type id } }');
    await sleep(400);

    const updated = await gql(`mutation {
      updateManyPosts(where: { title: { startsWith: "batch-" } }, data: { published: true }) { count }
    }`);
    const deleted = await gql(`mutation {
      deleteManyPosts(where: { title: { startsWith: "batch-" } }) { count }
    }`);
    await sleep(400);
    sub.unsubscribe();

    expect(updated.body.data.updateManyPosts.count).toBe(2);
    expect(deleted.body.data.deleteManyPosts.count).toBe(2);
    expect(sub.errors).toEqual([]);
    expect(sub.events.map((entry) => entry.data.postEvents)).toEqual([
      { type: 'UPDATED', id: ids[0] },
      { type: 'UPDATED', id: ids[1] },
      { type: 'DELETED', id: ids[0] },
      { type: 'DELETED', id: ids[1] },
    ]);
  });

  it('emits ordered composite identities for composite batch mutations', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const tags = await prisma.tag.findMany({ orderBy: { id: 'asc' }, take: 2 });
    await prisma.postTag.createMany({
      data: tags.map((tag) => ({ postId: post.id, tagId: tag.id })),
    });
    const identities = tags
      .map((tag) => ({ postId: post.id, tagId: tag.id }))
      .sort((left, right) => left.tagId.localeCompare(right.tagId));
    const sub = collect(
      'subscription { postTagEvents { type id { postId tagId } } }',
    );
    await sleep(400);

    const updated = await gql(`mutation {
      updateManyPostTags(
        where: { postId: { equals: "${post.id}" } }
        data: { addedAt: "2026-01-01T00:00:00.000Z" }
      ) { count }
    }`);
    const deleted = await gql(`mutation {
      deleteManyPostTags(where: { postId: { equals: "${post.id}" } }) { count }
    }`);
    await sleep(400);
    sub.unsubscribe();

    expect(updated.body.errors).toBeUndefined();
    expect(deleted.body.errors).toBeUndefined();
    expect(updated.body.data.updateManyPostTags.count).toBe(2);
    expect(deleted.body.data.deleteManyPostTags.count).toBe(2);
    expect(sub.events.map((entry) => entry.data.postTagEvents)).toEqual([
      ...identities.map((id) => ({ type: 'UPDATED', id })),
      ...identities.map((id) => ({ type: 'DELETED', id })),
    ]);
  });
});

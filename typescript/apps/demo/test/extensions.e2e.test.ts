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

describe('golem extensions and programmatic engine (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;
  let wsClient: Client;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
    engine = app.get(GOLEM_ENGINE);
    const address = app.getHttpServer().address();
    wsClient = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
  });

  afterAll(async () => {
    await wsClient.dispose();
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', 'token-roy@example.com').send({ query });
  }

  it('serves computed fields resolved from required columns', async () => {
    const response = await gql('{ users(orderBy: [{ email: asc }]) { displayName } }');
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { displayName: 'Ada' },
      { displayName: 'Guest' },
      { displayName: 'Mod' },
      { displayName: 'Roy' },
    ]);

    await prisma.user.update({ where: { email: 'ada@example.com' }, data: { name: null } });
    const fallback = await gql('{ user(where: { email: "ada@example.com" }) { displayName } }');
    expect(fallback.body.data.user.displayName).toBe('ada@example.com');
  });

  it('runs computed fields through Nest parameter decorators, args, pipes, and interceptors', async () => {
    const response = await gql(`{
      user(where: { email: "roy@example.com" }) {
        emailDomain
        requestAuthorization
        greeting(prefix: "hello")
        interceptedName
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.user).toEqual({
      emailDomain: 'example.com',
      requestAuthorization: 'token-roy@example.com',
      greeting: 'HELLO Roy',
      interceptedName: 'Roy!',
    });
  });

  it('runs computed fields through injected Nest guards', async () => {
    const response = await request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .set('x-deny-search', 'true')
      .send({ query: '{ user(where: { email: "roy@example.com" }) { displayName } }' });

    expect(response.body.data.user).toBeNull();
    expect(response.body.errors[0].message).toBe('Forbidden resource');
  });

  it('creates computed-field dependencies once per request', async () => {
    const first = await gql('{ users(take: 2) { requestScope } }');
    const second = await gql('{ users(take: 2) { requestScope } }');

    expect(new Set(first.body.data.users.map((user: { requestScope: number }) => user.requestScope)).size).toBe(1);
    expect(new Set(second.body.data.users.map((user: { requestScope: number }) => user.requestScope)).size).toBe(1);
    expect(first.body.data.users[0].requestScope).not.toBe(second.body.data.users[0].requestScope);
  });

  it('maps Golem errors thrown by computed fields through the Nest filter', async () => {
    const response = await gql('{ user(where: { email: "roy@example.com" }) { rejectComputedField } }');

    expect(response.body.data.user).toBeNull();
    expect(response.body.errors[0]).toMatchObject({
      message: 'computed field rejected',
      extensions: { code: 'BAD_USER_INPUT' },
    });
  });

  it('serves custom queries running through the engine', async () => {
    const response = await gql('{ searchPosts(term: "First") { title published } }');
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.searchPosts).toEqual([{ title: 'First post', published: true }]);
  });

  it('runs custom queries through Nest guards with injected dependencies', async () => {
    const response = await request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .set('x-deny-search', 'true')
      .send({ query: '{ searchPosts(term: "First") { title } }' });

    expect(response.body.data).toBeNull();
    expect(response.body.errors[0].message).toBe('Forbidden resource');
  });

  it('validates custom query args like any generated field', async () => {
    const response = await gql('{ searchPosts { title } }');
    expect(response.body.errors[0].message).toContain('"term" of type "String!" is required');
  });

  it('maps Golem errors thrown through the Nest resolver pipeline', async () => {
    const response = await gql('mutation { rejectCustomOperation }');

    expect(response.body.data).toBeNull();
    expect(response.body.errors[0]).toMatchObject({
      message: 'custom operation rejected',
      extensions: { code: 'BAD_USER_INPUT' },
    });
  });

  it('publishes subscription events from raw prisma writes outside any resolver', async () => {
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const post = await prisma.post.findFirst({ where: { title: 'First post' } });
    await prisma.post.update({
      where: { id: post!.id },
      data: { title: 'First post (worker edit)' },
    });
    await sleep(400);
    unsubscribe();

    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents).toEqual({
      type: 'UPDATED',
      entity: { title: 'First post (worker edit)' },
    });
  });

  it('publishes events from writes through user-chained client extensions', async () => {
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const chained = prisma.$extends({});
    const post = await chained.post.findFirst({ where: { title: 'First post (worker edit)' } });
    await chained.post.update({
      where: { id: post!.id },
      data: { title: 'First post (chained edit)' },
    });
    await sleep(400);
    unsubscribe();

    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents.entity).toEqual({ title: 'First post (chained edit)' });
  });

  it('classifies raw Prisma upsert events by the branch that executed', async () => {
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { userEvents { type entity { email name } } }' },
      { next: (value) => events.push(value), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    await prisma.user.upsert({
      where: { email: 'raw-upsert@example.com' },
      create: { email: 'raw-upsert@example.com', name: 'Created' },
      update: { name: 'Updated' },
    });
    await prisma.user.upsert({
      where: { email: 'raw-upsert@example.com' },
      create: { email: 'raw-upsert@example.com', name: 'Created' },
      update: { name: 'Updated' },
    });
    await sleep(400);
    unsubscribe();

    expect(events.map((event) => event.data.userEvents.type)).toEqual(['CREATED', 'UPDATED']);
    expect(events[1].data.userEvents.entity.name).toBe('Updated');
  });

  it('still publishes events from engine calls through the instrumented client', async () => {
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const post = await prisma.post.findFirst({ where: { title: 'First post (chained edit)' } });
    await engine.update({
      model: 'Post',
      where: { id: post!.id },
      data: { title: 'First post (engine edit)' },
    });
    await sleep(400);
    unsubscribe();

    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents.entity).toEqual({ title: 'First post (engine edit)' });
  });
});

import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GOLEM_ENGINE, GolemEngine } from '@eleven-am/golem';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe('golem extensions and programmatic engine (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;
  let wsClient: Client;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    await app.listen(0);
    prisma = app.get(GolemPrismaService);
    engine = app.get(GOLEM_ENGINE);
    await seed(prisma);
    const address = app.getHttpServer().address();
    wsClient = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
  });

  afterAll(async () => {
    await wsClient.dispose();
    await app.close();
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

  it('serves custom queries running through the engine', async () => {
    const response = await gql('{ searchPosts(term: "First") { title published } }');
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.searchPosts).toEqual([{ title: 'First post', published: true }]);
  });

  it('validates custom query args like any generated field', async () => {
    const response = await gql('{ searchPosts { title } }');
    expect(response.body.errors[0].message).toContain('"term" of type "String!" is required');
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

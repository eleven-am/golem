import { INestApplication } from '@nestjs/common';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GolemNotFoundError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('predicted-row checks (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let adaId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
    const ada = await prisma.user.findUnique({ where: { email: 'ada@example.com' } });
    adaId = ada!.id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, token: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send({ query });
  }

  it('allows creates whose candidate matches the ability', async () => {
    const response = await gql(
      `mutation { createPost(data: { title: "my weekend", author: { connect: { id: "${adaId}" } } }) { title type } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createPost.type).toBe('PERSONAL');
  });

  it('rejects creates outside the ability zone', async () => {
    const response = await gql(
      `mutation { createPost(data: { title: "hot take", type: EDITORIAL, author: { connect: { id: "${adaId}" } } }) { id } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
    const none = await prisma.post.findFirst({ where: { title: 'hot take' } });
    expect(none).toBeNull();
  });

  it('rejects impersonation through connect translation', async () => {
    const roy = await prisma.user.findUnique({ where: { email: 'roy@example.com' } });
    const response = await gql(
      `mutation { createPost(data: { title: "as roy", author: { connect: { id: "${roy!.id}" } } }) { id } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
  });

  it('rejects nested smuggling of out-of-zone creates', async () => {
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).user.update({
        where: { id: adaId },
        data: { posts: { create: [{ title: 'sneaky editorial', type: 'EDITORIAL' }] } },
      }),
    ).rejects.toThrow('Cannot create Post');
    const none = await prisma.post.findFirst({ where: { title: 'sneaky editorial' } });
    expect(none).toBeNull();
  });

  it('rejects an in-place nested update that moves an existing child out of policy', async () => {
    const adasPost = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).user.update({
        where: { id: adaId },
        data: {
          posts: {
            update: { where: { id: adasPost!.id }, data: { type: 'EDITORIAL' } },
          },
        },
      }),
    ).rejects.toThrow('Cannot update Post');
    expect((await prisma.post.findUnique({ where: { id: adasPost!.id } }))!.type).toBe('PERSONAL');
  });

  it('applies nested field-read policy to relation shorthand through forContext', async () => {
    await expect(
      prisma.forContext(ctxFor('guest@example.com')).post.findMany({
        where: { published: true },
        select: { author: true },
      }),
    ).rejects.toThrow('Cannot read field "phone" on User');
  });

  it('rejects updates that move a row out of the ability zone', async () => {
    const post = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    const response = await gql(
      `mutation { updatePost(where: { id: "${post!.id}" }, data: { type: EDITORIAL }) { id } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
    const untouched = await prisma.post.findUnique({ where: { id: post!.id } });
    expect(untouched!.type).toBe('PERSONAL');
  });

  it('still allows in-zone updates', async () => {
    const post = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    const response = await gql(
      `mutation { updatePost(where: { id: "${post!.id}" }, data: { title: "Memory systems II" }) { title } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.updatePost.title).toBe('Memory systems II');
  });

  it('upserts through forContext with truthful events per branch', async () => {
    const address = app.getHttpServer().address();
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { userEvents { type entity { email name } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const roy = ctxFor('roy@example.com');
    await prisma.forContext(roy).user.upsert({
      where: { email: 'upserted@example.com' },
      create: { email: 'upserted@example.com', name: 'First' },
      update: { name: 'Second' },
    });
    await prisma.forContext(roy).user.upsert({
      where: { email: 'upserted@example.com' },
      create: { email: 'upserted@example.com', name: 'First' },
      update: { name: 'Second' },
    });
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(events.map((e) => e.data.userEvents.type)).toEqual(['CREATED', 'UPDATED']);
    expect(events[1].data.userEvents.entity.name).toBe('Second');
  });

  it('publishes no events for denied writes', async () => {
    const address = app.getHttpServer().address();
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type id } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const denied = await gql(
      `mutation { createPost(data: { title: "phantom", type: EDITORIAL, author: { connect: { id: "${adaId}" } } }) { id } }`,
      'token-ada@example.com',
    );
    expect(denied.body.errors[0].extensions.code).toBe('FORBIDDEN');
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(events).toEqual([]);
    const none = await prisma.post.findFirst({ where: { title: 'phantom' } });
    expect(none).toBeNull();
  });

  it('hides inaccessible rows from the upsert update branch as NOT_FOUND', async () => {
    const roy = await prisma.user.findUnique({ where: { email: 'roy@example.com' } });
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).user.upsert({
        where: { id: roy!.id },
        create: { email: 'never@example.com' },
        update: { name: 'hijacked' },
      }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);
    const untouched = await prisma.user.findUnique({ where: { email: 'roy@example.com' } });
    expect(untouched!.name).not.toBe('hijacked');
  });
});

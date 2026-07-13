import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe('golem authorization (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    await app.listen(0);
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
  });

  function gql(query: string, token?: string) {
    const req = request(app.getHttpServer()).post('/graphql');
    if (token) {
      req.set('authorization', token);
    }
    return req.send({ query });
  }

  it('rejects unauthenticated requests with UNAUTHENTICATED', async () => {
    const response = await gql('{ users { id } }');
    expect(response.body.errors[0].extensions.code).toBe('UNAUTHENTICATED');
    expect(response.body.data).toBeNull();
  });

  it('scopes reads with the ability constraint', async () => {
    const response = await gql('{ profiles { bio } }', 'token-ada@example.com');
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.profiles).toEqual([{ bio: 'countess of computing' }]);

    const all = await gql('{ profiles { bio } }', 'token-roy@example.com');
    expect(all.body.data.profiles).toHaveLength(2);
  });

  it('hides other users rows from update as NOT_FOUND without leaking existence', async () => {
    const roysPost = await prisma.post.findFirst({ where: { title: 'First post' } });
    const response = await gql(
      `mutation { updatePost(where: { id: "${roysPost!.id}" }, data: { title: "stolen" }) { id } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('NOT_FOUND');
    const untouched = await prisma.post.findUnique({ where: { id: roysPost!.id } });
    expect(untouched!.title).toBe('First post');
  });

  it('updates own rows through the same constrained path', async () => {
    const adasPost = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    const response = await gql(
      `mutation { updatePost(where: { id: "${adasPost!.id}" }, data: { title: "Memory systems v2" }) { title } }`,
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.updatePost.title).toBe('Memory systems v2');
  });

  it('returns FORBIDDEN for actions the ability denies at model level', async () => {
    const response = await gql(
      'mutation { createUser(data: { email: "new@example.com" }) { id } }',
      'token-ada@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
  });

  it('gates nested writes against the target model', async () => {
    const response = await gql(
      `mutation { createPost(data: { title: "sneaky", author: { connect: { email: "guest@example.com" } } }) { id } }`,
      'token-guest@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
    const none = await prisma.post.findFirst({ where: { title: 'sneaky' } });
    expect(none).toBeNull();
  });

  it('filters subscription events by ability and reacts to revocation without reconnecting', async () => {
    const address = app.getHttpServer().address();
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-ada@example.com' },
    });
    const events: any[] = [];
    const errors: unknown[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title } } }' },
      { next: (v) => events.push(v), error: (e) => errors.push(e), complete: () => undefined },
    );
    await sleep(400);

    await prisma.post.create({
      data: { title: 'visible to ada', author: { connect: { email: 'roy@example.com' } } },
    });
    await sleep(400);
    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents.entity).toEqual({ title: 'visible to ada' });

    await prisma.user.update({
      where: { email: 'ada@example.com' },
      data: { name: 'REVOKED' },
    });
    await prisma.post.create({
      data: { title: 'hidden from ada', author: { connect: { email: 'roy@example.com' } } },
    });
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(events).toHaveLength(1);
    expect(errors).toEqual([]);
    await prisma.user.update({ where: { email: 'ada@example.com' }, data: { name: 'Ada' } });
  });
});

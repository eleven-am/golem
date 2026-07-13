import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { Client, createClient } from 'graphql-ws';
import WebSocket from 'ws';
import { GolemNotFoundError, GolemUnauthorizedError } from '@eleven-am/golem';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('forContext facade (e2e)', () => {
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

  it('scopes reads with the caller ability', async () => {
    const asAda = await prisma.forContext(ctxFor('ada@example.com')).profile.findMany({
      select: { bio: true },
    });
    expect(asAda).toEqual([{ bio: 'countess of computing' }]);

    const asRoy = await prisma.forContext(ctxFor('roy@example.com')).profile.findMany({
      select: { bio: true },
    });
    expect(asRoy).toHaveLength(2);
  });

  it('hides other users rows from writes as NOT_FOUND', async () => {
    const roysPost = await prisma.post.findFirst({ where: { title: 'First post' } });
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).post.update({
        where: { id: roysPost!.id },
        data: { title: 'stolen' },
      }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);
    const untouched = await prisma.post.findUnique({ where: { id: roysPost!.id } });
    expect(untouched!.title).toBe('First post');
  });

  it('rejects unauthenticated contexts', async () => {
    await expect(
      prisma.forContext({ req: { headers: {} } }).post.findMany({}),
    ).rejects.toBeInstanceOf(GolemUnauthorizedError);
  });

  it('runs hooks on facade writes', async () => {
    const created = await prisma.forContext(ctxFor('roy@example.com')).user.create({
      data: { email: 'Facade-Hook@Example.com' },
    });
    expect(created.email).toBe('facade-hook@example.com');
    expect(created.apiKey).toBe('key_facade-hook@example.com');
  });

  it('publishes subscription events from facade writes', async () => {
    const address = app.getHttpServer().address();
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    const adasPost = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    await prisma.forContext(ctxFor('ada@example.com')).post.update({
      where: { id: adasPost!.id },
      data: { title: 'Memory systems (facade edit)' },
    });
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents.entity).toEqual({ title: 'Memory systems (facade edit)' });
  });
});

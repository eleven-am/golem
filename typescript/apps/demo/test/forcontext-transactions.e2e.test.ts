import { INestApplication } from '@nestjs/common';
import {
  GOLEM_EVENT_BUS,
  GolemForbiddenError,
  PubSubEventBus,
  eventTopic,
} from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

function wait(ms: number): Promise<'waiting'> {
  return new Promise((resolve) => setTimeout(() => resolve('waiting'), ms));
}

describe('forContext interactive transactions (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let eventBus: PubSubEventBus;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    eventBus = app.get(GOLEM_EVENT_BUS);
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  it('commits multi-model writes atomically', async () => {
    await prisma.forContext(ctxFor('roy@example.com')).$transaction(async (tx) => {
      const author = await tx.user.create({
        data: { email: 'tx-committed@example.com', name: 'TX Author' },
      });
      await tx.post.create({
        data: { title: 'tx committed post', author: { connect: { id: author.id } } },
      });
    });

    expect(
      await prisma.user.findUnique({ where: { email: 'tx-committed@example.com' } }),
    ).not.toBeNull();
    expect(await prisma.post.findFirst({ where: { title: 'tx committed post' } })).not.toBeNull();
  });

  it('rolls back an earlier write when a later policy denial aborts the transaction', async () => {
    const ada = await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } });
    const roy = await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } });

    await expect(
      prisma.forContext(ctxFor('ada@example.com')).$transaction(async (tx) => {
        await tx.post.create({
          data: {
            title: 'ada rollback candidate',
            type: 'PERSONAL',
            author: { connect: { id: ada.id } },
          },
        });
        await tx.user.delete({ where: { id: roy.id } });
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);

    expect(await prisma.post.findMany({ where: { title: 'ada rollback candidate' } })).toHaveLength(
      0,
    );
    expect(await prisma.user.findUnique({ where: { id: roy.id } })).not.toBeNull();
  });

  it('publishes a write from inside the transaction only after it commits', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await prisma.forContext(ctxFor('roy@example.com')).$transaction(async (tx) => {
      await tx.post.update({
        where: { id: post.id },
        data: { title: 'ctx tx committed edit' },
      });
      await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)])).resolves.toBe(
        'waiting',
      );
    });

    await expect(nextEvent).resolves.toMatchObject({
      value: { type: 'UPDATED', model: 'Post', id: post.id },
      done: false,
    });
    await events.return?.();
  });

  it('publishes nothing when the transaction rolls back', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(
      prisma.forContext(ctxFor('roy@example.com')).$transaction(async (tx) => {
        await tx.post.update({
          where: { id: post.id },
          data: { title: 'ctx tx rolled-back edit' },
        });
        throw new Error('rollback');
      }),
    ).rejects.toThrow('rollback');

    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)])).resolves.toBe(
      'waiting',
    );
    await expect(prisma.post.findUnique({ where: { id: post.id } })).resolves.toMatchObject({
      title: 'First post',
    });
    await events.return?.();
  });

  it('emits ordered batch events from the policy-aware forContext surface', async () => {
    const selected = await prisma.post.findMany({
      where: { title: { in: ['First post', 'Draft post'] } },
      select: { id: true },
      orderBy: { id: 'asc' },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const received = selected.map(() => events.next());

    await expect(
      prisma.forContext(ctxFor('roy@example.com')).post.updateMany({
        where: { id: { in: selected.map(({ id }) => id) } },
        data: { published: true },
      }),
    ).resolves.toEqual({ count: selected.length });

    await expect(Promise.all(received)).resolves.toEqual(selected.map(({ id }) => ({
      value: { type: 'UPDATED', model: 'Post', id },
      done: false,
    })));
    await events.return?.();
  });

  it('keeps a guarded upsert branch and its event inside the caller-owned transaction', async () => {
    const events = eventBus.iterate(eventTopic('User'));
    const nextEvent = events.next();

    const created = await prisma.forContext(ctxFor('roy@example.com')).$transaction(async (tx) => {
      const result = await tx.user.upsert({
        where: { email: 'ambient-upsert@example.com' },
        create: { email: 'ambient-upsert@example.com', name: 'created' },
        update: { name: 'updated' },
        select: { id: true },
      });
      await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
        .resolves.toBe('waiting');
      return result;
    });

    await expect(nextEvent).resolves.toMatchObject({
      value: { type: 'CREATED', model: 'User', id: created.id },
      done: false,
    });
    await events.return?.();
  });

  it('exposes $transaction but not raw query escapes on the context-bound surface', async () => {
    const scoped = prisma.forContext(ctxFor('roy@example.com'));
    expect(typeof scoped.$transaction).toBe('function');
    // @ts-expect-error raw query access is deliberately absent from the context-bound surface
    scoped.$queryRaw;
    // @ts-expect-error raw execute access is deliberately absent from the context-bound surface
    scoped.$executeRaw;
  });
});

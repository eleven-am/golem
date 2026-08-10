import { INestApplication } from '@nestjs/common';
import {
  GOLEM_EVENT_BUS,
  PubSubEventBus,
  eventTopic,
} from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function wait(ms: number): Promise<'waiting'> {
  return new Promise((resolve) => setTimeout(() => resolve('waiting'), ms));
}

describe('application-owned transaction events (e2e)', () => {
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

  it('publishes intercepted writes only after an interactive transaction commits', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await prisma.$transaction(async (tx) => {
      await tx.post.update({
        where: { id: post.id },
        data: { title: 'committed transaction edit' },
      });
      await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
        .resolves.toBe('waiting');
    });

    await expect(nextEvent).resolves.toMatchObject({
      value: { type: 'UPDATED', model: 'Post', id: post.id },
      done: false,
    });
    await events.return?.();
  });

  it('discards intercepted writes when an interactive transaction rolls back', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(prisma.$transaction(async (tx) => {
      await tx.post.update({
        where: { id: post.id },
        data: { title: 'rolled-back transaction edit' },
      });
      throw new Error('rollback');
    })).rejects.toThrow('rollback');

    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
      .resolves.toBe('waiting');
    await expect(prisma.post.findUnique({ where: { id: post.id } }))
      .resolves.toMatchObject({ title: 'First post' });
    await events.return?.();
  });

  it('discards intercepted writes when a batch transaction rolls back', async () => {
    const post = await prisma.post.findFirstOrThrow({ where: { title: 'First post' } });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(prisma.$transaction([
      prisma.post.update({
        where: { id: post.id },
        data: { title: 'rolled-back batch edit' },
      }),
      prisma.user.create({ data: { email: 'roy@example.com' } }),
    ])).rejects.toThrow();

    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
      .resolves.toBe('waiting');
    await expect(prisma.post.findUnique({ where: { id: post.id } }))
      .resolves.toMatchObject({ title: 'First post' });
    await events.return?.();
  });

  it('emits ordered per-row events for a plain generated-client updateMany', async () => {
    const selected = await prisma.post.findMany({
      where: { title: { in: ['First post', 'Draft post'] } },
      select: { id: true },
      orderBy: { id: 'asc' },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const received = selected.map(() => events.next());

    await expect(prisma.post.updateMany({
      where: { id: { in: selected.map(({ id }) => id) } },
      data: { published: true },
    })).resolves.toEqual({ count: selected.length });

    await expect(Promise.all(received)).resolves.toEqual(selected.map(({ id }) => ({
      value: { type: 'UPDATED', model: 'Post', id },
      done: false,
    })));
    await events.return?.();
  });

  it('keeps interactive batch events buffered until commit', async () => {
    const selected = await prisma.post.findMany({
      where: { title: { in: ['First post', 'Draft post'] } },
      select: { id: true },
      orderBy: { id: 'asc' },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const received = selected.map(() => events.next());

    await prisma.$transaction(async (tx) => {
      await tx.post.updateMany({
        where: { id: { in: selected.map(({ id }) => id) } },
        data: { published: true },
      });
      await expect(Promise.race([received[0].then(() => 'published'), wait(25)]))
        .resolves.toBe('waiting');
    });

    await expect(Promise.all(received)).resolves.toEqual(selected.map(({ id }) => ({
      value: { type: 'UPDATED', model: 'Post', id },
      done: false,
    })));
    await events.return?.();
  });

  it('rolls back deleteMany rows and events together in an interactive transaction', async () => {
    const selected = await prisma.post.findMany({
      where: { title: { in: ['First post', 'Draft post'] } },
      select: { id: true },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(prisma.$transaction(async (tx) => {
      await tx.post.deleteMany({ where: { id: { in: selected.map(({ id }) => id) } } });
      throw new Error('rollback batch delete');
    })).rejects.toThrow('rollback batch delete');

    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
      .resolves.toBe('waiting');
    await expect(prisma.post.count({
      where: { id: { in: selected.map(({ id }) => id) } },
    })).resolves.toBe(selected.length);
    await events.return?.();
  });
});

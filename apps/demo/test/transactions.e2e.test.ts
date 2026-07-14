import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import {
  GOLEM_EVENT_BUS,
  PubSubEventBus,
  eventTopic,
} from '@eleven-am/golem';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function wait(ms: number): Promise<'waiting'> {
  return new Promise((resolve) => setTimeout(() => resolve('waiting'), ms));
}

describe('application-owned transaction events (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let eventBus: PubSubEventBus;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    prisma = app.get(GolemPrismaService);
    eventBus = app.get(GOLEM_EVENT_BUS);
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
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
});

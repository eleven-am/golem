import { INestApplication } from '@nestjs/common';
import { GOLEM_EVENT_BUS, GolemValidationError, PubSubEventBus, eventTopic } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function wait(ms: number): Promise<'waiting'> {
  return new Promise((resolve) => setTimeout(() => resolve('waiting'), ms));
}

describe('batch event limits on SQLite (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let eventBus: PubSubEventBus;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename, {
      seedBeforeBootstrap: true,
      golem: { batchEvents: { maxRows: 1, maxPayloadBytes: 256 } },
    });
    app = context.app;
    prisma = context.prisma;
    eventBus = app.get(GOLEM_EVENT_BUS);
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  it('rejects over-limit updateMany before changing any selected row or publishing', async () => {
    const rows = await prisma.post.findMany({
      where: { title: { in: ['First post', 'Draft post'] } },
      select: { id: true, published: true },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(prisma.post.updateMany({
      where: { id: { in: rows.map(({ id }) => id) } },
      data: { published: true },
    })).rejects.toBeInstanceOf(GolemValidationError);

    expect(await prisma.post.findMany({
      where: { id: { in: rows.map(({ id }) => id) } },
      select: { id: true, published: true },
    })).toEqual(expect.arrayContaining(rows));
    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
      .resolves.toBe('waiting');
    await events.return?.();
  });

  it('rejects an over-limit deletion snapshot payload before deleting or publishing', async () => {
    const author = await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } });
    const row = await prisma.post.create({
      data: { title: `large-${'x'.repeat(512)}`, authorId: author.id },
    });
    const events = eventBus.iterate(eventTopic('Post'));
    const nextEvent = events.next();

    await expect(prisma.post.deleteMany({ where: { id: row.id } }))
      .rejects.toBeInstanceOf(GolemValidationError);

    await expect(prisma.post.findUnique({ where: { id: row.id } })).resolves.not.toBeNull();
    await expect(Promise.race([nextEvent.then(() => 'published'), wait(25)]))
      .resolves.toBe('waiting');
    await events.return?.();
  });

  it('rejects primary-key mutation before issuing updateMany', async () => {
    const row = await prisma.post.findFirstOrThrow();
    await expect(prisma.post.updateMany({
      where: { id: row.id },
      data: { id: `${row.id}-changed` },
    })).rejects.toThrow('Eventful updateMany cannot modify primary key fields on Post');
    await expect(prisma.post.findUnique({ where: { id: row.id } })).resolves.not.toBeNull();
  });
});

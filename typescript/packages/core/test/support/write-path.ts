import { AuthorizationProvider, GolemAction } from '../../src/authorization';
import { DatamodelModel } from '../../src/datamodel';
import { GolemNotFoundError } from '../../src/errors';
import { GolemEventBus, GolemEventPayload } from '../../src/events';
import { GolemEngine } from '../../src/operations';
import { createEventPublisher, GolemBatchDelegate, GolemBatchRuntime } from '../../src/publisher';
import { field } from '../../src/testing';

type Client = Record<string, any>;

export interface WritePathDatabase {
  readonly prisma: Client;
  close(): Promise<void>;
}

const models: DatamodelModel[] = [
  {
    name: 'Thread',
    dbName: 'threads',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'watches', type: 'Watch', kind: 'object', isList: true, relationName: 'ThreadToWatch' }),
    ],
  },
  {
    name: 'Watch',
    dbName: 'watches',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'threadId', type: 'Int', isRequired: false, dbName: 'thread_id' }),
      field({
        name: 'thread', type: 'Thread', kind: 'object', isRequired: false, relationName: 'ThreadToWatch',
        relationFromFields: ['threadId'], relationToFields: ['id'], relationOnDelete: 'SetNull',
      }),
    ],
  },
  {
    name: 'Channel',
    dbName: 'channels',
    fields: [
      field({ name: 'slug', type: 'String', isUnique: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'messages', type: 'Message', kind: 'object', isList: true, relationName: 'ChannelToMessage' }),
      field({ name: 'pins', type: 'Pin', kind: 'object', isList: true, relationName: 'ChannelToPin' }),
    ],
  },
  {
    name: 'Pin',
    dbName: 'pins',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'channelSlug', type: 'String', isRequired: false, dbName: 'channel_slug' }),
      field({
        name: 'channel', type: 'Channel', kind: 'object', isRequired: false, relationName: 'ChannelToPin',
        relationFromFields: ['channelSlug'], relationToFields: ['slug'], relationOnDelete: 'SetNull',
      }),
    ],
  },
  {
    name: 'Message',
    dbName: 'messages',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'channelSlug', type: 'String', dbName: 'channel_slug' }),
      field({
        name: 'channel', type: 'Channel', kind: 'object', relationName: 'ChannelToMessage',
        relationFromFields: ['channelSlug'], relationToFields: ['slug'], relationOnDelete: 'Cascade',
      }),
    ],
  },
];

const READABLE: Record<string, unknown> = {
  Thread: { id: { in: [1] } },
  Watch: { id: { in: [20, 22] } },
  Channel: { slug: { in: ['general'] } },
  Pin: { id: { in: [40, 42] } },
};

function provider(): AuthorizationProvider {
  return {
    authorize: async () => undefined,
    constrain: async (action: GolemAction, model: string) => (action === 'read' ? READABLE[model] ?? {} : {}),
    check: async () => true,
    checkField: async () => true,
  };
}

function runtimeOver(tx: Client, model: string): GolemBatchRuntime {
  const delegate = (name: string) => tx[name.charAt(0).toLowerCase() + name.slice(1)] as GolemBatchDelegate;
  return {
    suppressed: false,
    run: (work) => work(delegate(model), {
      delegate,
      queryRaw: (sql, ...values) => tx.$queryRawUnsafe(sql, ...values),
    }),
  };
}

export function describeWritePath(
  provider_: 'sqlite' | 'postgresql',
  open: () => Promise<WritePathDatabase>,
): void {
  let database: WritePathDatabase;
  let prisma: Client;

  beforeAll(async () => {
    database = await open();
    prisma = database.prisma;
  });

  afterAll(async () => {
    await database?.close();
  });

  beforeEach(async () => {
    await prisma.pin.deleteMany();
    await prisma.watch.deleteMany();
    await prisma.thread.deleteMany();
    await prisma.message.deleteMany();
    await prisma.channel.deleteMany();
    await prisma.thread.createMany({ data: [{ id: 1, title: 'readable' }, { id: 2, title: 'hidden' }] });
    await prisma.watch.createMany({ data: [
      { id: 20, threadId: 1 },
      { id: 21, threadId: 1 },
      { id: 22, threadId: 2 },
    ] });
    await prisma.channel.createMany({ data: [{ slug: 'general', title: 'General' }, { slug: 'quiet', title: 'Quiet' }] });
    await prisma.message.createMany({ data: [
      { id: 30, channelSlug: 'general' },
      { id: 31, channelSlug: 'general' },
      { id: 32, channelSlug: 'quiet' },
    ] });
    await prisma.pin.createMany({ data: [
      { id: 40, channelSlug: 'general' },
      { id: 41, channelSlug: 'general' },
      { id: 42, channelSlug: 'quiet' },
    ] });
  });

  const outcome = (run: () => Promise<unknown>) =>
    run().then(() => 'succeeded', (error: Error) => `${error.constructor.name}: ${error.message}`);

  describe.each([true, false])('linking to a unique-only model with checkWriteResults %s', (checkWriteResults) => {
    const engine = (client: Client = prisma) => new GolemEngine(client, models, {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });
    const ctx = { req: {} };

    it('links a foreign-key scalar only to a readable row, refusing a hidden one exactly as a missing one', async () => {
      const write = (channelSlug: string, id: number) =>
        outcome(() => engine().create({ model: 'Message', data: { id, channelSlug }, context: ctx }));

      await expect(write('general', 33)).resolves.toBe('succeeded');
      const hidden = await write('quiet', 34);
      expect(hidden).toBe(await write('nowhere', 35));
      expect(hidden).toBe('GolemNotFoundError: Channel not found');
      await expect(prisma.message.count({ where: { id: { in: [34, 35] } } })).resolves.toBe(0);
    });

    it('connects only to a readable row', async () => {
      const connect = (slug: string, id: number) =>
        outcome(() => engine().create({ model: 'Pin', data: { id, channel: { connect: { slug } } }, context: ctx }));

      await expect(connect('general', 43)).resolves.toBe('succeeded');
      await expect(connect('quiet', 44)).resolves.toBe('GolemNotFoundError: Channel not found');
      await expect(prisma.pin.count({ where: { id: 44 } })).resolves.toBe(0);
    });

    it('disconnects only from a readable row', async () => {
      await expect(outcome(() => engine().update({
        model: 'Pin', where: { id: 42 }, data: { channel: { disconnect: true } }, context: ctx,
      }))).resolves.toBe('GolemNotFoundError: Channel not found');
      await expect(prisma.pin.findUnique({ where: { id: 42 } })).resolves.toEqual({ id: 42, channelSlug: 'quiet' });

      await expect(outcome(() => engine().update({
        model: 'Pin', where: { id: 40 }, data: { channel: { disconnect: true } }, context: ctx,
      }))).resolves.toBe('succeeded');
      await expect(prisma.pin.findUnique({ where: { id: 40 } })).resolves.toEqual({ id: 40, channelSlug: null });
    });

    it('detaches from a unique-only parent only rows the caller can read', async () => {
      await expect(outcome(() => engine().update({
        model: 'Channel', where: { slug: 'general' }, data: { pins: { set: [] } }, context: ctx,
      }))).resolves.toBe('GolemNotFoundError: Pin not found');
      await expect(prisma.pin.count({ where: { channelSlug: 'general' } })).resolves.toBe(2);
    });

    it('validates the row connectOrCreate actually connected, after a concurrent insert of an unreadable match', async () => {
      let injected = false;
      let probes = 0;
      const raced = new Proxy(prisma, {
        get: (target, property, receiver) => {
          if (property !== 'channel') return Reflect.get(target, property, receiver);
          const channel = target.channel;
          return new Proxy(channel, {
            get: (inner, name, innerReceiver) => {
              if (name !== 'findFirst') return Reflect.get(inner, name, innerReceiver);
              return async (args: unknown) => {
                const found = await inner.findFirst(args);
                probes += 1;
                if (probes === 2) {
                  injected = true;
                  await target.channel.create({ data: { slug: 'late', title: 'someone else' } });
                }
                return found;
              };
            },
          });
        },
      });

      await expect(outcome(() => engine(raced).create({
        model: 'Pin',
        data: { id: 45, channel: { connectOrCreate: { where: { slug: 'late' }, create: { slug: 'late', title: 'mine' } } } },
        context: ctx,
      }))).resolves.toBe('GolemNotFoundError: Channel not found');

      expect(injected).toBe(true);
      await expect(prisma.pin.count({ where: { id: 45 } })).resolves.toBe(0);
      await expect(prisma.channel.findUnique({ where: { slug: 'late' } }))
        .resolves.toEqual({ slug: 'late', title: 'someone else' });
    });
  });

  const watchesOf = async (threadId: number) =>
    (await prisma.watch.findMany({ where: { threadId }, orderBy: { id: 'asc' } })).map((row: { id: number }) => row.id);

  describe.each([true, false])('removing links with checkWriteResults %s', (checkWriteResults) => {
    const engine = () => new GolemEngine(prisma, models, {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });
    const ctx = { req: {} };

    it('refuses a to-many set that would detach a linked row the caller cannot read, changing nothing', async () => {
      const attempt = engine().update({
        model: 'Thread', where: { id: 1 }, data: { title: 'renamed', watches: { set: [] } }, context: ctx,
      });

      await expect(attempt).rejects.toBeInstanceOf(GolemNotFoundError);
      await expect(attempt).rejects.toThrow('Watch not found');
      await expect(watchesOf(1)).resolves.toEqual([20, 21]);
      await expect(prisma.thread.findUnique({ where: { id: 1 } })).resolves.toMatchObject({ title: 'readable' });
    });

    it('refuses a to-one disconnect from a linked row the caller cannot read, changing nothing', async () => {
      await expect(engine().update({
        model: 'Watch', where: { id: 22 }, data: { thread: { disconnect: true } }, context: ctx,
      })).rejects.toThrow('Thread not found');
      await expect(watchesOf(2)).resolves.toEqual([22]);
    });

    it('reports a hidden linked row exactly as it reports a hidden named target', async () => {
      const hiddenCurrent = await engine().update({
        model: 'Watch', where: { id: 22 }, data: { thread: { disconnect: true } }, context: ctx,
      }).catch((error: Error) => `${error.constructor.name}: ${error.message}`);
      const hiddenNamed = await engine().update({
        model: 'Watch', where: { id: 20 }, data: { thread: { connect: { id: 2 } } }, context: ctx,
      }).catch((error: Error) => `${error.constructor.name}: ${error.message}`);

      expect(hiddenCurrent).toBe(hiddenNamed);
    });

    it('still lets the caller detach rows it can read', async () => {
      await prisma.watch.delete({ where: { id: 21 } });

      await engine().update({ model: 'Thread', where: { id: 1 }, data: { watches: { set: [] } }, context: ctx });
      await expect(watchesOf(1)).resolves.toEqual([]);

      await prisma.watch.update({ where: { id: 20 }, data: { threadId: 1 } });
      await engine().update({ model: 'Watch', where: { id: 20 }, data: { thread: { disconnect: true } }, context: ctx });
      await expect(watchesOf(1)).resolves.toEqual([]);
    });

    it('leaves a caller without a context free to detach any row', async () => {
      await engine().update({ model: 'Thread', where: { id: 1 }, data: { watches: { set: [] } } });
      await expect(watchesOf(1)).resolves.toEqual([]);
    });
  });

  describe('deleting a root identified only by a unique field', () => {
    function publisherFor(maxRows?: number) {
      const published: GolemEventPayload[] = [];
      const bus: GolemEventBus = {
        publish: async (_topic, event) => { published.push(event); },
        publishMany: async (_topic, events) => { published.push(...events); },
        iterate: (async function* () {})() as never,
      };
      const publisher = createEventPublisher({
        datamodel: { models, enums: [], provider: provider_ },
        eventBus: bus,
        models: new Set(['Message']),
        ...(maxRows === undefined ? {} : { batch: { maxRows } }),
      });
      return { publisher, published };
    }

    it('emits an event for every dependent it cascades to', async () => {
      const { publisher, published } = publisherFor();

      await prisma.$transaction((tx: Client) => publisher({
        model: 'Channel',
        operation: 'delete',
        args: { where: { slug: 'general' } },
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(tx, 'Channel'),
      }));

      expect(published).toEqual([
        { type: 'DELETED', model: 'Message', id: 30, entity: { id: 30, channelSlug: 'general' } },
        { type: 'DELETED', model: 'Message', id: 31, entity: { id: 31, channelSlug: 'general' } },
      ]);
      await expect(prisma.message.count({ where: { channelSlug: 'general' } })).resolves.toBe(0);
    });

    it('counts its dependents against the touched-row cap', async () => {
      const { publisher, published } = publisherFor(2);

      await expect(prisma.$transaction((tx: Client) => publisher({
        model: 'Channel',
        operation: 'deleteMany',
        args: { where: { slug: 'general' } },
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(tx, 'Channel'),
      }))).rejects.toThrow('Deleting from Channel would touch more than the maximum of 2 rows');

      expect(published).toEqual([]);
      await expect(prisma.channel.count()).resolves.toBe(2);
      await expect(prisma.message.count()).resolves.toBe(3);
    });
  });
}

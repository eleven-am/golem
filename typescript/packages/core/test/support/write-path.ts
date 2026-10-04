import { AuthorizationProvider, GolemAction } from '../../src/authorization';
import { DatamodelModel } from '../../src/datamodel';
import { GolemConflictError, GolemNotFoundError, GolemValidationError } from '../../src/errors';
import { GolemEventBus, GolemEventPayload } from '../../src/events';
import { GolemEngine } from '../../src/operations';
import { createEventPublisher, GolemBatchDelegate, GolemBatchRuntime } from '../../src/publisher';
import { field } from '../../src/testing';
import { golemClient } from './golem-client';

type Client = Record<string, any>;

export interface WritePathDatabase {
  readonly prisma: Client;
  readonly concurrent: Client;
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
      field({ name: 'replies', type: 'Reply', kind: 'object', isList: true, relationName: 'ReplyToThread' }),
    ],
  },
  {
    name: 'Reply',
    dbName: 'replies',
    fields: [
      field({ name: 'id', type: 'Int', isId: true }),
      field({ name: 'threadId', type: 'Int', dbName: 'thread_id' }),
      field({ name: 'body', type: 'String' }),
      field({ name: 'amount', type: 'Decimal', isRequired: false }),
      field({ name: 'postedAt', type: 'DateTime', dbName: 'posted_at' }),
      field({
        name: 'thread', type: 'Thread', kind: 'object', relationName: 'ReplyToThread',
        relationFromFields: ['threadId'], relationToFields: ['id'], relationOnDelete: 'Cascade',
      }),
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

const people: DatamodelModel = {
  name: 'Person',
  dbName: 'people',
  fields: [
    field({ name: 'id', type: 'Int', isId: true }),
    field({ name: 'buddyId', type: 'Int', isRequired: false, dbName: 'buddy_id' }),
    field({
      name: 'buddy', type: 'Person', kind: 'object', isRequired: false, relationName: 'Buddy',
      relationFromFields: ['buddyId'], relationToFields: ['id'], relationOnDelete: 'SetNull',
    }),
    field({ name: 'buddiedBy', type: 'Person', kind: 'object', isList: true, relationName: 'Buddy' }),
  ],
};

const READABLE: Record<string, unknown> = {
  Thread: { title: { not: 'hidden' } },
  Watch: { id: { in: [20, 22] } },
  Channel: { OR: [{ slug: { in: ['general'] } }, { title: 'open' }] },
  Pin: { id: { in: [40, 42] } },
};

function provider(): AuthorizationProvider {
  return {
    authorize: async () => undefined,
    constrain: async (action: GolemAction, model: string) =>
      action === 'read' ? READABLE[model] ?? {} : model === 'Thread' ? { title: { not: 'locked' } } : {},
    check: async () => true,
    checkField: async () => true,
  };
}

function runtimeOver(tx: Client, model: string): GolemBatchRuntime {
  const delegate = (name: string) => tx[name.charAt(0).toLowerCase() + name.slice(1)] as GolemBatchDelegate;
  return {
    suppressed: false,
    run: (work) => work(delegate(model), {
      scope: tx,
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
    await prisma.reply.createMany({ data: [
      { id: 10, threadId: 1, body: 'a', postedAt: new Date('2026-01-01T00:00:00.000Z') },
      { id: 11, threadId: 1, body: 'b', postedAt: new Date('2026-01-02T00:00:00.000Z') },
    ] });
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

  function racing(
    client: Client,
    delegateName: string,
    injection: () => Promise<unknown>,
    afterCall = 1,
    method = 'findFirst',
  ): { client: Client; injected: () => boolean } {
    let injected = false;
    let calls = 0;
    const wrap = (inner: Client): Client => new Proxy(inner, {
      get: (target, property, receiver) => {
        if (property === '$transaction') {
          return (work: (tx: Client) => Promise<unknown>, ...rest: unknown[]) =>
            target.$transaction((tx: Client) => work(wrap(tx)), ...rest);
        }
        if (property !== delegateName) return Reflect.get(target, property, receiver);
        return new Proxy(target[delegateName], {
          get: (delegate, name, delegateReceiver) => {
            if (name !== method) return Reflect.get(delegate, name, delegateReceiver);
            if (method !== 'findFirst') {
              return async (args: unknown) => {
                injected = true;
                await injection();
                return delegate[method](args);
              };
            }
            return async (args: unknown) => {
              const found = await delegate.findFirst(args);
              calls += 1;
              if (calls === afterCall) {
                injected = true;
                await injection();
              }
              return found;
            };
          },
        });
      },
    });
    return { client: wrap(client), injected: () => injected };
  }

  function cascadePublisher() {
    const published: GolemEventPayload[] = [];
    const bus: GolemEventBus = {
      publish: async (_topic, event) => { published.push(event); },
      publishMany: async (_topic, events) => { published.push(...events); },
      iterate: (async function* () {})() as never,
    };
    const publisher = createEventPublisher({
      datamodel: { models: [...models, people], enums: [], provider: provider_ },
      eventBus: bus,
      models: new Set(),
    });
    return { publisher, published };
  }

  function observing(onStatement: (sql: string) => Promise<void>): (inner: Client) => Client {
    const wrap = (inner: Client): Client => new Proxy(inner, {
      get: (target, property, receiver) => {
        if (property === '$transaction') {
          return (work: (tx: Client) => Promise<unknown>, ...rest: unknown[]) =>
            target.$transaction((tx: Client) => work(wrap(tx)), ...rest);
        }
        if (property === '$queryRawUnsafe') {
          return async (sql: string, ...values: unknown[]) => {
            const rows = await target.$queryRawUnsafe(sql, ...values);
            await onStatement(sql);
            return rows;
          };
        }
        return Reflect.get(target, property, receiver);
      },
    });
    return wrap;
  }

  function observed(onStatement: (sql: string) => Promise<void>): Client {
    return observing(onStatement)(prisma);
  }

  function barrier(): { arrive(): void; wait(): Promise<void> } {
    let arrive!: () => void;
    const arrived = new Promise<void>((resolve) => { arrive = resolve; });
    return {
      arrive: () => arrive(),
      wait: () => Promise.race([arrived, new Promise<void>((resolve) => setTimeout(resolve, 500))]),
    };
  }

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
      const race = racing(prisma, 'channel', () =>
        database.concurrent.channel.create({ data: { slug: 'late', title: 'someone else' } }));

      await expect(outcome(() => engine(race.client).create({
        model: 'Pin',
        data: { id: 45, channel: { connectOrCreate: { where: { slug: 'late' }, create: { slug: 'late', title: 'mine' } } } },
        context: ctx,
      }))).resolves.toBe('GolemNotFoundError: Channel not found');

      expect(race.injected()).toBe(true);
      await expect(prisma.pin.count({ where: { id: 45 } })).resolves.toBe(0);
      await expect(prisma.channel.count({ where: { title: 'mine' } })).resolves.toBe(0);
    });
  });

  const watchesOf = async (threadId: number) =>
    (await prisma.watch.findMany({ where: { threadId }, orderBy: { id: 'asc' } })).map((row: { id: number }) => row.id);

  describe.each([true, false])('the stored state judges a link, with checkWriteResults %s', (checkWriteResults) => {
    const ctx = { req: {} };
    const engine = (client: Client) => new GolemEngine(client, models, {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });

    const hide = () => database.concurrent.thread.update({ where: { id: 1 }, data: { title: 'hidden' } });

    it('refuses a link whose target a concurrent update hid before the write locked it', async () => {
      const race = racing(prisma, 'thread', hide);

      await expect(engine(race.client).create({ model: 'Watch', data: { id: 23, threadId: 1 }, context: ctx }))
        .rejects.toThrow('Thread not found');

      expect(race.injected()).toBe(true);
      await expect(prisma.watch.count({ where: { id: 23 } })).resolves.toBe(0);
    });

    it('refuses a connect whose target a concurrent update hid before the write locked it', async () => {
      const race = racing(prisma, 'thread', hide);

      await expect(engine(race.client).update({
        model: 'Watch', where: { id: 22 }, data: { thread: { connect: { id: 1 } } }, context: ctx,
      })).rejects.toThrow('Thread not found');

      expect(race.injected()).toBe(true);
      await expect(watchesOf(2)).resolves.toEqual([22]);
    });

    if (provider_ === 'sqlite') {
      it('judges the link against the state the write stored, after every pre-write read', async () => {
        const race = racing(prisma, 'thread', hide, 2);

        await expect(engine(race.client).create({ model: 'Watch', data: { id: 24, threadId: 1 }, context: ctx }))
          .rejects.toThrow('Thread not found');

        expect(race.injected()).toBe(true);
        await expect(prisma.watch.count({ where: { id: 24 } })).resolves.toBe(0);
      });
    } else {
      it('holds a concurrent update of the target back until the link commits', async () => {
        const order: string[] = [];
        let pending: Promise<unknown> = Promise.resolve();
        const race = racing(prisma, 'thread', async () => {
          pending = hide().then(() => order.push('target hidden'));
          await new Promise((resolve) => setTimeout(resolve, 300));
        }, 2);

        await engine(race.client).create({ model: 'Watch', data: { id: 24, threadId: 1 }, context: ctx });
        order.push('link committed');
        await pending;

        expect(race.injected()).toBe(true);
        expect(order).toEqual(['link committed', 'target hidden']);
      });
    }
  });

  if (provider_ === 'postgresql') {
    describe.each([true, false])('a connectOrCreate target supplied concurrently, with checkWriteResults %s', (checkWriteResults) => {
      it('holds a concurrent change to the connected row back until the link commits', async () => {
        const order: string[] = [];
        let pending: Promise<unknown> = Promise.resolve();
        let calls = 0;
        const onCall = async () => {
          calls += 1;
          if (calls === 1) {
            await database.concurrent.channel.create({ data: { slug: 'late', title: 'open' } });
          }
          if (calls === 3) {
            pending = database.concurrent.channel
              .update({ where: { slug: 'late' }, data: { title: 'closed' } })
              .then(() => order.push('target hidden'));
            await new Promise((resolve) => setTimeout(resolve, 300));
          }
        };
        const wrap = (inner: Client): Client => new Proxy(inner, {
          get: (target, property, receiver) => {
            if (property === '$transaction') {
              return (work: (tx: Client) => Promise<unknown>, ...rest: unknown[]) =>
                target.$transaction((tx: Client) => work(wrap(tx)), ...rest);
            }
            if (property !== 'channel') return Reflect.get(target, property, receiver);
            return new Proxy(target.channel, {
              get: (delegate, name, delegateReceiver) => {
                if (name !== 'findFirst') return Reflect.get(delegate, name, delegateReceiver);
                return async (args: unknown) => {
                  const found = await delegate.findFirst(args);
                  await onCall();
                  return found;
                };
              },
            });
          },
        });
        const engine = new GolemEngine(wrap(prisma), models, {
          authorization: provider(),
          checkWriteResults,
          checkReadFields: false,
          provider: provider_,
        });

        await engine.create({
          model: 'Pin',
          data: { id: 46, channel: { connectOrCreate: { where: { slug: 'late' }, create: { slug: 'late', title: 'mine' } } } },
          context: { req: {} },
        });
        order.push('link committed');
        await pending;

        expect(calls).toBeGreaterThanOrEqual(3);
        expect(order).toEqual(['link committed', 'target hidden']);
        await expect(prisma.pin.findUnique({ where: { id: 46 } })).resolves.toEqual({ id: 46, channelSlug: 'late' });
      });
    });
  }

  if (provider_ === 'postgresql') {
    describe.each([true, false])('locking link targets, with checkWriteResults %s', (checkWriteResults) => {
      const ctx = { req: {} };

      const engineOver = (client: Client) => new GolemEngine(client, models, {
        authorization: provider(),
        checkWriteResults,
        checkReadFields: false,
        provider: provider_,
      });

      async function race(first: (client: Client) => Promise<unknown>, second: (client: Client) => Promise<unknown>) {
        const secondLocked = barrier();
        let firstWaited = false;
        const firstClient = observed(async (sql) => {
          if (!firstWaited && sql.includes('FROM "watches"')) {
            firstWaited = true;
            await secondLocked.wait();
          }
        });
        const secondClient = observed(async (sql) => {
          if (sql.includes('FROM "watches"')) secondLocked.arrive();
        });
        const firstRun = first(firstClient);
        await new Promise((resolve) => setTimeout(resolve, 100));
        const secondRun = second(secondClient);
        return Promise.allSettled([firstRun, secondRun]);
      }

      it('serialises two self-relation updateMany calls that each point at the other row', async () => {
        await prisma.person.deleteMany();
        await prisma.person.createMany({ data: [{ id: 1 }, { id: 2 }] });
        const personEngine = (client: Client) => new GolemEngine(client, [...models, people], {
          authorization: provider(),
          checkWriteResults,
          checkReadFields: false,
          provider: provider_,
        });
        const secondLocked = barrier();
        let firstWaited = false;
        const firstClient = observed(async (sql) => {
          if (!firstWaited && sql.includes('FROM "people"')) {
            firstWaited = true;
            await secondLocked.wait();
          }
        });
        const secondClient = observed(async (sql) => {
          if (sql.includes('FROM "people"')) secondLocked.arrive();
        });

        const firstRun = personEngine(firstClient).updateMany({
          model: 'Person', where: { id: 1 }, data: { buddyId: 2 }, context: ctx,
        });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const secondRun = personEngine(secondClient).updateMany({
          model: 'Person', where: { id: 2 }, data: { buddyId: 1 }, context: ctx,
        });
        const outcomes = await Promise.allSettled([firstRun, secondRun]);

        expect(outcomes.map((outcome) => outcome.status)).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.person.findMany({ orderBy: { id: 'asc' } })).resolves.toEqual([
          { id: 1, buddyId: 2 },
          { id: 2, buddyId: 1 },
        ]);
      });

      it('serialises two self-relation updates that each link the other row without a deadlock', async () => {
        await prisma.person.deleteMany();
        await prisma.person.createMany({ data: [{ id: 1 }, { id: 2 }] });
        const personEngine = (client: Client) => new GolemEngine(client, [...models, people], {
          authorization: provider(),
          checkWriteResults,
          checkReadFields: false,
          provider: provider_,
        });
        const secondLocked = barrier();
        let firstWaited = false;
        const firstClient = observed(async (sql) => {
          if (!firstWaited && sql.includes('FROM "people"')) {
            firstWaited = true;
            await secondLocked.wait();
          }
        });
        const secondClient = observed(async (sql) => {
          if (sql.includes('FROM "people"')) secondLocked.arrive();
        });

        const firstRun = personEngine(firstClient).update({
          model: 'Person', where: { id: 1 }, data: { buddy: { connect: { id: 2 } } }, context: ctx,
        });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const secondRun = personEngine(secondClient).update({
          model: 'Person', where: { id: 2 }, data: { buddy: { connect: { id: 1 } } }, context: ctx,
        });
        const outcomes = await Promise.allSettled([firstRun, secondRun]);

        expect(outcomes.map((outcome) => outcome.status)).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.person.findMany({ orderBy: { id: 'asc' } })).resolves.toEqual([
          { id: 1, buddyId: 2 },
          { id: 2, buddyId: 1 },
        ]);
      });

      async function deleteRacesLink(
        link: (client: Client) => Promise<unknown>,
        model: string,
        where: Record<string, unknown>,
      ): Promise<string[]> {
        const { publisher } = cascadePublisher();
        const cascadeLocked = barrier();
        let linkWaited = false;
        const linkClient = observed(async () => {
          if (!linkWaited) {
            linkWaited = true;
            await cascadeLocked.wait();
          }
        });
        const cascadeClient = observed(async () => { cascadeLocked.arrive(); });
        const linking = link(linkClient);
        await new Promise((resolve) => setTimeout(resolve, 100));
        const deleting = cascadeClient.$transaction((tx: Client) => publisher({
          model, operation: 'delete', args: { where },
          query: async () => { throw new Error('the native delete escaped interception'); },
          batch: runtimeOver(tx, model),
        }));
        const outcomes = await Promise.allSettled([linking, deleting]);
        return outcomes.map((result) => result.status === 'fulfilled' ? 'fulfilled' : String((result as PromiseRejectedResult).reason));
      }

      it('serialises a cascade delete of a parent with a write linking its existing child to it', async () => {
        const outcomes = await deleteRacesLink(
          (client) => engineOver(client).update({
            model: 'Watch', where: { id: 20 }, data: { thread: { connect: { id: 1 } } }, context: ctx,
          }),
          'Thread',
          { id: 1 },
        );

        expect(outcomes).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.thread.count({ where: { id: 1 } })).resolves.toBe(0);
        await expect(prisma.watch.findUnique({ where: { id: 20 } })).resolves.toEqual({ id: 20, threadId: null });
      });

      it('serialises a cascade delete of a parent with a write reattaching a child whose model sorts first', async () => {
        const outcomes = await deleteRacesLink(
          (client) => engineOver(client).update({
            model: 'Thread', where: { id: 1 }, data: { replies: { connect: [{ id: 10 }] } }, context: ctx,
          }),
          'Thread',
          { id: 1 },
        );

        expect(outcomes).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.reply.count({ where: { threadId: 1 } })).resolves.toBe(0);
      });

      it('serialises a self-relation cascade delete with a write linking the dependent row to it', async () => {
        await prisma.person.deleteMany();
        await prisma.person.createMany({ data: [{ id: 2 }, { id: 1, buddyId: 2 }] });
        const personEngine = (client: Client) => new GolemEngine(client, [...models, people], {
          authorization: provider(),
          checkWriteResults,
          checkReadFields: false,
          provider: provider_,
        });

        const outcomes = await deleteRacesLink(
          (client) => personEngine(client).update({
            model: 'Person', where: { id: 1 }, data: { buddy: { connect: { id: 2 } } }, context: ctx,
          }),
          'Person',
          { id: 2 },
        );

        expect(outcomes).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.person.findMany()).resolves.toEqual([{ id: 1, buddyId: null }]);
      });

      it('keeps one lock order across the link check and the nested delete of the same update', async () => {
        await prisma.person.deleteMany();
        await prisma.person.createMany({ data: [{ id: 5 }, { id: 6, buddyId: 5 }] });
        await prisma.person.create({ data: { id: 2, buddyId: 6 } });
        const { publisher } = cascadePublisher();
        const personEngine = (client: Client) => new GolemEngine(client, [...models, people], {
          authorization: provider(),
          checkWriteResults,
          checkReadFields: false,
          provider: provider_,
        });
        const hooked = (onStatement: (sql: string) => Promise<void>) => (tx: Client): Client => new Proxy(tx, {
          get: (target, property, receiver) => {
            if (property !== '$queryRawUnsafe') return Reflect.get(target, property, receiver);
            return async (sql: string, ...values: unknown[]) => {
              const rows = await target.$queryRawUnsafe(sql, ...values);
              await onStatement(sql);
              return rows;
            };
          },
        });
        const secondLocked = barrier();
        let firstStatements = 0;
        const first = golemClient(prisma, publisher, hooked(async (sql) => {
          if (sql.includes('FROM "people"')) {
            firstStatements += 1;
            if (firstStatements === 2) await secondLocked.wait();
          }
        }));
        const second = golemClient(prisma, publisher, hooked(async (sql) => {
          if (sql.includes('FROM "people"')) secondLocked.arrive();
        }));

        const firstRun = personEngine(first).update({
          model: 'Person', where: { id: 5 }, data: { buddiedBy: { delete: [{ id: 6 }] } }, context: ctx,
        });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const secondRun = personEngine(second).update({
          model: 'Person', where: { id: 2 }, data: { buddy: { connect: { id: 5 } } }, context: ctx,
        });
        const outcomes = await Promise.allSettled([firstRun, secondRun]);

        expect(outcomes[1].status).toBe('fulfilled');
        expect(outcomes[0].status).toBe('rejected');
        expect((outcomes[0] as PromiseRejectedResult).reason).toEqual(
          new GolemConflictError('A row of Person this write needs is held by a concurrent write'),
        );
        await expect(prisma.person.findMany({ orderBy: { id: 'asc' } })).resolves.toEqual([
          { id: 2, buddyId: 5 },
          { id: 5, buddyId: null },
          { id: 6, buddyId: 5 },
        ]);
      });

      it('serialises two reverse connects of the same child without a deadlock', async () => {
        await prisma.thread.create({ data: { id: 3, title: 'third' } });

        const outcomes = await race(
          (client) => engineOver(client).update({
            model: 'Thread', where: { id: 1 }, data: { watches: { connect: [{ id: 22 }] } }, context: ctx,
          }),
          (client) => engineOver(client).update({
            model: 'Thread', where: { id: 3 }, data: { watches: { connect: [{ id: 22 }] } }, context: ctx,
          }),
        );

        expect(outcomes.map((outcome) => outcome.status)).toEqual(['fulfilled', 'fulfilled']);
        await expect(prisma.watch.findUnique({ where: { id: 22 } })).resolves.toEqual({ id: 22, threadId: 3 });
      });

      it('locks overlapping link targets in one order, whatever order the inputs name them', async () => {
        await prisma.thread.create({ data: { id: 3, title: 'third' } });

        const outcomes = await race(
          (client) => engineOver(client).update({
            model: 'Thread', where: { id: 3 }, data: { watches: { connect: [{ id: 20 }, { id: 22 }] } }, context: ctx,
          }),
          (client) => engineOver(client).update({
            model: 'Thread', where: { id: 1 }, data: { watches: { connect: [{ id: 22 }, { id: 20 }] } }, context: ctx,
          }),
        );

        expect(outcomes.map((outcome) => outcome.status)).toEqual(['fulfilled', 'fulfilled']);
        await expect(watchesOf(1)).resolves.toEqual([20, 21, 22]);
      });

      it('takes a share lock on a target a forward foreign key only references, and an update lock on a child it moves', async () => {
        const statements: string[] = [];
        const client = observed(async (sql) => { statements.push(sql); });

        await engineOver(client).create({ model: 'Watch', data: { id: 25, threadId: 1 }, context: ctx });
        const forward = statements.splice(0);
        await engineOver(client).update({
          model: 'Thread', where: { id: 1 }, data: { watches: { connect: [{ id: 22 }] } }, context: ctx,
        });

        expect(forward.filter((sql) => sql.includes('FROM "threads"'))).not.toHaveLength(0);
        expect(forward.every((sql) => sql.endsWith('FOR SHARE'))).toBe(true);
        expect(statements.find((sql) => sql.includes('FROM "watches"'))).toMatch(/FOR UPDATE$/);
      });
    });
  }

  describe('the rows a deleteMany selects', () => {
    const run = (args: Record<string, unknown>) => {
      const { publisher } = cascadePublisher();
      return prisma.$transaction((tx: Client) => publisher({
        model: 'Watch', operation: 'deleteMany', args,
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(tx, 'Watch'),
      }));
    };

    it('deletes no more rows than its limit', async () => {
      await expect(run({ where: { threadId: 1 }, limit: 1 })).resolves.toEqual({ count: 1 });
      await expect(prisma.watch.count({ where: { threadId: 1 } })).resolves.toBe(1);
      await expect(run({ where: { threadId: 1 }, limit: 0 })).resolves.toEqual({ count: 0 });
      await expect(prisma.watch.count({ where: { threadId: 1 } })).resolves.toBe(1);
    });

    it('refuses an argument it does not know how to honour, deleting nothing', async () => {
      await expect(run({ where: { threadId: 1 }, take: 1 })).rejects.toThrow(
        new GolemValidationError('deleteMany on Watch does not support the argument take'),
      );
      await expect(run({ where: { threadId: 1 }, limit: -1 })).rejects.toThrow(
        new GolemValidationError('deleteMany on Watch needs a non-negative integer limit'),
      );
      await expect(prisma.watch.count({ where: { threadId: 1 } })).resolves.toBe(2);
    });
  });

  describe.each([true, false])('the nested branch that executes, with checkWriteResults %s', (checkWriteResults) => {
    const ctx = { req: {} };
    const personEngine = () => new GolemEngine(prisma, [...models, people], {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });

    beforeEach(async () => {
      await prisma.person.deleteMany();
      await prisma.person.createMany({ data: [{ id: 5 }, { id: 6, buddyId: 5 }] });
    });

    it('connects to an existing row without checking links only its create branch names', async () => {
      await personEngine().update({
        model: 'Person',
        where: { id: 5 },
        data: { buddy: { connectOrCreate: { where: { id: 6 }, create: { id: 6, buddy: { connect: { id: 999 } } } } } },
        context: ctx,
      });
      await expect(prisma.person.findUnique({ where: { id: 5 } })).resolves.toEqual({ id: 5, buddyId: 6 });
    });

    it('updates an existing nested row without checking links only its create branch names', async () => {
      await personEngine().update({
        model: 'Person',
        where: { id: 5 },
        data: { buddiedBy: { upsert: [{ where: { id: 6 }, create: { id: 6, buddiedBy: { connect: [{ id: 999 }] } }, update: {} }] } },
        context: ctx,
      });
      await expect(prisma.person.findUnique({ where: { id: 6 } })).resolves.toEqual({ id: 6, buddyId: 5 });
    });

    it('still checks the links of the branch that executes', async () => {
      await expect(personEngine().update({
        model: 'Person',
        where: { id: 5 },
        data: { buddiedBy: { upsert: [{ where: { id: 6 }, create: { id: 6 }, update: { buddiedBy: { connect: [{ id: 999 }] } } }] } },
        context: ctx,
      })).rejects.toThrow('Person not found');
      await expect(personEngine().update({
        model: 'Person',
        where: { id: 5 },
        data: { buddy: { connectOrCreate: { where: { id: 7 }, create: { id: 7, buddy: { connect: { id: 999 } } } } } },
        context: ctx,
      })).rejects.toThrow('Person not found');
      await expect(prisma.person.count()).resolves.toBe(2);
    });
  });

  if (provider_ === 'postgresql') {
    describe.each([true, false])('single-row writes in interactive transactions, with checkWriteResults %s', (checkWriteResults) => {
      async function crossed(
        write: (client: Client, id: number) => Promise<unknown>,
        transact: (watch: (inner: Client) => Client, work: (tx: Client) => Promise<void>) => Promise<unknown>,
      ) {
        await prisma.thread.create({ data: { id: 3, title: 'third' } });
        const secondLocked = barrier();
        let firstWaited = false;
        const firstClient = observing(async (sql) => {
          if (!firstWaited && sql.includes('FROM "threads"')) {
            firstWaited = true;
            await secondLocked.wait();
          }
        });
        const secondClient = observing(async (sql) => {
          if (sql.includes('FROM "threads"')) secondLocked.arrive();
        });
        const firstRun = transact(firstClient, async (tx) => {
          await write(tx, 1);
          await write(tx, 3);
        });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const secondRun = transact(secondClient, async (tx) => {
          await write(tx, 3);
          await write(tx, 1);
        });
        return Promise.allSettled([firstRun, secondRun]);
      }

      const verdict = (outcomes: PromiseSettledResult<unknown>[]) => outcomes.map((outcome) => {
        if (outcome.status === 'fulfilled') return 'fulfilled';
        const reason = (outcome as PromiseRejectedResult).reason;
        return reason instanceof GolemConflictError ? 'conflict' : `other: ${String(reason).slice(0, 120)}`;
      });

      it('through the engine, one completes and the other is refused, never a deadlock', async () => {
        const engine = (client: Client) => new GolemEngine(client, models, {
          authorization: provider(), checkWriteResults, checkReadFields: false, provider: provider_,
        });
        const outcomes = await crossed(
          (tx, id) => (tx as unknown as { update(request: unknown): Promise<unknown> }).update({
            model: 'Thread', where: { id }, data: { title: `edited ${id}` },
          }),
          (watch, work) => engine(watch(prisma)).transaction({ req: {} }, (tx) => work(tx as unknown as Client)),
        );
        expect(verdict(outcomes).sort()).toEqual(['conflict', 'fulfilled']);
      });

      it('through the unscoped system client, one completes and the other is refused, never a deadlock', async () => {
        const { publisher } = cascadePublisher();
        const outcomes = await crossed(
          (tx, id) => tx.thread.update({ where: { id }, data: { title: `edited ${id}` } }),
          (watch, work) => golemClient(prisma, publisher, watch).$transaction((tx: Client) => work(tx)),
        );
        expect(verdict(outcomes).sort()).toEqual(['conflict', 'fulfilled']);
      });
    });

    describe('a row deciding a nested branch that appears while the write locks', () => {
      it('is refused with a conflict', async () => {
        await prisma.person.deleteMany();
        await prisma.person.createMany({ data: [{ id: 5 }, { id: 6, buddyId: 5 }] });
        const race = racing(prisma, 'person', () => database.concurrent.person.create({ data: { id: 7 } }), 2);

        await expect(new GolemEngine(race.client, [...models, people], {
          authorization: provider(), checkWriteResults: false, checkReadFields: false, provider: provider_,
        }).update({
          model: 'Person',
          where: { id: 5 },
          data: { buddy: { connectOrCreate: { where: { id: 7 }, create: { id: 7, buddy: { connect: { id: 6 } } } } } },
          context: { req: {} },
        })).rejects.toThrow(new GolemConflictError('The Person row deciding this nested write changed concurrently'));
        expect(race.injected()).toBe(true);
        await expect(prisma.person.findUnique({ where: { id: 5 } })).resolves.toEqual({ id: 5, buddyId: null });
      });
    });

    describe('a nested mutation row that attaches while the write locks', () => {
      it('is refused with a conflict', async () => {
        let attached = false;
        const raced = new Proxy(prisma, {
          get: (target, property, receiver) => {
            if (property === '$transaction') {
              return (work: (tx: Client) => Promise<unknown>, ...rest: unknown[]) =>
                target.$transaction((tx: Client) => work(new Proxy(tx, {
                  get: (inner, name, innerReceiver) => {
                    if (name !== 'reply') return Reflect.get(inner, name, innerReceiver);
                    return new Proxy(inner.reply, {
                      get: (delegate, method, delegateReceiver) => {
                        if (method !== 'findMany') return Reflect.get(delegate, method, delegateReceiver);
                        return async (args: unknown) => {
                          const rows = await delegate.findMany(args);
                          if (!attached) {
                            attached = true;
                            await database.concurrent.reply.create({
                              data: { id: 12, threadId: 1, body: 'late', postedAt: new Date('2026-01-03T00:00:00.000Z') },
                            });
                          }
                          return rows;
                        };
                      },
                    });
                  },
                })), ...rest);
            }
            return Reflect.get(target, property, receiver);
          },
        });

        await expect(new GolemEngine(raced, models, {
          authorization: provider(), checkWriteResults: false, checkReadFields: false, provider: provider_,
        }).update({
          model: 'Thread', where: { id: 1 }, data: { replies: { updateMany: { where: {}, data: { body: 'x' } } } }, context: { req: {} },
        })).rejects.toThrow(new GolemConflictError('The Reply rows linked to this Thread changed concurrently'));
        expect(attached).toBe(true);
        await expect(prisma.reply.count({ where: { body: 'x' } })).resolves.toBe(0);
      });
    });
  }

  describe('a delete whose closure changes between enumeration and lock', () => {
    it('is refused, deleting nothing', async () => {
      const { publisher, published } = cascadePublisher();
      let inserted = false;
      const wrapped = (tx: Client): Client => new Proxy(tx, {
        get: (target, property, receiver) => {
          if (property !== 'reply') return Reflect.get(target, property, receiver);
          return new Proxy(target.reply, {
            get: (delegate, name, delegateReceiver) => {
              if (name !== 'findMany') return Reflect.get(delegate, name, delegateReceiver);
              return async (args: unknown) => {
                const found = await delegate.findMany(args);
                if (!inserted) {
                  inserted = true;
                  await database.concurrent.reply.create({
                    data: { id: 12, threadId: 1, body: 'late', postedAt: new Date('2026-01-03T00:00:00.000Z') },
                  });
                }
                return found;
              };
            },
          });
        },
      });

      await expect(prisma.$transaction((tx: Client) => publisher({
        model: 'Thread', operation: 'delete', args: { where: { id: 1 } },
        query: async () => { throw new Error('the native delete escaped interception'); },
        batch: runtimeOver(wrapped(tx), 'Thread'),
      }))).rejects.toThrow(new GolemConflictError('The rows deleting from Thread touches changed while they were being locked'));

      expect(inserted).toBe(true);
      expect(published).toEqual([]);
      await expect(prisma.thread.count({ where: { id: 1 } })).resolves.toBe(1);
    });
  });

  describe.each([true, false])('a row moved out of policy before its write, with checkWriteResults %s', (checkWriteResults) => {
    const ctx = { req: {} };
    const lock = () => database.concurrent.thread.update({ where: { id: 1 }, data: { title: 'locked' } });
    const engine = (client: Client) => new GolemEngine(client, models, {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });

    it('deletes nothing and reports not found', async () => {
      const race = racing(prisma, 'thread', lock, 1, 'delete');

      await expect(outcome(() => engine(race.client).delete({ model: 'Thread', where: { id: 1 }, context: ctx })))
        .resolves.toBe('GolemNotFoundError: Thread not found');

      expect(race.injected()).toBe(true);
      await expect(prisma.thread.count({ where: { id: 1 } })).resolves.toBe(1);
    });

    it('updates nothing and reports not found', async () => {
      const race = provider_ === 'postgresql' ? racing(prisma, 'thread', lock, 1) : racing(prisma, 'thread', lock, 1, 'update');

      await expect(outcome(() => engine(race.client).update({
        model: 'Thread', where: { id: 1 }, data: { title: 'renamed' }, context: ctx,
      }))).resolves.toBe('GolemNotFoundError: Thread not found');

      expect(race.injected()).toBe(true);
      const row = await prisma.thread.findUnique({ where: { id: 1 } });
      expect(row).not.toBeNull();
      expect(row.title).not.toBe('renamed');
    });
  });

  describe.each([true, false])('changing a row identity with checkWriteResults %s', (checkWriteResults) => {
    const ctx = { req: {} };
    const engine = () => new GolemEngine(prisma, models, {
      authorization: provider(),
      checkWriteResults,
      checkReadFields: false,
      provider: provider_,
    });

    it.each([
      ['a primary key', () => engine().update({ model: 'Thread', where: { id: 1 }, data: { id: 9 }, context: ctx }), 'Thread.id'],
      ['a primary key on many rows', () => engine().updateMany({ model: 'Thread', where: { id: 1 }, data: { id: 9 }, context: ctx }), 'Thread.id'],
      ['a fallback unique identity', () => engine().update({ model: 'Channel', where: { slug: 'general' }, data: { slug: 'renamed' }, context: ctx }), 'Channel.slug'],
      ['a nested row identity', () => engine().update({
        model: 'Thread', where: { id: 1 }, data: { watches: { update: [{ where: { id: 20 }, data: { id: 99 } }] } }, context: ctx,
      }), 'Watch.id'],
      ['the update branch of an upsert', () => engine().upsert({
        model: 'Thread', where: { id: 1 }, create: { id: 1, title: 'x' }, update: { id: 9 }, context: ctx,
      }), 'Thread.id'],
    ])('refuses changing %s before any query, changing nothing', async (_label, run, field) => {
      const attempt = run();
      await expect(attempt).rejects.toBeInstanceOf(GolemValidationError);
      await expect(attempt).rejects.toThrow(`${field} identifies the row and cannot be changed by an update`);
      await expect(prisma.thread.findMany({ orderBy: { id: 'asc' } })).resolves.toEqual([
        { id: 1, title: 'readable' },
        { id: 2, title: 'hidden' },
      ]);
      await expect(prisma.channel.count({ where: { slug: 'general' } })).resolves.toBe(1);
      await expect(prisma.watch.count({ where: { id: 20 } })).resolves.toBe(1);
    });

    it('leaves a caller without a context free to change an identity', async () => {
      await prisma.thread.create({ data: { id: 3, title: 'alone' } });
      await engine().update({ model: 'Thread', where: { id: 3 }, data: { id: 9 } });
      await expect(prisma.thread.count({ where: { id: 9 } })).resolves.toBe(1);
    });
  });

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

import { parse, subscribe } from 'graphql';
import { GolemEventBus, GolemEventPayload } from './events';
import { buildGolemSchema } from './schema';
import { field } from './testing';
import { DatamodelDocument } from './datamodel';
import { AuthorizationProvider } from './authorization';

class FakeBus implements GolemEventBus {
  published: Array<{ topic: string; payload: GolemEventPayload }> = [];
  openedIterators = 0;
  closedIterators = 0;
  returnable = true;
  private buffer: GolemEventPayload[] = [];
  private notify: (() => void) | null = null;

  async publish(topic: string, payload: GolemEventPayload): Promise<void> {
    this.published.push({ topic, payload });
  }

  push(payload: GolemEventPayload): void {
    this.buffer.push(payload);
    this.notify?.();
    this.notify = null;
  }

  iterate(): AsyncIterableIterator<GolemEventPayload> {
    this.openedIterators += 1;
    const owner = this;
    const inner = (async function* (): AsyncIterableIterator<GolemEventPayload> {
      while (true) {
        while (owner.buffer.length > 0) {
          yield owner.buffer.shift()!;
        }
        await new Promise<void>((resolve) => {
          owner.notify = resolve;
        });
      }
    })();
    const source: AsyncIterableIterator<GolemEventPayload> = {
      next: () => inner.next(),
      throw: (error?: unknown) => inner.throw(error),
      [Symbol.asyncIterator]() {
        return this;
      },
    };
    if (this.returnable) {
      source.return = (value?: never) => {
        owner.closedIterators += 1;
        return inner.return(value as never);
      };
    }
    return source;
  }
}

class BroadcastBus implements GolemEventBus {
  openedIterators = 0;
  private readonly listeners = new Set<{
    values: GolemEventPayload[];
    notify?: () => void;
  }>();

  async publish(_topic: string, payload: GolemEventPayload): Promise<void> {
    for (const listener of this.listeners) {
      listener.values.push(payload);
      listener.notify?.();
      listener.notify = undefined;
    }
  }

  iterate(): AsyncIterableIterator<GolemEventPayload> {
    this.openedIterators += 1;
    const listener: { values: GolemEventPayload[]; notify?: () => void } = { values: [] };
    this.listeners.add(listener);
    const owner = this;
    const inner = (async function* () {
      try {
        while (true) {
          while (listener.values.length > 0) yield listener.values.shift()!;
          await new Promise<void>((resolve) => {
            listener.notify = resolve;
          });
        }
      } finally {
        owner.listeners.delete(listener);
      }
    })();
    return inner as AsyncIterableIterator<GolemEventPayload>;
  }
}

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
    ],
  },
];

const datamodel: DatamodelDocument<{ User: 'id' | 'email' }> = { models, enums: [] };

function fakeClient() {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      findFirst: jest.fn().mockResolvedValue(null),
      create: jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' }),
      update: jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' }),
      updateMany: jest.fn().mockResolvedValue({ count: 2 }),
      delete: jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 2 }),
    },
  };
}


describe('schema subscription wiring', () => {
  it('throws when subscriptions are enabled without an event bus', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { subscriptions: true } },
      }),
    ).toThrow('Subscriptions are enabled for User but no event bus was provided');
  });

  function subscribedSchema(
    client: ReturnType<typeof fakeClient>,
    bus: FakeBus,
    authorization?: AuthorizationProvider,
  ) {
    return buildGolemSchema({
      datamodel,
      client,
      models: { User: { subscriptions: true } },
      eventBus: bus,
      authorization,
      defaults: { checkWriteResults: false, checkReadFields: false },
    });
  }

  it('delivers created events with the subscriber selection', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ email: 'a@b.c' });
    const schema = subscribedSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse('subscription { userEvents { type id entity { email } } }'),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({ type: 'CREATED', model: 'User', id: 'u1' });
    const { value } = await next;
    expect(value.data.userEvents).toEqual({
      type: 'CREATED',
      id: 'u1',
      entity: { email: 'a@b.c' },
    });
    expect(client.user.findFirst).toHaveBeenCalledWith({
      where: { id: 'u1' },
      select: { email: true },
    });
    await iterator.return?.();
  });

  it('skips events that do not match the subscriber filter', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValueOnce(null).mockResolvedValueOnce({ email: 'match@b.c' });
    const schema = subscribedSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { userEvents(where: { email: { contains: "match" } }) { type entity { email } } }',
      ),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'u2' });
    const { value } = await next;
    expect(value.data.userEvents.entity).toEqual({ email: 'match@b.c' });
    expect(client.user.findFirst).toHaveBeenCalledTimes(2);
    expect(client.user.findFirst).toHaveBeenCalledWith({
      where: { AND: [{ id: 'u2' }, { email: { contains: 'match' } }] },
      select: { email: true },
    });
    await iterator.return?.();
  });

  it('suppresses deleted events when a subscriber filter cannot be evaluated safely', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    const schema = subscribedSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { userEvents(where: { email: { contains: "x" } }) { type id entity { email } } }',
      ),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({ type: 'DELETED', model: 'User', id: 'u9', entity: { id: 'u9', email: 'x@y.z' } });
    client.user.findFirst.mockResolvedValue({ email: 'match@b.c' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'u10' });
    const { value } = await next;
    expect(value.data.userEvents).toEqual({
      type: 'UPDATED', id: 'u10', entity: { email: 'match@b.c' },
    });
    await iterator.return?.();
  });

  it('uses a fresh instance check before delivering deleted row ids', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    let revoked = false;
    const authorization: AuthorizationProvider = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      check: jest.fn(async (_action, _model, entity: { email?: string }) =>
        !revoked && entity.email === 'allowed@b.c'),
      freshContext: jest.fn((context) => ({ context, fresh: {} })),
    };
    const schema = subscribedSchema(client, bus, authorization);
    const iterator = (await subscribe({
      schema,
      document: parse('subscription { userEvents { type id entity { email } } }'),
      contextValue: { request: {} },
    })) as AsyncIterableIterator<any>;

    const first = iterator.next();
    bus.push({ type: 'DELETED', model: 'User', id: 'denied', entity: { id: 'denied', email: 'no@b.c' } });
    bus.push({ type: 'DELETED', model: 'User', id: 'allowed', entity: { id: 'allowed', email: 'allowed@b.c' } });
    expect((await first).value.data.userEvents.id).toBe('allowed');

    revoked = true;
    const next = iterator.next();
    bus.push({ type: 'DELETED', model: 'User', id: 'revoked', entity: { id: 'revoked', email: 'allowed@b.c' } });
    client.user.findFirst.mockResolvedValue({ email: 'visible@b.c' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'visible' });
    expect((await next).value.data.userEvents.type).toBe('UPDATED');
    await iterator.return?.();
  });

  async function settle(closing: Promise<unknown>): Promise<string> {
    return Promise.race([
      closing.then(() => 'closed'),
      new Promise<string>((resolve) => setTimeout(() => resolve('still running'), 250)),
    ]);
  }

  it('stops reading the bus when a subscriber disconnects mid-wait', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    const schema = subscribedSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { userEvents(where: { email: { contains: "match" } }) { type id } }',
      ),
    })) as AsyncIterableIterator<any>;

    const pending = iterator.next();
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(await settle(iterator.return!())).toBe('closed');
    expect(bus.closedIterators).toBe(1);
    expect((await pending).done).toBe(true);

    client.user.findFirst.mockResolvedValue({ email: 'match@b.c' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'u2' });
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(client.user.findFirst).not.toHaveBeenCalled();
  });

  it('stops even when the bus hands back an iterator that cannot be returned', async () => {
    const bus = new FakeBus();
    bus.returnable = false;
    const client = fakeClient();
    const schema = subscribedSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse('subscription { userEvents { type id } }'),
    })) as AsyncIterableIterator<any>;

    const pending = iterator.next();
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(await settle(iterator.return!())).toBe('closed');
    expect((await pending).done).toBe(true);

    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(client.user.findFirst).not.toHaveBeenCalled();
  });

  it('shares one model iterator until the final local subscriber disconnects', async () => {
    const bus = new FakeBus();
    const schema = subscribedSchema(fakeClient(), bus);
    const first = (await subscribe({
      schema,
      document: parse('subscription { userEvents { id } }'),
    })) as AsyncIterableIterator<any>;
    const second = (await subscribe({
      schema,
      document: parse('subscription { userEvents { id } }'),
    })) as AsyncIterableIterator<any>;

    expect(bus.openedIterators).toBe(1);
    await first.return?.();
    expect(bus.closedIterators).toBe(0);
    await second.return?.();
    expect(bus.closedIterators).toBe(1);
  });

  it('deduplicates one event evaluation only for identical context, where, and entity selection', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ email: 'match@b.c' });
    const schema = subscribedSchema(client, bus);
    const context = { request: {} };
    const document = parse(
      'subscription { userEvents(where: { email: { contains: "match" } }) { entity { alias: email } } }',
    );
    const first = (await subscribe({ schema, document, contextValue: context })) as AsyncIterableIterator<any>;
    const second = (await subscribe({ schema, document, contextValue: context })) as AsyncIterableIterator<any>;

    const firstNext = first.next();
    const secondNext = second.next();
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await Promise.all([firstNext, secondNext]);

    expect(client.user.findFirst).toHaveBeenCalledTimes(1);
    await first.return?.();
    await second.return?.();
  });

  it('normalizes object field order and entity aliases before grouping evaluations', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ email: 'match@b.c' });
    const schema = subscribedSchema(client, bus);
    const context = { request: {} };
    const first = (await subscribe({
      schema,
      contextValue: context,
      document: parse(
        'subscription { userEvents(where: { email: { contains: "match", startsWith: "m" } }) { entity { first: email } } }',
      ),
    })) as AsyncIterableIterator<any>;
    const second = (await subscribe({
      schema,
      contextValue: context,
      document: parse(
        'subscription { userEvents(where: { email: { startsWith: "m", contains: "match" } }) { entity { second: email } } }',
      ),
    })) as AsyncIterableIterator<any>;

    const results = [first.next(), second.next()];
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await Promise.all(results);

    expect(client.user.findFirst).toHaveBeenCalledTimes(1);
    await first.return?.();
    await second.return?.();
  });

  it('does not group different filters or different entity selections in one context', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ id: 'u1', email: 'match@b.c' });
    const schema = subscribedSchema(client, bus);
    const context = { request: {} };
    const documents = [
      'subscription { userEvents(where: { email: { contains: "match" } }) { entity { email } } }',
      'subscription { userEvents(where: { email: { contains: "other" } }) { entity { email } } }',
      'subscription { userEvents(where: { email: { contains: "match" } }) { entity { id } } }',
    ];
    const iterators = await Promise.all(documents.map(async (source) =>
      (await subscribe({ schema, contextValue: context, document: parse(source) })) as AsyncIterableIterator<any>,
    ));

    const results = iterators.map((iterator) => iterator.next());
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await Promise.all(results);

    expect(client.user.findFirst).toHaveBeenCalledTimes(3);
    await Promise.all(iterators.map((iterator) => iterator.return?.()));
  });

  it('uses a fresh authorization context for each event evaluation, including a shared one', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ email: 'visible@b.c' });
    const freshContext = jest.fn((context) => ({ original: context, fresh: {} }));
    const authorization: AuthorizationProvider = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      check: jest.fn(async () => true),
      freshContext,
    };
    const schema = subscribedSchema(client, bus, authorization);
    const context = { request: {} };
    const document = parse('subscription { userEvents { entity { email } } }');
    const first = (await subscribe({ schema, document, contextValue: context })) as AsyncIterableIterator<any>;
    const second = (await subscribe({ schema, document, contextValue: context })) as AsyncIterableIterator<any>;

    const firstEvent = [first.next(), second.next()];
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await Promise.all(firstEvent);
    const secondEvent = [first.next(), second.next()];
    bus.push({ type: 'UPDATED', model: 'User', id: 'u2' });
    await Promise.all(secondEvent);

    expect(client.user.findFirst).toHaveBeenCalledTimes(2);
    expect(freshContext).toHaveBeenCalledTimes(4);
    await first.return?.();
    await second.return?.();
  });

  it('never deduplicates evaluations across distinct context identities', async () => {
    const bus = new FakeBus();
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ email: 'match@b.c' });
    const schema = subscribedSchema(client, bus);
    const document = parse('subscription { userEvents { entity { email } } }');
    const first = (await subscribe({
      schema,
      document,
      contextValue: { request: { user: 'one' } },
    })) as AsyncIterableIterator<any>;
    const second = (await subscribe({
      schema,
      document,
      contextValue: { request: { user: 'one' } },
    })) as AsyncIterableIterator<any>;

    const firstNext = first.next();
    const secondNext = second.next();
    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    await Promise.all([firstNext, secondNext]);

    expect(client.user.findFirst).toHaveBeenCalledTimes(2);
    await first.return?.();
    await second.return?.();
  });

  it('disconnects an overflowing subscriber with an explicit GraphQL error', async () => {
    const bus = new FakeBus();
    const overflowDisconnected = jest.fn();
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { subscriptions: true } },
      eventBus: bus,
      subscription: {
        queueCapacity: 1,
        observer: { overflowDisconnected },
      },
    });
    const iterator = (await subscribe({
      schema,
      document: parse('subscription { userEvents { id } }'),
    })) as AsyncIterableIterator<any>;

    bus.push({ type: 'UPDATED', model: 'User', id: 'u1' });
    bus.push({ type: 'UPDATED', model: 'User', id: 'u2' });
    await new Promise((resolve) => setTimeout(resolve, 20));

    await expect(iterator.next()).rejects.toMatchObject({
      extensions: { code: 'GOLEM_SUBSCRIPTION_OVERFLOW', capacity: 1 },
    });
    expect(overflowDisconnected).toHaveBeenCalledWith('User');
    expect(bus.closedIterators).toBe(1);
  });

  it('keeps independent schema-instance hubs on a shared distributed-style bus', async () => {
    const bus = new BroadcastBus();
    const firstSchema = subscribedSchema(fakeClient(), bus as never);
    const secondSchema = subscribedSchema(fakeClient(), bus as never);
    const document = parse('subscription { userEvents { id } }');
    const first = (await subscribe({ schema: firstSchema, document })) as AsyncIterableIterator<any>;
    const second = (await subscribe({ schema: secondSchema, document })) as AsyncIterableIterator<any>;
    const results = [first.next(), second.next()];

    await bus.publish('golem.User', { type: 'UPDATED', model: 'User', id: 'shared' });

    expect((await results[0]).value.data.userEvents.id).toBe('shared');
    expect((await results[1]).value.data.userEvents.id).toBe('shared');
    expect(bus.openedIterators).toBe(2);
    await first.return?.();
    await second.return?.();
  });
});

describe('composite identity subscription wiring', () => {
  const compositeDatamodel: DatamodelDocument = {
    models: [{
      name: 'PostTag',
      fields: [
        field({ name: 'postId', type: 'String' }),
        field({ name: 'tagId', type: 'String' }),
        field({ name: 'label', type: 'String' }),
      ],
      primaryKey: { fields: ['postId', 'tagId'] },
    }],
    enums: [],
  };

  function compositeClient() {
    return {
      postTag: {
        findMany: jest.fn().mockResolvedValue([]),
        findUnique: jest.fn().mockResolvedValue(null),
        findFirst: jest.fn().mockResolvedValue(null),
        create: jest.fn().mockResolvedValue({}),
        update: jest.fn().mockResolvedValue({}),
        updateMany: jest.fn().mockResolvedValue({ count: 0 }),
        delete: jest.fn().mockResolvedValue({}),
        deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
      },
    };
  }

  function compositeSchema(
    client: ReturnType<typeof compositeClient>,
    bus: FakeBus,
    authorization?: AuthorizationProvider,
  ) {
    return buildGolemSchema({
      datamodel: compositeDatamodel,
      client,
      models: { PostTag: { subscriptions: true } },
      eventBus: bus,
      authorization,
      defaults: { checkWriteResults: false, checkReadFields: false },
    });
  }

  it.each([
    ['unnamed', undefined],
    ['named', 'postTagKey'],
  ] as const)('generates the same ordered identity object for a %s compound primary key', (_kind, name) => {
    const schema = buildGolemSchema({
      datamodel: {
        ...compositeDatamodel,
        models: [{ ...compositeDatamodel.models[0]!, primaryKey: {
          ...(name ? { name } : {}),
          fields: ['postId', 'tagId'],
        } }],
      },
      client: compositeClient(),
      models: { PostTag: { subscriptions: true } },
      eventBus: new FakeBus(),
    });
    const identity = schema.getType('PostTagEventIdentity') as {
      getFields(): Record<string, unknown>;
    };

    expect(Object.keys(identity.getFields())).toEqual(['postId', 'tagId']);
  });

  it('delivers an ordered identity object and looks up by a scalar conjunction', async () => {
    const bus = new FakeBus();
    const client = compositeClient();
    client.postTag.findFirst.mockResolvedValue({ label: 'joined' });
    const schema = compositeSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { postTagEvents { type id { postId tagId } entity { label } } }',
      ),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({
      type: 'CREATED',
      model: 'PostTag',
      id: { postId: 'p1', tagId: 't1' },
    });

    expect((await next).value.data.postTagEvents).toEqual({
      type: 'CREATED',
      id: { postId: 'p1', tagId: 't1' },
      entity: { label: 'joined' },
    });
    expect(client.postTag.findFirst).toHaveBeenCalledWith({
      where: { postId: 'p1', tagId: 't1' },
      select: { label: true },
    });
    await iterator.return?.();
  });

  it('combines composite identity components with subscription filters', async () => {
    const bus = new FakeBus();
    const client = compositeClient();
    client.postTag.findFirst.mockResolvedValue({ label: 'match' });
    const schema = compositeSchema(client, bus);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { postTagEvents(where: { label: { equals: "match" } }) { id { postId tagId } } }',
      ),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    bus.push({
      type: 'UPDATED',
      model: 'PostTag',
      id: { postId: 'p2', tagId: 't2' },
    });
    await next;

    expect(client.postTag.findFirst).toHaveBeenCalledWith({
      where: {
        AND: [
          { postId: 'p2', tagId: 't2' },
          { label: { equals: 'match' } },
        ],
      },
      select: { postId: true, tagId: true },
    });
    await iterator.return?.();
  });

  it('authorizes a composite deletion snapshot and exposes no deleted entity', async () => {
    const bus = new FakeBus();
    const client = compositeClient();
    const authorization: AuthorizationProvider = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      check: jest.fn(async () => true),
    };
    const schema = compositeSchema(client, bus, authorization);
    const iterator = (await subscribe({
      schema,
      document: parse(
        'subscription { postTagEvents { type id { postId tagId } entity { label } } }',
      ),
      contextValue: { request: {} },
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    const snapshot = { postId: 'p3', tagId: 't3', label: 'deleted' };
    bus.push({ type: 'DELETED', model: 'PostTag', id: { postId: 'p3', tagId: 't3' }, entity: snapshot });

    expect((await next).value.data.postTagEvents).toEqual({
      type: 'DELETED',
      id: { postId: 'p3', tagId: 't3' },
      entity: null,
    });
    expect(authorization.check).toHaveBeenCalledWith(
      'read',
      'PostTag',
      snapshot,
      { request: {} },
    );
    await iterator.return?.();
  });
});

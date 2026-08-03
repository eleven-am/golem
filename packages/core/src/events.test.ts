import { parse, subscribe } from 'graphql';
import { GolemEventBus, GolemEventPayload } from './events';
import { buildGolemSchema } from './schema';
import { field } from './testing';
import { DatamodelDocument } from './datamodel';
import { AuthorizationProvider } from './authorization';

class FakeBus implements GolemEventBus {
  published: Array<{ topic: string; payload: GolemEventPayload }> = [];
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
});

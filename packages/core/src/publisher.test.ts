import { DatamodelDocument } from './datamodel';
import { GolemEventBus, GolemEventPayload } from './events';
import { createEventPublisher } from './publisher';
import { field } from './testing';

const datamodel: DatamodelDocument = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
      ],
    },
    {
      name: 'Log',
      fields: [field({ name: 'entry', type: 'String' })],
    },
  ],
  enums: [],
};

function busSpy() {
  const published: Array<{ topic: string; payload: GolemEventPayload }> = [];
  const bus: GolemEventBus = {
    publish: async (topic, payload) => {
      published.push({ topic, payload });
    },
    iterate: (async function* () {})() as never,
  };
  return { bus, published };
}

describe('createEventPublisher', () => {
  it('publishes create, update, both upsert branches and delete with the row id', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const query = jest.fn().mockResolvedValue({ id: 'u1' });

    await publisher({ model: 'User', operation: 'create', args: {}, query });
    await publisher({ model: 'User', operation: 'update', args: {}, query });
    await publisher({
      model: 'User', operation: 'upsert', args: { where: { id: 'new' } }, query,
      findExisting: jest.fn().mockResolvedValue(null),
    });
    await publisher({
      model: 'User', operation: 'upsert', args: { where: { id: 'u1' } }, query,
      findExisting: jest.fn().mockResolvedValue({ id: 'u1' }),
    });
    await publisher({ model: 'User', operation: 'delete', args: {}, query });
    expect(published.map((p) => p.payload.type)).toEqual([
      'CREATED', 'UPDATED', 'CREATED', 'UPDATED', 'DELETED',
    ]);
    expect(published.every((p) => p.topic === 'golem.User' && p.payload.id === 'u1')).toBe(true);
  });

  it('unions the primary key into narrow selects and returns the query result', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const query = jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' });

    const result = await publisher({
      model: 'User',
      operation: 'update',
      args: { where: { id: 'u1' }, select: { email: true } },
      query,
    });
    expect(query).toHaveBeenCalledWith({
      where: { id: 'u1' },
      select: { email: true, id: true },
    });
    expect(result).toEqual({ id: 'u1', email: 'a@b.c' });
    expect(published).toHaveLength(1);
  });

  it('recovers omitted primary keys for events without returning them to the caller', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const query = jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' });

    const result = await publisher({
      model: 'User',
      operation: 'delete',
      args: { where: { id: 'u1' }, omit: { id: true } },
      query,
    });

    expect(query).toHaveBeenCalledWith({
      where: { id: 'u1' },
      omit: { id: false },
    });
    expect(result).toEqual({ email: 'a@b.c' });
    expect(published).toEqual([
      {
        topic: 'golem.User',
        payload: {
          type: 'DELETED',
          model: 'User',
          id: 'u1',
          entity: { id: 'u1', email: 'a@b.c' },
        },
      },
    ]);
  });

  it('stays silent for batch operations, reads and unlisted models', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const query = jest.fn().mockResolvedValue({ count: 2 });

    await publisher({ model: 'User', operation: 'updateMany', args: {}, query });
    await publisher({ model: 'User', operation: 'deleteMany', args: {}, query });
    await publisher({ model: 'User', operation: 'findMany', args: {}, query });
    await publisher({ model: 'Post', operation: 'create', args: {}, query });
    expect(published).toEqual([]);
    expect(query).toHaveBeenCalledTimes(4);
  });

  it('passes through models without a primary key', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['Log']) });
    const query = jest.fn().mockResolvedValue({ entry: 'x' });

    await publisher({ model: 'Log', operation: 'create', args: {}, query });
    expect(published).toEqual([]);
  });
});

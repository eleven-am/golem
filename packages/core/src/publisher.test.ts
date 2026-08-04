import { DatamodelDocument } from './datamodel';
import { GolemEventBus, GolemEventPayload } from './events';
import { createEventPublisher } from './publisher';
import { GolemConflictError, GolemValidationError } from './errors';
import { withBufferedEvents } from './event-buffer';
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
    {
      name: 'PostTag',
      fields: [
        field({ name: 'postId', type: 'String' }),
        field({ name: 'tagId', type: 'String' }),
        field({ name: 'label', type: 'String' }),
      ],
      primaryKey: { fields: ['postId', 'tagId'] },
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

  it('unions the primary key into narrow selects without leaking the injected field', async () => {
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
    expect(result).toEqual({ email: 'a@b.c' });
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

  it('publishes every composite primary-key component in declared order', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({
      datamodel,
      eventBus: bus,
      models: new Set(['PostTag']),
    });
    const query = jest.fn().mockResolvedValue({ postId: 'p1', tagId: 't1', label: 'new' });
    const findExisting = jest.fn().mockResolvedValue({ postId: 'p1', tagId: 't1' });

    await publisher({
      model: 'PostTag',
      operation: 'upsert',
      args: { where: { postId_tagId: { postId: 'p1', tagId: 't1' } } },
      query,
      findExisting,
    });

    expect(findExisting).toHaveBeenCalledWith(
      { postId_tagId: { postId: 'p1', tagId: 't1' } },
      { postId: true, tagId: true },
    );
    expect(published).toEqual([{ topic: 'golem.PostTag', payload: {
      type: 'UPDATED',
      model: 'PostTag',
      id: { postId: 'p1', tagId: 't1' },
    } }]);
  });

  it('injects and restores all composite identity fields for select and omit', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({
      datamodel,
      eventBus: bus,
      models: new Set(['PostTag']),
    });
    const query = jest.fn().mockResolvedValue({ postId: 'p1', tagId: 't1', label: 'x' });

    const selected = await publisher({
      model: 'PostTag',
      operation: 'update',
      args: { select: { label: true, postId: true } },
      query,
    });
    expect(query).toHaveBeenLastCalledWith({
      select: { label: true, postId: true, tagId: true },
    });
    expect(selected).toEqual({ postId: 'p1', label: 'x' });

    const omitted = await publisher({
      model: 'PostTag',
      operation: 'delete',
      args: { omit: { postId: true, tagId: true } },
      query,
    });
    expect(query).toHaveBeenLastCalledWith({
      omit: { postId: false, tagId: false },
    });
    expect(omitted).toEqual({ label: 'x' });
    expect(published[1].payload).toEqual({
      type: 'DELETED',
      model: 'PostTag',
      id: { postId: 'p1', tagId: 't1' },
      entity: { postId: 'p1', tagId: 't1', label: 'x' },
    });
  });

  it('updates a bounded identity set and publishes deterministic per-row events', async () => {
    const published: GolemEventPayload[][] = [];
    const bus: GolemEventBus = {
      publish: jest.fn(),
      publishMany: async (_topic, events) => { published.push([...events]); },
      iterate: (async function* () {})() as never,
    };
    const publisher = createEventPublisher({
      datamodel,
      eventBus: bus,
      models: new Set(['PostTag']),
      batch: { maxRows: 2 },
    });
    const rows = [
      { postId: 'p1', tagId: 't1' },
      { postId: 'p1', tagId: 't2' },
    ];
    const delegate = {
      findMany: jest.fn().mockResolvedValue(rows),
      updateManyAndReturn: jest.fn().mockResolvedValue([...rows].reverse()),
      deleteMany: jest.fn(),
    };
    const result = await publisher({
      model: 'PostTag',
      operation: 'updateMany',
      args: { where: { postId: 'p1' }, data: { label: 'new' } },
      query: jest.fn(),
      batch: { suppressed: false, run: (work) => work(delegate) },
    });

    expect(delegate.findMany).toHaveBeenCalledWith({
      where: { postId: 'p1' },
      select: { postId: true, tagId: true },
      orderBy: [{ postId: 'asc' }, { tagId: 'asc' }],
      take: 3,
    });
    expect(delegate.updateManyAndReturn).toHaveBeenCalledWith({
      where: { OR: rows },
      data: { label: 'new' },
      select: { postId: true, tagId: true, label: true },
    });
    expect(result).toEqual({ count: 2 });
    expect(published).toEqual([[
      { type: 'UPDATED', model: 'PostTag', id: { postId: 'p1', tagId: 't1' } },
      { type: 'UPDATED', model: 'PostTag', id: { postId: 'p1', tagId: 't2' } },
    ]]);
  });

  it('rejects row caps, payload caps, and primary-key updates before mutation', async () => {
    const query = jest.fn();
    const overRows = {
      findMany: jest.fn().mockResolvedValue([{ id: '1' }, { id: '2' }]),
      updateManyAndReturn: jest.fn(),
      deleteMany: jest.fn(),
    };
    const rowPublisher = createEventPublisher({
      datamodel,
      eventBus: busSpy().bus,
      models: new Set(['User']),
      batch: { maxRows: 1 },
    });
    await expect(rowPublisher({
      model: 'User', operation: 'updateMany', args: { data: { email: 'x' } }, query,
      batch: { suppressed: false, run: (work) => work(overRows) },
    })).rejects.toBeInstanceOf(GolemValidationError);
    expect(overRows.updateManyAndReturn).not.toHaveBeenCalled();

    const large = {
      findMany: jest.fn().mockResolvedValue([{ id: '1', email: 'a'.repeat(100) }]),
      updateManyAndReturn: jest.fn(),
      deleteMany: jest.fn(),
    };
    const bytePublisher = createEventPublisher({
      datamodel,
      eventBus: busSpy().bus,
      models: new Set(['User']),
      batch: { maxPayloadBytes: 1 },
    });
    await expect(bytePublisher({
      model: 'User', operation: 'deleteMany', args: {}, query,
      batch: { suppressed: false, run: (work) => work(large) },
    })).rejects.toBeInstanceOf(GolemValidationError);
    expect(large.deleteMany).not.toHaveBeenCalled();

    const run = jest.fn();
    await expect(rowPublisher({
      model: 'User', operation: 'updateMany', args: { data: { id: undefined } }, query,
      batch: { suppressed: false, run },
    })).rejects.toThrow('Eventful updateMany cannot modify primary key fields on User');
    expect(run).not.toHaveBeenCalled();
  });

  it('deletes exactly selected identities, verifies the count, and retains snapshots', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const snapshots = [
      { id: 'u1', email: 'one@example.com' },
      { id: 'u2', email: 'two@example.com' },
    ];
    const delegate = {
      findMany: jest.fn().mockResolvedValue(snapshots),
      updateManyAndReturn: jest.fn(),
      deleteMany: jest.fn().mockResolvedValue({ count: 2 }),
    };
    const result = await publisher({
      model: 'User', operation: 'deleteMany', args: { where: { email: { contains: '@' } } },
      query: jest.fn(),
      batch: { suppressed: false, run: (work) => work(delegate) },
    });

    expect(delegate.findMany).toHaveBeenCalledWith({
      where: { email: { contains: '@' } },
      orderBy: [{ id: 'asc' }],
      take: 1001,
    });
    expect(delegate.deleteMany).toHaveBeenCalledWith({
      where: { OR: [{ id: 'u1' }, { id: 'u2' }] },
    });
    expect(result).toEqual({ count: 2 });
    expect(published.map(({ payload }) => payload)).toEqual([
      { type: 'DELETED', model: 'User', id: 'u1', entity: snapshots[0] },
      { type: 'DELETED', model: 'User', id: 'u2', entity: snapshots[1] },
    ]);
  });

  it('raises a stable conflict and publishes nothing on a delete concurrency mismatch', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const delegate = {
      findMany: jest.fn().mockResolvedValue([{ id: 'u1', email: 'x' }]),
      updateManyAndReturn: jest.fn(),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    };
    await expect(publisher({
      model: 'User', operation: 'deleteMany', args: {}, query: jest.fn(),
      batch: { suppressed: false, run: (work) => work(delegate) },
    })).rejects.toBeInstanceOf(GolemConflictError);
    expect(published).toEqual([]);
  });

  it('discards a buffered batch envelope when its surrounding transaction rolls back', async () => {
    const { bus, published } = busSpy();
    const publisher = createEventPublisher({ datamodel, eventBus: bus, models: new Set(['User']) });
    const delegate = {
      findMany: jest.fn().mockResolvedValue([{ id: 'u1' }]),
      updateManyAndReturn: jest.fn().mockResolvedValue([{ id: 'u1' }]),
      deleteMany: jest.fn(),
    };
    await expect(withBufferedEvents(async () => {
      await publisher({
        model: 'User', operation: 'updateMany', args: { data: { email: 'x' } }, query: jest.fn(),
        batch: { suppressed: false, run: (work) => work(delegate) },
      });
      throw new Error('rollback');
    })).rejects.toThrow('rollback');
    expect(published).toEqual([]);
  });
});

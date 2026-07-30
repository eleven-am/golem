import { CompiledReadEvent } from './compiled-read';
import { GolemConflictError, GolemNotFoundError, GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  { name: 'User', fields: [field({ name: 'id', type: 'String', isId: true })] },
];

function engineWith(user: Record<string, jest.Mock>) {
  return new GolemEngine({ user }, models);
}

describe('GolemEngine', () => {
  it('passes create requests through to the delegate', async () => {
    const create = jest.fn().mockResolvedValue({ id: '1' });
    const engine = engineWith({ create });
    const result = await engine.create({ model: 'User', data: { email: 'a' }, select: { id: true } });
    expect(result).toEqual({ id: '1' });
    expect(create).toHaveBeenCalledWith({ data: { email: 'a' }, select: { id: true } });
  });

  it('translates P2025 into GolemNotFoundError', async () => {
    const update = jest.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'P2025' }));
    const engine = engineWith({ update });
    await expect(
      engine.update({ model: 'User', where: { id: '1' }, data: {}, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);
  });

  it('translates P2002 into GolemConflictError', async () => {
    const create = jest.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'P2002' }));
    const engine = engineWith({ create });
    await expect(
      engine.create({ model: 'User', data: {}, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemConflictError);
  });

  it('translates a unique race after a missing upsert probe into GolemConflictError', async () => {
    const findFirst = jest.fn().mockResolvedValue(null);
    const create = jest.fn().mockRejectedValue(
      Object.assign(new Error('unique race'), { code: 'P2002' }),
    );
    const engine = engineWith({ findFirst, create });

    await expect(engine.upsert({
      model: 'User',
      where: { id: 'raced' },
      create: { id: 'raced' },
      update: { id: 'raced' },
    })).rejects.toBeInstanceOf(GolemConflictError);
    expect(findFirst).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledTimes(1);
  });

  it('translates relation constraint codes into GolemValidationError', async () => {
    const update = jest.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'P2014' }));
    const engine = engineWith({ update });
    await expect(
      engine.update({ model: 'User', where: { id: '1' }, data: {}, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('translates prisma validation errors into GolemValidationError', async () => {
    const error = new Error('both create and connect');
    error.name = 'PrismaClientValidationError';
    const create = jest.fn().mockRejectedValue(error);
    const engine = engineWith({ create });
    await expect(
      engine.create({ model: 'User', data: {}, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });

  it('rethrows unknown errors untouched', async () => {
    const boom = new Error('disk on fire');
    const create = jest.fn().mockRejectedValue(boom);
    const engine = engineWith({ create });
    await expect(engine.create({ model: 'User', data: {}, select: { id: true } })).rejects.toBe(boom);
  });

  it('rejects unknown models', async () => {
    const engine = engineWith({ create: jest.fn() });
    await expect(
      engine.create({ model: 'Ghost', data: {}, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });
});

describe('composite primary keys', () => {
  const compositeModels = [
    {
      name: 'PostTag',
      fields: [
        field({ name: 'postId', type: 'String' }),
        field({ name: 'tagId', type: 'String' }),
        field({ name: 'addedAt', type: 'DateTime', hasDefaultValue: true }),
      ],
      primaryKey: { fields: ['postId', 'tagId'] },
    },
  ];

  function compositeProvider() {
    return {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      check: jest.fn(async () => true),
      checkField: jest.fn(async () => true),
    } as never;
  }

  it('resolves a constrained delete through the compound unique identity', async () => {
    const findFirst = jest.fn().mockResolvedValue({ postId: 'p1', tagId: 't1' });
    const del = jest.fn().mockResolvedValue({ postId: 'p1', tagId: 't1' });
    const engine = new GolemEngine({ postTag: { findFirst, delete: del } }, compositeModels, {
      authorization: compositeProvider(),
      checkWriteResults: false,
      checkReadFields: false,
    });

    await engine.delete({
      model: 'PostTag',
      where: { postId_tagId: { postId: 'p1', tagId: 't1' } },
      context: { req: {} },
    });

    expect(findFirst).toHaveBeenCalledWith({
      where: { AND: [{ postId: 'p1', tagId: 't1' }, {}] },
      select: { postId: true, tagId: true },
    });
    expect(del.mock.calls[0][0].where).toEqual({ postId_tagId: { postId: 'p1', tagId: 't1' } });
  });

  it('reports a clear error when a model has no primary key at all', async () => {
    const engine = new GolemEngine(
      { thing: { findFirst: jest.fn(), delete: jest.fn() } },
      [{ name: 'Thing', fields: [field({ name: 'label', type: 'String' })] }],
      { authorization: compositeProvider(), checkWriteResults: false, checkReadFields: false },
    );
    await expect(
      engine.delete({ model: 'Thing', where: { label: 'x' }, context: { req: {} } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });
});

describe('composite updateMany identity', () => {
  const compositeModels = [
    {
      name: 'PostTag',
      fields: [
        field({ name: 'postId', type: 'String' }),
        field({ name: 'tagId', type: 'String' }),
        field({ name: 'addedAt', type: 'DateTime', hasDefaultValue: true }),
      ],
      primaryKey: { fields: ['postId', 'tagId'] },
    },
  ];

  it('targets composite rows by scalar identity, never the compound accessor', async () => {
    const rows = [
      { postId: 'p1', tagId: 't1', addedAt: 1 },
      { postId: 'p1', tagId: 't2', addedAt: 1 },
    ];
    const findMany = jest.fn().mockResolvedValue(rows);
    const updateMany = jest.fn().mockResolvedValue({ count: 2 });
    const delegates = { postTag: { findMany, updateMany } };
    const client = {
      ...delegates,
      $transaction: jest.fn(async (fn: (tx: unknown) => Promise<unknown>) => fn(delegates)),
    };
    const engine = new GolemEngine(client, compositeModels, {
      authorization: {
        authorize: jest.fn(async () => undefined),
        constrain: jest.fn(async () => ({})),
        check: jest.fn(async () => true),
        checkField: jest.fn(async () => true),
      } as never,
      checkWriteResults: true,
      checkReadFields: false,
    });

    const result = await engine.updateMany({
      model: 'PostTag',
      where: { postId: 'p1' },
      data: { addedAt: 2 },
      context: { req: {} },
    });

    expect(result).toEqual({ count: 2 });
    expect(updateMany).toHaveBeenCalledWith({
      where: { OR: [{ postId: 'p1', tagId: 't1' }, { postId: 'p1', tagId: 't2' }] },
      data: { addedAt: 2 },
    });
  });
});

describe('compound unique selectors in filterable where', () => {
  const unnamedModels = [
    {
      name: 'Branch',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'authorId', type: 'String' }),
        field({ name: 'name', type: 'String' }),
      ],
      uniqueIndexes: [{ fields: ['authorId', 'name'] }],
    },
  ];

  it('unwraps an unnamed compound unique selector in the upsert probe', async () => {
    const findFirst = jest.fn().mockResolvedValue(null);
    const create = jest.fn().mockResolvedValue({ id: 'b1' });
    const engine = new GolemEngine({ branch: { findFirst, create } }, unnamedModels);

    await engine.upsert({
      model: 'Branch',
      where: { authorId_name: { authorId: 'a1', name: 'main' } },
      create: { authorId: 'a1', name: 'main' },
      update: { name: 'main' },
      select: { id: true },
    });

    expect(findFirst).toHaveBeenCalledWith({
      where: { authorId: 'a1', name: 'main' },
      select: { id: true },
    });
  });

  it('unwraps a named compound unique selector and preserves sibling filters', async () => {
    const namedModels = [
      {
        name: 'Branch',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'authorId', type: 'String' }),
          field({ name: 'name', type: 'String' }),
        ],
        uniqueIndexes: [{ name: 'authorNameKey', fields: ['authorId', 'name'] }],
      },
    ];
    const findFirst = jest.fn().mockResolvedValue(null);
    const create = jest.fn().mockResolvedValue({ id: 'b1' });
    const engine = new GolemEngine({ branch: { findFirst, create } }, namedModels);

    await engine.upsert({
      model: 'Branch',
      where: { authorNameKey: { authorId: 'a1', name: 'main' }, id: 'b9' },
      create: { authorId: 'a1', name: 'main' },
      update: { name: 'main' },
      select: { id: true },
    });

    expect(findFirst).toHaveBeenCalledWith({
      where: { id: 'b9', authorId: 'a1', name: 'main' },
      select: { id: true },
    });
  });

  it('leaves a where without any compound selector untouched', async () => {
    const findFirst = jest.fn().mockResolvedValue(null);
    const create = jest.fn().mockResolvedValue({ id: 'b1' });
    const engine = new GolemEngine({ branch: { findFirst, create } }, unnamedModels);

    await engine.upsert({
      model: 'Branch',
      where: { id: 'b9' },
      create: { authorId: 'a1', name: 'main' },
      update: { name: 'main' },
      select: { id: true },
    });

    expect(findFirst).toHaveBeenCalledWith({ where: { id: 'b9' }, select: { id: true } });
  });

  it('passes through a real scalar field that shares a compound selector name', async () => {
    const collisionModels = [
      {
        name: 'Branch',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'authorId', type: 'String' }),
          field({ name: 'name', type: 'String' }),
          field({ name: 'authorId_name', type: 'String' }),
        ],
        uniqueIndexes: [{ fields: ['authorId', 'name'] }],
      },
    ];
    const findFirst = jest.fn().mockResolvedValue(null);
    const create = jest.fn().mockResolvedValue({ id: 'b1' });
    const engine = new GolemEngine({ branch: { findFirst, create } }, collisionModels);

    await engine.upsert({
      model: 'Branch',
      where: { authorId_name: { contains: 'x' } },
      create: { authorId: 'a1', name: 'main' },
      update: { name: 'main' },
      select: { id: true },
    });

    expect(findFirst).toHaveBeenCalledWith({
      where: { authorId_name: { contains: 'x' } },
      select: { id: true },
    });
  });

  it('falls back to Prisma when the client exposes no $queryRawUnsafe to run a compiled read on', async () => {
    const findMany = jest.fn().mockResolvedValue([{ id: '1' }]);
    const engine = new GolemEngine({ user: { findMany } }, models, { provider: 'sqlite' });
    const events: CompiledReadEvent[] = [];
    engine.observeCompiledRead((event) => events.push(event));

    const rows = await engine.findMany({ model: 'User', select: { id: true }, compiled: true });

    expect(events).toEqual([
      expect.objectContaining({
        model: 'User',
        operation: 'findMany',
        outcome: 'fallback',
        reason: 'client',
      }),
    ]);
    expect(rows).toEqual([{ id: '1' }]);
    expect(findMany).toHaveBeenCalledWith({ select: { id: true } });
  });
});

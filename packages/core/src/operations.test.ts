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

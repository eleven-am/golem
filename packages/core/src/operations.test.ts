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

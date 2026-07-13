import { GolemValidationError } from './errors';
import { HookRegistry } from './hooks';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
    ],
  },
];

function fakeUserDelegate() {
  return {
    findMany: jest.fn().mockResolvedValue([]),
    findUnique: jest.fn().mockResolvedValue(null),
    create: jest.fn().mockResolvedValue({ id: 'u1', email: 'a@b.c' }),
    delete: jest.fn().mockResolvedValue({ id: 'u1' }),
  };
}

describe('engine hooks', () => {
  it('chains before hooks and feeds each the previous output', async () => {
    const registry = new HookRegistry();
    registry.registerBefore('User', 'create', (req) => ({
      ...req,
      data: { ...req.data, email: req.data.email.toLowerCase() },
    }));
    registry.registerBefore('User', 'create', (req) => ({
      ...req,
      data: { ...req.data, tagged: true },
    }));
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await engine.create({ model: 'User', data: { email: 'A@B.C' }, select: { id: true } });
    expect(user.create).toHaveBeenCalledWith({
      data: { email: 'a@b.c', tagged: true },
      select: { id: true },
    });
  });

  it('leaves the request unchanged when a before hook returns nothing', async () => {
    const registry = new HookRegistry();
    const observer = jest.fn();
    registry.registerBefore('User', 'create', observer);
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await engine.create({ model: 'User', data: { email: 'a@b.c' }, select: { id: true } });
    expect(observer).toHaveBeenCalledWith(
      expect.objectContaining({ data: { email: 'a@b.c' } }),
      { model: 'User', operation: 'create', context: undefined },
    );
    expect(user.create).toHaveBeenCalledWith({ data: { email: 'a@b.c' }, select: { id: true } });
  });

  it('aborts the operation when a before hook throws', async () => {
    const registry = new HookRegistry();
    registry.registerBefore('User', 'delete', () => {
      throw new GolemValidationError('protected');
    });
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await expect(
      engine.delete({ model: 'User', where: { id: 'u1' }, select: { id: true } }),
    ).rejects.toThrow('protected');
    expect(user.delete).not.toHaveBeenCalled();
  });

  it('runs after hooks with the operation result', async () => {
    const registry = new HookRegistry();
    const seen: unknown[] = [];
    registry.registerAfter('User', 'create', async (result) => {
      seen.push(result);
    });
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await engine.create({ model: 'User', data: { email: 'a@b.c' }, select: { id: true } });
    expect(seen).toEqual([{ id: 'u1', email: 'a@b.c' }]);
  });

  it('transforms find requests through before hooks', async () => {
    const registry = new HookRegistry();
    registry.registerBefore('User', 'findMany', (req) => ({
      ...req,
      where: { AND: [{ email: { contains: 'active' } }, req.where ?? {}] },
    }));
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await engine.findMany({ model: 'User', where: { id: { equals: 'u1' } }, select: { id: true } });
    expect(user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        where: { AND: [{ email: { contains: 'active' } }, { id: { equals: 'u1' } }] },
      }),
    );
  });

  it('passes the request context to hooks', async () => {
    const registry = new HookRegistry();
    const seen: unknown[] = [];
    registry.registerBefore('User', 'create', (_req, ctx) => {
      seen.push(ctx.context);
    });
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, { hooks: registry });

    await engine.create({
      model: 'User',
      data: { email: 'a@b.c' },
      select: { id: true },
      context: { requestId: 'r1' },
    });
    expect(seen).toEqual([{ requestId: 'r1' }]);
  });

  it('rejects take values above the configured limit after hooks run', async () => {
    const user = fakeUserDelegate();
    const engine = new GolemEngine({ user }, models, {
      takeLimits: new Map([['User', 10]]),
    });

    await expect(
      engine.findMany({ model: 'User', take: 11, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(user.findMany).not.toHaveBeenCalled();
    await expect(engine.findMany({ model: 'User', take: 10, select: { id: true } })).resolves.toEqual(
      [],
    );
    await expect(
      engine.findMany({ model: 'User', take: -11, select: { id: true } }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(engine.findMany({ model: 'User', take: -10, select: { id: true } })).resolves.toEqual(
      [],
    );
  });
});

import { AuthorizationProvider, FieldClassification } from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemForbiddenError } from './errors';
import { withBufferedEvents, bufferEvent } from './event-buffer';
import { buildModelMetadata } from './model-meta';
import { GolemEngine } from './operations';
import { field } from './testing';
import { planVerification, verifyUpdatedRow, VerifyContext } from './verify';

const post: DatamodelModel = {
  name: 'Post',
  fields: [
    field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
    field({ name: 'title', type: 'String' }),
    field({ name: 'type', type: 'PostType', kind: 'enum', hasDefaultValue: true }),
    field({ name: 'published', type: 'Boolean', hasDefaultValue: true }),
    field({ name: 'views', type: 'Int', hasDefaultValue: true }),
    field({ name: 'author', type: 'User', kind: 'object', relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
    field({ name: 'authorId', type: 'String', isReadOnly: true }),
  ],
};

const user: DatamodelModel = {
  name: 'User',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'email', type: 'String', isUnique: true }),
    field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
  ],
};

const models = [post, user];
const ctx = { req: {} };

function txClient(delegates: Record<string, Record<string, jest.Mock>>) {
  return {
    ...delegates,
    $transaction: jest.fn(async (fn: (tx: unknown) => Promise<unknown>) => fn(delegates)),
  };
}

function provider(overrides: Partial<AuthorizationProvider> = {}): AuthorizationProvider & {
  check: jest.Mock;
  checkField: jest.Mock;
} {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => ({})),
    check: jest.fn(async () => true),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_a, _m, fields: readonly string[]) =>
      Object.fromEntries(fields.map((fieldName) => [fieldName, { access: 'conditional' }]))),
    ...overrides,
  } as never;
}

describe('event buffer', () => {
  it('defers buffered events until the scope resolves', async () => {
    const published: string[] = [];
    const result = await withBufferedEvents(async () => {
      bufferEvent({ publish: async () => void published.push('a') });
      expect(published).toEqual([]);
      return 'done';
    });
    expect(result).toBe('done');
    expect(published).toEqual(['a']);
  });

  it('discards buffered events when the scope throws', async () => {
    const published: string[] = [];
    await expect(
      withBufferedEvents(async () => {
        bufferEvent({ publish: async () => void published.push('a') });
        throw new Error('rollback');
      }),
    ).rejects.toThrow('rollback');
    expect(published).toEqual([]);
  });

  it('keeps nested events for the outer commit boundary', async () => {
    const published: string[] = [];
    await withBufferedEvents(async () => {
      await withBufferedEvents(async () => {
        bufferEvent({ publish: async () => void published.push('nested') });
      });
      expect(published).toEqual([]);
    });
    expect(published).toEqual(['nested']);
  });

  it('discards a failed nested scope without losing earlier outer events', async () => {
    const published: string[] = [];
    await withBufferedEvents(async () => {
      bufferEvent({ publish: async () => void published.push('outer') });
      await expect(withBufferedEvents(async () => {
        bufferEvent({ publish: async () => void published.push('rolled back') });
        throw new Error('nested rollback');
      })).rejects.toThrow('nested rollback');
    });
    expect(published).toEqual(['outer']);
  });

  it('publishes immediately outside any scope', () => {
    expect(bufferEvent({ publish: async () => undefined })).toBe(false);
  });
});

describe('transactional create verification', () => {
  it('checks the actual database row including server-generated values', async () => {
    const row = { id: 'cuid-real', title: 'x', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const delegates = {
      post: {
        create: jest.fn().mockResolvedValue(row),
        findUnique: jest.fn().mockResolvedValue({ id: 'cuid-real' }),
      },
    };
    const client = txClient(delegates);
    const authz = provider();
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkReadFields: false,
    });

    await engine.create({ model: 'Post', data: { title: 'x' }, context: ctx });
    expect(client.$transaction).toHaveBeenCalled();
    expect(authz.check).toHaveBeenCalledWith('create', 'Post', row, ctx);
    expect(authz.checkField).toHaveBeenCalledWith('create', 'Post', row, 'title', ctx);
  });

  it('rolls back and surfaces FORBIDDEN when the real row fails the check', async () => {
    const delegates = {
      post: {
        create: jest.fn().mockResolvedValue({ id: 'p1', type: 'EDITORIAL' }),
        findUnique: jest.fn(),
      },
    };
    const client = txClient(delegates);
    const authz = provider({ check: jest.fn(async () => false) });
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await expect(
      engine.create({ model: 'Post', data: { title: 'x', type: 'EDITORIAL' }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(delegates.post.findUnique).not.toHaveBeenCalled();
  });

  it('checks connect-implied foreign keys against the resolved row', async () => {
    const row = { id: 'p1', title: 'x', type: 'PERSONAL', published: false, views: 0, authorId: 'u-roy' };
    const delegates = {
      post: {
        create: jest.fn().mockResolvedValue(row),
        findUnique: jest.fn().mockResolvedValue(row),
      },
    };
    const client = txClient(delegates);
    const authz = provider({
      checkField: jest.fn(async (_a, _m, _e, f: string) => f !== 'authorId'),
    });
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await expect(
      engine.create({
        model: 'Post',
        data: { title: 'x', author: { connect: { email: 'roy@x.com' } } },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot create field "authorId" on Post');
  });
});

describe('transactional update verification', () => {
  const before = { id: 'p1', title: 'old', type: 'PERSONAL', published: false, views: 3, authorId: 'u1' };

  function updateClient(after: Record<string, unknown>) {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue(before),
        update: jest.fn().mockResolvedValue(after),
        findUnique: jest.fn().mockResolvedValue({ id: 'p1' }),
      },
    };
    return { client: txClient(delegates), delegates };
  }

  it('verifies atomic operations against the computed result', async () => {
    const { client } = updateClient({ ...before, views: 4 });
    const authz = provider();
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkWriteResults: true,
      checkReadFields: false,
    });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { views: { increment: 1 } }, context: ctx });
    expect(authz.check).toHaveBeenCalledWith('update', 'Post', expect.objectContaining({ views: 4 }), ctx);
    expect(authz.checkField).toHaveBeenCalledWith('update', 'Post', before, 'views', ctx);
  });

  it('passes no-op writes to restricted fields', async () => {
    const { client } = updateClient({ ...before });
    const authz = provider({
      checkField: jest.fn(async (_a, _m, _e, f: string) => f !== 'title'),
    });
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkWriteResults: true,
      checkReadFields: false,
    });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: { set: 'old' } }, context: ctx });
    expect(authz.checkField).not.toHaveBeenCalled();
  });

  it('rejects changes to denied fields found by diff', async () => {
    const { client, delegates } = updateClient({ ...before, title: 'new' });
    const authz = provider({
      checkField: jest.fn(async (_a, _m, _e, f: string) => f !== 'title'),
    });
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await expect(
      engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx }),
    ).rejects.toThrow('Cannot update field "title" on Post');
    expect(delegates.post.findUnique).not.toHaveBeenCalled();
  });
});

describe('nested relation diff verification', () => {
  it('checks appeared children as real rows and rejects denied ones', async () => {
    const beforeUser = { id: 'u1', email: 'a@b.c', posts: [{ id: 'old1', title: 'kept', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' }] };
    const afterUser = {
      ...beforeUser,
      posts: [
        beforeUser.posts[0],
        { id: 'new1', title: 'sneaky', type: 'EDITORIAL', published: false, views: 0, authorId: 'u1' },
      ],
    };
    const delegates = {
      user: {
        findFirst: jest.fn().mockResolvedValue(beforeUser),
        update: jest.fn().mockResolvedValue(afterUser),
        findUnique: jest.fn(),
      },
    };
    const client = txClient(delegates);
    const authz = provider({
      check: jest.fn(async (_a: string, model: string, row: { type?: string }) =>
        !(model === 'Post' && row.type === 'EDITORIAL'),
      ),
    });
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await expect(
      engine.update({
        model: 'User',
        where: { id: 'u1' },
        data: { posts: { create: [{ title: 'sneaky', type: 'EDITORIAL' }] } },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot create Post with the provided values');
    expect(authz.check).not.toHaveBeenCalledWith('create', 'Post', expect.objectContaining({ id: 'old1' }), ctx);
  });

  it('checks connected rows under the update action and both actions when ambiguous', async () => {
    const beforeUser = { id: 'u1', email: 'a@b.c', posts: [] as unknown[] };
    const appeared = { id: 'pX', title: 'joined', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const delegates = {
      user: {
        findFirst: jest.fn().mockResolvedValue(beforeUser),
        update: jest.fn().mockResolvedValue({ ...beforeUser, posts: [appeared] }),
        findUnique: jest.fn().mockResolvedValue({ id: 'u1' }),
      },
    };
    const client = txClient(delegates);
    const authz = provider({ classifyFields: undefined });
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkWriteResults: true,
      checkReadFields: false,
    });

    await engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { connect: [{ id: 'pX' }] } },
      context: ctx,
    });
    expect(authz.check).toHaveBeenCalledWith('update', 'Post', appeared, ctx);
    expect(authz.check).not.toHaveBeenCalledWith('create', 'Post', appeared, ctx);

    (authz.check as jest.Mock).mockClear();
    delegates.user.findFirst.mockResolvedValue(beforeUser);
    delegates.user.update.mockResolvedValue({ ...beforeUser, posts: [appeared] });
    await engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { connect: [{ id: 'pX' }], create: [{ title: 'joined' }] } },
      context: ctx,
    });
    expect(authz.check).toHaveBeenCalledWith('update', 'Post', appeared, ctx);
    expect(authz.check).toHaveBeenCalledWith('create', 'Post', appeared, ctx);
  });

  it('checks row and changed fields for an existing child updated in place', async () => {
    const oldPost = { id: 'p1', title: 'old', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const beforeUser = { id: 'u1', email: 'a@b.c', posts: [oldPost] };
    const updatedPost = { ...oldPost, title: 'forbidden' };
    const delegates = {
      user: {
        findFirst: jest.fn().mockResolvedValue(beforeUser),
        update: jest.fn().mockResolvedValue({ ...beforeUser, posts: [updatedPost] }),
        findUnique: jest.fn(),
      },
    };
    const authz = provider({
      checkField: jest.fn(async (_a, model, _row, fieldName) =>
        !(model === 'Post' && fieldName === 'title')),
    });
    const engine = new GolemEngine(txClient(delegates), models, {
      authorization: authz,
      checkWriteResults: true,
    });

    await expect(engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { update: { where: { id: 'p1' }, data: { title: 'forbidden' } } } },
      context: ctx,
    })).rejects.toThrow('Cannot update field "title" on Post');
    expect(authz.check).toHaveBeenCalledWith('update', 'Post', updatedPost, ctx);
  });

  it('checks only rows targeted by a nested updateMany when siblings are unchanged', async () => {
    const target = { id: 'p1', title: 'old', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const sibling = { ...target, id: 'p2', title: 'untouched' };
    const beforeUser = { id: 'u1', email: 'a@b.c', posts: [target, sibling] };
    const afterTarget = { ...target, title: 'new' };
    const delegates = {
      user: {
        findFirst: jest.fn().mockResolvedValue(beforeUser),
        update: jest.fn().mockResolvedValue({ ...beforeUser, posts: [afterTarget, sibling] }),
        findUnique: jest.fn().mockResolvedValue({ id: 'u1' }),
      },
    };
    const authz = provider({
      check: jest.fn(async (_action, model, row: { id?: string }) =>
        !(model === 'Post' && row.id === 'p2')),
    });
    const engine = new GolemEngine(txClient(delegates), models, {
      authorization: authz,
      checkWriteResults: true,
    });

    await expect(engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { updateMany: { where: { id: 'p1' }, data: { title: 'new' } } } },
      context: ctx,
    })).resolves.toEqual({ id: 'u1' });
    expect(authz.check).toHaveBeenCalledWith('update', 'Post', afterTarget, ctx);
    expect(authz.check).not.toHaveBeenCalledWith('update', 'Post', sibling, ctx);
  });

  it.each([
    ['disconnect', { disconnect: [{ id: 'p1' }] }, 'update'],
    ['set', { set: [] }, 'update'],
    ['delete', { delete: [{ id: 'p1' }] }, 'delete'],
    ['deleteMany', { deleteMany: [{ id: 'p1' }] }, 'delete'],
  ])('checks removed children for nested %s', async (_name, envelope, expectedAction) => {
    const oldPost = { id: 'p1', title: 'old', type: 'PERSONAL', published: false, views: 0, authorId: 'u1' };
    const beforeUser = { id: 'u1', email: 'a@b.c', posts: [oldPost] };
    const delegates = {
      user: {
        findFirst: jest.fn().mockResolvedValue(beforeUser),
        update: jest.fn().mockResolvedValue({ ...beforeUser, posts: [] }),
        findUnique: jest.fn(),
      },
    };
    const authz = provider({
      check: jest.fn(async (action, model) => !(action === expectedAction && model === 'Post')),
    });
    const engine = new GolemEngine(txClient(delegates), models, {
      authorization: authz,
      checkWriteResults: true,
    });

    await expect(engine.update({
      model: 'User', where: { id: 'u1' }, data: { posts: envelope }, context: ctx,
    })).rejects.toThrow(`Cannot ${expectedAction} Post with the provided values`);
  });
});

describe('flag off and upsert', () => {
  it('runs no transaction and no checks when the flag is off', async () => {
    const delegates = { post: { create: jest.fn().mockResolvedValue({ id: 'p1' }) } };
    const client = txClient(delegates);
    const authz = provider({ check: jest.fn(async () => false) });
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkWriteResults: false,
      checkReadFields: false,
    });

    await engine.create({ model: 'Post', data: { title: 'x' }, context: ctx });
    expect(client.$transaction).not.toHaveBeenCalled();
    expect(authz.check).not.toHaveBeenCalled();
  });

  it('fails at construction when the flag lacks provider support', () => {
    expect(
      () =>
        new GolemEngine({}, models, {
          authorization: { authorize: jest.fn(), constrain: jest.fn() },
          checkWriteResults: true,
          checkReadFields: false,
        }),
    ).toThrow('does not implement check and checkField');
  });

  it('dispatches upsert to the matching branch', async () => {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValueOnce({ id: 'p1' }).mockResolvedValue(null),
        create: jest.fn().mockResolvedValue({ id: 'p2' }),
        update: jest.fn().mockResolvedValue({ id: 'p1' }),
      },
    };
    const client = txClient(delegates);
    const engine = new GolemEngine(client, models, {});

    await engine.upsert({ model: 'Post', where: { id: 'p1' }, create: { title: 'new' }, update: { title: 'edited' } });
    expect(delegates.post.update).toHaveBeenCalled();
    await engine.upsert({ model: 'Post', where: { id: 'p9' }, create: { title: 'new' }, update: { title: 'edited' } });
    expect(delegates.post.create).toHaveBeenCalled();
  });
});

describe('narrow verification readback (M14)', () => {
  function classifyingProvider(
    perField: Record<string, FieldClassification>,
    constraint: unknown = {},
  ) {
    return {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => constraint),
      check: jest.fn(async () => true),
      checkField: jest.fn(async () => true),
      classifyFields: jest.fn(async (_a: string, _m: string, fields: readonly string[]) =>
        Object.fromEntries(fields.map((f) => [f, perField[f] ?? { access: 'always' }])),
      ),
    } as never as AuthorizationProvider & { check: jest.Mock };
  }

  const before = { id: 'p1', title: 'old', authorId: 'u1' };

  it('narrows the readback to payload, rule and identity columns', async () => {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue(before),
        update: jest.fn().mockResolvedValue({ ...before, title: 'new' }),
        findUnique: jest.fn().mockResolvedValue({ id: 'p1' }),
      },
    };
    const client = txClient(delegates);
    const authz = classifyingProvider(
      { title: { access: 'conditional', requires: ['authorId'] } },
      { authorId: 'me' },
    );
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx });
    const select = delegates.post.findFirst.mock.calls[0][0].select;
    expect(select.title).toBe(true);
    expect(select.authorId).toBe(true);
    expect(select.id).toBe(true);
    expect(select.published).toBeUndefined();
    expect(select.views).toBeUndefined();
    expect(select.type).toBeUndefined();
  });

  it('takes the fast path when nothing is row-dependent', async () => {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue({ id: 'p1' }),
        update: jest.fn().mockResolvedValue({ id: 'p1' }),
        findUnique: jest.fn(),
      },
    };
    const client = txClient(delegates);
    const authz = classifyingProvider({ title: { access: 'always' } }, {});
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx });
    expect(client.$transaction).not.toHaveBeenCalled();
    expect((authz as { check: jest.Mock }).check).not.toHaveBeenCalled();
  });

  it('keeps the verified path when any payload field is row-dependent', async () => {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue(before),
        update: jest.fn().mockResolvedValue({ ...before, title: 'new' }),
        findUnique: jest.fn().mockResolvedValue({ id: 'p1' }),
      },
    };
    const client = txClient(delegates);
    const authz = classifyingProvider({ title: { access: 'conditional' } }, {});
    const engine = new GolemEngine(client, models, { authorization: authz, checkWriteResults: true });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx });
    expect(client.$transaction).toHaveBeenCalled();
  });

  it('falls back to full scalars without classifyFields support', async () => {
    const delegates = {
      post: {
        findFirst: jest.fn().mockResolvedValue(before),
        update: jest.fn().mockResolvedValue({ ...before, title: 'new' }),
        findUnique: jest.fn().mockResolvedValue({ id: 'p1' }),
      },
    };
    const client = txClient(delegates);
    const authz = provider({ classifyFields: undefined });
    const engine = new GolemEngine(client, models, {
      authorization: authz,
      checkWriteResults: true,
      checkReadFields: false,
    });

    await engine.update({ model: 'Post', where: { id: 'p1' }, data: { title: 'new' }, context: ctx });
    const select = delegates.post.findFirst.mock.calls[0][0].select;
    expect(select.views).toBe(true);
    expect(select.published).toBe(true);
  });

});

describe('planVerification relation-aware hydration', () => {
  const planUser: DatamodelModel = {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
      field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
      field({ name: 'profile', type: 'Profile', kind: 'object', relationName: 'ProfileToUser' }),
    ],
  };
  const planPost: DatamodelModel = {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'published', type: 'Boolean' }),
      field({
        name: 'author',
        type: 'User',
        kind: 'object',
        relationName: 'PostToUser',
        relationFromFields: ['authorId'],
        relationToFields: ['id'],
      }),
      field({ name: 'authorId', type: 'String' }),
    ],
  };
  const planProfile: DatamodelModel = {
    name: 'Profile',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'bio', type: 'String' }),
      field({ name: 'userId', type: 'String', isUnique: true }),
      field({
        name: 'user',
        type: 'User',
        kind: 'object',
        relationName: 'ProfileToUser',
        relationFromFields: ['userId'],
        relationToFields: ['id'],
      }),
    ],
  };
  const planModels = [planUser, planPost, planProfile];

  function planContext(): VerifyContext {
    return {
      modelsByName: new Map(planModels.map((model) => [model.name, model])),
      metadata: buildModelMetadata(planModels),
      provider: provider(),
      context: ctx,
    };
  }

  function modelByName(name: string): DatamodelModel {
    return planModels.find((model) => model.name === name)!;
  }

  it('hydrates exact deep field dependencies for transactional write checks', async () => {
    const context = planContext();
    context.provider.classifyFields = jest.fn(async () => ({
      title: {
        access: 'conditional',
        requires: ['author'],
        dependencies: { author: { profile: { bio: true } } },
      },
    }));

    const plan = await planVerification(
      context,
      modelByName('Post'),
      { title: 'new title' },
      {},
      'update',
    );

    expect(plan.select.author).toEqual({
      select: { profile: { select: { bio: true } } },
    });
  });

  it('preserves deep field dependencies when the same relation has a nested write', async () => {
    const context = planContext();
    context.provider.classifyFields = jest.fn(async () => ({
      title: {
        access: 'conditional',
        requires: ['author'],
        dependencies: { author: { profile: { bio: true } } },
      },
      authorId: { access: 'always' },
    }));

    const plan = await planVerification(
      context,
      modelByName('Post'),
      { title: 'new title', author: { update: { email: 'new@example.com' } } },
      {},
      'update',
    );

    expect(plan.select.author).toEqual({
      select: {
        id: true,
        email: true,
        profile: { select: { bio: true } },
      },
    });
  });

  it('fails closed when a write field dependency cannot be hydrated', async () => {
    const context = planContext();
    context.provider.classifyFields = jest.fn(async () => ({
      title: {
        access: 'conditional',
        requires: ['author'],
        dependencies: { author: { missingRelation: { secret: true } } },
      },
    }));

    await expect(
      planVerification(
        context,
        modelByName('Post'),
        { title: 'new title' },
        {},
        'update',
      ),
    ).rejects.toThrow('authorization dependencies cannot be hydrated safely');
  });

  it('fails closed when a write provider names a relation without an exact tree', async () => {
    const context = planContext();
    context.provider.classifyFields = jest.fn(async () => ({
      title: { access: 'conditional', requires: ['author'] },
    }));

    await expect(
      planVerification(
        context,
        modelByName('Post'),
        { title: 'new title' },
        {},
        'update',
      ),
    ).rejects.toThrow('relation dependency "author" has no exact hydration tree');
  });

  it('hydrates a to-one relation referenced by an is condition', async () => {
    const plan = await planVerification(
      planContext(),
      modelByName('Post'),
      { title: 't' },
      { author: { is: { id: 'u1' } } },
      'create',
    );
    expect(plan.fastPath).toBe(false);
    expect(plan.select.author).toEqual({ select: { id: true } });
  });

  it('hydrates a to-many relation referenced by a some condition', async () => {
    const plan = await planVerification(
      planContext(),
      modelByName('User'),
      { email: 'e' },
      { posts: { some: { published: true } } },
      'update',
    );
    expect(plan.select.posts).toEqual({ select: { published: true } });
  });

  it('recurses into nested relation conditions', async () => {
    const plan = await planVerification(
      planContext(),
      modelByName('Post'),
      { title: 't' },
      { author: { is: { profile: { is: { bio: 'x' } } } } },
      'create',
    );
    expect(plan.select.author).toEqual({ select: { profile: { select: { bio: true } } } });
  });

  it('merges logical OR branches into the hydration select', async () => {
    const plan = await planVerification(
      planContext(),
      modelByName('Post'),
      { title: 't' },
      { OR: [{ author: { is: { id: 'u1' } } }, { author: { is: { email: 'x' } } }] },
      'create',
    );
    expect(plan.select.author).toEqual({ select: { id: true, email: true } });
  });

  it('rejects an unrecognized relation condition shape before verification', async () => {
    await expect(planVerification(
      planContext(),
      modelByName('Post'),
      { title: 't' },
      { author: 'weird' },
      'create',
    )).rejects.toThrow(/cannot be hydrated safely/);
  });

  it('rejects an unrecognized relation operand before verification', async () => {
    await expect(planVerification(
      planContext(),
      modelByName('Post'),
      { title: 't' },
      { author: { is: 5 } },
      'create',
    )).rejects.toThrow(/cannot be hydrated safely/);
  });
});

describe('nested composite-child update verification', () => {
  const childPost: DatamodelModel = {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'authorId', type: 'String' }),
    ],
  };
  const tag: DatamodelModel = {
    name: 'Tag',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'label', type: 'String' }),
      field({ name: 'postTags', type: 'PostTag', kind: 'object', isList: true, relationName: 'PostTagToTag' }),
    ],
  };
  const postTag: DatamodelModel = {
    name: 'PostTag',
    fields: [
      field({ name: 'postId', type: 'String' }),
      field({ name: 'tagId', type: 'String' }),
      field({
        name: 'post',
        type: 'Post',
        kind: 'object',
        relationName: 'PostToPostTag',
        relationFromFields: ['postId'],
        relationToFields: ['id'],
      }),
    ],
    primaryKey: { fields: ['postId', 'tagId'] },
  };
  const compositeModels = [childPost, tag, postTag];

  function compositeContext(overrides: Partial<AuthorizationProvider> = {}): VerifyContext {
    return {
      modelsByName: new Map(compositeModels.map((model) => [model.name, model])),
      metadata: buildModelMetadata(compositeModels),
      provider: provider(overrides),
      context: ctx,
    };
  }

  const before = { id: 'tag1', label: 'x', postTags: [] as unknown[] };
  const after = {
    id: 'tag1',
    label: 'x',
    postTags: [
      { postId: 'pV', tagId: 'tag1', post: { id: 'pV', authorId: 'victim' } },
    ],
  };
  const data = { postTags: { create: [{ post: { connect: { id: 'pV' } } }] } };

  it('rejects an appeared composite child that fails its value check', async () => {
    const vctx = compositeContext({ check: jest.fn(async (_a, model) => model !== 'PostTag') });
    await expect(verifyUpdatedRow(vctx, tag, before, after, data)).rejects.toBeInstanceOf(
      GolemForbiddenError,
    );
  });

  it('verifies an appeared composite child that passes its value check', async () => {
    const vctx = compositeContext();
    await expect(verifyUpdatedRow(vctx, tag, before, after, data)).resolves.toBeUndefined();
    expect(vctx.provider.check).toHaveBeenCalledWith(
      'create',
      'PostTag',
      expect.objectContaining({ postId: 'pV', tagId: 'tag1' }),
      ctx,
    );
  });

  it('fails closed for a nested child model that has no primary key', async () => {
    const box: DatamodelModel = {
      name: 'Box',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'items', type: 'Item', kind: 'object', isList: true, relationName: 'BoxToItem' }),
      ],
    };
    const item: DatamodelModel = { name: 'Item', fields: [field({ name: 'name', type: 'String' })] };
    const vctx: VerifyContext = {
      modelsByName: new Map([box, item].map((model) => [model.name, model])),
      metadata: buildModelMetadata([box, item]),
      provider: provider(),
      context: ctx,
    };
    await expect(
      verifyUpdatedRow(
        vctx,
        box,
        { id: 'b1', items: [] },
        { id: 'b1', items: [{ name: 'n' }] },
        { items: { create: [{ name: 'n' }] } },
      ),
    ).rejects.toThrow('Cannot verify nested writes to Item: model has no primary key');
  });
});

import { AuthorizationProvider, FieldClassification } from './authorization';
import { GolemForbiddenError, GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
      field({ name: 'note', type: 'String', isRequired: false }),
      field({ name: 'secret', type: 'String', isRequired: false }),
      field({ name: 'posts', type: 'Post', kind: 'object' as const, isList: true }),
    ],
  },
  {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'secret', type: 'String', isRequired: false }),
      field({ name: 'note', type: 'String', isRequired: false }),
      field({ name: 'authorId', type: 'String' }),
      field({ name: 'author', type: 'User', kind: 'object' as const }),
      field({ name: 'comments', type: 'Comment', kind: 'object' as const, isList: true }),
    ],
  },
  {
    name: 'Comment',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'body', type: 'String' }),
      field({ name: 'secret', type: 'String', isRequired: false }),
      field({ name: 'note', type: 'String', isRequired: false }),
      field({ name: 'postId', type: 'String' }),
      field({ name: 'post', type: 'Post', kind: 'object' as const }),
    ],
  },
];

const ctx = { req: {} };

function delegate() {
  return {
    findMany: jest.fn().mockResolvedValue([]),
    findFirst: jest.fn().mockResolvedValue(null),
    findUnique: jest.fn().mockResolvedValue(null),
    update: jest.fn().mockResolvedValue({}),
    updateMany: jest.fn().mockResolvedValue({ count: 0 }),
    delete: jest.fn().mockResolvedValue({}),
    deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    create: jest.fn().mockResolvedValue({}),
    count: jest.fn().mockResolvedValue(0),
    aggregate: jest.fn().mockResolvedValue({}),
  };
}

function fakeClient() {
  return { user: delegate(), post: delegate(), comment: delegate() };
}

const readable: FieldClassification = { access: 'always' };

const open: Record<string, Record<string, FieldClassification>> = {
  User: {
    '*': readable,
    id: readable,
    email: readable,
    note: readable,
    secret: readable,
    posts: readable,
  },
  Post: {
    id: readable,
    title: readable,
    authorId: readable,
    author: readable,
    comments: readable,
  },
  Comment: { id: readable, body: readable, note: readable, postId: readable, post: readable },
};

function engineFor(
  client: ReturnType<typeof fakeClient>,
  classification: Record<string, Record<string, FieldClassification>> = open,
): { engine: GolemEngine; classifyFields: jest.Mock } {
  const classifyFields = jest.fn(
    async (_action: string, model: string, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [
          name,
          classification[model]?.[name] ??
            classification[model]?.['*'] ?? { access: 'never' },
        ]),
      ),
  );
  const authorization = {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => undefined),
    check: jest.fn(async () => true),
    checkField: jest.fn(async () => true),
    classifyFields,
  } as unknown as AuthorizationProvider;
  return {
    engine: new GolemEngine(client, models, {
      authorization,
      checkReadFields: true,
      checkWriteResults: false,
    }),
    classifyFields,
  };
}

describe('classifying the filters a relation entry carries', () => {
  it.each([
    ['a where under select', {
      email: true,
      posts: { where: { secret: 'x' }, select: { title: true } },
    }],
    ['an operator in a where under select', {
      email: true,
      posts: { where: { secret: { startsWith: 'a' } }, select: { title: true } },
    }],
    ['a where buried in an OR branch', {
      email: true,
      posts: {
        where: { OR: [{ title: 'x' }, { secret: { contains: 'a' } }] },
        select: { title: true },
      },
    }],
    ['an orderBy under select', {
      email: true,
      posts: { orderBy: { secret: 'asc' }, select: { title: true } },
    }],
    ['a cursor under select', {
      email: true,
      posts: { cursor: { secret: 'x' }, select: { title: true } },
    }],
  ] as const)('refuses %s naming a never-readable field', async (_shape, select) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({ model: 'User', select, context: ctx }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a where on a relation reached through include', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        include: { posts: { where: { secret: 'x' }, select: { title: true } } },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a where two relations deep', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: {
            select: {
              title: true,
              comments: { where: { secret: 'x' }, select: { body: true } },
            },
          },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Comment');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses an orderBy two relations deep', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: {
            select: {
              title: true,
              comments: { orderBy: [{ secret: 'desc' }], select: { body: true } },
            },
          },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Comment');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a nested filter reaching further through a relation of its own', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: {
            where: { comments: { some: { secret: 'x' } } },
            select: { title: true },
          },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Comment');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a nested filter on a conditional field the constraint does not discharge', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...open,
      Post: {
        ...open.Post,
        secret: { access: 'conditional', requires: ['authorId'], dischargedByConstraint: false },
      },
    });

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: { where: { secret: 'x' }, select: { title: true } },
        },
        context: ctx,
      }),
    ).rejects.toThrow(
      'Cannot filter or order by field "secret" on Post: readability depends on authorId, ' +
        'which the query constraint does not discharge',
    );
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('allows a nested filter on a conditional field the constraint discharges', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...open,
      Post: {
        ...open.Post,
        secret: { access: 'conditional', requires: ['authorId'], dischargedByConstraint: true },
      },
    });

    await engine.findMany({
      model: 'User',
      select: {
        email: true,
        posts: { where: { secret: 'x' }, select: { title: true } },
      },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });

  it('classifies a nested filter against the model that owns the field', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        where: { note: 'shared' },
        select: {
          email: true,
          posts: { where: { note: 'shared' }, select: { title: true } },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "note" on Post');
    expect(classifyFields).toHaveBeenCalledWith('read', 'Post', ['note'], ctx);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('leaves a filter on a field readable everywhere alone', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'User',
      where: { note: 'shared' },
      select: {
        email: true,
        posts: {
          where: { title: 'x' },
          orderBy: [{ title: 'asc' }],
          cursor: { id: 'p1' },
          select: {
            title: true,
            comments: { where: { body: 'hi' }, select: { body: true } },
          },
        },
      },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });

  it('refuses a relation count filtered by a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          _count: { select: { posts: { where: { secret: 'x' } } } },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('leaves a relation count filtered by a readable field alone', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'User',
      select: {
        email: true,
        _count: { select: { posts: { where: { title: 'x' } } } },
      },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });

  it('refuses a nested filter on the tree a write returns', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.update({
        model: 'User',
        where: { id: 'u1' },
        data: { email: 'a@b.c' },
        select: {
          email: true,
          posts: { where: { secret: 'x' }, select: { title: true } },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('leaves nested filters alone when field checks are off', async () => {
    const client = fakeClient();
    const authorization = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
    } as unknown as AuthorizationProvider;
    const engine = new GolemEngine(client, models, {
      authorization,
      checkReadFields: false,
      checkWriteResults: false,
    });

    await engine.findMany({
      model: 'User',
      select: { email: true, posts: { where: { secret: 'x' }, select: { title: true } } },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });
});

describe('classifying a filter that hops a relation against the model that owns the field', () => {
  it.each([
    ['some', { posts: { some: { note: 'x' } } }],
    ['every', { posts: { every: { note: 'x' } } }],
    ['none', { posts: { none: { note: 'x' } } }],
    ['a branch under some', { posts: { some: { OR: [{ title: 'x' }, { note: 'x' }] } } }],
  ] as const)('never runs a User read whose %s filter names a field only User may read', async (
    _shape,
    where,
  ) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({ model: 'User', where, select: { email: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it.each([
    ['the bare to-one shorthand', { post: { note: 'x' } }],
    ['is', { post: { is: { note: 'x' } } }],
    ['isNot', { post: { isNot: { note: 'x' } } }],
  ] as const)('never runs a Comment read whose %s filter names a field only Comment may read', async (
    _shape,
    where,
  ) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({ model: 'Comment', where, select: { body: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.comment.findMany).not.toHaveBeenCalled();
  });

  it('never runs a read whose filter hops two relations to reach the field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        where: { posts: { some: { comments: { some: { secret: 'x' } } } } },
        select: { email: true },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('never runs a write whose filter hops a relation to reach the field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.deleteMany({
        model: 'User',
        where: { posts: { some: { note: 'x' } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.deleteMany).not.toHaveBeenCalled();
  });

  it('never runs a read whose nested clause hops a relation to reach the field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: {
            where: { comments: { some: { secret: 'x' } } },
            select: { title: true },
          },
        },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('still runs a relation filter naming a field the related model may read', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'User',
      where: { posts: { some: { comments: { some: { body: 'hi' } } } } },
      select: { email: true },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });
});

describe('classifying the filter a write selects rows with', () => {
  it('refuses an updateMany filtered by a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.updateMany({
        model: 'Post',
        where: { secret: { startsWith: 'a' } },
        data: { title: 'x' },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.post.updateMany).not.toHaveBeenCalled();
  });

  it('refuses a deleteMany filtered by a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.deleteMany({
        model: 'Post',
        where: { secret: { startsWith: 'a' } },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.post.deleteMany).not.toHaveBeenCalled();
  });

  it('refuses a batch write filtered through a relation on a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.deleteMany({
        model: 'Post',
        where: { comments: { some: { secret: 'x' } } },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Comment');
    expect(client.post.deleteMany).not.toHaveBeenCalled();
  });

  it('refuses a batch write on a conditional field the constraint does not discharge', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...open,
      Post: {
        ...open.Post,
        secret: { access: 'conditional', requires: ['authorId'], dischargedByConstraint: false },
      },
    });

    await expect(
      engine.updateMany({
        model: 'Post',
        where: { secret: 'x' },
        data: { title: 'x' },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.post.updateMany).not.toHaveBeenCalled();
  });

  it.each([
    ['update', (engine: GolemEngine) =>
      engine.update({
        model: 'Post',
        where: { id: 'p1', secret: { startsWith: 'a' } },
        data: { title: 'x' },
        select: { title: true },
        context: ctx,
      })],
    ['delete', (engine: GolemEngine) =>
      engine.delete({
        model: 'Post',
        where: { id: 'p1', secret: { startsWith: 'a' } },
        select: { title: true },
        context: ctx,
      })],
    ['upsert', (engine: GolemEngine) =>
      engine.upsert({
        model: 'Post',
        where: { id: 'p1', secret: { startsWith: 'a' } },
        create: { title: 'x' },
        update: { title: 'x' },
        select: { title: true },
        context: ctx,
      })],
  ] as const)('refuses a %s whose unique filter also probes a never-readable field', async (
    _operation,
    run,
  ) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(run(engine)).rejects.toThrow(
      'Cannot filter or order by field "secret" on Post',
    );
    expect(client.post.update).not.toHaveBeenCalled();
    expect(client.post.delete).not.toHaveBeenCalled();
    expect(client.post.findFirst).not.toHaveBeenCalled();
  });

  it('still runs a batch write filtered by readable fields', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    const updated = await engine.updateMany({
      model: 'Post',
      where: { title: { startsWith: 'a' } },
      data: { title: 'x' },
      context: ctx,
    });
    const deleted = await engine.deleteMany({
      model: 'Post',
      where: { title: { startsWith: 'a' } },
      context: ctx,
    });

    expect(updated).toEqual({ count: 0 });
    expect(deleted).toEqual({ count: 0 });
    expect(client.post.updateMany).toHaveBeenCalled();
    expect(client.post.deleteMany).toHaveBeenCalled();
  });

  it('leaves batch writes alone when field checks are off', async () => {
    const client = fakeClient();
    const authorization = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
    } as unknown as AuthorizationProvider;
    const engine = new GolemEngine(client, models, {
      authorization,
      checkReadFields: false,
      checkWriteResults: false,
    });

    await engine.deleteMany({ model: 'Post', where: { secret: 'x' }, context: ctx });

    expect(client.post.deleteMany).toHaveBeenCalled();
  });
});

describe('classifying the filter a nested write selects rows with', () => {
  it.each([
    ['deleteMany', { posts: { deleteMany: { secret: { startsWith: 'a' } } } }],
    ['updateMany', {
      posts: { updateMany: { where: { secret: { startsWith: 'a' } }, data: { title: 'x' } } },
    }],
    ['update', {
      posts: { update: { where: { secret: { startsWith: 'a' } }, data: { title: 'x' } } },
    }],
    ['upsert', {
      posts: {
        upsert: {
          where: { secret: { startsWith: 'a' } },
          update: { title: 'x' },
          create: { title: 'x' },
        },
      },
    }],
    ['delete', { posts: { delete: { secret: { startsWith: 'a' } } } }],
    ['connect', { posts: { connect: { secret: { startsWith: 'a' } } } }],
    ['disconnect', { posts: { disconnect: { secret: { startsWith: 'a' } } } }],
    ['set', { posts: { set: [{ secret: { startsWith: 'a' } }] } }],
    ['connectOrCreate', {
      posts: {
        connectOrCreate: { where: { secret: { startsWith: 'a' } }, create: { title: 'x' } },
      },
    }],
  ] as const)('never runs an update whose nested %s filters on an unreadable field', async (
    _kind,
    posts,
  ) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.update({ model: 'User', where: { id: 'u1' }, data: posts, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('never runs a create whose nested filter names an unreadable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.create({
        model: 'User',
        data: { email: 'a@b.c', posts: { connect: { secret: 'x' } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.create).not.toHaveBeenCalled();
  });

  it('never runs an updateMany whose nested filter names an unreadable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.updateMany({
        model: 'User',
        where: { email: 'a@b.c' },
        data: { posts: { deleteMany: { secret: 'x' } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.updateMany).not.toHaveBeenCalled();
  });

  it('never runs a write whose nested filter is two relations deep', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.update({
        model: 'User',
        where: { id: 'u1' },
        data: {
          posts: {
            update: {
              where: { id: 'p1' },
              data: { comments: { deleteMany: { secret: { startsWith: 'a' } } } },
            },
          },
        },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('never runs a write whose nested filter hops a relation to reach the field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.update({
        model: 'User',
        where: { id: 'u1' },
        data: { posts: { deleteMany: { comments: { some: { secret: 'x' } } } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('still runs a nested write filtered by a field the related model may read', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { deleteMany: { title: { startsWith: 'a' } } } },
      context: ctx,
    });

    expect(client.user.update).toHaveBeenCalled();
  });

  it('leaves nested write filters alone when field checks are off', async () => {
    const client = fakeClient();
    const authorization = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
    } as unknown as AuthorizationProvider;
    const engine = new GolemEngine(client, models, {
      authorization,
      checkReadFields: false,
      checkWriteResults: false,
    });

    await engine.update({
      model: 'User',
      where: { id: 'u1' },
      data: { posts: { deleteMany: { secret: 'x' } } },
      context: ctx,
    });

    expect(client.user.update).toHaveBeenCalled();
  });
});

describe('refusing a filter key that names no field at all', () => {
  it.each([
    ['a where', { where: { nOte: { startsWith: 'a' } } }],
    ['an orderBy', { orderBy: [{ nOte: 'asc' }] }],
    ['a cursor', { cursor: { nOte: 'a' } }],
    ['a branch of an OR', { where: { OR: [{ email: 'a@b.c' }, { nOte: 'a' }] } }],
    ['a filter reached through a relation', { where: { posts: { some: { titel: 'x' } } } }],
  ] as const)('never runs a read whose %s names an unknown key', async (_shape, request) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({ model: 'User', ...request, select: { email: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('never runs a nested write whose filter names an unknown key', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.update({
        model: 'User',
        where: { id: 'u1' },
        data: { posts: { deleteMany: { titel: 'x' } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('leaves an unknown key to the query layer when field checks are off', async () => {
    const client = fakeClient();
    const authorization = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
    } as unknown as AuthorizationProvider;
    const engine = new GolemEngine(client, models, {
      authorization,
      checkReadFields: false,
      checkWriteResults: false,
    });

    await engine.findMany({ model: 'User', where: { nOte: 'a' }, context: ctx });
    await engine.aggregate({
      model: 'User',
      orderBy: { nOte: 'desc' },
      _count: { email: true },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
    expect(client.user.aggregate).toHaveBeenCalled();
  });
});

describe('classifying the fields a relevance ordering searches', () => {
  it('refuses an ordering by relevance over a field the caller may not read', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'Post',
        orderBy: [{ _relevance: { fields: ['secret'], search: 'x', sort: 'asc' } }],
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it('refuses a relevance ordering that names no fields', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'Post',
        orderBy: [{ _relevance: { search: 'x', sort: 'asc' } }],
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it('still orders by relevance over fields the caller may read', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'Post',
      orderBy: [{ _relevance: { fields: ['title'], search: 'x', sort: 'asc' } }],
      select: { title: true },
      context: ctx,
    });

    expect(client.post.findMany).toHaveBeenCalled();
  });
});

describe('classifying the fields a distinct read groups rows by', () => {
  it.each([
    ['an array', ['secret']],
    ['a bare name', 'secret'],
  ] as const)('refuses a root distinct naming a never-readable field as %s', async (
    _shape,
    distinct,
  ) => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client);

    await expect(
      engine.findMany({ model: 'Post', distinct, select: { title: true }, context: ctx }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(classifyFields).toHaveBeenCalledWith('read', 'Post', ['secret'], ctx);
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it('refuses a distinct on findFirst', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findFirst({
        model: 'Post',
        distinct: ['secret'],
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(client.post.findFirst).not.toHaveBeenCalled();
  });

  it('refuses a distinct inside a relation entry against the model that owns the field', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: { distinct: ['secret'], select: { title: true } },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Post');
    expect(classifyFields).toHaveBeenCalledWith('read', 'Post', ['secret'], ctx);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a distinct two relations deep', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'User',
        select: {
          email: true,
          posts: {
            select: {
              title: true,
              comments: { distinct: ['secret'], select: { body: true } },
            },
          },
        },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "secret" on Comment');
    expect(classifyFields).toHaveBeenCalledWith('read', 'Comment', ['secret'], ctx);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a distinct on a conditional field the constraint does not discharge', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...open,
      Post: {
        ...open.Post,
        secret: { access: 'conditional', requires: ['authorId'], dischargedByConstraint: false },
      },
    });

    await expect(
      engine.findMany({
        model: 'Post',
        distinct: ['secret'],
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toThrow(
      'Cannot filter or order by field "secret" on Post: readability depends on authorId, ' +
        'which the query constraint does not discharge',
    );
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it('still reads distinct on a conditional field the constraint discharges', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...open,
      Post: {
        ...open.Post,
        secret: { access: 'conditional', requires: ['authorId'], dischargedByConstraint: true },
      },
    });

    await engine.findMany({
      model: 'Post',
      distinct: ['secret'],
      select: { title: true },
      context: ctx,
    });

    expect(client.post.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ distinct: ['secret'] }),
    );
  });

  it('leaves a distinct over readable fields alone', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'User',
      distinct: ['note', 'email'],
      select: {
        email: true,
        posts: { distinct: ['title'], select: { title: true } },
      },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ distinct: ['note', 'email'] }),
    );
  });

  it.each([
    ['an unknown name', ['sekret']],
    ['a name that is not a string', [7]],
  ] as const)('refuses a distinct naming %s', async (_shape, distinct) => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({ model: 'Post', distinct, select: { title: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it('leaves a distinct alone when field checks are off', async () => {
    const client = fakeClient();
    const authorization = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
    } as unknown as AuthorizationProvider;
    const engine = new GolemEngine(client, models, {
      authorization,
      checkReadFields: false,
      checkWriteResults: false,
    });

    await engine.findMany({
      model: 'Post',
      distinct: ['secret'],
      select: { title: true },
      context: ctx,
    });

    expect(client.post.findMany).toHaveBeenCalled();
  });
});

describe('classifying every key a relation filter carries', () => {
  it('never runs a read whose relation filter carries an unreadable sibling key', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await expect(
      engine.findMany({
        model: 'Comment',
        where: { post: { is: { title: 'x' }, secret: 'y' } },
        select: { body: true },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.comment.findMany).not.toHaveBeenCalled();
  });

  it('still runs a relation filter whose sibling keys are readable', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client);

    await engine.findMany({
      model: 'Comment',
      where: { post: { is: { title: 'x' }, authorId: 'u1' } },
      select: { body: true },
      context: ctx,
    });

    expect(client.comment.findMany).toHaveBeenCalled();
  });
});

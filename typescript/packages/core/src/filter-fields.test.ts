import { AuthorizationProvider, FieldClassification } from './authorization';
import { GolemForbiddenError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
      field({ name: 'phone', type: 'String', isRequired: false }),
      field({ name: 'posts', type: 'Post', kind: 'object' as const, isList: true }),
    ],
  },
  {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'authorId', type: 'String' }),
      field({ name: 'author', type: 'User', kind: 'object' as const }),
    ],
  },
  {
    name: 'Membership',
    fields: [
      field({ name: 'userId', type: 'String' }),
      field({ name: 'orgId', type: 'String' }),
      field({ name: 'tier', type: 'String' }),
    ],
    primaryKey: { fields: ['userId', 'orgId'] },
  },
];

const ctx = { req: {} };

function fakeClient() {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue([]),
      findFirst: jest.fn().mockResolvedValue(null),
      findUnique: jest.fn().mockResolvedValue(null),
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
      groupBy: jest.fn().mockResolvedValue([]),
    },
    post: {
      findMany: jest.fn().mockResolvedValue([]),
      findFirst: jest.fn().mockResolvedValue(null),
      findUnique: jest.fn().mockResolvedValue(null),
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
      groupBy: jest.fn().mockResolvedValue([]),
    },
    membership: {
      findMany: jest.fn().mockResolvedValue([]),
      findFirst: jest.fn().mockResolvedValue(null),
      findUnique: jest.fn().mockResolvedValue(null),
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
      groupBy: jest.fn().mockResolvedValue([]),
    },
  };
}

const readable: FieldClassification = { access: 'always' };

function engineFor(
  client: ReturnType<typeof fakeClient>,
  classification: Record<string, Record<string, FieldClassification>>,
): { engine: GolemEngine; classifyFields: jest.Mock } {
  const classifyFields = jest.fn(
    async (_action: string, model: string, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [name, classification[model]?.[name] ?? { access: 'never' }]),
      ),
  );
  const authorization = {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => undefined),
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

const openUser = {
  User: { id: readable, email: readable, posts: readable },
  Post: { id: readable, title: readable, authorId: readable, author: readable },
  Membership: { userId: readable, orgId: readable, tier: readable },
};

describe('classifying the fields a read filters and orders by', () => {
  it.each([
    ['a top-level equality', { phone: '+44-555' }],
    ['an operator on the field', { phone: { startsWith: '+44' } }],
    ['a branch of an OR', { OR: [{ email: 'a@b.c' }, { phone: { contains: '5' } }] }],
    ['a branch nested three levels deep', {
      AND: [{ OR: [{ NOT: { phone: { endsWith: '00' } } }] }],
    }],
    ['a branch of a NOT array', { NOT: [{ phone: null }] }],
  ] as const)('refuses a findMany filtering on a never-readable field through %s', async (
    _shape,
    where,
  ) => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await expect(
      engine.findMany({ model: 'User', where, select: { email: true }, context: ctx }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a findMany ordering by a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await expect(
      engine.findMany({
        model: 'User',
        orderBy: [{ email: 'asc' }, { phone: 'desc' }],
        select: { email: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('refuses a findMany paging from a cursor on a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await expect(
      engine.findMany({
        model: 'User',
        cursor: { phone: '+44-555' },
        select: { email: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
  });

  it.each([
    ['findOne', (engine: GolemEngine) =>
      engine.findOne({
        model: 'User',
        where: { phone: '+44-555' },
        select: { email: true },
        context: ctx,
      })],
    ['findFirst', (engine: GolemEngine) =>
      engine.findFirst({
        model: 'User',
        where: { phone: { startsWith: '+44' } },
        select: { email: true },
        context: ctx,
      })],
    ['count', (engine: GolemEngine) =>
      engine.count({ model: 'User', where: { phone: { startsWith: '+44' } }, context: ctx })],
    ['aggregate', (engine: GolemEngine) =>
      engine.aggregate({
        model: 'User',
        where: { phone: { startsWith: '+44' } },
        _count: { email: true },
        context: ctx,
      })],
    ['groupBy', (engine: GolemEngine) =>
      engine.groupBy({
        model: 'User',
        by: ['email'],
        where: { phone: { startsWith: '+44' } },
        _count: { email: true },
        context: ctx,
      })],
  ] as const)('refuses %s over a never-readable filter field', async (_operation, run) => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await expect(run(engine)).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.findUnique).not.toHaveBeenCalled();
    expect(client.user.findFirst).not.toHaveBeenCalled();
    expect(client.user.count).not.toHaveBeenCalled();
    expect(client.user.aggregate).not.toHaveBeenCalled();
    expect(client.user.groupBy).not.toHaveBeenCalled();
  });

  it('refuses a conditionally-readable filter field the constraint does not discharge', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      User: {
        ...openUser.User,
        phone: { access: 'conditional', requires: ['id'], dischargedByConstraint: false },
      },
    });

    await expect(
      engine.findMany({
        model: 'User',
        where: { phone: { startsWith: '+44' } },
        select: { email: true },
        context: ctx,
      }),
    ).rejects.toThrow(
      'Cannot filter or order by field "phone" on User: readability depends on id, ' +
        'which the query constraint does not discharge',
    );
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('allows a conditionally-readable filter field the constraint discharges', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      User: {
        ...openUser.User,
        phone: { access: 'conditional', requires: ['id'], dischargedByConstraint: true },
      },
    });

    await engine.findMany({
      model: 'User',
      where: { phone: { startsWith: '+44' } },
      select: { email: true },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });

  it('allows a filter over always-readable fields', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await engine.findMany({
      model: 'User',
      where: { OR: [{ email: 'a@b.c' }, { id: 'u1' }] },
      orderBy: [{ email: 'asc' }],
      select: { email: true },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });

  it('classifies nothing at all when the caller filters on nothing', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client, openUser);

    await engine.count({ model: 'User', context: ctx });

    expect(classifyFields).not.toHaveBeenCalled();
    expect(client.user.count).toHaveBeenCalled();
  });

  it('leaves filters alone when field checks are off', async () => {
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
      where: { phone: { startsWith: '+44' } },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalled();
  });
});

describe('classifying a filter field reached through a relation', () => {
  it('classifies the related field against the related model, not the queried one', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client, {
      ...openUser,
      User: { ...openUser.User, phone: { access: 'never' } },
    });

    await expect(
      engine.findMany({
        model: 'Post',
        where: { author: { is: { phone: { startsWith: '+44' } } } },
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');

    expect(classifyFields).toHaveBeenCalledWith('read', 'Post', ['author'], ctx);
    expect(classifyFields).toHaveBeenCalledWith('read', 'User', ['phone'], ctx);
    expect(client.post.findMany).not.toHaveBeenCalled();
  });

  it.each([
    ['is', { author: { is: { phone: '+44' } } }],
    ['isNot', { author: { isNot: { phone: '+44' } } }],
    ['a bare to-one condition', { author: { phone: '+44' } }],
    ['a branch under a relation filter', {
      author: { is: { OR: [{ phone: { contains: '4' } }] } },
    }],
  ] as const)('reaches a related field through %s', async (_shape, where) => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      User: { ...openUser.User, phone: { access: 'never' } },
    });

    await expect(
      engine.findMany({ model: 'Post', where, select: { title: true }, context: ctx }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
  });

  it.each([
    ['some', { posts: { some: { title: 'x' } } }],
    ['every', { posts: { every: { title: 'x' } } }],
    ['none', { posts: { none: { title: 'x' } } }],
  ] as const)('reaches a to-many related field through %s', async (_shape, where) => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      Post: { ...openUser.Post, title: { access: 'never' } },
    });

    await expect(
      engine.findMany({ model: 'User', where, select: { email: true }, context: ctx }),
    ).rejects.toThrow('Cannot filter or order by field "title" on Post');
  });

  it('classifies a field ordered by through a relation against the related model', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      User: { ...openUser.User, phone: { access: 'never' } },
    });

    await expect(
      engine.findMany({
        model: 'Post',
        orderBy: { author: { phone: 'asc' } },
        select: { title: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "phone" on User');
  });

  it('lets a relation filter through when every field it names is readable', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, openUser);

    await engine.findMany({
      model: 'Post',
      where: { author: { is: { email: 'a@b.c' } } },
      select: { title: true },
      context: ctx,
    });

    expect(client.post.findMany).toHaveBeenCalled();
  });
});

describe('classifying a filter that names a compound selector', () => {
  it('classifies the scalar fields behind a composite primary key, not the selector', async () => {
    const client = fakeClient();
    const { engine, classifyFields } = engineFor(client, openUser);

    await engine.findOne({
      model: 'Membership',
      where: { userId_orgId: { userId: 'u1', orgId: 'o1' } },
      select: { tier: true },
      context: ctx,
    });

    const classified = classifyFields.mock.calls
      .filter((call) => call[1] === 'Membership')
      .flatMap((call) => call[2] as string[]);
    expect(classified).toEqual(expect.arrayContaining(['userId', 'orgId']));
    expect(classified).not.toContain('userId_orgId');
  });

  it('refuses a composite key selector naming a never-readable field', async () => {
    const client = fakeClient();
    const { engine } = engineFor(client, {
      ...openUser,
      Membership: { ...openUser.Membership, orgId: { access: 'never' } },
    });

    await expect(
      engine.findOne({
        model: 'Membership',
        where: { userId_orgId: { userId: 'u1', orgId: 'o1' } },
        select: { tier: true },
        context: ctx,
      }),
    ).rejects.toThrow('Cannot filter or order by field "orgId" on Membership');
  });
});

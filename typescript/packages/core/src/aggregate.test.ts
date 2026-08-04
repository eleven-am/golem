import { AuthorizationProvider, FieldClassification } from './authorization';
import { GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'email', type: 'String', isUnique: true }),
      field({ name: 'phone', type: 'String', isRequired: false }),
    ],
  },
  {
    name: 'Post',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'title', type: 'String' }),
      field({ name: 'viewCount', type: 'BigInt', hasDefaultValue: true }),
      field({ name: 'authorId', type: 'String', isReadOnly: true }),
    ],
  },
  {
    name: 'Membership',
    fields: [
      field({ name: 'userId', type: 'String' }),
      field({ name: 'orgId', type: 'String' }),
    ],
    primaryKey: { fields: ['userId', 'orgId'] },
  },
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

const ctx = { req: {} };

function fakeClient() {
  return {
    user: {
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
    },
    post: {
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
    },
    membership: {
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
    },
    branch: {
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue({}),
    },
  };
}

function constrainProvider(constraint: unknown): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => constraint),
  };
}

function classifyProvider(
  classification: Record<string, FieldClassification>,
): AuthorizationProvider & { classifyFields: jest.Mock } {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => undefined),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [name, classification[name] ?? { access: 'never' }]),
      ),
    ),
  };
}

describe('engine count', () => {
  it('merges the read constraint into the delegate count', async () => {
    const client = fakeClient();
    client.post.count.mockResolvedValue(3);
    const engine = new GolemEngine(client, models, {
      authorization: constrainProvider({ authorId: 'me' }),
      checkWriteResults: false,
      checkReadFields: false,
    });

    const total = await engine.count({
      model: 'Post',
      where: { title: { contains: 'x' } },
      context: ctx,
    });

    expect(total).toBe(3);
    expect(client.post.count).toHaveBeenCalledWith({
      where: { AND: [{ title: { contains: 'x' } }, { authorId: 'me' }] },
    });
  });

  it('counts without a constraint when the policy is unconstrained', async () => {
    const client = fakeClient();
    client.post.count.mockResolvedValue(5);
    const engine = new GolemEngine(client, models, {
      authorization: constrainProvider(undefined),
      checkWriteResults: false,
      checkReadFields: false,
    });

    const total = await engine.count({ model: 'Post', context: ctx });

    expect(total).toBe(5);
    expect(client.post.count).toHaveBeenCalledWith({ where: undefined });
  });
});

describe('engine aggregate', () => {
  it('merges the read constraint and forwards aggregation operators', async () => {
    const client = fakeClient();
    client.post.aggregate.mockResolvedValue({ _sum: { viewCount: 42n }, _count: 2 });
    const engine = new GolemEngine(client, models, {
      authorization: constrainProvider({ authorId: 'me' }),
      checkWriteResults: false,
      checkReadFields: false,
    });

    const result = await engine.aggregate({
      model: 'Post',
      where: { published: true },
      orderBy: { createdAt: 'desc' },
      cursor: { id: 'p9' },
      take: 10,
      skip: 2,
      _sum: { viewCount: true },
      _count: true,
      context: ctx,
    });

    expect(result).toEqual({ _sum: { viewCount: 42n }, _count: 2 });
    expect(client.post.aggregate).toHaveBeenCalledWith({
      where: { AND: [{ published: true }, { authorId: 'me' }] },
      orderBy: { createdAt: 'desc' },
      cursor: { id: 'p9' },
      take: 10,
      skip: 2,
      _sum: { viewCount: true },
      _count: true,
    });
  });

  it('rejects an aggregation over a never-readable field', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(client, models, {
      authorization: classifyProvider({ phone: { access: 'never' } }),
      checkWriteResults: false,
      checkReadFields: true,
    });

    await expect(
      engine.aggregate({ model: 'User', _max: { phone: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.user.aggregate).not.toHaveBeenCalled();
  });

  it('rejects an aggregation over a conditionally-readable field', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(client, models, {
      authorization: classifyProvider({ phone: { access: 'conditional', requires: ['id'] } }),
      checkWriteResults: false,
      checkReadFields: true,
    });

    await expect(
      engine.aggregate({ model: 'User', _avg: { phone: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.user.aggregate).not.toHaveBeenCalled();
  });

  it('refuses a per-field count over a never-readable field', async () => {
    const client = fakeClient();
    const provider = classifyProvider({
      email: { access: 'always' },
      phone: { access: 'never' },
    });
    const engine = new GolemEngine(client, models, {
      authorization: provider,
      checkWriteResults: false,
      checkReadFields: true,
    });

    await expect(
      engine.aggregate({
        model: 'User',
        _count: { _all: true, phone: true },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);

    expect(provider.classifyFields).toHaveBeenCalledWith(
      'read',
      'User',
      expect.arrayContaining(['phone']),
      ctx,
    );
    expect(client.user.aggregate).not.toHaveBeenCalled();
  });

  it.each([
    ['orderBy', { orderBy: { phone: 'desc' } }],
    ['cursor', { cursor: { phone: '555-0100' } }],
  ] as const)(
    'rejects aggregate pagination through a never-readable %s field',
    async (_argument, pagination) => {
      const client = fakeClient();
      const provider = classifyProvider({
        email: { access: 'always' },
        phone: { access: 'never' },
      });
      const engine = new GolemEngine(client, models, {
        authorization: provider,
        checkWriteResults: false,
        checkReadFields: true,
      });

      await expect(
        engine.aggregate({
          model: 'User',
          _count: { email: true },
          ...pagination,
          context: ctx,
        }),
      ).rejects.toBeInstanceOf(GolemValidationError);

      expect(provider.classifyFields).toHaveBeenCalledWith(
        'read',
        'User',
        expect.arrayContaining(['email', 'phone']),
        ctx,
      );
      expect(client.user.aggregate).not.toHaveBeenCalled();
    },
  );

  it.each([
    [
      'composite primary key',
      'Membership',
      'userId_orgId',
      { userId_orgId: { userId: 'u1', orgId: 'o1' } },
      ['userId', 'orgId'],
    ],
    [
      'compound unique key',
      'Branch',
      'authorId_name',
      { authorId_name: { authorId: 'a1', name: 'main' } },
      ['authorId', 'name'],
    ],
  ] as const)(
    'classifies scalar fields behind a %s aggregate cursor',
    async (_kind, model, selector, cursor, expectedFields) => {
      const client = fakeClient();
      const provider = classifyProvider(
        Object.fromEntries(expectedFields.map((field) => [field, { access: 'always' as const }])),
      );
      const engine = new GolemEngine(client, models, {
        authorization: provider,
        checkWriteResults: false,
        checkReadFields: true,
      });

      await engine.aggregate({
        model,
        cursor,
        _count: true,
        context: ctx,
      });

      const classified = provider.classifyFields.mock.calls[0][2] as string[];
      expect(classified).toEqual(expect.arrayContaining(expectedFields));
      expect(classified).not.toContain(selector);
    },
  );

  it('allows an aggregation over always-readable fields', async () => {
    const client = fakeClient();
    client.user.aggregate.mockResolvedValue({ _count: { email: 4 } });
    const engine = new GolemEngine(client, models, {
      authorization: classifyProvider({ email: { access: 'always' } }),
      checkWriteResults: false,
      checkReadFields: true,
    });

    const result = await engine.aggregate({
      model: 'User',
      _count: { email: true },
      context: ctx,
    });

    expect(result).toEqual({ _count: { email: 4 } });
    expect(client.user.aggregate).toHaveBeenCalled();
  });

  it('allows a field-free row count under field checks without classifying', async () => {
    const client = fakeClient();
    const provider = classifyProvider({});
    client.user.aggregate.mockResolvedValue({ _count: 9 });
    const engine = new GolemEngine(client, models, {
      authorization: provider,
      checkWriteResults: false,
      checkReadFields: true,
    });

    const result = await engine.aggregate({ model: 'User', _count: true, context: ctx });

    expect(result).toEqual({ _count: 9 });
    expect(provider.classifyFields).not.toHaveBeenCalled();
    expect(client.user.aggregate).toHaveBeenCalled();
  });

  it('allows a _count _all row count under field checks without classifying', async () => {
    const client = fakeClient();
    const provider = classifyProvider({});
    client.user.aggregate.mockResolvedValue({ _count: { _all: 9 } });
    const engine = new GolemEngine(client, models, {
      authorization: provider,
      checkWriteResults: false,
      checkReadFields: true,
    });

    const result = await engine.aggregate({
      model: 'User',
      _count: { _all: true },
      context: ctx,
    });

    expect(result).toEqual({ _count: { _all: 9 } });
    expect(provider.classifyFields).not.toHaveBeenCalled();
    expect(client.user.aggregate).toHaveBeenCalled();
  });
});

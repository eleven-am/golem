import { AuthorizationProvider, FieldClassification } from './authorization';
import { DatamodelDocument } from './datamodel';
import { GolemForbiddenError, GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { prepareReadTree } from './readtree';
import { field } from './testing';

const datamodel: DatamodelDocument = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
        field({ name: 'profile', type: 'Profile', kind: 'object', isRequired: false, relationName: 'ProfileToUser' }),
      ],
    },
    {
      name: 'Post',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'title', type: 'String' }),
        field({ name: 'author', type: 'User', kind: 'object', relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
        field({ name: 'authorId', type: 'String', isReadOnly: true }),
      ],
    },
    {
      name: 'Profile',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'bio', type: 'String' }),
        field({ name: 'user', type: 'User', kind: 'object', relationName: 'ProfileToUser', relationFromFields: ['userId'], relationToFields: ['id'] }),
        field({ name: 'userId', type: 'String', isReadOnly: true }),
      ],
    },
  ],
  enums: [],
};

function fakeClient() {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      findFirst: jest.fn().mockResolvedValue(null),
    },
    post: { findMany: jest.fn().mockResolvedValue([]) },
    profile: { findMany: jest.fn().mockResolvedValue([]) },
  };
}

function providerWith(constraints: Record<string, unknown>, check?: jest.Mock): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async (_action: string, model: string) => constraints[model] ?? {}),
    ...(check ? { check } : {}),
  };
}

function relationPolicy(provider: AuthorizationProvider) {
  return { authorization: provider, checkWriteResults: false, checkReadFields: false };
}

const ctx = { req: {} };

describe('nested relation constraints', () => {
  it('injects constraints into to-many relation selects', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: { published: true }, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({
      model: 'User',
      select: { email: true, posts: { select: { title: true } } },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          email: true,
          posts: { select: { title: true }, where: { published: true } },
        },
      }),
    );
  });

  it('expands bare true relation entries and merges user filters', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: { published: true }, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({
      model: 'User',
      select: { posts: true },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { posts: { where: { published: true } } },
      }),
    );

    await engine.findMany({
      model: 'User',
      select: { posts: { where: { title: { contains: 'x' } } } },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenLastCalledWith(
      expect.objectContaining({
        select: {
          posts: { where: { AND: [{ title: { contains: 'x' } }, { published: true }] } },
        },
      }),
    );
  });

  it('leaves unconditional relations untouched', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: {}, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({ model: 'User', select: { posts: true }, context: ctx });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { posts: true } }),
    );
  });

  it('nulls conditional to-one relations that fail the instance check', async () => {
    const client = fakeClient();
    const check = jest
      .fn()
      .mockResolvedValueOnce(true)
      .mockResolvedValueOnce(false);
    const provider = providerWith({ Profile: { userId: 'me' }, User: {} }, check);
    const rows = [
      { email: 'a', profile: { id: 'pr1', bio: 'mine' } },
      { email: 'b', profile: { id: 'pr2', bio: 'other' } },
    ];
    client.user.findMany.mockResolvedValue(rows);
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    const result = (await engine.findMany({
      model: 'User',
      select: { email: true, profile: { select: { id: true, bio: true } } },
      context: ctx,
    })) as Array<{ profile: unknown }>;
    expect(result[0].profile).toEqual({ id: 'pr1', bio: 'mine' });
    expect(result[1].profile).toBeNull();
    expect(check).toHaveBeenCalledWith('read', 'Profile', { id: 'pr2', bio: 'other' }, ctx);
  });

  it('hydrates deep to-one constraint relations exactly and strips them after checking', async () => {
    const client = fakeClient();
    const checked: unknown[] = [];
    const check = jest.fn(async (_action, _model, row) => {
      checked.push(structuredClone(row));
      return true;
    });
    const provider = providerWith({
      User: {},
      Profile: { user: { is: { profile: { is: { bio: 'allowed' } } } } },
    }, check);
    client.user.findMany.mockResolvedValue([
      {
        email: 'a',
        profile: {
          id: 'pr1',
          user: { profile: { bio: 'allowed' } },
        },
      },
    ]);
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    const result = await engine.findMany({
      model: 'User',
      select: { email: true, profile: { select: { id: true } } },
      context: ctx,
    });

    expect(client.user.findMany).toHaveBeenCalledWith(expect.objectContaining({
      select: {
        email: true,
        profile: {
          select: {
            id: true,
            user: { select: { profile: { select: { bio: true } } } },
          },
        },
      },
    }));
    expect(checked).toEqual([{
      id: 'pr1',
      user: { profile: { bio: 'allowed' } },
    }]);
    expect(result).toEqual([{ email: 'a', profile: { id: 'pr1' } }]);
  });

  it('fails closed when a to-one authorization constraint cannot be hydrated', async () => {
    const client = fakeClient();
    const provider = providerWith({ Profile: { user: { is: 5 } }, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await expect(engine.findMany({
      model: 'User',
      select: { profile: { select: { id: true } } },
      context: ctx,
    })).rejects.toThrow(/cannot be hydrated safely/);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('rejects conditional to-one traversal when the provider lacks check', async () => {
    const client = fakeClient();
    const provider = providerWith({ Profile: { userId: 'me' }, User: {} });
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await expect(
      engine.findMany({ model: 'User', select: { profile: true }, context: ctx }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
  });

  it('skips all injection for internal calls without context', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: { published: true } }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({ model: 'User', select: { posts: true } });
    expect(provider.constrain).not.toHaveBeenCalled();
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { posts: true } }),
    );
  });
});

describe('relation counts', () => {
  it('narrows a counted relation by the policy on the model it counts', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: { published: true }, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({
      model: 'User',
      select: { email: true, _count: { select: { posts: true } } },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          email: true,
          _count: { select: { posts: { where: { published: true } } } },
        },
      }),
    );
  });

  it('intersects a caller filter on a counted relation with the policy on it', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: { published: true }, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({
      model: 'User',
      select: { _count: { select: { posts: { where: { title: { contains: 'x' } } } } } },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          _count: {
            select: {
              posts: { where: { AND: [{ title: { contains: 'x' } }, { published: true }] } },
            },
          },
        },
      }),
    );
  });

  it('leaves a counted relation nothing constrains untouched', async () => {
    const client = fakeClient();
    const provider = providerWith({ Post: {}, User: {} }, jest.fn());
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await engine.findMany({
      model: 'User',
      select: { _count: { select: { posts: true } } },
      context: ctx,
    });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { _count: { select: { posts: true } } } }),
    );
  });

  it('refuses a count of a model the caller may not read at all', async () => {
    const client = fakeClient();
    const provider: AuthorizationProvider = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async (_action: string, model: string) => {
        if (model === 'Post') {
          throw new GolemForbiddenError('Cannot read Post');
        }
        return {};
      }),
      check: jest.fn(async () => true),
    };
    const engine = new GolemEngine(client, datamodel.models, relationPolicy(provider));

    await expect(
      engine.findMany({
        model: 'User',
        select: { _count: { select: { posts: true } } },
        context: ctx,
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('counts a relation against the depth the read is allowed to reach', async () => {
    const client = fakeClient();
    const strict = new GolemEngine(client, datamodel.models, { maxDepth: 1 });

    await expect(
      strict.findMany({ model: 'User', select: { _count: { select: { posts: true } } } }),
    ).rejects.toThrow('Query depth 2 exceeds the maximum of 1');
  });
});

describe('maxDepth', () => {
  it('rejects selections beyond the default depth of five', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(client, datamodel.models, {});

    const depthSix = {
      posts: { select: { author: { select: { posts: { select: { author: { select: { posts: true } } } } } } } },
    };
    await expect(
      engine.findMany({ model: 'User', select: depthSix }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('allows selections at the limit and honors overrides', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(client, datamodel.models, {});
    await expect(
      engine.findMany({
        model: 'User',
        select: { posts: { select: { author: { select: { posts: { select: { author: true } } } } } } },
      }),
    ).resolves.toEqual([]);

    const strict = new GolemEngine(client, datamodel.models, { maxDepth: 2 });
    await expect(
      engine.findMany({ model: 'User', select: { posts: { select: { author: true } } } }),
    ).resolves.toEqual([]);
    await expect(
      strict.findMany({ model: 'User', select: { posts: { select: { author: true } } } }),
    ).rejects.toThrow('Query depth 3 exceeds the maximum of 2');
  });
});

describe('omit projections', () => {
  function fieldProvider(classifyFields: jest.Mock, checkField = jest.fn(async () => true)) {
    return {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => undefined),
      classifyFields,
      checkField,
    } satisfies AuthorizationProvider;
  }

  it('classifies only fields that Prisma will return', async () => {
    const client = fakeClient();
    const classifyFields = jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(fields.map((name) => [name, { access: 'always' as const }])),
    );
    const engine = new GolemEngine(client, datamodel.models, {
      authorization: fieldProvider(classifyFields),
      checkWriteResults: false,
    });

    await engine.findFirst({ model: 'User', omit: { email: true }, context: ctx });

    expect(classifyFields).toHaveBeenCalledWith('read', 'User', ['id'], ctx);
    expect(client.user.findFirst).toHaveBeenCalledWith(
      expect.objectContaining({ omit: { email: true } }),
    );
  });

  it('fetches an omitted policy dependency internally and strips it from the result', async () => {
    const client = fakeClient();
    client.user.findFirst.mockResolvedValue({ id: 'u1', email: 'a@b.c' });
    const classifyFields = jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [
          name,
          name === 'email'
            ? { access: 'conditional' as const, requires: ['id'] }
            : { access: 'always' as const },
        ]),
      ),
    );
    const checkedRows: unknown[] = [];
    const checkField = jest.fn(async (_action, _model, row) => {
      checkedRows.push({ ...row });
      return true;
    });
    const engine = new GolemEngine(client, datamodel.models, {
      authorization: fieldProvider(classifyFields, checkField),
      checkWriteResults: false,
    });

    const result = await engine.findFirst({ model: 'User', omit: { id: true }, context: ctx });

    expect(client.user.findFirst).toHaveBeenCalledWith(
      expect.objectContaining({ omit: {} }),
    );
    expect(checkField).toHaveBeenCalledWith('read', 'User', expect.any(Object), 'email', ctx);
    expect(checkedRows).toEqual([{ id: 'u1', email: 'a@b.c' }]);
    expect(result).toEqual({ email: 'a@b.c' });
  });

  it('rejects omit combined with select before calling Prisma', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(client, datamodel.models);

    await expect(
      engine.findFirst({ model: 'User', select: { id: true }, omit: { email: true } }),
    ).rejects.toThrow('select and omit cannot be used together');
    expect(client.user.findFirst).not.toHaveBeenCalled();
  });
});

describe('relation-scoped read hydration', () => {
  const relationModels: DatamodelDocument = {
    models: [
      {
        name: 'Session',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'progress', type: 'Int' }),
          field({ name: 'note', type: 'String', isRequired: false }),
          field({ name: 'post', type: 'Post', kind: 'object', relationName: 'PostToSession', relationFromFields: ['postId'], relationToFields: ['id'] }),
          field({ name: 'postId', type: 'String', isReadOnly: true }),
        ],
      },
      {
        name: 'Post',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'title', type: 'String' }),
          field({ name: 'is', type: 'String' }),
          field({ name: 'authorId', type: 'String' }),
          field({ name: 'organization', type: 'Organization', kind: 'object', relationName: 'OrganizationToPost', relationFromFields: ['organizationId'], relationToFields: ['id'] }),
          field({ name: 'organizationId', type: 'String', isReadOnly: true }),
          field({ name: 'comments', type: 'Comment', kind: 'object', isList: true, relationName: 'CommentToPost' }),
        ],
      },
      {
        name: 'Organization',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'suspended', type: 'Boolean' }),
          field({ name: 'company', type: 'Company', kind: 'object', relationName: 'CompanyToOrganization', relationFromFields: ['companyId'], relationToFields: ['id'] }),
          field({ name: 'companyId', type: 'String', isReadOnly: true }),
        ],
      },
      {
        name: 'Company',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'blocked', type: 'Boolean' }),
        ],
      },
      {
        name: 'Comment',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'flagged', type: 'Boolean' }),
          field({ name: 'some', type: 'String' }),
        ],
      },
    ],
    enums: [],
  };

  function sessionClient(rows: unknown[]) {
    return { session: { findMany: jest.fn().mockResolvedValue(rows) }, post: { findMany: jest.fn() } };
  }

  function relationProvider(
    sessionConstraint: unknown,
    checkField: jest.Mock,
    dependencies: Record<string, true | Record<string, unknown>> = {
      post: { authorId: true },
    },
  ): AuthorizationProvider {
    return {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async (_action: string, model: string) =>
        model === 'Session' ? sessionConstraint : {},
      ),
      checkField,
      classifyFields: jest.fn(async (_action, model, fields: readonly string[]) =>
        Object.fromEntries(
          fields.map((name) => [
            name,
            model === 'Session'
              ? {
                  access: 'conditional' as const,
                  requires: ['post'],
                  dependencies,
                }
              : { access: 'always' as const },
          ]),
        ),
      ),
    };
  }

  const policy = { checkWriteResults: false, checkReadFields: true };
  const ctx = { req: {} };

  it('injects the relation named by a field requirement and strips it from the result', async () => {
    const client = sessionClient([{ progress: 5, post: { authorId: 'me' } }]);
    const seenRows: unknown[] = [];
    const checkField = jest.fn(async (_action, _model, row) => {
      seenRows.push(JSON.parse(JSON.stringify(row)));
      return true;
    });
    const provider = relationProvider({ post: { is: { authorId: 'me' } } }, checkField);
    const engine = new GolemEngine(client, relationModels.models, { authorization: provider, ...policy });

    const result = (await engine.findMany({
      model: 'Session',
      select: { progress: true },
      context: ctx,
    })) as Array<Record<string, unknown>>;

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { progress: true, post: { select: { authorId: true } } },
      }),
    );
    expect(seenRows).toEqual([{ progress: 5, post: { authorId: 'me' } }]);
    expect(result).toEqual([{ progress: 5 }]);
  });

  it('merges the hydrated relation into a user selection without leaking injected columns', async () => {
    const client = sessionClient([{ progress: 5, post: { title: 't', authorId: 'me' } }]);
    const checkField = jest.fn(async () => true);
    const provider = relationProvider({ post: { is: { authorId: 'me' } } }, checkField);
    const engine = new GolemEngine(client, relationModels.models, { authorization: provider, ...policy });

    const result = (await engine.findMany({
      model: 'Session',
      select: { progress: true, post: { select: { title: true } } },
      context: ctx,
    })) as Array<Record<string, unknown>>;

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { progress: true, post: { select: { title: true, authorId: true } } },
      }),
    );
    expect(result).toEqual([{ progress: 5, post: { title: 't' } }]);
  });

  it('hydrates the required relation scalars when the row constraint does not surface it', async () => {
    const client = sessionClient([{ progress: 5, post: { id: 'p1', title: 't', authorId: 'other' } }]);
    const checkField = jest.fn(async (_action, _model, row) =>
      (row as { post?: { authorId?: string } }).post?.authorId === 'me',
    );
    const provider = relationProvider({}, checkField);
    const engine = new GolemEngine(client, relationModels.models, { authorization: provider, ...policy });

    const result = (await engine.findMany({
      model: 'Session',
      select: { progress: true },
      context: ctx,
    })) as Array<Record<string, unknown>>;

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { progress: true, post: { select: { authorId: true } } },
      }),
    );
    expect(result).toEqual([{ progress: null }]);
  });

  it('hydrates through an include for a default selection and strips it', async () => {
    const client = sessionClient([
      { id: 's1', progress: 5, note: 'n', postId: 'p1', post: { authorId: 'me' } },
    ]);
    const checkField = jest.fn(async () => true);
    const provider = relationProvider({ post: { is: { authorId: 'me' } } }, checkField);
    const engine = new GolemEngine(client, relationModels.models, { authorization: provider, ...policy });

    const result = (await engine.findMany({ model: 'Session', context: ctx })) as Array<
      Record<string, unknown>
    >;

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ include: { post: { select: { authorId: true } } } }),
    );
    expect(result).toEqual([{ id: 's1', progress: 5, note: 'n', postId: 'p1' }]);
  });

  it('unions the full relation scalars over a constraint shape that omits a field-rule scalar', async () => {
    const client = sessionClient([{ note: 'secret', post: { authorId: 'other' } }]);
    const checkField = jest.fn(async (_action, _model, row) =>
      (row as { post?: { authorId?: string } }).post?.authorId !== 'other',
    );
    const provider = relationProvider({ post: { is: { title: 'x' } } }, checkField);
    const engine = new GolemEngine(client, relationModels.models, { authorization: provider, ...policy });

    const masked = (await engine.findMany({
      model: 'Session',
      select: { note: true },
      context: ctx,
    })) as Array<Record<string, unknown>>;

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { note: true, post: { select: { authorId: true } } },
      }),
    );
    expect(masked).toEqual([{ note: null }]);

    client.session.findMany.mockResolvedValueOnce([{ note: 'secret', post: { authorId: 'me' } }]);
    const unmasked = (await engine.findMany({
      model: 'Session',
      select: { note: true },
      context: ctx,
    })) as Array<Record<string, unknown>>;
    expect(unmasked).toEqual([{ note: 'secret' }]);
  });

  it('hydrates relation operators that collide with target field names', async () => {
    const client = sessionClient([
      {
        progress: 5,
        post: {
          title: 'visible title',
          organization: { company: { blocked: true } },
        },
      },
    ]);
    const seenRows: unknown[] = [];
    const checkField = jest.fn(async (_action, _model, row) => {
      seenRows.push(structuredClone(row));
      return !(row as {
        post?: { organization?: { company?: { blocked?: boolean } } };
      }).post?.organization?.company?.blocked;
    });
    const provider = relationProvider({}, checkField, {
      post: {
        is: {
          organization: { isNot: { company: { is: { blocked: true } } } },
        },
      },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      select: { progress: true, post: { select: { title: true } } },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          progress: true,
          post: {
            select: {
              title: true,
              organization: {
                select: { company: { select: { blocked: true } } },
              },
            },
          },
        },
      }),
    );
    expect(seenRows).toEqual([
      {
        progress: 5,
        post: {
          title: 'visible title',
          organization: { company: { blocked: true } },
        },
      },
    ]);
    expect(result).toEqual([
      { progress: null, post: { title: 'visible title' } },
    ]);
  });

  it('merges deep dependencies through caller include projections without leaking them', async () => {
    const client = sessionClient([
      {
        id: 's1',
        progress: 5,
        post: {
          id: 'p1',
          title: 'visible title',
          authorId: 'author',
          organizationId: 'o1',
          organization: { id: 'o1', suspended: false },
        },
      },
    ]);
    const provider = relationProvider({}, jest.fn(async () => true), {
      post: { organization: { suspended: true } },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      include: {
        post: { include: { organization: { select: { id: true } } } },
      },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        include: {
          post: {
            include: {
              organization: { select: { id: true, suspended: true } },
            },
          },
        },
      }),
    );
    expect(result).toEqual([
      {
        id: 's1',
        progress: 5,
        post: {
          id: 'p1',
          title: 'visible title',
          authorId: 'author',
          organizationId: 'o1',
          organization: { id: 'o1' },
        },
      },
    ]);
  });

  it('temporarily removes a nested omit for policy evaluation and restores the public shape', async () => {
    const client = sessionClient([
      {
        id: 's1',
        progress: 5,
        post: { id: 'p1', title: 'visible title', authorId: 'author', organizationId: 'o1' },
      },
    ]);
    const provider = relationProvider({}, jest.fn(async () => true));
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      include: { post: { omit: { authorId: true } } },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ include: { post: { omit: {} } } }),
    );
    expect(result).toEqual([
      {
        id: 's1',
        progress: 5,
        post: { id: 'p1', title: 'visible title', organizationId: 'o1' },
      },
    ]);
  });

  it('hydrates a to-many operator when the target has a field named some', async () => {
    const client = sessionClient([
      {
        note: 'secret',
        post: { comments: [{ flagged: true }, { flagged: false }] },
      },
    ]);
    const checkField = jest.fn(async (_action, _model, row) =>
      !(row as { post?: { comments?: Array<{ flagged?: boolean }> } })
        .post?.comments?.some(({ flagged }) => flagged),
    );
    const provider = relationProvider({}, checkField, {
      post: { comments: { some: { flagged: true } } },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      select: { note: true },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          note: true,
          post: {
            select: {
              comments: { select: { flagged: true } },
            },
          },
        },
      }),
    );
    expect(result).toEqual([{ note: null }]);
  });

  it('preserves nullable relation state for field checks without leaking the relation', async () => {
    const client = sessionClient([{ note: 'secret', post: null }]);
    const seenRows: unknown[] = [];
    const checkField = jest.fn(async (_action, _model, row) => {
      seenRows.push(structuredClone(row));
      return (row as { post?: unknown }).post !== null;
    });
    const provider = relationProvider({}, checkField, {
      post: { is: true },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      select: { note: true },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: { note: true, post: { select: { id: true } } },
      }),
    );
    expect(seenRows).toEqual([{ note: 'secret', post: null }]);
    expect(result).toEqual([{ note: null }]);
  });

  it('hydrates and strips deep dependencies for a field on a caller-selected relation', async () => {
    const client = sessionClient([
      {
        id: 's1',
        post: {
          title: 'nested secret',
          organization: { company: { blocked: true } },
        },
      },
    ]);
    const provider: AuthorizationProvider = {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      checkField: jest.fn(async (_action, model, row, fieldName) =>
        model !== 'Post' || fieldName !== 'title' ||
        !(row as { organization?: { company?: { blocked?: boolean } } })
          .organization?.company?.blocked,
      ),
      classifyFields: jest.fn(async (_action, model, fields: readonly string[]) =>
        Object.fromEntries(fields.map((name) => [
          name,
          model === 'Post' && name === 'title'
            ? {
                access: 'conditional' as const,
                requires: ['organization'],
                dependencies: {
                  organization: { company: { blocked: true } },
                },
              }
            : { access: 'always' as const },
        ]))),
    };
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    const result = await engine.findMany({
      model: 'Session',
      select: { id: true, post: { select: { title: true } } },
      context: ctx,
    });

    expect(client.session.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          id: true,
          post: {
            select: {
              title: true,
              organization: {
                select: { company: { select: { blocked: true } } },
              },
            },
          },
        },
      }),
    );
    expect(result).toEqual([{ id: 's1', post: { title: null } }]);
  });

  it('fails closed when an exact dependency cannot be resolved through the datamodel', async () => {
    const client = sessionClient([]);
    const provider = relationProvider({}, jest.fn(async () => true), {
      post: { missingRelation: { secret: true } },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    await expect(
      engine.findMany({ model: 'Session', select: { progress: true }, context: ctx }),
    ).rejects.toThrow('authorization dependencies cannot be hydrated safely');
    expect(client.session.findMany).not.toHaveBeenCalled();
  });

  it('fails closed for a legacy provider that names a relation without an exact tree', async () => {
    const client = sessionClient([]);
    const provider = relationProvider({}, jest.fn(async () => true));
    (provider.classifyFields as jest.Mock).mockResolvedValue({
      progress: { access: 'conditional', requires: ['post'] },
    });
    const engine = new GolemEngine(client, relationModels.models, {
      authorization: provider,
      ...policy,
    });

    await expect(
      engine.findMany({ model: 'Session', select: { progress: true }, context: ctx }),
    ).rejects.toThrow('relation dependency "post" has no exact hydration tree');
    expect(client.session.findMany).not.toHaveBeenCalled();
  });
});

describe('what a prepared read tree records about a masked field', () => {
  const models: DatamodelDocument = {
    models: [
      {
        name: 'Note',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'body', type: 'String' }),
          field({ name: 'secret', type: 'String', isRequired: false }),
          field({ name: 'ownerId', type: 'String' }),
          field({
            name: 'owner',
            type: 'Owner',
            kind: 'object',
            relationName: 'NoteToOwner',
            relationFromFields: ['ownerId'],
            relationToFields: ['id'],
          }),
        ],
      },
      {
        name: 'Owner',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'tenant', type: 'String' }),
        ],
      },
    ],
    enums: [],
  };

  function noteProvider(
    classification: Record<string, FieldClassification>,
    constrainField?: jest.Mock,
  ): AuthorizationProvider {
    return {
      authorize: jest.fn(async () => undefined),
      constrain: jest.fn(async () => ({})),
      checkField: jest.fn(async () => true),
      classifyFields: jest.fn(async (_action, _model, fields: readonly string[]) =>
        Object.fromEntries(
          fields.map((name) => [name, classification[name] ?? { access: 'always' as const }]),
        ),
      ),
      ...(constrainField ? { constrainField } : {}),
    };
  }

  function prepare(provider: AuthorizationProvider, select?: Record<string, unknown>) {
    return prepareReadTree({
      model: models.models[0],
      modelsByName: new Map(models.models.map((model) => [model.name, model])),
      select,
      provider,
      context: { req: {} },
      maxDepth: 5,
      readFieldChecks: true,
    });
  }

  it('carries the condition the provider hands over for each masked field', async () => {
    const constrainField = jest.fn(async () => ({ ownerId: 'me' }));
    const prepared = await prepare(
      noteProvider({ secret: { access: 'conditional', requires: ['ownerId'] } }, constrainField),
      { body: true, secret: true },
    );

    expect(prepared.maskChecks).toEqual([
      { path: [], model: 'Note', field: 'secret', constraint: { ownerId: 'me' } },
    ]);
    expect(constrainField).toHaveBeenCalledWith('read', 'Note', 'secret', { req: {} });
  });

  it('carries no condition at all when the provider offers none', async () => {
    const prepared = await prepare(
      noteProvider({ secret: { access: 'conditional', requires: ['ownerId'] } }),
      { body: true, secret: true },
    );

    expect(prepared.maskChecks).toEqual([
      { path: [], model: 'Note', field: 'secret', constraint: undefined },
    ]);
  });

  it('names the masked field each column was hydrated for', async () => {
    const prepared = await prepare(
      noteProvider(
        { secret: { access: 'conditional', requires: ['ownerId'] } },
        jest.fn(async () => ({ ownerId: 'me' })),
      ),
      { body: true, secret: true },
    );

    expect(prepared.injected).toEqual([{ path: [], field: 'ownerId', masks: ['secret'] }]);
  });

  it('names every masked field a shared column was hydrated for', async () => {
    const prepared = await prepare(
      noteProvider(
        {
          secret: { access: 'conditional', requires: ['ownerId'] },
          body: { access: 'conditional', requires: ['ownerId'] },
        },
        jest.fn(async () => ({ ownerId: 'me' })),
      ),
      { body: true, secret: true },
    );

    expect(prepared.injected).toEqual([
      { path: [], field: 'ownerId', masks: ['body', 'secret'] },
    ]);
  });

  it('names the masked field a hydrated relation was injected for', async () => {
    const prepared = await prepare(
      noteProvider(
        {
          secret: {
            access: 'conditional',
            requires: ['owner'],
            dependencies: { owner: { tenant: true } },
          },
        },
        jest.fn(async () => ({ owner: { is: { tenant: 'acme' } } })),
      ),
      { body: true, secret: true },
    );

    expect(prepared.injected).toEqual([{ path: [], field: 'owner', masks: ['secret'] }]);
    expect(prepared.select).toEqual({
      body: true,
      secret: true,
      owner: { select: { tenant: true } },
    });
  });
});

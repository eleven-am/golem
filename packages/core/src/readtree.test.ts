import { AuthorizationProvider } from './authorization';
import { DatamodelDocument } from './datamodel';
import { GolemForbiddenError, GolemValidationError } from './errors';
import { GolemEngine } from './operations';
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

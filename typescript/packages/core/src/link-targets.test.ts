import { AuthorizationProvider } from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemNotFoundError, GolemValidationError } from './errors';
import { HookRegistry } from './hooks';
import { buildModelMetadata } from './model-meta';
import { GolemEngine } from './operations';
import { collectLinkTargets } from './link-targets';
import { field } from './testing';

const post: DatamodelModel = {
  name: 'Post',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'title', type: 'String' }),
    field({ name: 'author', type: 'User', kind: 'object', relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
    field({ name: 'authorId', type: 'String' }),
  ],
};

const user: DatamodelModel = {
  name: 'User',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
  ],
};

const membership: DatamodelModel = {
  name: 'Membership',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'team', type: 'Team', kind: 'object', relationName: 'MembershipToTeam', relationFromFields: ['orgId', 'teamKey'], relationToFields: ['orgId', 'key'] }),
    field({ name: 'orgId', type: 'String' }),
    field({ name: 'teamKey', type: 'String' }),
  ],
};

const team: DatamodelModel = {
  name: 'Team',
  fields: [
    field({ name: 'orgId', type: 'String' }),
    field({ name: 'key', type: 'String' }),
    field({ name: 'memberships', type: 'Membership', kind: 'object', isList: true, relationName: 'MembershipToTeam' }),
  ],
  primaryKey: { fields: ['orgId', 'key'] },
};

const models = [post, user, membership, team];
const metadata = buildModelMetadata(models);
const ctx = { req: {} };

function provider(): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async (action: string, model: string) =>
      action === 'read' && model === 'User' ? { id: 'readable' } : {}),
  };
}

function delegates() {
  const users = [{ id: 'readable' }, { id: 'hidden' }];
  const matches = (where: unknown, row: { id: string }): boolean => {
    const clause = where as { id?: string; AND?: unknown[] };
    if (clause.AND) return clause.AND.every((part) => matches(part, row));
    return clause.id === undefined || clause.id === row.id;
  };
  return {
    user: {
      findFirst: jest.fn(async ({ where }: { where: unknown }) =>
        users.find((row) => matches(where, row)) ?? null),
    },
    post: { create: jest.fn(async ({ data }: { data: unknown }) => data) },
  };
}

describe('collectLinkTargets', () => {
  it('collects foreign-key scalars and linking nested operations at every depth', () => {
    expect(collectLinkTargets(metadata, 'User', {
      posts: {
        create: [{ title: 'a', authorId: 'u2' }],
        connect: [{ id: 'p1' }],
        set: [{ id: 'p2' }],
        disconnect: [{ id: 'p3' }],
        connectOrCreate: [{ where: { id: 'p4' }, create: { title: 'b' } }],
      },
    })).toEqual([
      { model: 'Post', where: { id: 'p1' }, createsWhenMissing: false },
      { model: 'Post', where: { id: 'p2' }, createsWhenMissing: false },
      { model: 'Post', where: { id: 'p3' }, createsWhenMissing: false },
      { model: 'Post', where: { id: 'p4' }, createsWhenMissing: true },
      { model: 'User', where: { id: 'u2' }, createsWhenMissing: false },
    ]);
  });

  it('maps a compound foreign key onto the fields it references', () => {
    expect(collectLinkTargets(metadata, 'Membership', { orgId: 'o1', teamKey: { set: 'k1' } }))
      .toEqual([{ model: 'Team', where: { orgId: 'o1', key: 'k1' }, createsWhenMissing: false }]);
  });

  it('refuses a compound foreign key written only in part', () => {
    expect(() => collectLinkTargets(metadata, 'Membership', { orgId: 'o1' }))
      .toThrow(GolemValidationError);
  });

  it('ignores a foreign key cleared to null', () => {
    expect(collectLinkTargets(metadata, 'Post', { authorId: null })).toEqual([]);
  });
});

describe('linking to a row the caller cannot read', () => {
  it('refuses an unreadable target exactly as a missing one', async () => {
    const client = delegates();
    const engine = new GolemEngine(client, models, {
      authorization: provider(),
      checkWriteResults: false,
      checkReadFields: false,
    });
    const attempt = (authorId: string) =>
      engine.create({ model: 'Post', data: { id: 'p', title: 't', authorId }, context: ctx })
        .then(() => 'created', (error: Error) => `${error.constructor.name}: ${error.message}`);

    const hidden = await attempt('hidden');
    expect(hidden).toBe(await attempt('nowhere'));
    expect(hidden).toBe(`${GolemNotFoundError.name}: User not found`);
    await expect(attempt('readable')).resolves.toBe('created');
    expect(client.post.create).toHaveBeenCalledTimes(1);
  });

  it('does not check a foreign key written by a before hook', async () => {
    const client = delegates();
    const hooks = new HookRegistry();
    hooks.registerBefore('Post', 'create', (request: any) => ({
      ...request,
      data: { ...request.data, authorId: 'hidden' },
    }));
    const engine = new GolemEngine(client, models, {
      authorization: provider(),
      hooks,
      checkWriteResults: false,
      checkReadFields: false,
    });

    await engine.create({ model: 'Post', data: { id: 'p', title: 't' }, context: ctx });
    expect(client.post.create).toHaveBeenCalledWith(
      expect.objectContaining({ data: { id: 'p', title: 't', authorId: 'hidden' } }),
    );
  });

  it('does not check links written without a caller context', async () => {
    const client = delegates();
    const engine = new GolemEngine(client, models, {
      authorization: provider(),
      checkWriteResults: false,
      checkReadFields: false,
    });

    await engine.create({ model: 'Post', data: { id: 'p', title: 't', authorId: 'hidden' } });
    expect(client.user.findFirst).not.toHaveBeenCalled();
    expect(client.post.create).toHaveBeenCalledTimes(1);
  });
});

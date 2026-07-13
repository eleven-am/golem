import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { GolemValidationError } from './errors';

import { buildGolemSchema, createGolemEngine } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{ User: 'id' | 'email' | 'name' }> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'name', type: 'String', isRequired: false }),
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
      create: jest.fn().mockResolvedValue({ id: 'u1' }),
      update: jest.fn().mockResolvedValue({ id: 'u1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: 'u1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    },
  };
}

const displayName = {
  model: 'User',
  name: 'displayName',
  type: 'String!',
  requires: ['name', 'email'],
  resolve: (parent: { name?: string; email: string }) => parent.name ?? parent.email,
};

describe('computed fields', () => {
  it('adds the field to the object type and resolves it from required columns', async () => {
    const client = fakeClient();
    client.user.findMany.mockResolvedValue([{ name: null, email: 'a@b.c' }]);
    const schema = buildGolemSchema({ datamodel, client, computedFields: [displayName] });
    expect(printSchema(schema)).toContain('displayName: String!');

    const result = await graphql({ schema, source: '{ users { displayName } }' });
    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ users: [{ displayName: 'a@b.c' }] });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { name: true, email: true } }),
    );
  });

  it('does not fetch required columns when the computed field is not requested', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({ datamodel, client, computedFields: [displayName] });
    await graphql({ schema, source: '{ users { id } }' });
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { id: true } }),
    );
  });

  it('rejects unknown models, unknown requires and collisions at build time', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        computedFields: [{ ...displayName, model: 'Ghost' }],
      }),
    ).toThrow('targets unknown model Ghost');
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        computedFields: [{ ...displayName, requires: ['ghost'] }],
      }),
    ).toThrow('requires unknown field ghost');
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        computedFields: [{ ...displayName, name: 'email' }],
      }),
    ).toThrow('collides with an existing field');
  });
});

describe('custom operations', () => {
  it('registers custom queries with resolved arg and return types', async () => {
    const client = fakeClient();
    client.user.findMany.mockResolvedValue([{ id: 'u1' }]);
    const schema = buildGolemSchema({
      datamodel,
      client,
      customOperations: [
        {
          kind: 'query',
          name: 'searchUsers',
          type: '[User!]!',
          args: { where: 'UserWhereInput', limit: 'Int' },
          resolve: async (args: { limit?: number }) =>
            client.user.findMany({ take: args.limit, select: { id: true } }),
        },
      ],
    });
    const sdl = printSchema(schema);
    expect(sdl).toContain('searchUsers(where: UserWhereInput, limit: Int): [User!]!');

    const result = await graphql({ schema, source: '{ searchUsers(limit: 3) { id } }' });
    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ searchUsers: [{ id: 'u1' }] });
  });

  it('maps golem errors thrown by custom operations to extension codes', async () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      customOperations: [
        {
          kind: 'mutation',
          name: 'failHard',
          type: 'Boolean!',
          resolve: () => {
            throw new GolemValidationError('nope');
          },
        },
      ],
    });
    const result = await graphql({ schema, source: 'mutation { failHard }' });
    expect(result.errors?.[0].extensions?.code).toBe('BAD_USER_INPUT');
  });

  it('rejects collisions with generated fields and unknown type references', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        customOperations: [
          { kind: 'query', name: 'users', type: '[User!]!', resolve: () => [] },
        ],
      }),
    ).toThrow('Custom query users collides with an existing field');
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        customOperations: [
          { kind: 'query', name: 'broken', type: 'Ghost!', resolve: () => null },
        ],
      }),
    ).toThrow('Unknown type Ghost');
  });
});

describe('createGolemEngine', () => {
  it('builds a standalone engine that schema construction can reuse', async () => {
    const client = fakeClient();
    const options = { datamodel, client };
    const engine = createGolemEngine(options);
    buildGolemSchema({ ...options, engine });

    await engine.update({ model: 'User', where: { id: 'u1' }, data: { name: 'x' } });
    expect(client.user.update).toHaveBeenCalledWith({
      where: { id: 'u1' },
      data: { name: 'x' },
      select: undefined,
      include: undefined,
    });
  });
});

import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { GolemForbiddenError } from './errors';
import { HookRegistry } from './hooks';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{
  User: 'id' | 'email' | 'name' | 'apiKey' | 'credential' | 'serverLabel';
}> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'name', type: 'String', isRequired: false }),
        field({ name: 'apiKey', type: 'String', isRequired: false }),
        field({
          name: 'credential',
          type: 'String',
          isRequired: false,
          isUnique: true,
        }),
        field({ name: 'serverLabel', type: 'String', isRequired: false, isUnique: true }),
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
      update: jest.fn(),
      updateMany: jest.fn(),
      delete: jest.fn(),
      deleteMany: jest.fn(),
    },
  };
}

function inputBlock(sdl: string, name: string): string {
  const start = sdl.indexOf(`input ${name}`);
  return sdl.slice(start, sdl.indexOf('}', start));
}

describe('model configuration', () => {
  it('removes disabled operations from the schema entirely', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { operations: ['findOne', 'findMany', 'create'] } },
    });
    const sdl = printSchema(schema);
    expect(sdl).toContain('createUser');
    expect(sdl).not.toContain('updateUser');
    expect(sdl).not.toContain('deleteUser');
    expect(sdl).not.toContain('updateManyUsers');
    expect(sdl).not.toContain('deleteManyUsers');
  });

  it('applies default operations when the model has no override', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      defaults: { operations: ['findOne', 'findMany'] },
    });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('type Mutation');
    expect(sdl).toContain('user(where');
  });

  it('hides hidden fields from every schema surface', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { hidden: ['apiKey'] } },
    });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('apiKey');
  });

  it('excludes immutable fields from update inputs but keeps them in create', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { immutable: ['email'] } },
    });
    const sdl = printSchema(schema);
    const createInput = sdl.slice(
      sdl.indexOf('input UserCreateInput'),
      sdl.indexOf('}', sdl.indexOf('input UserCreateInput')),
    );
    const updateInput = sdl.slice(
      sdl.indexOf('input UserUpdateInput'),
      sdl.indexOf('}', sdl.indexOf('input UserUpdateInput')),
    );
    const updateManyInput = sdl.slice(
      sdl.indexOf('input UserUpdateManyInput'),
      sdl.indexOf('}', sdl.indexOf('input UserUpdateManyInput')),
    );
    expect(createInput).toContain('email: String!');
    expect(updateInput).not.toContain('email');
    expect(updateManyInput).not.toContain('email');
  });

  it('keeps read-only fields queryable but removes them from every write input', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { readOnly: ['serverLabel'] } },
    });
    const sdl = printSchema(schema);

    expect(sdl).toContain('serverLabel: String');
    expect(inputBlock(sdl, 'UserWhereInput')).toContain('serverLabel: StringFilter');
    expect(inputBlock(sdl, 'UserWhereUniqueInput')).toContain('serverLabel: String');
    expect(inputBlock(sdl, 'UserOrderByInput')).toContain('serverLabel: SortOrder');
    expect(inputBlock(sdl, 'UserCreateInput')).not.toContain('serverLabel');
    expect(inputBlock(sdl, 'UserUpdateInput')).not.toContain('serverLabel');
    expect(inputBlock(sdl, 'UserUpdateManyInput')).not.toContain('serverLabel');
  });

  it('accepts write-only fields in writes and removes them from every read surface', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { writeOnly: ['credential'] } },
    });
    const sdl = printSchema(schema);

    expect(inputBlock(sdl, 'UserCreateInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'UserUpdateInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'UserUpdateManyInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'UserWhereInput')).not.toContain('credential');
    expect(inputBlock(sdl, 'UserWhereUniqueInput')).not.toContain('credential');
    expect(inputBlock(sdl, 'UserOrderByInput')).not.toContain('credential');
    expect(sdl.slice(sdl.indexOf('type User'), sdl.indexOf('}', sdl.indexOf('type User')))).not.toContain(
      'credential',
    );
  });

  it('rejects GraphQL attempts to read or filter by a write-only field', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { User: { writeOnly: ['credential'] } },
    });

    const selection = await graphql({ schema, source: '{ users { credential } }' });
    const filter = await graphql({
      schema,
      source: '{ users(where: { credential: { equals: "secret" } }) { id } }',
    });

    expect(selection.errors?.[0].message).toContain('Cannot query field "credential"');
    expect(filter.errors?.[0].message).toContain('Field "credential" is not defined');
    expect(client.user.findMany).not.toHaveBeenCalled();
  });

  it('supports create-only write-only fields through immutable', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { User: { writeOnly: ['credential'], immutable: ['credential'] } },
    });
    const sdl = printSchema(schema);

    expect(inputBlock(sdl, 'UserCreateInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'UserUpdateInput')).not.toContain('credential');
    expect(inputBlock(sdl, 'UserUpdateManyInput')).not.toContain('credential');
    expect(sdl.slice(sdl.indexOf('type User'), sdl.indexOf('}', sdl.indexOf('type User')))).not.toContain(
      'credential',
    );

    const result = await graphql({
      schema,
      source: 'mutation { updateUser(where: { id: "u1" }, data: { credential: "new" }) { id } }',
    });
    expect(result.errors?.[0].message).toContain('Field "credential" is not defined');
    expect(client.user.update).not.toHaveBeenCalled();
  });

  it('passes write-only input through hooks before authorization and Prisma', async () => {
    const order: string[] = [];
    const hooks = new HookRegistry();
    hooks.registerBefore('User', 'create', (request) => {
      order.push('hook');
      return {
        ...request,
        data: { ...request.data, credential: `hashed:${request.data.credential}` },
      };
    });
    const authorization = {
      authorize: jest.fn(async () => {
        order.push('authorize');
      }),
      constrain: jest.fn(async () => undefined),
    };
    const client = fakeClient();
    const schema = buildGolemSchema({
      datamodel,
      client,
      hooks,
      authorization,
      defaults: { checkWriteResults: false, checkReadFields: false },
      models: { User: { writeOnly: ['credential'] } },
    });

    const result = await graphql({
      schema,
      source: 'mutation { createUser(data: { email: "a@b.c", credential: "plain" }) { id } }',
      contextValue: { userId: 'u1' },
    });

    expect(result.errors).toBeUndefined();
    expect(order).toEqual(['hook', 'authorize']);
    expect(client.user.create).toHaveBeenCalledWith({
      data: { email: 'a@b.c', credential: 'hashed:plain' },
      select: { id: true },
    });
  });

  it('does not let write-only input bypass authorization', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({
      datamodel,
      client,
      authorization: {
        authorize: jest.fn(async () => {
          throw new GolemForbiddenError('denied');
        }),
        constrain: jest.fn(async () => undefined),
      },
      defaults: { checkWriteResults: false, checkReadFields: false },
      models: { User: { writeOnly: ['credential'] } },
    });

    const result = await graphql({
      schema,
      source: 'mutation { createUser(data: { email: "a@b.c", credential: "plain" }) { id } }',
      contextValue: { userId: 'u1' },
    });

    expect(result.errors?.[0].extensions?.code).toBe('FORBIDDEN');
    expect(client.user.create).not.toHaveBeenCalled();
  });

  it('applies read-only and write-only rules to nested create and update inputs', async () => {
    const relational: DatamodelDocument<{
      User: 'id';
      Post: 'id' | 'credential' | 'serverLabel';
    }> = {
      models: [
        {
          name: 'User',
          fields: [
            field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
            field({
              name: 'posts',
              type: 'Post',
              kind: 'object',
              isList: true,
              relationName: 'PostToUser',
            }),
          ],
        },
        {
          name: 'Post',
          fields: [
            field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
            field({ name: 'credential', type: 'String', isRequired: false }),
            field({ name: 'serverLabel', type: 'String', isRequired: false }),
            field({
              name: 'author',
              type: 'User',
              kind: 'object',
              relationName: 'PostToUser',
              relationFromFields: ['authorId'],
              relationToFields: ['id'],
            }),
            field({ name: 'authorId', type: 'String', isReadOnly: true }),
          ],
        },
      ],
      enums: [],
    };
    const client = {
      ...fakeClient(),
      post: {
        findMany: jest.fn(),
        findUnique: jest.fn(),
        create: jest.fn(),
        update: jest.fn(),
        updateMany: jest.fn(),
        delete: jest.fn(),
        deleteMany: jest.fn(),
      },
    };
    client.user.update.mockResolvedValue({ id: 'u1' });
    const schema = buildGolemSchema({
      datamodel: relational,
      client,
      models: { Post: { readOnly: ['serverLabel'], writeOnly: ['credential'] } },
    });
    const sdl = printSchema(schema);

    expect(inputBlock(sdl, 'PostCreateWithoutAuthorInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'PostCreateWithoutAuthorInput')).not.toContain('serverLabel');
    expect(inputBlock(sdl, 'PostUpdateWithoutAuthorInput')).toContain('credential: String');
    expect(inputBlock(sdl, 'PostUpdateWithoutAuthorInput')).not.toContain('serverLabel');

    const createResult = await graphql({
      schema,
      source: `mutation {
        createUser(data: { posts: { create: [{ credential: "initial" }] } }) { id }
      }`,
    });
    expect(createResult.errors).toBeUndefined();
    expect(client.user.create).toHaveBeenCalledWith({
      data: { posts: { create: [{ credential: 'initial' }] } },
      select: { id: true },
    });

    const result = await graphql({
      schema,
      source: `mutation {
        updateUser(
          where: { id: "u1" }
          data: { posts: { update: [{ where: { id: "p1" }, data: { credential: "new" } }] } }
        ) { id }
      }`,
    });
    expect(result.errors).toBeUndefined();
    expect(client.user.update).toHaveBeenCalledWith({
      where: { id: 'u1' },
      data: {
        posts: { update: [{ where: { id: 'p1' }, data: { credential: 'new' } }] },
      },
      select: { id: true },
    });
  });

  it('keeps nested update envelopes distinct for multiple relations to one model', () => {
    const relational: DatamodelDocument<{
      User: 'id';
      Post: 'id' | 'author' | 'reviewer';
    }> = {
      models: [
        {
          name: 'User',
          fields: [
            field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
            field({
              name: 'authoredPosts',
              type: 'Post',
              kind: 'object',
              isList: true,
              relationName: 'PostAuthor',
            }),
            field({
              name: 'reviewedPosts',
              type: 'Post',
              kind: 'object',
              isList: true,
              relationName: 'PostReviewer',
            }),
          ],
        },
        {
          name: 'Post',
          fields: [
            field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
            field({
              name: 'author',
              type: 'User',
              kind: 'object',
              relationName: 'PostAuthor',
              relationFromFields: ['authorId'],
              relationToFields: ['id'],
            }),
            field({ name: 'authorId', type: 'String', isReadOnly: true }),
            field({
              name: 'reviewer',
              type: 'User',
              kind: 'object',
              relationName: 'PostReviewer',
              relationFromFields: ['reviewerId'],
              relationToFields: ['id'],
            }),
            field({ name: 'reviewerId', type: 'String', isReadOnly: true }),
          ],
        },
      ],
      enums: [],
    };
    const schema = buildGolemSchema({
      datamodel: relational,
      client: {
        ...fakeClient(),
        post: {
          findMany: jest.fn(),
          findUnique: jest.fn(),
          create: jest.fn(),
          update: jest.fn(),
          updateMany: jest.fn(),
          delete: jest.fn(),
          deleteMany: jest.fn(),
        },
      },
    });
    const sdl = printSchema(schema);
    const userUpdate = inputBlock(sdl, 'UserUpdateInput');

    expect(userUpdate).toContain('authoredPosts: PostUpdateManyWithoutAuthorInput');
    expect(userUpdate).toContain('reviewedPosts: PostUpdateManyWithoutReviewerInput');
    expect(inputBlock(sdl, 'PostUpdateManyWithoutAuthorInput')).toContain(
      'update: [PostUpdateWithWhereUniqueWithoutAuthorInput!]',
    );
    expect(inputBlock(sdl, 'PostUpdateManyWithoutReviewerInput')).toContain(
      'update: [PostUpdateWithWhereUniqueWithoutReviewerInput!]',
    );
  });

  it('resolves the opposite field for nested updates on self-relations', () => {
    const selfRelational: DatamodelDocument<{ Node: 'id' | 'parent' | 'children' }> = {
      models: [
        {
          name: 'Node',
          fields: [
            field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
            field({
              name: 'parent',
              type: 'Node',
              kind: 'object',
              isRequired: false,
              relationName: 'NodeTree',
              relationFromFields: ['parentId'],
              relationToFields: ['id'],
            }),
            field({ name: 'parentId', type: 'String', isReadOnly: true }),
            field({
              name: 'children',
              type: 'Node',
              kind: 'object',
              isList: true,
              relationName: 'NodeTree',
            }),
          ],
        },
      ],
      enums: [],
    };
    const schema = buildGolemSchema({
      datamodel: selfRelational,
      client: {
        node: {
          findMany: jest.fn(),
          findUnique: jest.fn(),
          create: jest.fn(),
          update: jest.fn(),
          updateMany: jest.fn(),
          delete: jest.fn(),
          deleteMany: jest.fn(),
        },
      },
    });
    const nodeUpdate = inputBlock(printSchema(schema), 'NodeUpdateInput');

    expect(nodeUpdate).toContain('parent: NodeUpdateOneWithoutChildrenInput');
    expect(nodeUpdate).toContain('children: NodeUpdateManyWithoutParentInput');
  });

  it('rejects hiding the primary key', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { hidden: ['id' as 'apiKey'] } },
      }),
    ).toThrow('Cannot hide primary key User.id');
  });

  it('rejects unknown field names in every field configuration', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { hidden: ['ghost' as 'apiKey'] } },
      }),
    ).toThrow('Unknown field ghost in configuration for model User');
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { writeOnly: ['ghost' as 'credential'] } },
      }),
    ).toThrow('Unknown field ghost in configuration for model User');
  });

  it.each([
    { config: { hidden: ['credential'], writeOnly: ['credential'] }, modes: 'hidden, writeOnly' },
    { config: { readOnly: ['credential'], writeOnly: ['credential'] }, modes: 'readOnly, writeOnly' },
    { config: { immutable: ['credential'], readOnly: ['credential'] }, modes: 'immutable, readOnly' },
  ])('rejects conflicting $modes configuration', ({ config, modes }) => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: config },
      }),
    ).toThrow(`Conflicting field configuration for User.credential: ${modes}`);
  });

  it('rejects write-only primary keys, relations, and Prisma read-only fields', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { writeOnly: ['id' as 'credential'] } },
      }),
    ).toThrow('Cannot make primary key User.id write-only');

    const relational: DatamodelDocument<{
      User: 'id' | 'posts';
      Post: 'id' | 'author' | 'authorId';
    }> = {
      models: [
        {
          name: 'User',
          fields: [
            field({ name: 'id', type: 'String', isId: true }),
            field({
              name: 'posts',
              type: 'Post',
              kind: 'object',
              isList: true,
              relationName: 'PostToUser',
            }),
          ],
        },
        {
          name: 'Post',
          fields: [
            field({ name: 'id', type: 'String', isId: true }),
            field({
              name: 'author',
              type: 'User',
              kind: 'object',
              relationName: 'PostToUser',
              relationFromFields: ['authorId'],
              relationToFields: ['id'],
            }),
            field({ name: 'authorId', type: 'String', isReadOnly: true }),
          ],
        },
      ],
      enums: [],
    };
    expect(() =>
      buildGolemSchema({
        datamodel: relational,
        client: {},
        models: { User: { writeOnly: ['posts'] } },
      }),
    ).toThrow('Cannot make relation field User.posts write-only');
    expect(() =>
      buildGolemSchema({
        datamodel: relational,
        client: {},
        models: { Post: { writeOnly: ['authorId' as 'author'] } },
      }),
    ).toThrow('Cannot make Prisma read-only field Post.authorId write-only');
  });
});

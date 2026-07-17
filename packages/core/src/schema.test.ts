import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{ User: 'id' | 'email'; Post: 'id' | 'title' }> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'name', type: 'String', isRequired: false }),
        field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostToUser' }),
      ],
    },
    {
      name: 'Post',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'title', type: 'String' }),
        field({ name: 'published', type: 'Boolean', hasDefaultValue: true }),
        field({ name: 'author', type: 'User', kind: 'object', relationName: 'PostToUser', relationFromFields: ['authorId'], relationToFields: ['id'] }),
        field({ name: 'authorId', type: 'String', isReadOnly: true }),
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
      create: jest.fn().mockResolvedValue({ id: '1' }),
      update: jest.fn().mockResolvedValue({ id: '1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: '1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    },
    post: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      create: jest.fn().mockResolvedValue({ id: '1' }),
      update: jest.fn().mockResolvedValue({ id: '1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: '1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
    },
  };
}

describe('buildGolemSchema queries', () => {
  it('exposes findOne and findMany per model', () => {
    const schema = buildGolemSchema({ datamodel, client: fakeClient() });
    const sdl = printSchema(schema);
    expect(sdl).toContain('user(where: UserWhereUniqueInput!): User');
    expect(sdl).toContain('users(where: UserWhereInput, orderBy: [UserOrderByInput!], take: Int, skip: Int): [User!]!');
    expect(sdl).toContain('post(where: PostWhereUniqueInput!): Post');
    expect(sdl).toContain('posts(');
  });

  it('derives a nested prisma select from the query selection set', async () => {
    const client = fakeClient();
    client.user.findMany.mockResolvedValue([{ email: 'a@b.c', posts: [{ title: 'hello' }] }]);
    const schema = buildGolemSchema({ datamodel, client });
    const result = await graphql({ schema, source: '{ users { email posts { title } } }' });
    expect(result.errors).toBeUndefined();
    expect(client.user.findMany).toHaveBeenCalledWith({
      where: undefined,
      orderBy: undefined,
      take: undefined,
      skip: undefined,
      select: { email: true, posts: { select: { title: true } } },
    });
  });

  it('passes filters, ordering and pagination through to prisma', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({ datamodel, client });
    const result = await graphql({
      schema,
      source: `{
        users(
          where: { email: { contains: "@b.c" }, AND: [{ name: { equals: "roy" } }] }
          orderBy: [{ email: asc }]
          take: 10
          skip: 5
        ) { id }
      }`,
    });
    expect(result.errors).toBeUndefined();
    expect(client.user.findMany).toHaveBeenCalledWith({
      where: { email: { contains: '@b.c' }, AND: [{ name: { equals: 'roy' } }] },
      orderBy: [{ email: 'asc' }],
      take: 10,
      skip: 5,
      select: { id: true },
    });
  });

  it('selects only the primary key when no model field is requested', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({ datamodel, client });
    const result = await graphql({ schema, source: '{ users { __typename } }' });
    expect(result.errors).toBeUndefined();
    expect(client.user.findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { id: true } }),
    );
  });

  it('excludes models configured as false', () => {
    const schema = buildGolemSchema({ datamodel, client: fakeClient(), models: { Post: false } });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('type Post');
    expect(sdl).not.toContain('createPost');
    expect(sdl).toContain('type User');
  });

  it('rejects exposing a composite primary key model on the GraphQL surface', () => {
    const composite: DatamodelDocument = {
      models: [
        {
          name: 'PostTag',
          fields: [
            field({ name: 'postId', type: 'String' }),
            field({ name: 'tagId', type: 'String' }),
          ],
          primaryKey: { fields: ['postId', 'tagId'] },
        },
      ],
      enums: [],
    };
    expect(() => buildGolemSchema({ datamodel: composite, client: {} })).toThrow(
      'Model PostTag has a composite primary key and cannot be exposed on the generated GraphQL surface',
    );
  });

  it('rejects enabling subscriptions on a composite primary key model', () => {
    const composite: DatamodelDocument = {
      models: [
        {
          name: 'PostTag',
          fields: [
            field({ name: 'postId', type: 'String' }),
            field({ name: 'tagId', type: 'String' }),
          ],
          primaryKey: { fields: ['postId', 'tagId'] },
        },
      ],
      enums: [],
    };
    expect(() =>
      buildGolemSchema({
        datamodel: composite,
        client: {},
        models: { PostTag: { subscriptions: true } },
        eventBus: {} as never,
      }),
    ).toThrow('Model PostTag has a composite primary key and cannot enable subscriptions');
  });

  it('keeps a hidden composite primary key model available to the engine', () => {
    const composite: DatamodelDocument = {
      models: [
        {
          name: 'User',
          fields: [field({ name: 'id', type: 'String', isId: true }), field({ name: 'email', type: 'String' })],
        },
        {
          name: 'PostTag',
          fields: [
            field({ name: 'postId', type: 'String' }),
            field({ name: 'tagId', type: 'String' }),
          ],
          primaryKey: { fields: ['postId', 'tagId'] },
        },
      ],
      enums: [],
    };
    const schema = buildGolemSchema({
      datamodel: composite,
      client: { user: { findMany: jest.fn() }, postTag: { findMany: jest.fn() } },
      models: { PostTag: false },
    });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('type PostTag');
    expect(sdl).toContain('type User');
  });

  it('rejects unsupported scalar types with a clear error', () => {
    const bad: DatamodelDocument = {
      models: [
        {
          name: 'Blob',
          fields: [
            field({ name: 'id', type: 'String', isId: true }),
            field({ name: 'payload', type: 'Bytes' }),
          ],
        },
      ],
      enums: [],
    };
    const client = { blob: { findMany: jest.fn(), findUnique: jest.fn() } };
    expect(() => buildGolemSchema({ datamodel: bad, client })).toThrow(
      'Unsupported scalar type Bytes on Blob.payload',
    );
  });
});

describe('buildGolemSchema mutations', () => {
  it('exposes the five mutations per model', () => {
    const schema = buildGolemSchema({ datamodel, client: fakeClient() });
    const sdl = printSchema(schema);
    expect(sdl).toContain('createUser(data: UserCreateInput!): User!');
    expect(sdl).toContain('updateUser(where: UserWhereUniqueInput!, data: UserUpdateInput!): User!');
    expect(sdl).toContain('deleteUser(where: UserWhereUniqueInput!): User!');
    expect(sdl).toContain('updateManyUsers(where: UserWhereInput, data: UserUpdateManyInput!): BatchPayload!');
    expect(sdl).toContain('deleteManyUsers(where: UserWhereInput): BatchPayload!');
  });

  it('excludes foreign key scalars from write inputs and requires bare scalars', () => {
    const schema = buildGolemSchema({ datamodel, client: fakeClient() });
    const sdl = printSchema(schema);
    const createInput = sdl.slice(sdl.indexOf('input PostCreateInput'), sdl.indexOf('}', sdl.indexOf('input PostCreateInput')));
    expect(createInput).not.toContain('authorId');
    expect(createInput).toContain('title: String!');
    expect(createInput).toContain('published: Boolean');
    expect(createInput).not.toContain('published: Boolean!');
    expect(createInput).toContain('author: UserCreateNestedOneWithoutPostsInput!');
    const updateInput = sdl.slice(sdl.indexOf('input PostUpdateInput'), sdl.indexOf('}', sdl.indexOf('input PostUpdateInput')));
    expect(updateInput).not.toContain('authorId');
    expect(updateInput).toContain('title: String');
    expect(updateInput).toContain('author: UserUpdateOneRequiredWithoutPostsInput');
  });

  it('builds Without variants that drop the back relation', () => {
    const schema = buildGolemSchema({ datamodel, client: fakeClient() });
    const sdl = printSchema(schema);
    const withoutInput = sdl.slice(
      sdl.indexOf('input PostCreateWithoutAuthorInput'),
      sdl.indexOf('}', sdl.indexOf('input PostCreateWithoutAuthorInput')),
    );
    expect(withoutInput).toContain('title: String!');
    expect(withoutInput).not.toContain('author');
  });

  it('passes nested create and connect payloads through to prisma', async () => {
    const client = fakeClient();
    const schema = buildGolemSchema({ datamodel, client });
    const result = await graphql({
      schema,
      source: `mutation {
        createUser(data: {
          email: "a@b.c"
          posts: { create: [{ title: "hi" }], connect: [{ id: "p1" }] }
        }) { id }
      }`,
    });
    expect(result.errors).toBeUndefined();
    expect(client.user.create).toHaveBeenCalledWith({
      data: {
        email: 'a@b.c',
        posts: { create: [{ title: 'hi' }], connect: [{ id: 'p1' }] },
      },
      select: { id: true },
    });
  });

  it('maps engine errors to graphql extension codes', async () => {
    const client = fakeClient();
    client.user.update.mockRejectedValue(Object.assign(new Error('missing'), { code: 'P2025' }));
    client.user.create.mockRejectedValue(Object.assign(new Error('dupe'), { code: 'P2002' }));
    const schema = buildGolemSchema({ datamodel, client });

    const updateResult = await graphql({
      schema,
      source: 'mutation { updateUser(where: { id: "x" }, data: { name: "n" }) { id } }',
    });
    expect(updateResult.errors?.[0].extensions?.code).toBe('NOT_FOUND');
    expect(updateResult.errors?.[0].message).not.toContain('P2025');

    const createResult = await graphql({
      schema,
      source: 'mutation { createUser(data: { email: "a@b.c" }) { id } }',
    });
    expect(createResult.errors?.[0].extensions?.code).toBe('CONFLICT');
  });

  it('returns batch counts from the many mutations', async () => {
    const client = fakeClient();
    client.post.deleteMany.mockResolvedValue({ count: 3 });
    const schema = buildGolemSchema({ datamodel, client });
    const result = await graphql({
      schema,
      source: 'mutation { deleteManyPosts(where: { published: { equals: false } }) { count } }',
    });
    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ deleteManyPosts: { count: 3 } });
    expect(client.post.deleteMany).toHaveBeenCalledWith({
      where: { published: { equals: false } },
    });
  });
});

import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument = {
  models: [
    {
      name: 'PostTag',
      fields: [
        field({ name: 'postId', type: 'String' }),
        field({ name: 'tagId', type: 'String' }),
        field({ name: 'label', type: 'String' }),
      ],
      primaryKey: { fields: ['postId', 'tagId'] },
    },
    {
      name: 'Membership',
      fields: [
        field({ name: 'tenantId', type: 'String' }),
        field({ name: 'userId', type: 'String' }),
        field({ name: 'role', type: 'String' }),
      ],
      primaryKey: { name: 'membershipKey', fields: ['tenantId', 'userId'] },
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
    {
      name: 'Repository',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'ownerId', type: 'String' }),
        field({ name: 'slug', type: 'String' }),
      ],
      uniqueIndexes: [{ name: 'ownerSlugKey', fields: ['ownerId', 'slug'] }],
    },
  ],
  enums: [],
};

function delegate() {
  return {
    findMany: jest.fn().mockResolvedValue([]),
    findUnique: jest.fn().mockResolvedValue(null),
    findFirst: jest.fn().mockResolvedValue(null),
    create: jest.fn().mockResolvedValue({}),
    update: jest.fn().mockResolvedValue({}),
    updateMany: jest.fn().mockResolvedValue({ count: 0 }),
    delete: jest.fn().mockResolvedValue({}),
    deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
  };
}

function client() {
  return {
    postTag: delegate(),
    membership: delegate(),
    branch: delegate(),
    repository: delegate(),
  };
}

describe('composite selectors on the generated GraphQL surface', () => {
  it('generates named and unnamed compound id and unique inputs alongside scalar uniques', () => {
    const sdl = printSchema(buildGolemSchema({ datamodel, client: client() }));

    expect(sdl).toContain('postId_tagId: PostTagPostId_tagIdCompoundUniqueInput');
    expect(sdl).toContain('membershipKey: MembershipMembershipKeyCompoundUniqueInput');
    expect(sdl).toContain('authorId_name: BranchAuthorId_nameCompoundUniqueInput');
    expect(sdl).toContain('ownerSlugKey: RepositoryOwnerSlugKeyCompoundUniqueInput');
    expect(sdl).toContain('id: String');
  });

  it('preserves the Prisma compound accessor for find-one, update, delete, and upsert', async () => {
    const prisma = client();
    const selector = { postId_tagId: { postId: 'p1', tagId: 't1' } };
    prisma.postTag.findUnique.mockResolvedValue({ postId: 'p1', tagId: 't1', label: 'old' });
    prisma.postTag.findFirst.mockResolvedValue({ postId: 'p1', tagId: 't1' });
    prisma.postTag.update.mockResolvedValue({ postId: 'p1', tagId: 't1', label: 'new' });
    prisma.postTag.delete.mockResolvedValue({ postId: 'p1', tagId: 't1', label: 'new' });
    const schema = buildGolemSchema({ datamodel, client: prisma });

    const find = await graphql({
      schema,
      source: '{ postTag(where: { postId_tagId: { postId: "p1", tagId: "t1" } }) { label } }',
    });
    expect(find.errors).toBeUndefined();
    expect(prisma.postTag.findUnique).toHaveBeenLastCalledWith({
      where: selector,
      select: { label: true },
    });

    const update = await graphql({
      schema,
      source: 'mutation { updatePostTag(where: { postId_tagId: { postId: "p1", tagId: "t1" } }, data: { label: "new" }) { label } }',
    });
    expect(update.errors).toBeUndefined();
    expect(prisma.postTag.update).toHaveBeenLastCalledWith({
      where: selector,
      data: { label: 'new' },
      select: { label: true },
    });

    const upsert = await graphql({
      schema,
      source: 'mutation { upsertPostTag(where: { postId_tagId: { postId: "p1", tagId: "t1" } }, create: { postId: "p1", tagId: "t1", label: "new" }, update: { label: "new" }) { label } }',
    });
    expect(upsert.errors).toBeUndefined();
    expect(prisma.postTag.findFirst).toHaveBeenLastCalledWith({
      where: { postId: 'p1', tagId: 't1' },
      select: { postId: true, tagId: true },
    });
    expect(prisma.postTag.update).toHaveBeenLastCalledWith(expect.objectContaining({
      where: selector,
      data: { label: 'new' },
    }));

    const remove = await graphql({
      schema,
      source: 'mutation { deletePostTag(where: { postId_tagId: { postId: "p1", tagId: "t1" } }) { label } }',
    });
    expect(remove.errors).toBeUndefined();
    expect(prisma.postTag.delete).toHaveBeenLastCalledWith({
      where: selector,
      select: { label: true },
    });
  });

  it('selects every primary-key component for a composite __typename-only result', async () => {
    const prisma = client();
    const schema = buildGolemSchema({ datamodel, client: prisma });
    const result = await graphql({ schema, source: '{ postTags { __typename } }' });

    expect(result.errors).toBeUndefined();
    expect(prisma.postTag.findMany).toHaveBeenCalledWith(expect.objectContaining({
      select: { postId: true, tagId: true },
    }));
  });

  it.each(['hidden', 'writeOnly'] as const)(
    'rejects an exposed composite primary key with a %s component',
    (mode) => {
      expect(() =>
        buildGolemSchema({
          datamodel,
          client: client(),
          models: { PostTag: { [mode]: ['tagId'] } },
        }),
      ).toThrow(
        mode === 'hidden'
          ? 'Cannot hide primary key PostTag.tagId'
          : 'Cannot make primary key PostTag.tagId write-only',
      );
    },
  );
});

describe('compound selectors in generated nested writes', () => {
  const nestedDatamodel: DatamodelDocument = {
    models: [
      {
        name: 'User',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({
            name: 'memberships',
            type: 'Membership',
            kind: 'object',
            isList: true,
            relationName: 'MembershipToUser',
          }),
        ],
      },
      {
        name: 'Membership',
        fields: [
          field({ name: 'userId', type: 'String', isReadOnly: true }),
          field({ name: 'teamId', type: 'String' }),
          field({ name: 'role', type: 'String' }),
          field({
            name: 'user',
            type: 'User',
            kind: 'object',
            relationName: 'MembershipToUser',
            relationFromFields: ['userId'],
            relationToFields: ['id'],
          }),
        ],
        primaryKey: { fields: ['userId', 'teamId'] },
      },
    ],
    enums: [],
  };

  function nestedClient() {
    return { user: delegate(), membership: delegate() };
  }

  it('uses the compound unique input for connect-or-create, nested update, upsert, and delete', () => {
    const sdl = printSchema(buildGolemSchema({ datamodel: nestedDatamodel, client: nestedClient() }));
    const selector = 'MembershipWhereUniqueInput!';
    expect(sdl).toContain(`where: ${selector}`);
    expect(sdl).toContain('connectOrCreate: [MembershipCreateOrConnectWithoutUserInput!]');
    expect(sdl).toContain('update: [MembershipUpdateWithWhereUniqueWithoutUserInput!]');
    expect(sdl).toContain('upsert: [MembershipUpsertWithWhereUniqueWithoutUserInput!]');
    expect(sdl).toContain('delete: [MembershipWhereUniqueInput!]');
  });

  it('passes compound nested payloads through unchanged', async () => {
    const prisma = nestedClient();
    prisma.user.create.mockResolvedValue({ id: 'u1' });
    const schema = buildGolemSchema({ datamodel: nestedDatamodel, client: prisma });
    const result = await graphql({
      schema,
      source: `mutation {
        createUser(data: {
          id: "u1"
          memberships: { connectOrCreate: [{
            where: { userId_teamId: { userId: "u1", teamId: "t1" } }
            create: { teamId: "t1", role: "owner" }
          }] }
        }) { id }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(prisma.user.create).toHaveBeenCalledWith({
      data: {
        id: 'u1',
        memberships: {
          connectOrCreate: [{
            where: { userId_teamId: { userId: 'u1', teamId: 't1' } },
            create: { teamId: 't1', role: 'owner' },
          }],
        },
      },
      select: { id: true },
    });
  });
});

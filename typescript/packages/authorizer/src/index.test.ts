import type { AuthorizationService } from '@eleven-am/authorizer';
import { createAbility } from '@eleven-am/authorizer/prisma';
import {
  type DatamodelField,
  type DatamodelModel,
  buildGolemSchema,
  GolemEngine,
} from '@eleven-am/golem-core';
import { graphql } from 'graphql';
import { GolemAuthorizationAdapter } from './index';

function field(
  overrides: Partial<DatamodelField> & Pick<DatamodelField, 'name' | 'type'>,
): DatamodelField {
  return {
    kind: 'scalar',
    isList: false,
    isRequired: true,
    isUnique: false,
    isId: false,
    hasDefaultValue: false,
    isReadOnly: false,
    isUpdatedAt: false,
    ...overrides,
  };
}

describe('GolemAuthorizationAdapter field dependencies', () => {
  it('reports the complete dependency tree across logical and relation operators', async () => {
    const ability = {
      can: jest.fn(),
      rulesFor: jest.fn(() => [
        {
          fields: ['secret'],
          inverted: true,
          conditions: {
            AND: [
              {
                author: {
                  is: {
                    organization: {
                      isNot: { suspended: { equals: true } },
                    },
                  },
                },
              },
              {
                comments: {
                  some: {
                    author: {
                      organization: { region: { equals: 'eu' } },
                    },
                  },
                },
              },
            ],
            NOT: { status: { equals: 'DRAFT' } },
          },
        },
      ]),
    };
    const authorizationService = {
      getAbility: jest.fn(async () => ability),
    } as unknown as AuthorizationService;
    const adapter = new GolemAuthorizationAdapter(authorizationService);

    const classification = await adapter.classifyFields(
      'read',
      'Post',
      ['secret'],
      { request: {} },
    );

    expect(classification.secret).toEqual({
      access: 'conditional',
      requires: ['author', 'comments', 'status'],
      dependencies: {
        author: {
          is: {
            organization: {
              isNot: { suspended: { equals: true } },
            },
          },
        },
        comments: {
          some: {
            author: {
              organization: { region: { equals: true } },
            },
          },
        },
        status: { equals: true },
      },
      dischargedByConstraint: false,
    });
  });

  it('unions dependencies from every conditional rule in the field chain', async () => {
    const ability = {
      can: jest.fn(),
      rulesFor: jest.fn(() => [
        {
          fields: ['secret'],
          inverted: false,
          conditions: { owner: { organizationId: 'one' } },
        },
        {
          fields: ['secret'],
          inverted: true,
          conditions: { reviewer: { organizationId: 'two' } },
        },
      ]),
    };
    const authorizationService = {
      getAbility: jest.fn(async () => ability),
    } as unknown as AuthorizationService;
    const adapter = new GolemAuthorizationAdapter(authorizationService);

    const classification = await adapter.classifyFields(
      'read',
      'Post',
      ['secret'],
      { request: {} },
    );

    expect(classification.secret.dependencies).toEqual({
      owner: { organizationId: true },
      reviewer: { organizationId: true },
    });
  });

  it('prevents a deep negative field rule from evaluating against an under-selected row', async () => {
    const models: DatamodelModel[] = [
      {
        name: 'Post',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'secret', type: 'String', isRequired: false }),
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
      {
        name: 'User',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({
            name: 'organization',
            type: 'Organization',
            kind: 'object',
            relationName: 'OrganizationToUser',
            relationFromFields: ['organizationId'],
            relationToFields: ['id'],
          }),
          field({ name: 'organizationId', type: 'String', isReadOnly: true }),
        ],
      },
      {
        name: 'Organization',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'suspended', type: 'Boolean' }),
        ],
      },
    ];
    const ability = createAbility([
      { action: 'read', subject: 'Post' },
      {
        action: 'read',
        subject: 'Post',
        fields: ['secret'],
        inverted: true,
        conditions: {
          author: {
            is: {
              organization: { is: { suspended: { equals: true } } },
            },
          },
        },
      },
    ]);
    const authorizationService = {
      getAbility: jest.fn(async () => ability),
    } as unknown as AuthorizationService;
    const adapter = new GolemAuthorizationAdapter(authorizationService);
    const findMany = jest.fn().mockResolvedValue([
      {
        secret: 'must not leak',
        author: { organization: { suspended: true } },
      },
    ]);
    const engine = new GolemEngine({ post: { findMany } }, models, {
      authorization: adapter,
      checkReadFields: true,
      checkWriteResults: false,
    });

    const result = await engine.findMany({
      model: 'Post',
      select: { secret: true },
      context: { request: {} },
    });

    expect(findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        select: {
          secret: true,
          author: {
            select: {
              organization: { select: { suspended: true } },
            },
          },
        },
      }),
    );
    expect(result).toEqual([{ secret: null }]);

    findMany.mockResolvedValueOnce([
      {
        secret: 'must not leak',
        author: { organization: { suspended: true } },
      },
    ]);
    const schema = buildGolemSchema({
      datamodel: { models, enums: [] },
      client: { post: { findMany } },
      engine,
      authorization: adapter,
      models: {
        Post: { operations: ['findMany'] },
        User: false,
        Organization: false,
      },
    });
    const graphResult = await graphql({
      schema,
      source: '{ posts { secret } }',
      contextValue: { request: {} },
    });

    expect(graphResult.errors).toBeUndefined();
    expect(graphResult.data).toEqual({ posts: [{ secret: null }] });
  });
});

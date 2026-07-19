import {
  GraphQLObjectType,
  GraphQLSchema,
  GraphQLString,
} from 'graphql';
import type { ComputedFieldSpec, CustomOperationSpec } from '@eleven-am/golem-core';
import { createGolemGraphQLArtifacts } from './graphql-artifacts';

function fixture() {
  const generatedUsers = jest.fn(() => []);
  const directCustomResolver = jest.fn(() => []);
  const directComputedResolver = jest.fn(() => 'direct');
  const userType = new GraphQLObjectType({
    name: 'User',
    fields: {
      displayName: { type: GraphQLString, resolve: directComputedResolver },
    },
  });
  const schema = new GraphQLSchema({
    query: new GraphQLObjectType({
      name: 'Query',
      fields: {
        users: { type: userType, resolve: generatedUsers },
        searchUsers: { type: GraphQLString, resolve: directCustomResolver },
      },
    }),
  });
  const customOperations: CustomOperationSpec[] = [{
    kind: 'query',
    name: 'searchUsers',
    type: 'String',
    resolve: directCustomResolver,
  }];
  const computedFields: ComputedFieldSpec[] = [{
    model: 'User',
    name: 'displayName',
    type: 'String',
    requires: ['name'],
    resolve: directComputedResolver,
  }];
  return {
    schema,
    computedFields,
    customOperations,
    generatedUsers,
    directCustomResolver,
    directComputedResolver,
  };
}

describe('Nest GraphQL artifacts', () => {
  it('combines generated resolvers with the Nest-owned custom operation', async () => {
    const {
      schema,
      computedFields,
      customOperations,
      generatedUsers,
      directCustomResolver,
      directComputedResolver,
    } = fixture();
    const nestCustomResolver = jest.fn(() => []);
    const nestComputedResolver = jest.fn(() => 'nest');
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);
    const resolvers = await artifacts.transformResolvers({
      Query: { searchUsers: nestCustomResolver },
      User: { displayName: nestComputedResolver },
    }) as Record<string, Record<string, unknown>>;

    expect(artifacts.typeDefs).toContain('searchUsers: String');
    expect(artifacts.fieldResolverEnhancers).toEqual(['guards', 'interceptors', 'filters']);
    expect(resolvers.Query.users).toBe(generatedUsers);
    expect(resolvers.Query.searchUsers).toBe(nestCustomResolver);
    expect(resolvers.Query.searchUsers).not.toBe(directCustomResolver);
    expect(resolvers.User.displayName).toBe(nestComputedResolver);
    expect(resolvers.User.displayName).not.toBe(directComputedResolver);
  });

  it('fails when Nest did not discover a declared custom operation', () => {
    const { schema, computedFields, customOperations } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);

    expect(() => artifacts.transformResolvers({ User: { displayName: () => 'nest' } })).toThrow(
      'Custom query searchUsers was not discovered by Nest GraphQL',
    );
  });

  it('fails when Nest did not discover a declared computed field', () => {
    const { schema, computedFields, customOperations } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);

    expect(() => artifacts.transformResolvers({
      Query: { searchUsers: () => [] },
    })).toThrow('Computed field User.displayName was not discovered by Nest GraphQL');
  });

  it('rejects Nest resolvers that replace Golem-owned fields', () => {
    const { schema, computedFields, customOperations } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);

    expect(() => artifacts.transformResolvers({
      Query: {
        searchUsers: () => [],
        users: () => [],
      },
      User: { displayName: () => 'nest' },
    })).toThrow('Nest resolver Query.users collides with a Golem-generated resolver');
  });
});

import {
  GraphQLObjectType,
  GraphQLSchema,
  GraphQLString,
} from 'graphql';
import type { ComputedFieldSpec, CustomOperationSpec } from '@eleven-am/golem-core';
import { createGolemGraphQLArtifacts } from './graphql-artifacts';
import { SHARED_CONTEXT_MESSAGE, golemRequestBoundary } from './request-boundary';

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
    const users = resolvers.Query.users as (...callArgs: unknown[]) => unknown;
    expect(users(undefined, { take: 1 }, { user: 'roy' }, {})).toEqual([]);
    expect(generatedUsers).toHaveBeenCalledWith(undefined, { take: 1 }, { user: 'roy' }, {});
    expect(resolvers.Query.searchUsers).toBe(nestCustomResolver);
    expect(resolvers.Query.searchUsers).not.toBe(directCustomResolver);
    expect(resolvers.User.displayName).toBe(nestComputedResolver);
    expect(resolvers.User.displayName).not.toBe(directComputedResolver);
  });

  it('refuses a generated root resolver handed a context from another request', async () => {
    const { schema, computedFields, customOperations, generatedUsers } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);
    const resolvers = await artifacts.transformResolvers({
      Query: { searchUsers: () => [] },
      User: { displayName: () => 'nest' },
    }) as Record<string, Record<string, (...callArgs: unknown[]) => unknown>>;
    const users = resolvers.Query.users;
    const ctx = {};

    golemRequestBoundary({}, {}, () => users(undefined, {}, ctx, {}));

    expect(() => golemRequestBoundary({}, {}, () => users(undefined, {}, ctx, {}))).toThrow(
      SHARED_CONTEXT_MESSAGE,
    );
    expect(generatedUsers).toHaveBeenCalledTimes(1);
  });

  it('leaves a generated root resolver alone when each request builds its own context', () => {
    const { schema, computedFields, customOperations, generatedUsers } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, computedFields, customOperations);
    const resolvers = artifacts.transformResolvers({
      Query: { searchUsers: () => [] },
      User: { displayName: () => 'nest' },
    }) as Record<string, Record<string, (...callArgs: unknown[]) => unknown>>;
    const users = resolvers.Query.users;

    golemRequestBoundary({}, {}, () => users(undefined, {}, {}, {}));
    golemRequestBoundary({}, {}, () => users(undefined, {}, {}, {}));

    expect(generatedUsers).toHaveBeenCalledTimes(2);
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

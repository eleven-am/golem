import {
  GraphQLObjectType,
  GraphQLSchema,
  GraphQLString,
} from 'graphql';
import type { CustomOperationSpec } from '@eleven-am/golem-core';
import { createGolemGraphQLArtifacts } from './graphql-artifacts';

function fixture() {
  const generatedUsers = jest.fn(() => []);
  const directCustomResolver = jest.fn(() => []);
  const schema = new GraphQLSchema({
    query: new GraphQLObjectType({
      name: 'Query',
      fields: {
        users: { type: GraphQLString, resolve: generatedUsers },
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
  return { schema, customOperations, generatedUsers, directCustomResolver };
}

describe('Nest GraphQL artifacts', () => {
  it('combines generated resolvers with the Nest-owned custom operation', async () => {
    const { schema, customOperations, generatedUsers, directCustomResolver } = fixture();
    const nestCustomResolver = jest.fn(() => []);
    const artifacts = createGolemGraphQLArtifacts(schema, customOperations);
    const resolvers = await artifacts.transformResolvers({
      Query: { searchUsers: nestCustomResolver },
    }) as Record<string, Record<string, unknown>>;

    expect(artifacts.typeDefs).toContain('searchUsers: String');
    expect(resolvers.Query.users).toBe(generatedUsers);
    expect(resolvers.Query.searchUsers).toBe(nestCustomResolver);
    expect(resolvers.Query.searchUsers).not.toBe(directCustomResolver);
  });

  it('fails when Nest did not discover a declared custom operation', () => {
    const { schema, customOperations } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, customOperations);

    expect(() => artifacts.transformResolvers({})).toThrow(
      'Custom query searchUsers was not discovered by Nest GraphQL',
    );
  });

  it('rejects Nest resolvers that replace Golem-owned fields', () => {
    const { schema, customOperations } = fixture();
    const artifacts = createGolemGraphQLArtifacts(schema, customOperations);

    expect(() => artifacts.transformResolvers({
      Query: {
        searchUsers: () => [],
        users: () => [],
      },
    })).toThrow('Nest resolver Query.users collides with a Golem-generated resolver');
  });
});

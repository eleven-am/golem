import type { GqlModuleOptions } from '@nestjs/graphql';
import {
  GraphQLScalarType,
  isObjectType,
  isScalarType,
  isSpecifiedScalarType,
  printSchema,
} from 'graphql';
import type { CustomOperationSpec } from '@eleven-am/golem-core';
import type { GraphQLField, GraphQLSchema } from 'graphql';

type ResolverMap = Record<string, any>;
type ResolverTransform = NonNullable<GqlModuleOptions['transformResolvers']>;

export interface GolemGraphQLArtifacts {
  typeDefs: string;
  transformResolvers: ResolverTransform;
}

function fieldResolver(field: GraphQLField<unknown, unknown>): unknown {
  if (field.subscribe) {
    return {
      subscribe: field.subscribe,
      ...(field.resolve ? { resolve: field.resolve } : {}),
    };
  }
  return field.resolve;
}

function generatedResolvers(
  schema: GraphQLSchema,
  customOperations: readonly CustomOperationSpec[],
): ResolverMap {
  const customRootFields = new Map<string, Set<string>>([
    ['Query', new Set(customOperations.filter((spec) => spec.kind === 'query').map((spec) => spec.name))],
    ['Mutation', new Set(customOperations.filter((spec) => spec.kind === 'mutation').map((spec) => spec.name))],
  ]);
  const resolvers: ResolverMap = {};

  for (const type of Object.values(schema.getTypeMap())) {
    if (type.name.startsWith('__')) {
      continue;
    }
    if (isScalarType(type)) {
      if (!isSpecifiedScalarType(type)) {
        resolvers[type.name] = type;
      }
      continue;
    }
    if (!isObjectType(type)) {
      continue;
    }
    const fields: ResolverMap = {};
    for (const [fieldName, field] of Object.entries(type.getFields())) {
      if (customRootFields.get(type.name)?.has(fieldName)) {
        continue;
      }
      const resolve = fieldResolver(field);
      if (resolve) {
        fields[fieldName] = resolve;
      }
    }
    if (Object.keys(fields).length > 0) {
      resolvers[type.name] = fields;
    }
  }

  return resolvers;
}

function isResolverGroup(value: unknown): value is ResolverMap {
  return !!value && typeof value === 'object' && !(value instanceof GraphQLScalarType) && !Array.isArray(value);
}

function normalizeResolvers(resolvers: ResolverMap | ResolverMap[]): ResolverMap {
  const entries = Array.isArray(resolvers) ? resolvers : [resolvers];
  const normalized: ResolverMap = {};
  for (const resolverMap of entries) {
    for (const [typeName, resolver] of Object.entries(resolverMap ?? {})) {
      const current = normalized[typeName];
      normalized[typeName] = isResolverGroup(current) && isResolverGroup(resolver)
        ? { ...current, ...resolver }
        : resolver;
    }
  }
  return normalized;
}

function assertCustomOperationsDiscovered(
  discovered: ResolverMap,
  customOperations: readonly CustomOperationSpec[],
): void {
  for (const operation of customOperations) {
    const root = operation.kind === 'query' ? 'Query' : 'Mutation';
    const resolver = isResolverGroup(discovered[root]) ? discovered[root][operation.name] : undefined;
    if (typeof resolver !== 'function') {
      throw new Error(
        `Custom ${operation.kind} ${operation.name} was not discovered by Nest GraphQL`,
      );
    }
  }
}

function mergeResolvers(generated: ResolverMap, discovered: ResolverMap): ResolverMap {
  const merged: ResolverMap = { ...generated };
  for (const [typeName, resolver] of Object.entries(discovered)) {
    const existing = merged[typeName];
    if (!isResolverGroup(existing) || !isResolverGroup(resolver)) {
      if (existing !== undefined) {
        throw new Error(`Nest resolver ${typeName} collides with a Golem-generated resolver`);
      }
      merged[typeName] = resolver;
      continue;
    }
    for (const fieldName of Object.keys(resolver)) {
      if (existing[fieldName] !== undefined) {
        throw new Error(
          `Nest resolver ${typeName}.${fieldName} collides with a Golem-generated resolver`,
        );
      }
    }
    merged[typeName] = { ...existing, ...resolver };
  }
  return merged;
}

export function createGolemGraphQLArtifacts(
  schema: GraphQLSchema,
  customOperations: readonly CustomOperationSpec[],
): GolemGraphQLArtifacts {
  const generated = generatedResolvers(schema, customOperations);
  return {
    typeDefs: printSchema(schema),
    transformResolvers: ((resolvers: ResolverMap | ResolverMap[]) => {
      const discovered = normalizeResolvers(resolvers);
      assertCustomOperationsDiscovered(discovered, customOperations);
      return mergeResolvers(generated, discovered);
    }) as ResolverTransform,
  };
}

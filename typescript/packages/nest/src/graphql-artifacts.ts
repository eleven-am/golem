import type { GqlModuleOptions } from '@nestjs/graphql';
import {
  GraphQLScalarType,
  isObjectType,
  isScalarType,
  isSpecifiedScalarType,
  printSchema,
} from 'graphql';
import type { ComputedFieldSpec, CustomOperationSpec } from '@eleven-am/golem-core';
import type { GraphQLField, GraphQLFieldResolver, GraphQLSchema } from 'graphql';
import { assertContextWithinRequest } from './request-boundary';

type ResolverMap = Record<string, any>;
type ResolverTransform = NonNullable<GqlModuleOptions['transformResolvers']>;

export interface GolemGraphQLArtifacts {
  typeDefs: string;
  transformResolvers: ResolverTransform;
  fieldResolverEnhancers: NonNullable<GqlModuleOptions['fieldResolverEnhancers']>;
}

function boundToRequest(
  resolve: GraphQLFieldResolver<unknown, unknown>,
): GraphQLFieldResolver<unknown, unknown> {
  return function resolveWithinRequest(this: unknown, parent, args, ctx, info) {
    assertContextWithinRequest(ctx);
    return resolve.call(this, parent, args, ctx, info);
  };
}

function fieldResolver(field: GraphQLField<unknown, unknown>, atRoot: boolean): unknown {
  if (field.subscribe) {
    return {
      subscribe: field.subscribe,
      ...(field.resolve ? { resolve: field.resolve } : {}),
    };
  }
  return atRoot && field.resolve ? boundToRequest(field.resolve) : field.resolve;
}

function generatedResolvers(
  schema: GraphQLSchema,
  computedFields: readonly ComputedFieldSpec[],
  customOperations: readonly CustomOperationSpec[],
): ResolverMap {
  const customRootFields = new Map<string, Set<string>>([
    ['Query', new Set(customOperations.filter((spec) => spec.kind === 'query').map((spec) => spec.name))],
    ['Mutation', new Set(customOperations.filter((spec) => spec.kind === 'mutation').map((spec) => spec.name))],
  ]);
  const computedByModel = new Map<string, Set<string>>();
  for (const spec of computedFields) {
    const fields = computedByModel.get(spec.model) ?? new Set<string>();
    fields.add(spec.name);
    computedByModel.set(spec.model, fields);
  }
  const resolvers: ResolverMap = {};
  const rootTypeNames = new Set(
    [schema.getQueryType()?.name, schema.getMutationType()?.name].filter(
      (name): name is string => typeof name === 'string',
    ),
  );

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
      if (
        customRootFields.get(type.name)?.has(fieldName) ||
        computedByModel.get(type.name)?.has(fieldName)
      ) {
        continue;
      }
      const resolve = fieldResolver(field, rootTypeNames.has(type.name));
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

function assertComputedFieldsDiscovered(
  discovered: ResolverMap,
  computedFields: readonly ComputedFieldSpec[],
): void {
  for (const field of computedFields) {
    const resolver = isResolverGroup(discovered[field.model])
      ? discovered[field.model][field.name]
      : undefined;
    if (typeof resolver !== 'function') {
      throw new Error(
        `Computed field ${field.model}.${field.name} was not discovered by Nest GraphQL`,
      );
    }
  }
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
  computedFields: readonly ComputedFieldSpec[],
  customOperations: readonly CustomOperationSpec[],
): GolemGraphQLArtifacts {
  const generated = generatedResolvers(schema, computedFields, customOperations);
  return {
    typeDefs: printSchema(schema),
    fieldResolverEnhancers: ['guards', 'interceptors', 'filters'],
    transformResolvers: ((resolvers: ResolverMap | ResolverMap[]) => {
      const discovered = normalizeResolvers(resolvers);
      assertComputedFieldsDiscovered(discovered, computedFields);
      assertCustomOperationsDiscovered(discovered, customOperations);
      return mergeResolvers(generated, discovered);
    }) as ResolverTransform,
  };
}

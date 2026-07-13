import { GraphQLResolveInfo } from 'graphql';

export interface ComputedFieldSpec {
  model: string;
  name: string;
  type: string;
  requires: readonly string[];
  resolve: (parent: any, ctx: unknown, info: GraphQLResolveInfo) => unknown;
}

export interface CustomOperationSpec {
  kind: 'query' | 'mutation';
  name: string;
  type: string;
  args?: Record<string, string>;
  resolve: (args: any, ctx: unknown, info: GraphQLResolveInfo) => unknown;
}

export type ComputedRequiresMap = ReadonlyMap<string, ReadonlyMap<string, readonly string[]>>;

export function buildComputedRequiresMap(specs: readonly ComputedFieldSpec[]): ComputedRequiresMap {
  const map = new Map<string, Map<string, readonly string[]>>();
  for (const spec of specs) {
    const perModel = map.get(spec.model) ?? new Map<string, readonly string[]>();
    perModel.set(spec.name, spec.requires);
    map.set(spec.model, perModel);
  }
  return map;
}

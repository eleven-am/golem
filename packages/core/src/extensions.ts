import { GraphQLResolveInfo } from 'graphql';
import { BatchLoadFn, createBatchLoader } from './batch';

export interface ComputedFieldBatch<TKey = any, TValue = any, TArgs = any> {
  key: string | ((parent: any) => TKey | null | undefined);
  load: BatchLoadFn<TKey, TValue, TArgs>;
  cacheKey?: (key: TKey) => unknown;
  maxBatchSize?: number;
}

export interface ComputedFieldSpec {
  model: string;
  name: string;
  type: string;
  requires: readonly string[];
  args?: Record<string, string>;
  batch?: ComputedFieldBatch;
  resolve: (parent: any, args: any, ctx: unknown, info: GraphQLResolveInfo) => unknown;
}

export interface CustomOperationSpec {
  kind: 'query' | 'mutation';
  name: string;
  type: string;
  args?: Record<string, string>;
  resolve: (args: any, ctx: unknown, info: GraphQLResolveInfo) => unknown;
}

export type ComputedRequiresMap = ReadonlyMap<string, ReadonlyMap<string, readonly string[]>>;

export function computedFieldRequires(spec: ComputedFieldSpec): readonly string[] {
  const key = typeof spec.batch?.key === 'string' ? spec.batch.key : undefined;
  if (key === undefined || spec.requires.includes(key)) {
    return spec.requires;
  }
  return [...spec.requires, key];
}

export function computedFieldKey(
  batch: ComputedFieldBatch,
): (parent: any) => unknown {
  if (typeof batch.key !== 'string') {
    return batch.key;
  }
  const field = batch.key;
  return (parent: any) => (parent === null || parent === undefined ? undefined : parent[field]);
}

export function computedFieldResolver(
  spec: ComputedFieldSpec,
): (parent: any, args: any, ctx: unknown, info: GraphQLResolveInfo) => unknown {
  const batch = spec.batch;
  if (!batch) {
    return (parent, args, ctx, info) => spec.resolve(parent, args, ctx, info);
  }
  const keyOf = computedFieldKey(batch);
  const loader = createBatchLoader<unknown, unknown, unknown>({
    name: `${spec.model}.${spec.name}`,
    load: batch.load,
    cacheKey: batch.cacheKey,
    maxBatchSize: batch.maxBatchSize,
  });
  return (parent, args, ctx, info) => loader.load(ctx, keyOf(parent), args, info);
}

export function buildComputedRequiresMap(specs: readonly ComputedFieldSpec[]): ComputedRequiresMap {
  const map = new Map<string, Map<string, readonly string[]>>();
  for (const spec of specs) {
    const perModel = map.get(spec.model) ?? new Map<string, readonly string[]>();
    perModel.set(spec.name, computedFieldRequires(spec));
    map.set(spec.model, perModel);
  }
  return map;
}

import DataLoader from 'dataloader';
import { CanonicalValueError, canonicalToken } from './canonical';
import { COMPILED_READ_BATCH_CHUNK } from './compiled-read-run';

export type BatchLoadResult<TKey, TValue> =
  | ReadonlyArray<TValue | Error>
  | ReadonlyMap<TKey, TValue>;

export type BatchLoadFn<TKey, TValue, TArgs> = (
  keys: readonly TKey[],
  ctx: unknown,
  args: TArgs,
) => BatchLoadResult<TKey, TValue> | PromiseLike<BatchLoadResult<TKey, TValue>>;

export interface BatchLoaderSpec<TKey, TValue, TArgs = undefined> {
  name: string;
  load: BatchLoadFn<TKey, TValue, TArgs>;
  cacheKey?: (key: TKey) => unknown;
  maxBatchSize?: number;
}

export interface BatchScope {
  rootValue?: unknown;
}

export interface BatchLoader<TKey, TValue, TArgs = undefined> {
  load(
    ctx: unknown,
    key: TKey | null | undefined,
    args?: TArgs,
    scope?: BatchScope,
  ): Promise<TValue | null>;
}

type LoaderVariants = Map<string, DataLoader<any, any, any>>;
type RequestLoaders = Map<object, LoaderVariants>;

const loadersByRequest = new WeakMap<object, WeakMap<object, RequestLoaders>>();
const WHOLE_REQUEST = {};

function executionOf(scope: BatchScope | undefined): object {
  const root = scope?.rootValue;
  return typeof root === 'object' && root !== null ? root : WHOLE_REQUEST;
}

function requestLoaders(ctx: unknown, scope: BatchScope | undefined, name: string): RequestLoaders {
  if (typeof ctx !== 'object' || ctx === null) {
    throw new Error(
      `Batch loader ${name} needs an object request context to scope its batch to, but received ${ctx === null ? 'null' : typeof ctx}`,
    );
  }
  let byExecution = loadersByRequest.get(ctx);
  if (!byExecution) {
    byExecution = new WeakMap();
    loadersByRequest.set(ctx, byExecution);
  }
  const execution = executionOf(scope);
  const existing = byExecution.get(execution);
  if (existing) {
    return existing;
  }
  const created: RequestLoaders = new Map();
  byExecution.set(execution, created);
  return created;
}

export function clearBatchCaches(ctx: unknown, scope?: BatchScope): void {
  if (typeof ctx !== 'object' || ctx === null) {
    return;
  }
  const request = loadersByRequest.get(ctx)?.get(executionOf(scope));
  if (!request) {
    return;
  }
  for (const variants of request.values()) {
    for (const loader of variants.values()) {
      loader.clearAll();
    }
  }
}

function refuseArgument(name: string, path: string, reason: string): never {
  throw new Error(
    `Batch loader ${name} cannot tell one batch from another by ${path === '' ? 'its arguments' : `argument ${path}`}: ${reason}`,
  );
}

const SERIALIZABLE =
  'batch arguments must be built from canonical scalar values, Dates, bytes, Decimals, arrays and plain objects';

function stableToken(value: unknown, name: string): string {
  try {
    return canonicalToken(value);
  } catch (error) {
    if (error instanceof CanonicalValueError) {
      return refuseArgument(name, error.path, `${error.reason}, ${SERIALIZABLE}`);
    }
    throw error;
  }
}

function isBatchMap<TKey, TValue>(
  result: BatchLoadResult<TKey, TValue>,
): result is ReadonlyMap<TKey, TValue> {
  return !Array.isArray(result) && typeof (result as ReadonlyMap<TKey, TValue>).get === 'function';
}

function alignBatch<TKey, TValue>(
  name: string,
  keys: readonly TKey[],
  result: BatchLoadResult<TKey, TValue>,
  cacheKey: ((key: TKey) => unknown) | undefined,
): Array<TValue | Error | null> {
  if (isBatchMap(result)) {
    const identify = cacheKey ?? ((key: TKey): unknown => key);
    const asked = new Set(keys.map((key) => identify(key)));
    const lookup = new Map<unknown, TValue>();
    const unasked: unknown[] = [];
    for (const [key, value] of result) {
      const id = identify(key);
      if (!asked.has(id)) {
        unasked.push(key);
      }
      lookup.set(id, value);
    }
    if (unasked.length > 0) {
      throw new Error(
        `Batch loader ${name} answered for ${unasked.length} key${unasked.length === 1 ? '' : 's'} it was not given, starting with ${String(unasked[0])}; the keys it answers with must identify the same way as the keys it is given`,
      );
    }
    return keys.map((key) => {
      const id = identify(key);
      return lookup.has(id) ? (lookup.get(id) as TValue) : null;
    });
  }
  if (!Array.isArray(result)) {
    throw new Error(
      `Batch loader ${name} must return an array aligned with its keys or a map keyed by them`,
    );
  }
  if (result.length !== keys.length) {
    throw new Error(
      `Batch loader ${name} returned ${result.length} results for ${keys.length} keys`,
    );
  }
  return [...result];
}

export function createBatchLoader<TKey, TValue, TArgs = undefined>(
  spec: BatchLoaderSpec<TKey, TValue, TArgs>,
): BatchLoader<TKey, TValue, TArgs> {
  const identity = {};
  const loaderFor = (
    ctx: unknown,
    args: TArgs,
    scope: BatchScope | undefined,
  ): DataLoader<TKey, TValue | null, unknown> => {
    const request = requestLoaders(ctx, scope, spec.name);
    let variants = request.get(identity);
    if (!variants) {
      variants = new Map();
      request.set(identity, variants);
    }
    const variant = stableToken(args, spec.name);
    const existing = variants.get(variant) as DataLoader<TKey, TValue | null, unknown> | undefined;
    if (existing) {
      return existing;
    }
    const loader = new DataLoader<TKey, TValue | null, unknown>(
      async (keys) => alignBatch(spec.name, keys, await spec.load(keys, ctx, args), spec.cacheKey),
      {
        name: spec.name,
        cacheKeyFn: spec.cacheKey,
        maxBatchSize: spec.maxBatchSize ?? COMPILED_READ_BATCH_CHUNK,
      },
    );
    variants.set(variant, loader);
    return loader;
  };
  return {
    load(ctx, key, args, scope) {
      if (key === null || key === undefined) {
        return Promise.resolve(null);
      }
      try {
        return loaderFor(ctx, args as TArgs, scope).load(key);
      } catch (error) {
        return Promise.reject(error);
      }
    },
  };
}

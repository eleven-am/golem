import { SetMetadata, UseFilters } from '@nestjs/common';
import type {
  ComputedFieldBatchReturn,
  ComputedFieldReturn,
  GolemFieldName,
  RegisteredModels,
} from './register';
import { Mutation, PARAM_ARGS_METADATA, Query, ResolveField, Resolver } from '@nestjs/graphql';
import {
  BatchLoader,
  BatchScope,
  GolemHookOperation,
  createBatchLoader,
} from '@eleven-am/golem-core';
import { GolemGraphQLExceptionFilter } from './graphql-error.filter';
import { assertContextWithinRequest } from './request-boundary';

export const GOLEM_HOOKS_MODEL = 'GOLEM_HOOKS_MODEL';
export const GOLEM_HOOK = 'GOLEM_HOOK';

export interface GolemHookMetadata {
  phase: 'before' | 'after';
  operation: GolemHookOperation;
}

export function GolemHooks<TModel extends keyof RegisteredModels & string>(
  model: TModel,
): ClassDecorator {
  return SetMetadata(GOLEM_HOOKS_MODEL, model);
}

function hookDecorator(phase: 'before' | 'after', operation: GolemHookOperation): () => MethodDecorator {
  return () => SetMetadata<string, GolemHookMetadata>(GOLEM_HOOK, { phase, operation });
}

export const BeforeFindOne = hookDecorator('before', 'findOne');
export const BeforeFindFirst = hookDecorator('before', 'findFirst');
export const BeforeFindMany = hookDecorator('before', 'findMany');
export const BeforeCreate = hookDecorator('before', 'create');
export const BeforeUpdate = hookDecorator('before', 'update');
export const BeforeDelete = hookDecorator('before', 'delete');
export const BeforeUpdateMany = hookDecorator('before', 'updateMany');
export const BeforeDeleteMany = hookDecorator('before', 'deleteMany');
export const AfterFindOne = hookDecorator('after', 'findOne');
export const AfterFindFirst = hookDecorator('after', 'findFirst');
export const AfterFindMany = hookDecorator('after', 'findMany');
export const AfterCreate = hookDecorator('after', 'create');
export const AfterUpdate = hookDecorator('after', 'update');
export const AfterDelete = hookDecorator('after', 'delete');
export const AfterUpdateMany = hookDecorator('after', 'updateMany');
export const AfterDeleteMany = hookDecorator('after', 'deleteMany');

export const GOLEM_COMPUTED_FIELD = 'GOLEM_COMPUTED_FIELD';
export const GOLEM_COMPUTED_MODELS = 'GOLEM_COMPUTED_MODELS';
export const GOLEM_CUSTOM_OPERATION = 'GOLEM_CUSTOM_OPERATION';

export interface ComputedFieldOptions<
  TField extends string = string,
  TType extends string = string,
> {
  type: TType;
  requires?: readonly TField[];
  args?: Record<string, string>;
  name?: string;
}

export interface ComputedFieldMetadata extends ComputedFieldOptions {
  model: string;
}

export interface CustomOperationOptions {
  type: string;
  args?: Record<string, string>;
  name?: string;
}

export interface CustomOperationMetadata extends CustomOperationOptions {
  kind: 'query' | 'mutation';
}

export interface BatchedComputedFieldOptions<
  TField extends string = string,
  TType extends string = string,
  TKey = string,
> extends ComputedFieldOptions<TField, TType> {
  key: TField | ((parent: any) => TKey | null | undefined);
  cacheKey?: (key: TKey) => unknown;
  maxBatchSize?: number;
}

const GQL_PARAM_DECORATORS: Record<string, string> = {
  '0': '@Parent()',
  '1': '@Context()',
  '2': '@Info()',
  '3': '@Args()',
};

function assertResolvedPositionally(
  target: object,
  key: string | symbol,
  model: string,
  fieldName: string,
): void {
  const params = Reflect.getMetadata(PARAM_ARGS_METADATA, target.constructor, key) as
    | Record<string, unknown>
    | undefined;
  const declared = Object.keys(params ?? {});
  if (declared.length === 0) {
    return;
  }
  const used = [
    ...new Set(
      declared.map(
        (entry) => GQL_PARAM_DECORATORS[entry.split(':')[0]] ?? 'a custom parameter decorator',
      ),
    ),
  ].sort();
  throw new Error(
    `Batched computed field ${model}.${fieldName} on ${target.constructor.name}.${String(key)} cannot use ${used.join(', ')}: a batched method is called with (keys, ctx, args) and a Nest parameter decorator rewrites those positions`,
  );
}

function registerComputedField(
  metadata: ComputedFieldMetadata,
  fieldName: string,
  target: object,
  key: string | symbol,
  descriptor: PropertyDescriptor,
): void {
  SetMetadata<string, ComputedFieldMetadata>(GOLEM_COMPUTED_FIELD, metadata)(
    target,
    key,
    descriptor,
  );
  const constructor = target.constructor;
  const models = Reflect.getOwnMetadata(GOLEM_COMPUTED_MODELS, constructor) as
    | readonly string[]
    | undefined;
  Reflect.defineMetadata(
    GOLEM_COMPUTED_MODELS,
    [...new Set([...(models ?? []), metadata.model])],
    constructor,
  );
  Resolver(metadata.model)(constructor);
  ResolveField(fieldName)(target, key, descriptor);
  UseFilters(GolemGraphQLExceptionFilter)(target, key, descriptor);
}

export function ComputedField<
  TModel extends keyof RegisteredModels & string,
  TType extends string,
>(
  model: TModel,
  options: ComputedFieldOptions<GolemFieldName<TModel>, TType>,
): <TReturn extends ComputedFieldReturn<TType>>(
  target: object,
  key: string | symbol,
  descriptor: TypedPropertyDescriptor<(...args: never[]) => TReturn>,
) => void {
  return ((target: object, key: string | symbol, descriptor: PropertyDescriptor) => {
    registerComputedField(
      { model, ...options },
      options.name ?? String(key),
      target,
      key,
      descriptor,
    );
  }) as ReturnType<typeof ComputedField<TModel, TType>>;
}

export function BatchedComputedField<
  TModel extends keyof RegisteredModels & string,
  TType extends string,
  TKey = string,
>(
  model: TModel,
  options: BatchedComputedFieldOptions<GolemFieldName<TModel>, TType, TKey>,
): <TReturn extends ComputedFieldBatchReturn<TType, TKey>>(
  target: object,
  key: string | symbol,
  descriptor: TypedPropertyDescriptor<(...args: never[]) => TReturn>,
) => void {
  return ((target: object, key: string | symbol, descriptor: PropertyDescriptor) => {
    const batch = descriptor.value as (
      keys: readonly TKey[],
      ctx: unknown,
      args: Record<string, unknown>,
    ) => never;
    const fieldName = options.name ?? String(key);
    assertResolvedPositionally(target, key, model, fieldName);
    const keyOf =
      typeof options.key === 'function'
        ? options.key
        : (parent: any) =>
            parent === null || parent === undefined
              ? undefined
              : (parent[options.key as string] as TKey);
    const unbound = {};
    const loaders = new WeakMap<object, BatchLoader<TKey, unknown, Record<string, unknown>>>();
    const loaderFor = (owner: object): BatchLoader<TKey, unknown, Record<string, unknown>> => {
      const existing = loaders.get(owner);
      if (existing) {
        return existing;
      }
      const created = createBatchLoader<TKey, unknown, Record<string, unknown>>({
        name: `${model}.${fieldName}`,
        load: (keys, ctx, args) => batch.call(owner, keys, ctx, args),
        cacheKey: options.cacheKey,
        maxBatchSize: options.maxBatchSize,
      });
      loaders.set(owner, created);
      return created;
    };
    function resolveBatched(
      this: object | undefined,
      parent: unknown,
      args: Record<string, unknown>,
      ctx: unknown,
      info: BatchScope,
    ): Promise<unknown> {
      try {
        assertContextWithinRequest(ctx);
      } catch (error) {
        return Promise.reject(error);
      }
      return loaderFor(this ?? unbound).load(ctx, keyOf(parent), args, info);
    }
    for (const metadataKey of Reflect.getOwnMetadataKeys(batch)) {
      Reflect.defineMetadata(
        metadataKey,
        Reflect.getOwnMetadata(metadataKey, batch),
        resolveBatched,
      );
    }
    descriptor.value = resolveBatched;
    const requires = [
      ...new Set([
        ...(options.requires ?? []),
        ...(typeof options.key === 'string' ? [options.key] : []),
      ]),
    ];
    registerComputedField(
      { model, type: options.type, args: options.args, name: options.name, requires },
      fieldName,
      target,
      key,
      descriptor,
    );
  }) as ReturnType<typeof BatchedComputedField<TModel, TType, TKey>>;
}

export function CustomQuery(options: CustomOperationOptions): MethodDecorator {
  return (target, key, descriptor) => {
    SetMetadata<string, CustomOperationMetadata>(GOLEM_CUSTOM_OPERATION, {
      kind: 'query',
      ...options,
    })(target, key, descriptor);
    Query(options.name ?? String(key))(target, key, descriptor);
    UseFilters(GolemGraphQLExceptionFilter)(target, key, descriptor);
  };
}

export function CustomMutation(options: CustomOperationOptions): MethodDecorator {
  return (target, key, descriptor) => {
    SetMetadata<string, CustomOperationMetadata>(GOLEM_CUSTOM_OPERATION, {
      kind: 'mutation',
      ...options,
    })(target, key, descriptor);
    Mutation(options.name ?? String(key))(target, key, descriptor);
    UseFilters(GolemGraphQLExceptionFilter)(target, key, descriptor);
  };
}

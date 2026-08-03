import type {
  BatchLoadResult,
  GolemHookOperation,
  GolemTypesMap,
  HookRequestFor,
  HookResultFor,
} from '@eleven-am/golem-core';

declare global {
  interface GolemRegister {}
}

export interface GolemSchemaNotRegistered {
  GOLEM_SCHEMA_NOT_REGISTERED_RUN_PRISMA_GENERATE: never;
}

export type RegisteredModels = GolemRegister extends { models: infer TModels extends object }
  ? TModels
  : GolemSchemaNotRegistered;

export type RegisteredTypes = GolemRegister extends { types: infer TTypes extends GolemTypesMap }
  ? TTypes
  : Record<
      keyof GolemSchemaNotRegistered,
      GolemTypesMap[string]
    >;

export type GolemModelName = keyof RegisteredTypes & string;

export type GolemFieldName<TModel extends keyof RegisteredModels & string> = Extract<
  RegisteredModels[TModel],
  string
>;

export type GolemRequest<
  TModel extends GolemModelName,
  TOperation extends GolemHookOperation,
> = HookRequestFor<RegisteredTypes, TModel, TOperation>;

export type GolemResult<
  TModel extends GolemModelName,
  TOperation extends GolemHookOperation,
> = HookResultFor<RegisteredTypes, TModel, TOperation>;

export type GolemEntity<TModel extends GolemModelName> = RegisteredTypes[TModel]['entity'];

export type GraphQLToTS<TRef extends string> = TRef extends `${infer TBase}!`
  ? NonNullable<GraphQLToTS<TBase>>
  : TRef extends `[${infer TItem}]`
    ? Array<GraphQLToTS<TItem>>
    : TRef extends 'String' | 'ID'
      ? string | null
      : TRef extends 'Int' | 'Float'
        ? number | null
        : TRef extends 'Boolean'
          ? boolean | null
          : TRef extends 'DateTime'
            ? Date | null
            : unknown;

export type ComputedFieldReturn<TRef extends string> =
  | GraphQLToTS<TRef>
  | Promise<GraphQLToTS<TRef>>;

export type ComputedFieldBatchReturn<TRef extends string, TKey> =
  | BatchLoadResult<TKey, GraphQLToTS<TRef>>
  | PromiseLike<BatchLoadResult<TKey, GraphQLToTS<TRef>>>;

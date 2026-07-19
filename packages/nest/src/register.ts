import type {
  GolemHookOperation,
  GolemTypesMap,
  HookRequestFor,
  HookResultFor,
} from '@eleven-am/golem-core';

export interface Register {}

export type RegisteredModels = Register extends { models: infer TModels extends object }
  ? TModels
  : Record<string, string>;

export type RegisteredTypes = Register extends { types: infer TTypes extends GolemTypesMap }
  ? TTypes
  : GolemTypesMap;

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

import { SetMetadata, UseFilters } from '@nestjs/common';
import { Mutation, Query } from '@nestjs/graphql';
import { GolemHookOperation } from '@eleven-am/golem-core';
import { GolemGraphQLExceptionFilter } from './graphql-error.filter';

export const GOLEM_HOOKS_MODEL = 'GOLEM_HOOKS_MODEL';
export const GOLEM_HOOK = 'GOLEM_HOOK';

export interface GolemHookMetadata {
  phase: 'before' | 'after';
  operation: GolemHookOperation;
}

export function GolemHooks(model: string): ClassDecorator {
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
export const GOLEM_CUSTOM_OPERATION = 'GOLEM_CUSTOM_OPERATION';

export interface ComputedFieldOptions {
  type: string;
  requires?: readonly string[];
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

export function ComputedField(model: string, options: ComputedFieldOptions): MethodDecorator {
  return SetMetadata<string, ComputedFieldMetadata>(GOLEM_COMPUTED_FIELD, { model, ...options });
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

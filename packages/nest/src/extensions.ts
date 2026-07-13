import { ComputedFieldSpec, CustomOperationSpec } from '@eleven-am/golem-core';
import {
  GOLEM_COMPUTED_FIELD,
  GOLEM_CUSTOM_OPERATION,
  ComputedFieldMetadata,
  CustomOperationMetadata,
} from './decorators';

export interface ExtractedExtensions {
  computedFields: ComputedFieldSpec[];
  customOperations: CustomOperationSpec[];
}

export function extractExtensionSpecs(instances: readonly object[]): ExtractedExtensions {
  const computedFields: ComputedFieldSpec[] = [];
  const customOperations: CustomOperationSpec[] = [];
  for (const instance of instances) {
    const prototype = Object.getPrototypeOf(instance);
    for (const methodName of Object.getOwnPropertyNames(prototype)) {
      if (methodName === 'constructor') {
        continue;
      }
      const method = (instance as Record<string, unknown>)[methodName];
      if (typeof method !== 'function') {
        continue;
      }
      const bound = method.bind(instance);
      const computed = Reflect.getMetadata(GOLEM_COMPUTED_FIELD, method) as
        | ComputedFieldMetadata
        | undefined;
      if (computed) {
        computedFields.push({
          model: computed.model,
          name: computed.name ?? methodName,
          type: computed.type,
          requires: computed.requires ?? [],
          resolve: bound,
        });
      }
      const custom = Reflect.getMetadata(GOLEM_CUSTOM_OPERATION, method) as
        | CustomOperationMetadata
        | undefined;
      if (custom) {
        customOperations.push({
          kind: custom.kind,
          name: custom.name ?? methodName,
          type: custom.type,
          args: custom.args,
          resolve: bound,
        });
      }
    }
  }
  return { computedFields, customOperations };
}

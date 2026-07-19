import { ComputedFieldSpec, CustomOperationSpec } from '@eleven-am/golem-core';
import {
  GOLEM_COMPUTED_FIELD,
  GOLEM_COMPUTED_MODELS,
  GOLEM_CUSTOM_OPERATION,
  ComputedFieldMetadata,
  CustomOperationMetadata,
} from './decorators';

export interface ExtractedExtensions {
  computedFields: ComputedFieldSpec[];
  customOperations: CustomOperationSpec[];
}

function methodEntries(type: Function): Array<{ name: string; method: Function }> {
  const entries: Array<{ name: string; method: Function }> = [];
  const visited = new Set<string>();
  let prototype = type.prototype;
  while (prototype && prototype !== Object.prototype) {
    for (const methodName of Object.getOwnPropertyNames(prototype)) {
      if (methodName === 'constructor' || visited.has(methodName)) {
        continue;
      }
      visited.add(methodName);
      const descriptor = Object.getOwnPropertyDescriptor(prototype, methodName);
      if (typeof descriptor?.value === 'function') {
        entries.push({ name: methodName, method: descriptor.value });
      }
    }
    prototype = Object.getPrototypeOf(prototype);
  }
  return entries;
}

function nestOwnedResolver(surface: string): never {
  throw new Error(`${surface} must be invoked through Nest`);
}

export function extractExtensionSpecs(types: readonly Function[]): ExtractedExtensions {
  const computedFields: ComputedFieldSpec[] = [];
  const customOperations: CustomOperationSpec[] = [];
  for (const type of types) {
    const declaredModels = new Set<string>(
      Reflect.getOwnMetadata(GOLEM_COMPUTED_MODELS, type) as readonly string[] | undefined,
    );
    for (const { name: methodName, method } of methodEntries(type)) {
      const computed = Reflect.getMetadata(GOLEM_COMPUTED_FIELD, method) as
        | ComputedFieldMetadata
        | undefined;
      if (computed) {
        declaredModels.add(computed.model);
        computedFields.push({
          model: computed.model,
          name: computed.name ?? methodName,
          type: computed.type,
          requires: computed.requires ?? [],
          args: computed.args,
          resolve: () => nestOwnedResolver(
            `Computed field ${computed.model}.${computed.name ?? methodName}`,
          ),
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
          resolve: () => nestOwnedResolver(
            `Custom ${custom.kind} ${custom.name ?? methodName}`,
          ),
        });
      }
    }
    if (declaredModels.size > 1) {
      throw new Error(
        `Extension ${type.name || '<anonymous>'} declares computed fields for multiple models: ${[...declaredModels].sort().join(', ')}`,
      );
    }
  }
  return { computedFields, customOperations };
}

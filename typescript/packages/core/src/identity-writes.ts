import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { planNestedWrites } from './nested-writes';

export function writesFields(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  fields: ReadonlySet<string>,
): string | undefined {
  if (!data || typeof data !== 'object') {
    return undefined;
  }
  const record = data as Record<string, unknown>;
  for (const name of fields) {
    if (record[name] !== undefined) {
      return name;
    }
  }
  for (const plan of planNestedWrites(metadata, metadata.get(model)!.model, record)) {
    const from = plan.field.relationFromFields ?? [];
    const referenced = new Set(from.flatMap((name, index) => (fields.has(name) ? [plan.field.relationToFields![index]] : [])));
    if (referenced.size === 0) {
      continue;
    }
    if (plan.addedActions.size + plan.removedActions.size > 0) {
      return plan.field.name;
    }
    if (plan.updatePayloads.some((payload) => writesFields(metadata, plan.target.name, payload, referenced) !== undefined)) {
      return plan.field.name;
    }
  }
  return undefined;
}

export function refuseIdentityChanges(metadata: ModelMetadataIndex, model: string, data: unknown): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  const identity = new Set(metadata.get(model)!.identityFields.map((field) => field.name));
  const changed = writesFields(metadata, model, data, identity);
  if (changed !== undefined) {
    throw new GolemValidationError(`${model}.${changed} identifies the row and cannot be changed by an update`);
  }
  for (const plan of planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>)) {
    for (const payload of plan.updatePayloads) {
      refuseIdentityChanges(metadata, plan.target.name, payload);
    }
  }
}

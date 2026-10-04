import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { planNestedWrites } from './nested-writes';

function changedIdentity(metadata: ModelMetadataIndex, model: string, data: Record<string, unknown>): string | undefined {
  const meta = metadata.get(model)!;
  const identity = new Set(meta.identityFields.map((field) => field.name));
  for (const name of identity) {
    if (data[name] !== undefined) {
      return name;
    }
  }
  return meta.relations.find((relation) =>
    data[relation.name] !== undefined &&
    (relation.relationFromFields ?? []).some((name) => identity.has(name)))?.name;
}

export function refuseIdentityChanges(metadata: ModelMetadataIndex, model: string, data: unknown): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  const changed = changedIdentity(metadata, model, data as Record<string, unknown>);
  if (changed !== undefined) {
    throw new GolemValidationError(`${model}.${changed} identifies the row and cannot be changed by an update`);
  }
  for (const relation of planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>)) {
    for (const payload of relation.updatePayloads) {
      refuseIdentityChanges(metadata, relation.target.name, payload);
    }
  }
}

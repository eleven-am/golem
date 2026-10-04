import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { NestedRelationWritePlan, planNestedWrites } from './nested-writes';

function reassignsIdentity(plan: NestedRelationWritePlan): boolean {
  if (plan.addedActions.size + plan.removedActions.size > 0) {
    return true;
  }
  const referenced = plan.field.relationToFields ?? [];
  return plan.updatePayloads.some((payload) => referenced.some((name) => payload[name] !== undefined));
}

function changedIdentity(
  metadata: ModelMetadataIndex,
  model: string,
  data: Record<string, unknown>,
  plans: readonly NestedRelationWritePlan[],
): string | undefined {
  const identity = new Set(metadata.get(model)!.identityFields.map((field) => field.name));
  for (const name of identity) {
    if (data[name] !== undefined) {
      return name;
    }
  }
  return plans.find((plan) =>
    (plan.field.relationFromFields ?? []).some((name) => identity.has(name)) && reassignsIdentity(plan))?.field.name;
}

export function refuseIdentityChanges(metadata: ModelMetadataIndex, model: string, data: unknown): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  const plans = planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>);
  const changed = changedIdentity(metadata, model, data as Record<string, unknown>, plans);
  if (changed !== undefined) {
    throw new GolemValidationError(`${model}.${changed} identifies the row and cannot be changed by an update`);
  }
  for (const plan of plans) {
    for (const payload of plan.updatePayloads) {
      refuseIdentityChanges(metadata, plan.target.name, payload);
    }
  }
}

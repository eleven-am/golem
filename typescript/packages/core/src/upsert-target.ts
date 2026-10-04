import { canonicalToken } from './canonical';
import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { planNestedWrites } from './nested-writes';

export function upsertTargetSelectors(
  metadata: ModelMetadataIndex,
  model: string,
  where: unknown,
): ReadonlyMap<string, unknown> {
  const selectors = new Map<string, unknown>();
  if (!where || typeof where !== 'object' || Array.isArray(where)) {
    return selectors;
  }
  const meta = metadata.get(model);
  if (!meta) {
    return selectors;
  }
  for (const [key, value] of Object.entries(where as Record<string, unknown>)) {
    const field = meta.fieldsByName.get(key);
    if (field) {
      if (field.kind !== 'object' && (field.isId || field.isUnique)) {
        selectors.set(key, value);
      }
      continue;
    }
    const members = key === meta.compoundKeyName
      ? meta.primaryKeys.map((member) => member.name)
      : meta.compoundUniqueSelectors.get(key);
    if (!members || !value || typeof value !== 'object') {
      continue;
    }
    for (const member of members) {
      selectors.set(member, (value as Record<string, unknown>)[member]);
    }
  }
  return selectors;
}

function writtenValue(
  metadata: ModelMetadataIndex,
  model: string,
  input: Record<string, unknown>,
  name: string,
): unknown {
  if (input[name] !== undefined) {
    return input[name];
  }
  for (const relation of metadata.get(model)?.relations ?? []) {
    const position = relation.relationFromFields?.indexOf(name) ?? -1;
    const connect = (input[relation.name] as { connect?: unknown } | undefined)?.connect;
    if (position < 0 || !connect || typeof connect !== 'object') {
      continue;
    }
    return (connect as Record<string, unknown>)[relation.relationToFields![position]];
  }
  return undefined;
}

export function refuseUpsertOffTarget(
  metadata: ModelMetadataIndex,
  model: string,
  where: unknown,
  create: unknown,
): void {
  const input = create && typeof create === 'object' ? create as Record<string, unknown> : {};
  for (const [name, target] of upsertTargetSelectors(metadata, model, where)) {
    const written = writtenValue(metadata, model, input, name);
    if (written === undefined || canonicalToken(written) !== canonicalToken(target)) {
      throw new GolemValidationError(
        `upsert create input does not set the target selector ${name} on ${model}`,
      );
    }
  }
}

export function refuseNestedUpsertsOffTarget(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  for (const relation of planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>)) {
    for (const operation of relation.operations) {
      if (operation.kind !== 'upsert') {
        continue;
      }
      for (const payload of operation.payloads) {
        const item = payload as { where?: unknown; create?: unknown } | null;
        refuseUpsertOffTarget(metadata, relation.target.name, item?.where, item?.create);
      }
    }
    for (const nested of [...relation.createPayloads, ...relation.updatePayloads]) {
      refuseNestedUpsertsOffTarget(metadata, relation.target.name, nested);
    }
  }
}

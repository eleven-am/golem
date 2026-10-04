import { canonicalToken } from './canonical';
import { GolemValidationError } from './errors';
import { flattenUniqueSelectors, ModelMetadataIndex } from './model-meta';
import { nestedPayloads, oppositeRelation, planNestedWrites } from './nested-writes';

export function upsertTargetSelectors(
  metadata: ModelMetadataIndex,
  model: string,
  where: unknown,
): ReadonlyMap<string, unknown> {
  const selectors = new Map<string, unknown>();
  const meta = metadata.get(model);
  const flattened = flattenUniqueSelectors(meta, where);
  if (!meta || !flattened.where || typeof flattened.where !== 'object' || Array.isArray(flattened.where)) {
    return selectors;
  }
  for (const [key, value] of Object.entries(flattened.where as Record<string, unknown>)) {
    const field = meta.fieldsByName.get(key);
    if (field && field.kind !== 'object' && (field.isId || field.isUnique || flattened.members.has(key))) {
      selectors.set(key, value);
    }
  }
  return selectors;
}

type Known = Readonly<Record<string, unknown>>;

function writtenValue(
  metadata: ModelMetadataIndex,
  model: string,
  input: Record<string, unknown>,
  name: string,
  implied: Known,
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
    const referenced = flattenUniqueSelectors(metadata.get(relation.type), connect).where;
    return (referenced as Record<string, unknown>)[relation.relationToFields![position]];
  }
  return implied[name];
}

function refuseOffTarget(
  metadata: ModelMetadataIndex,
  operation: 'upsert' | 'connectOrCreate',
  model: string,
  where: unknown,
  create: unknown,
  implied: Known,
): void {
  const input = create && typeof create === 'object' ? create as Record<string, unknown> : {};
  for (const [name, target] of upsertTargetSelectors(metadata, model, where)) {
    const written = writtenValue(metadata, model, input, name, implied);
    if (written === undefined || canonicalToken(written) !== canonicalToken(target)) {
      throw new GolemValidationError(
        `${operation} create input does not set the target selector ${name} on ${model}`,
      );
    }
  }
}

export function refuseUpsertOffTarget(
  metadata: ModelMetadataIndex,
  model: string,
  where: unknown,
  create: unknown,
): void {
  refuseOffTarget(metadata, 'upsert', model, where, create, {});
}

function scalarValues(data: unknown): Known {
  if (!data || typeof data !== 'object') {
    return {};
  }
  return Object.fromEntries(Object.entries(data as Record<string, unknown>)
    .filter(([, value]) => value === null || typeof value !== 'object' || value instanceof Date));
}

function selectedValues(metadata: ModelMetadataIndex, model: string, where: unknown): Known {
  return scalarValues(flattenUniqueSelectors(metadata.get(model), where).where);
}

const NESTED_ROWS = new Set(['create', 'createMany', 'connectOrCreate', 'upsert', 'update']);

function refuseBranchesOffTarget(metadata: ModelMetadataIndex, model: string, data: unknown, known: Known): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  for (const relation of planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>)) {
    const target = relation.target.name;
    if (!relation.operations.some((operation) => NESTED_ROWS.has(operation.kind))) {
      continue;
    }
    const opposite = oppositeRelation(metadata, model, relation.field);
    const implied: Known = Object.fromEntries((opposite.relationFromFields ?? [])
      .map((name, index) => [name, known[opposite.relationToFields![index]]])
      .filter(([, value]) => value !== undefined));
    const created = (payload: unknown) => refuseBranchesOffTarget(metadata, target, payload, { ...scalarValues(payload), ...implied });
    const updated = (where: unknown, payload: unknown) =>
      refuseBranchesOffTarget(metadata, target, payload, { ...implied, ...selectedValues(metadata, target, where) });
    for (const operation of relation.operations) {
      for (const payload of operation.payloads) {
        const item = (payload ?? {}) as { where?: unknown; create?: unknown; update?: unknown; data?: unknown };
        if (operation.kind === 'upsert' || operation.kind === 'connectOrCreate') {
          refuseOffTarget(metadata, operation.kind, target, item.where, item.create, implied);
          created(item.create);
        }
        if (operation.kind === 'upsert') {
          updated(item.where, item.update);
        }
        if (operation.kind === 'create') {
          created(payload);
        }
        if (operation.kind === 'createMany') {
          for (const entry of Array.isArray(item.data) ? item.data : [item.data]) created(entry);
        }
        if (operation.kind === 'update') {
          const nested = nestedPayloads('update', payload);
          updated(nested.where, nested.data);
        }
      }
    }
  }
}

export function refuseCreatedBranchesOffTarget(metadata: ModelMetadataIndex, model: string, data: unknown): void {
  refuseBranchesOffTarget(metadata, model, data, scalarValues(data));
}

export function refuseUpdatedBranchesOffTarget(
  metadata: ModelMetadataIndex,
  model: string,
  where: unknown,
  data: unknown,
): void {
  refuseBranchesOffTarget(metadata, model, data, selectedValues(metadata, model, where));
}

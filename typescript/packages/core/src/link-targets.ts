import { isPlainObject } from '@eleven-am/golem-policy';
import { canonicalToken } from './canonical';
import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { nestedPayloads, oppositeRelation, planNestedWrites } from './nested-writes';

type Filter = Record<string, unknown>;

export interface LinkRemoval {
  readonly model: string;
  linkedTo(root: Filter): Filter;
}

export interface LinkTarget {
  readonly model: string;
  readonly where: Record<string, unknown>;
  readonly createsWhenMissing: boolean;
}

function foreignKeyValue(model: string, field: string, value: unknown): unknown {
  if (!isPlainObject(value)) {
    return value;
  }
  const operations = Object.keys(value);
  if (operations.length === 1 && operations[0] === 'set') {
    return value.set;
  }
  throw new GolemValidationError(
    `foreign key ${model}.${field} must be set to a value, not changed arithmetically`,
  );
}

function foreignKeyTargets(
  metadata: ModelMetadataIndex,
  model: string,
  data: Record<string, unknown>,
  into: LinkTarget[],
): void {
  for (const relation of metadata.get(model)!.relations) {
    const from = relation.relationFromFields ?? [];
    const written = from.filter((name) => data[name] !== undefined);
    if (written.length === 0) {
      continue;
    }
    if (written.length !== from.length) {
      throw new GolemValidationError(
        `Every field of the foreign key ${relation.name} on ${model} must be written together`,
      );
    }
    const values = from.map((name) => foreignKeyValue(model, name, data[name]));
    if (values.some((value) => value === null)) {
      continue;
    }
    into.push(Object.freeze({
      model: relation.type,
      where: Object.fromEntries(relation.relationToFields!.map((name, index) => [name, values[index]])),
      createsWhenMissing: false,
    }));
  }
}

function collect(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  into: LinkTarget[],
): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  const record = data as Record<string, unknown>;
  foreignKeyTargets(metadata, model, record, into);
  for (const relation of planNestedWrites(metadata, metadata.get(model)!.model, record)) {
    for (const operation of relation.operations) {
      const linking = operation.kind === 'connect' || operation.kind === 'set' || operation.kind === 'disconnect';
      if (!linking && operation.kind !== 'connectOrCreate') {
        continue;
      }
      for (const payload of operation.payloads) {
        const where = linking ? payload : (payload as { where?: unknown } | null)?.where;
        if (!where || typeof where !== 'object') {
          continue;
        }
        into.push(Object.freeze({
          model: relation.target.name,
          where: where as Record<string, unknown>,
          createsWhenMissing: operation.kind === 'connectOrCreate',
        }));
      }
    }
    for (const nested of [...relation.createPayloads, ...relation.updatePayloads]) {
      collect(metadata, relation.target.name, nested, into);
    }
  }
}

export function collectLinkTargets(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
): readonly LinkTarget[] {
  const targets: LinkTarget[] = [];
  collect(metadata, model, data, targets);
  return targets;
}

export function linkedRowKey(
  metadata: ModelMetadataIndex,
  model: string,
  row: Record<string, unknown>,
): string {
  const key = metadata.get(model)!.identityFields.map((field) => row[field.name]);
  return `${model}\u0000${canonicalToken(key)}`;
}

function collectRemovals(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  parent: (root: Filter) => Filter,
  unwrap: (model: string, where: unknown) => unknown,
  into: LinkRemoval[],
): void {
  if (!data || typeof data !== 'object') {
    return;
  }
  for (const relation of planNestedWrites(metadata, metadata.get(model)!.model, data as Filter)) {
    const target = relation.target.name;
    const opposite = oppositeRelation(metadata, model, relation.field);
    const linkedTo = (root: Filter): Filter => ({
      [opposite.name]: opposite.isList ? { some: parent(root) } : { is: parent(root) },
    });
    for (const operation of relation.operations) {
      if (operation.kind === 'set' || (operation.kind === 'disconnect' && operation.payloads.includes(true))) {
        into.push(Object.freeze({ model: target, linkedTo }));
      }
      if (operation.kind !== 'update' && operation.kind !== 'upsert') {
        continue;
      }
      for (const payload of operation.payloads) {
        const nested = nestedPayloads(operation.kind, payload);
        const where = nested.where && typeof nested.where === 'object' ? unwrap(target, nested.where) : undefined;
        const child = (root: Filter): Filter => (where ? { AND: [where, linkedTo(root)] } : linkedTo(root));
        collectRemovals(metadata, target, nested.data, child, unwrap, into);
      }
    }
  }
}

export function collectLinkRemovals(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  unwrap: (model: string, where: unknown) => unknown,
): readonly LinkRemoval[] {
  const removals: LinkRemoval[] = [];
  collectRemovals(metadata, model, data, (root) => root, unwrap, removals);
  return removals;
}

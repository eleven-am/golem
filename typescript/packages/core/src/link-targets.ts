import { canonicalToken } from './canonical';
import { GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';
import { planNestedWrites } from './nested-writes';

export interface LinkTarget {
  readonly model: string;
  readonly where: Record<string, unknown>;
  readonly createsWhenMissing: boolean;
}

function scalarValue(value: unknown): unknown {
  if (value && typeof value === 'object' && !(value instanceof Date) && 'set' in value) {
    return (value as { set: unknown }).set;
  }
  return value;
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
    const values = from.map((name) => scalarValue(data[name]));
    if (values.every((value) => value === null)) {
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
  const key = metadata.get(model)!.primaryKeys.map((field) => row[field.name]);
  return `${model}\u0000${canonicalToken(key)}`;
}

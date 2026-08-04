import { GolemAction } from './authorization';
import { DatamodelField, DatamodelModel } from './datamodel';
import { ModelMetadataIndex } from './model-meta';

export type NestedWriteKind =
  | 'create'
  | 'createMany'
  | 'connectOrCreate'
  | 'connect'
  | 'disconnect'
  | 'set'
  | 'update'
  | 'updateMany'
  | 'upsert'
  | 'delete'
  | 'deleteMany';

const KINDS = new Set<NestedWriteKind>([
  'create',
  'createMany',
  'connectOrCreate',
  'connect',
  'disconnect',
  'set',
  'update',
  'updateMany',
  'upsert',
  'delete',
  'deleteMany',
]);

export interface NestedWriteOperation {
  readonly kind: NestedWriteKind;
  readonly payloads: readonly unknown[];
  readonly createPayloads: readonly Record<string, unknown>[];
  readonly updatePayloads: readonly Record<string, unknown>[];
}

export interface NestedRelationWritePlan {
  readonly field: DatamodelField;
  readonly target: DatamodelModel;
  readonly operations: readonly NestedWriteOperation[];
  readonly createPayloads: readonly Record<string, unknown>[];
  readonly updatePayloads: readonly Record<string, unknown>[];
  readonly modelActions: ReadonlySet<GolemAction>;
  readonly addedActions: ReadonlySet<GolemAction>;
  readonly removedActions: ReadonlySet<GolemAction>;
  readonly retainedActions: ReadonlySet<GolemAction>;
}

function array(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : [value];
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : undefined;
}

function operation(kind: NestedWriteKind, value: unknown): NestedWriteOperation {
  const payloads = array(value);
  const createPayloads: Record<string, unknown>[] = [];
  const updatePayloads: Record<string, unknown>[] = [];
  for (const payload of payloads) {
    const item = record(payload);
    if (!item) continue;
    if (kind === 'create') createPayloads.push(item);
    if (kind === 'createMany') {
      for (const data of array(item.data)) {
        const candidate = record(data);
        if (candidate) createPayloads.push(candidate);
      }
    }
    if (kind === 'connectOrCreate') {
      const candidate = record(item.create);
      if (candidate) createPayloads.push(candidate);
    }
    if (kind === 'update' || kind === 'updateMany') {
      const candidate = record(item.data) ?? item;
      if (candidate) updatePayloads.push(candidate);
    }
    if (kind === 'upsert') {
      const created = record(item.create);
      const updated = record(item.update);
      if (created) createPayloads.push(created);
      if (updated) updatePayloads.push(updated);
    }
  }
  return Object.freeze({ kind, payloads, createPayloads, updatePayloads });
}

function actionSets(operations: readonly NestedWriteOperation[]) {
  const model = new Set<GolemAction>();
  const added = new Set<GolemAction>();
  const removed = new Set<GolemAction>();
  const retained = new Set<GolemAction>();
  for (const { kind } of operations) {
    if (kind === 'create' || kind === 'createMany') {
      model.add('create'); added.add('create');
    } else if (kind === 'connectOrCreate') {
      model.add('create'); model.add('update'); added.add('create'); added.add('update');
    } else if (kind === 'connect') {
      model.add('update'); added.add('update');
    } else if (kind === 'disconnect' || kind === 'set') {
      model.add('update'); removed.add('update');
      if (kind === 'set') added.add('update');
    } else if (kind === 'update' || kind === 'updateMany') {
      model.add('update'); retained.add('update');
    } else if (kind === 'upsert') {
      model.add('create'); model.add('update'); added.add('create'); retained.add('update');
    } else if (kind === 'delete' || kind === 'deleteMany') {
      model.add('delete'); removed.add('delete');
    }
  }
  return { model, added, removed, retained };
}

export function planNestedWrites(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  data: Record<string, unknown>,
): readonly NestedRelationWritePlan[] {
  const plans: NestedRelationWritePlan[] = [];
  const relations = metadata.get(model.name)?.relations ?? model.fields.filter((f) => f.kind === 'object');
  for (const field of relations) {
    const envelope = record(data[field.name]);
    const target = metadata.get(field.type)?.model;
    if (!envelope || !target) continue;
    const operations = Object.entries(envelope)
      .filter(([key, value]) => KINDS.has(key as NestedWriteKind) && value !== undefined)
      .map(([key, value]) => operation(key as NestedWriteKind, value));
    if (operations.length === 0) continue;
    const sets = actionSets(operations);
    plans.push(Object.freeze({
      field,
      target,
      operations,
      createPayloads: operations.flatMap((entry) => entry.createPayloads),
      updatePayloads: operations.flatMap((entry) => entry.updatePayloads),
      modelActions: sets.model,
      addedActions: sets.added,
      removedActions: sets.removed,
      retainedActions: sets.retained,
    }));
  }
  return Object.freeze(plans);
}

function compareFilter(value: unknown, filter: unknown): boolean {
  if (value instanceof Date || filter instanceof Date) {
    const left = value instanceof Date ? value.getTime() : value;
    const right = filter instanceof Date ? filter.getTime() : filter;
    return Object.is(left, right);
  }
  if (!filter || typeof filter !== 'object' || Array.isArray(filter)) return Object.is(value, filter);
  const operations = filter as Record<string, unknown>;
  return Object.entries(operations).every(([operator, expected]) => {
    if (operator === 'equals') return Object.is(value, expected);
    if (operator === 'in') return Array.isArray(expected) && expected.some((item) => Object.is(value, item));
    if (operator === 'notIn') return Array.isArray(expected) && expected.every((item) => !Object.is(value, item));
    if (operator === 'not') return !compareFilter(value, expected);
    if (operator === 'lt') return (value as any) < expected!;
    if (operator === 'lte') return (value as any) <= expected!;
    if (operator === 'gt') return (value as any) > expected!;
    if (operator === 'gte') return (value as any) >= expected!;
    if (operator === 'contains') return typeof value === 'string' && value.includes(String(expected));
    if (operator === 'startsWith') return typeof value === 'string' && value.startsWith(String(expected));
    if (operator === 'endsWith') return typeof value === 'string' && value.endsWith(String(expected));
    return false;
  });
}

function matchesWhere(row: Record<string, unknown>, where: unknown): boolean {
  if (!where || typeof where !== 'object' || Array.isArray(where)) return true;
  return Object.entries(where as Record<string, unknown>).every(([key, expected]) => {
    const branches = Array.isArray(expected) ? expected : [expected];
    if (key === 'AND') return branches.every((branch) => matchesWhere(row, branch));
    if (key === 'OR') return branches.some((branch) => matchesWhere(row, branch));
    if (key === 'NOT') return branches.every((branch) => !matchesWhere(row, branch));
    return compareFilter(row[key], expected);
  });
}

/** Identifies no-op retained updates so they still receive an instance authorization check. */
export function nestedPlanTargetsRow(
  plan: NestedRelationWritePlan,
  row: Record<string, unknown>,
): boolean {
  return plan.operations.some((operation) => {
    if (operation.kind !== 'update' && operation.kind !== 'updateMany' && operation.kind !== 'upsert') {
      return false;
    }
    return operation.payloads.some((payload) => {
      const item = record(payload);
      return item ? matchesWhere(row, item.where) : false;
    });
  });
}

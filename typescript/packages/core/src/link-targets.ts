import { isPlainObject } from '@eleven-am/golem-policy';
import { canonicalToken } from './canonical';
import { GolemValidationError } from './errors';
import { LockMode } from './cascade';
import { DatamodelField } from './datamodel';
import { ModelMetadataIndex } from './model-meta';
import { nestedPayloads, oppositeRelation, planNestedWrites } from './nested-writes';

type Filter = Record<string, unknown>;

export type LinkLock = LockMode;

export interface LinkRemoval {
  readonly model: string;
  readonly lock: LinkLock;
  linkedTo(root: Filter): Filter;
}


export interface LinkTarget {
  readonly model: string;
  readonly where: Record<string, unknown>;
  readonly lock: LinkLock;
}

function linkLock(relation: DatamodelField): LinkLock {
  return relation.relationFromFields?.length ? 'SHARE' : 'UPDATE';
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
      lock: 'SHARE',
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
    const target = relation.target.name;
    for (const operation of relation.operations) {
      const linking = operation.kind === 'connect' || operation.kind === 'set' || operation.kind === 'disconnect';
      for (const payload of operation.payloads) {
        if (linking && payload && typeof payload === 'object') {
          into.push(Object.freeze({ model: target, where: payload as Record<string, unknown>, lock: linkLock(relation.field) }));
        }
        if (operation.kind === 'create') {
          collect(metadata, target, payload, into);
        }
        if (operation.kind === 'createMany') {
          const item = (payload ?? {}) as { data?: unknown };
          for (const entry of Array.isArray(item.data) ? item.data : [item.data]) {
            collect(metadata, target, entry, into);
          }
        }
        if (operation.kind === 'update' || operation.kind === 'updateMany') {
          collect(metadata, target, nestedPayloads('update', payload).data, into);
        }
      }
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

export function hasNestedBranches(metadata: ModelMetadataIndex, model: string, data: unknown): boolean {
  if (!data || typeof data !== 'object') {
    return false;
  }
  return planNestedWrites(metadata, metadata.get(model)!.model, data as Record<string, unknown>).some((relation) =>
    relation.operations.some((operation) =>
      operation.kind === 'upsert'
      || operation.kind === 'connectOrCreate'
      || (operation.kind === 'create' && operation.payloads.some((payload) => hasNestedBranches(metadata, relation.target.name, payload)))
      || (operation.kind === 'update'
        && operation.payloads.some((payload) => hasNestedBranches(metadata, relation.target.name, nestedPayloads('update', payload).data)))));
}

export type BranchDecider = (model: string, filter: unknown) => Promise<Record<string, unknown> | null>;

function identityWhere(metadata: ModelMetadataIndex, model: string, row: Record<string, unknown>): Record<string, unknown> {
  const meta = metadata.get(model)!;
  const scalars = Object.fromEntries(meta.identityFields.map((field) => [field.name, row[field.name]]));
  return meta.identityFields.length === 1 ? scalars : { [meta.identitySelector!]: scalars };
}

export async function resolveNestedBranches(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  root: Filter | undefined,
  unwrap: (model: string, where: unknown) => unknown,
  decide: BranchDecider,
  created: LinkTarget[] = [],
): Promise<unknown> {
  const resolve = async (current: string, payload: unknown, parent: ((root: Filter) => Filter) | undefined): Promise<unknown> => {
    if (!payload || typeof payload !== 'object') {
      return payload;
    }
    const record: Record<string, unknown> = { ...(payload as Record<string, unknown>) };
    for (const relation of planNestedWrites(metadata, metadata.get(current)!.model, record)) {
      const target = relation.target.name;
      const list = relation.field.isList;
      const linkedTo = parent === undefined
        ? undefined
        : (base: Filter): Filter => {
            const opposite = oppositeRelation(metadata, current, relation.field);
            return { [opposite.name]: opposite.isList ? { some: parent(base) } : { is: parent(base) } };
          };
      const scoped = (where: unknown) => linkedTo === undefined
        ? undefined
        : (base: Filter): Filter => (where && typeof where === 'object'
          ? { AND: [unwrap(target, where) as Filter, linkedTo(base)] }
          : linkedTo(base));
      const selected = (where: unknown) => {
        const filter = scoped(where);
        return filter ? filter(root!) : unwrap(target, where);
      };
      const envelope = record[relation.field.name] as Record<string, unknown>;
      const out: Record<string, unknown> = {};
      const explicit: Record<'create' | 'update' | 'connect', unknown[]> = { create: [], update: [], connect: [] };
      for (const [key, value] of Object.entries(envelope)) {
        const items = Array.isArray(value) ? value : [value];
        if (key === 'create') {
          for (const item of items) explicit.create.push(await resolve(target, item, undefined));
        } else if (key === 'update') {
          for (const item of items) {
            const nested = nestedPayloads('update', item);
            const resolved = await resolve(target, nested.data, scoped(nested.where));
            explicit.update.push(nested.data === item ? resolved : { ...(item as Record<string, unknown>), data: resolved });
          }
        } else if (key === 'connect') {
          explicit.connect.push(...items);
        } else if (key === 'upsert') {
          for (const item of items as Array<{ where?: unknown; create?: unknown; update?: unknown }>) {
            const row = await decide(target, selected(item.where));
            if (row) {
              const resolved = await resolve(target, item.update, scoped(item.where));
              explicit.update.push(list ? { where: identityWhere(metadata, target, row), data: resolved } : resolved);
            } else {
              explicit.create.push(await resolve(target, item.create, undefined));
            }
          }
        } else if (key === 'connectOrCreate') {
          for (const item of items as Array<{ where?: unknown; create?: unknown }>) {
            const row = await decide(target, unwrap(target, item.where));
            if (row) {
              explicit.connect.push(identityWhere(metadata, target, row));
            } else {
              explicit.create.push(await resolve(target, item.create, undefined));
              created.push(Object.freeze({ model: target, where: item.where as Record<string, unknown>, lock: linkLock(relation.field) }));
            }
          }
        } else {
          out[key] = value;
        }
      }
      for (const [key, entries] of Object.entries(explicit)) {
        if (entries.length > 0) {
          out[key] = list ? entries : entries.length === 1 ? entries[0] : entries;
        }
      }
      record[relation.field.name] = out;
    }
    return record;
  };
  return resolve(model, data, root === undefined ? undefined : (base) => base);
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
    const linkedTo = (root: Filter): Filter => {
      const opposite = oppositeRelation(metadata, model, relation.field);
      return { [opposite.name]: opposite.isList ? { some: parent(root) } : { is: parent(root) } };
    };
    for (const operation of relation.operations) {
      if (operation.kind === 'set' || (operation.kind === 'disconnect' && operation.payloads.includes(true))) {
        into.push(Object.freeze({ model: target, lock: linkLock(relation.field), linkedTo }));
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

function collectMutations(
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
    const linkedTo = (root: Filter): Filter => {
      const opposite = oppositeRelation(metadata, model, relation.field);
      return { [opposite.name]: opposite.isList ? { some: parent(root) } : { is: parent(root) } };
    };
    const scoped = (where: unknown) => {
      const filter = where && typeof where === 'object' ? unwrap(target, where) : undefined;
      return (root: Filter): Filter => (filter ? { AND: [filter, linkedTo(root)] } : linkedTo(root));
    };
    for (const operation of relation.operations) {
      if (operation.kind === 'delete' || operation.kind === 'deleteMany') {
        for (const payload of operation.payloads.filter((entry) => entry !== false)) {
          into.push(Object.freeze({ model: target, lock: 'UPDATE' as const, linkedTo: scoped(payload) }));
        }
      }
      if (operation.kind === 'updateMany') {
        for (const payload of operation.payloads) {
          into.push(Object.freeze({
            model: target,
            lock: 'UPDATE' as const,
            linkedTo: scoped((payload as { where?: unknown } | null)?.where),
          }));
        }
      }
      if (operation.kind === 'update' || operation.kind === 'upsert') {
        for (const payload of operation.payloads) {
          const nested = nestedPayloads(operation.kind, payload);
          const child = scoped(nested.where);
          into.push(Object.freeze({ model: target, lock: 'UPDATE' as const, linkedTo: child }));
          collectMutations(metadata, target, nested.data, child, unwrap, into);
        }
      }
    }
  }
}

export function collectNestedMutations(
  metadata: ModelMetadataIndex,
  model: string,
  data: unknown,
  unwrap: (model: string, where: unknown) => unknown,
): readonly LinkRemoval[] {
  const mutations: LinkRemoval[] = [];
  collectMutations(metadata, model, data, (root) => root, unwrap, mutations);
  return mutations;
}

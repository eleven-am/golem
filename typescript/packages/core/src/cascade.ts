import { GOLEM_UPSERT_GUARD_MODEL } from './upsert-guard';
import type { GolemProvider } from './datamodel';
import { canonicalToken } from './canonical';
import { DatamodelField, DatamodelModel, rowIdentityFields } from './datamodel';
import { GolemConflictError, GolemValidationError } from './errors';
import { buildModelMetadata, ModelMetadataIndex } from './model-meta';
import { nestedPayloads, oppositeRelation, planNestedWrites } from './nested-writes';

type Row = Record<string, unknown>;
type Tuples = readonly (readonly unknown[])[];

export type CascadeAction = 'Cascade' | 'SetNull' | 'SetDefault';

export interface DeleteDependency {
  readonly dependent: string;
  readonly from: readonly string[];
  readonly to: readonly string[];
  readonly action: CascadeAction;
}

export interface CascadeRow {
  readonly model: string;
  readonly row: Row;
}

export interface CascadeTouched {
  readonly deleted: readonly CascadeRow[];
  readonly updated: readonly CascadeRow[];
}

export interface CascadeTransaction {
  findMany(model: string, args: unknown): Promise<Row[]>;
}

const CASCADING = new Set<string>(['Cascade', 'SetNull', 'SetDefault']);

function tupleWhere(fields: readonly string[], tuples: Tuples): unknown {
  if (fields.length === 1) {
    return { [fields[0]]: { in: tuples.map((tuple) => tuple[0]) } };
  }
  return { OR: tuples.map((tuple) => Object.fromEntries(fields.map((name, index) => [name, tuple[index]]))) };
}

function quote(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`;
}

export function tooManyTouchedRows(model: string, limit: number): GolemValidationError {
  return new GolemValidationError(
    `Deleting from ${model} would touch more than the maximum of ${limit} rows, counting cascaded dependents`,
  );
}

export class CascadePlan {
  private readonly dependencies = new Map<string, DeleteDependency[]>();
  private readonly modelsByName: ReadonlyMap<string, DatamodelModel>;
  readonly metadata: ModelMetadataIndex;

  constructor(models: readonly DatamodelModel[], upsertGuard?: DatamodelModel) {
    this.modelsByName = new Map([...models, ...(upsertGuard ? [upsertGuard] : [])].map((model) => [model.name, model]));
    this.metadata = buildModelMetadata(models);
    for (const model of models) {
      for (const field of model.fields) {
        if (field.kind !== 'object' || !field.relationFromFields?.length) {
          continue;
        }
        if (!CASCADING.has(field.relationOnDelete ?? '')) {
          continue;
        }
        const dependencies = this.dependencies.get(field.type) ?? [];
        dependencies.push(Object.freeze({
          dependent: model.name,
          from: [...field.relationFromFields],
          to: [...field.relationToFields!],
          action: field.relationOnDelete as CascadeAction,
        }));
        this.dependencies.set(field.type, dependencies);
      }
    }
  }

  dependentsOf(model: string): readonly DeleteDependency[] {
    return this.dependencies.get(model) ?? [];
  }

  private definition(model: string): DatamodelModel {
    const found = this.modelsByName.get(model);
    if (!found) {
      throw new GolemValidationError(model === GOLEM_UPSERT_GUARD_MODEL
        ? 'The datamodel does not declare GolemUpsertGuard; regenerate the Golem client from a schema that contains it'
        : `Model ${model} is not in the datamodel`);
    }
    return found;
  }

  identity(model: string): readonly string[] {
    const identity = rowIdentityFields(this.definition(model));
    if (!identity) {
      throw new GolemValidationError(
        `Cannot delete from ${model}: it has no primary key or required unique field to identify the rows a delete touches`,
      );
    }
    return identity;
  }

  scalarFields(model: string): readonly DatamodelField[] {
    return this.definition(model).fields.filter((field) => field.kind !== 'object');
  }

  scalarSelect(model: string): Record<string, true> {
    return Object.fromEntries(this.scalarFields(model).map((field) => [field.name, true]));
  }

  column(model: string, name: string): string {
    const field = this.definition(model).fields.find((candidate) => candidate.name === name)!;
    return field.dbName ?? field.name;
  }

  qualifiedTable(model: string): string {
    const definition = this.definition(model);
    const table = quote(definition.dbName ?? definition.name);
    return definition.schema ? `${quote(definition.schema)}.${table}` : table;
  }

  rowKey(model: string, row: Row): string {
    return `${model}\u0000${canonicalToken(this.identity(model).map((name) => row[name]))}`;
  }
}

export function lockStatement(
  plan: CascadePlan,
  model: string,
  row: Row,
  mode: 'UPDATE' | 'SHARE',
  wait: boolean,
): { sql: string; values: unknown[] } {
  const identity = plan.identity(model);
  const columns = identity.map((name) => quote(plan.column(model, name))).join(', ');
  const placeholders = identity.map((_name, index) => `$${index + 1}`).join(', ');
  return {
    sql: `SELECT 1 FROM ${plan.qualifiedTable(model)} WHERE (${columns}) = (${placeholders}) FOR ${mode}${wait ? '' : ' NOWAIT'}`,
    values: identity.map((name) => row[name]),
  };
}

export type LockMode = 'UPDATE' | 'SHARE';

export interface LockRequest {
  readonly model: string;
  readonly row: Row;
  readonly mode: LockMode;
}

export type RowLocker = (request: LockRequest, wait: boolean) => Promise<boolean>;

function lockUnavailable(error: unknown): boolean {
  const meta = (error as { meta?: { code?: unknown } } | null)?.meta;
  return meta?.code === '55P03' || /55P03|could not obtain lock/.test(String((error as Error)?.message ?? ''));
}

export function rowLocker(
  provider: GolemProvider | undefined,
  plan: CascadePlan,
  run: (sql: string, values: unknown[]) => Promise<unknown>,
): RowLocker {
  switch (provider) {
    case 'postgresql':
      return async ({ model, row, mode }, wait) => {
        const statement = lockStatement(plan, model, row, mode, wait);
        try {
          return ((await run(statement.sql, statement.values)) as readonly unknown[]).length > 0;
        } catch (error) {
          if (lockUnavailable(error)) {
            throw new GolemConflictError(`A row of ${model} this write needs is held by a concurrent write`);
          }
          throw error;
        }
      };
    case 'sqlite':
    case undefined:
      return async () => true;
  }
}

export class RowLocks {
  private readonly held = new Map<string, LockMode>();

  constructor(private readonly plan: CascadePlan) {}

  async acquire(requests: readonly LockRequest[], lock: RowLocker): Promise<void> {
    const wanted = new Map<string, LockRequest>();
    for (const request of requests) {
      const key = this.plan.rowKey(request.model, request.row);
      const known = wanted.get(key);
      if (!known || (known.mode === 'SHARE' && request.mode === 'UPDATE')) {
        wanted.set(key, request);
      }
    }
    const ordered = [...wanted.entries()].sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0));
    const highest = [...this.held.keys()].reduce((top, key) => (key > top ? key : top), '');
    for (const [key, request] of ordered) {
      const held = this.held.get(key);
      if (held === 'UPDATE' || held === request.mode) {
        continue;
      }
      await lock(request, key > highest);
      this.held.set(key, request.mode);
    }
  }
}

const transactionLocks = new WeakMap<object, RowLocks>();

export function transactionRowLocks(transaction: object, plan: CascadePlan): RowLocks {
  const existing = transactionLocks.get(transaction);
  if (existing) {
    return existing;
  }
  const locks = new RowLocks(plan);
  transactionLocks.set(transaction, locks);
  return locks;
}

export interface CascadeScope {
  readonly roots: readonly CascadeRow[];
  readonly parents: readonly CascadeRow[];
}

export interface LockedCascade extends CascadeTouched {
  readonly roots: readonly CascadeRow[];
}

function scopeKeys(plan: CascadePlan, scope: CascadeScope, touched: CascadeTouched): string[] {
  return [
    ...scope.parents.map(({ model, row }) => `parent\u0000${plan.rowKey(model, row)}`),
    ...touched.deleted.map(({ model, row }) => `deleted\u0000${plan.rowKey(model, row)}`),
    ...touched.updated.map(({ model, row }) => `updated\u0000${plan.rowKey(model, row)}`),
  ].sort();
}

export async function lockCascade(
  plan: CascadePlan,
  transaction: CascadeTransaction,
  locks: RowLocks,
  lock: RowLocker,
  operationModel: string,
  readScope: () => Promise<CascadeScope>,
  limit: number,
): Promise<LockedCascade> {
  const scope = await readScope();
  const touched = await enumerateCascade(plan, transaction, operationModel, scope.roots, limit);
  await locks.acquire([
    ...scope.parents.map((entry) => ({ ...entry, mode: 'UPDATE' as const })),
    ...touched.deleted.map((entry) => ({ ...entry, mode: 'UPDATE' as const })),
    ...touched.updated.map((entry) => ({ ...entry, mode: 'UPDATE' as const })),
  ], lock);
  const lockedScope = await readScope();
  const lockedTouched = await enumerateCascade(plan, transaction, operationModel, lockedScope.roots, limit);
  const before = scopeKeys(plan, scope, touched);
  const after = scopeKeys(plan, lockedScope, lockedTouched);
  if (before.length !== after.length || before.some((key, index) => key !== after[index])) {
    throw new GolemConflictError(
      `The rows deleting from ${operationModel} touches changed while they were being locked`,
    );
  }
  return { roots: lockedScope.roots, ...lockedTouched };
}

export async function enumerateCascade(
  plan: CascadePlan,
  transaction: CascadeTransaction,
  operationModel: string,
  roots: readonly CascadeRow[],
  limit: number,
): Promise<CascadeTouched> {
  const kinds = new Map<string, { entry: CascadeRow; deleted: boolean }>();
  for (const entry of roots) {
    kinds.set(plan.rowKey(entry.model, entry.row), { entry, deleted: true });
  }
  if (kinds.size > limit) {
    throw tooManyTouchedRows(operationModel, limit);
  }
  const pending: CascadeRow[][] = [];
  const byModel = new Map<string, CascadeRow[]>();
  for (const entry of roots) {
    byModel.set(entry.model, [...(byModel.get(entry.model) ?? []), entry]);
  }
  pending.push(...byModel.values());
  while (pending.length > 0) {
    const level = pending.shift()!;
    const parent = level[0].model;
    for (const dependency of plan.dependentsOf(parent)) {
      const tuples = level.map(({ row }) => dependency.to.map((name) => row[name]));
      const found = await transaction.findMany(dependency.dependent, {
        where: tupleWhere(dependency.from, tuples),
        select: plan.scalarSelect(dependency.dependent),
        orderBy: plan.identity(dependency.dependent).map((name) => ({ [name]: 'asc' })),
        take: limit + 1,
      });
      if (found.length > limit) {
        throw tooManyTouchedRows(operationModel, limit);
      }
      const cascaded: CascadeRow[] = [];
      for (const row of found) {
        const key = plan.rowKey(dependency.dependent, row);
        const known = kinds.get(key);
        const deletes = dependency.action === 'Cascade';
        if (known && (known.deleted || !deletes)) {
          continue;
        }
        const entry = { model: dependency.dependent, row };
        kinds.set(key, { entry, deleted: deletes });
        if (deletes) {
          cascaded.push(entry);
        }
      }
      if (kinds.size > limit) {
        throw tooManyTouchedRows(operationModel, limit);
      }
      if (cascaded.length > 0) {
        pending.push(cascaded);
      }
    }
  }
  const deleted: CascadeRow[] = [];
  const updated: CascadeRow[] = [];
  for (const { entry, deleted: isDeleted } of kinds.values()) {
    (isDeleted ? deleted : updated).push(entry);
  }
  return { deleted, updated };
}

export async function enumerateNestedDeletes(
  plan: CascadePlan,
  transaction: CascadeTransaction,
  model: string,
  parents: readonly Row[],
  data: unknown,
  limit: number,
  into: CascadeRow[],
): Promise<void> {
  if (!data || typeof data !== 'object' || parents.length === 0) {
    return;
  }
  const keys = plan.identity(model);
  const parentWhere = tupleWhere(keys, parents.map((row) => keys.map((name) => row[name])));
  for (const relation of planNestedWrites(plan.metadata, plan.metadata.get(model)!.model, data as Row)) {
    const target = relation.target.name;
    const opposite = oppositeRelation(plan.metadata, model, relation.field);
    const link = { [opposite.name]: opposite.isList ? { some: parentWhere } : { is: parentWhere } };
    const rowsWhere = async (where: unknown): Promise<Row[]> => {
      const filter = where && typeof where === 'object' ? { AND: [where, link] } : link;
      const rows = await transaction.findMany(target, {
        where: filter,
        select: plan.scalarSelect(target),
        orderBy: plan.identity(target).map((name) => ({ [name]: 'asc' })),
        take: limit + 1,
      });
      if (rows.length > limit) {
        throw tooManyTouchedRows(model, limit);
      }
      return rows;
    };
    for (const operation of relation.operations) {
      if (operation.kind === 'delete' || operation.kind === 'deleteMany') {
        for (const payload of operation.payloads.filter((entry) => entry !== false)) {
          for (const row of await rowsWhere(payload)) {
            into.push({ model: target, row });
          }
        }
      }
      if (operation.kind === 'update' || operation.kind === 'upsert') {
        for (const payload of operation.payloads) {
          const nested = nestedPayloads(operation.kind, payload);
          await enumerateNestedDeletes(
            plan,
            transaction,
            target,
            await rowsWhere(nested.where),
            nested.data,
            limit,
            into,
          );
        }
      }
    }
  }
}

export function hasNestedDeletes(plan: CascadePlan, model: string, data: unknown): boolean {
  if (!data || typeof data !== 'object') {
    return false;
  }
  return planNestedWrites(plan.metadata, plan.metadata.get(model)!.model, data as Row).some((relation) =>
    relation.operations.some((operation) =>
      ((operation.kind === 'delete' || operation.kind === 'deleteMany') &&
        operation.payloads.some((payload) => payload !== false)) ||
      ((operation.kind === 'update' || operation.kind === 'upsert') &&
        operation.payloads.some((payload) =>
          hasNestedDeletes(plan, relation.target.name, nestedPayloads(operation.kind as 'update' | 'upsert', payload).data))),
    ));
}

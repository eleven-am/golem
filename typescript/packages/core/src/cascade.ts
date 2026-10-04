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
  queryRaw(sql: string, ...values: unknown[]): Promise<Row[]>;
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

  constructor(models: readonly DatamodelModel[]) {
    this.modelsByName = new Map(models.map((model) => [model.name, model]));
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

  primaryKey(model: string): readonly string[] {
    const identity = rowIdentityFields(this.modelsByName.get(model)!);
    if (!identity) {
      throw new GolemValidationError(
        `Cannot delete from ${model}: it has no primary key or required unique field to identify the rows a delete touches`,
      );
    }
    return identity;
  }

  scalarFields(model: string): readonly DatamodelField[] {
    return this.modelsByName.get(model)!.fields.filter((field) => field.kind !== 'object');
  }

  scalarSelect(model: string): Record<string, true> {
    return Object.fromEntries(this.scalarFields(model).map((field) => [field.name, true]));
  }

  column(model: string, name: string): string {
    const field = this.modelsByName.get(model)!.fields.find((candidate) => candidate.name === name)!;
    return field.dbName ?? field.name;
  }

  tableOf(model: string): string {
    const definition = this.modelsByName.get(model)!;
    return definition.dbName ?? definition.name;
  }

  rowKey(model: string, row: Row): string {
    return `${model}\u0000${canonicalToken(this.primaryKey(model).map((name) => row[name]))}`;
  }
}

export function lockedReadStatement(
  plan: CascadePlan,
  model: string,
  fields: readonly string[],
  tuples: Tuples,
  limit: number,
): { sql: string; values: unknown[] } {
  const values: unknown[] = [];
  const rows = tuples.map((tuple) =>
    `(${tuple.map((value) => {
      values.push(value);
      return `$${values.length}`;
    }).join(', ')})`,
  );
  const projection = plan.scalarFields(model)
    .map((field) => `${quote(field.dbName ?? field.name)} AS ${quote(field.name)}`)
    .join(', ');
  const columns = fields.map((name) => quote(plan.column(model, name))).join(', ');
  const order = plan.primaryKey(model).map((name) => quote(plan.column(model, name))).join(', ');
  return {
    sql: `SELECT ${projection} FROM ${quote(plan.tableOf(model))} WHERE (${columns}) IN (${rows.join(', ')}) ORDER BY ${order} LIMIT ${limit} FOR UPDATE`,
    values,
  };
}

export interface CascadeDialect {
  lockedRead(transaction: CascadeTransaction, model: string, fields: readonly string[], tuples: Tuples, limit: number): Promise<Row[]>;
  lockedRoots(
    transaction: CascadeTransaction,
    model: string,
    read: (select: Record<string, true>) => Promise<Row[]>,
  ): Promise<Row[]>;
}

const POSTGRES: (plan: CascadePlan) => CascadeDialect = (plan) => ({
  lockedRead: (transaction, model, fields, tuples, limit) => {
    const statement = lockedReadStatement(plan, model, fields, tuples, limit);
    return transaction.queryRaw(statement.sql, ...statement.values);
  },
  lockedRoots: async (transaction, model, read) => {
    const keys = plan.primaryKey(model);
    const located = await read(Object.fromEntries(keys.map((name) => [name, true])));
    if (located.length === 0) {
      return located;
    }
    const tuples = located.map((row) => keys.map((name) => row[name]));
    const rows = await POSTGRES(plan).lockedRead(transaction, model, keys, tuples, located.length);
    if (rows.length !== located.length) {
      throw new GolemConflictError(
        `Deleting from ${model} found ${rows.length} rows to lock after selecting ${located.length}`,
      );
    }
    return rows;
  },
});

const SQLITE: (plan: CascadePlan) => CascadeDialect = (plan) => ({
  lockedRead: (transaction, model, fields, tuples, limit) =>
    transaction.findMany(model, {
      where: tupleWhere(fields, tuples),
      select: plan.scalarSelect(model),
      orderBy: plan.primaryKey(model).map((name) => ({ [name]: 'asc' })),
      take: limit,
    }),
  lockedRoots: (_transaction, model, read) => read(plan.scalarSelect(model)),
});

export function cascadeDialect(provider: string | undefined, plan: CascadePlan): CascadeDialect {
  if (provider === 'postgresql') return POSTGRES(plan);
  if (provider === 'sqlite') return SQLITE(plan);
  const refuse = (): never => {
    throw new Error(`Deletes cannot lock the rows they touch on provider ${String(provider)}`);
  };
  return { lockedRead: refuse, lockedRoots: refuse };
}

export async function enumerateCascade(
  plan: CascadePlan,
  dialect: CascadeDialect,
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
      const found = await dialect.lockedRead(transaction, dependency.dependent, dependency.from, tuples, limit + 1);
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
  dialect: CascadeDialect,
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
  const keys = plan.primaryKey(model);
  const parentWhere = tupleWhere(keys, parents.map((row) => keys.map((name) => row[name])));
  for (const relation of planNestedWrites(plan.metadata, plan.metadata.get(model)!.model, data as Row)) {
    const target = relation.target.name;
    const opposite = oppositeRelation(plan.metadata, model, relation.field);
    const link = { [opposite.name]: opposite.isList ? { some: parentWhere } : { is: parentWhere } };
    const rowsWhere = async (where: unknown): Promise<Row[]> => {
      const filter = where && typeof where === 'object' ? { AND: [where, link] } : link;
      const rows = await dialect.lockedRoots(transaction, target, (select) =>
        transaction.findMany(target, {
          where: filter,
          select,
          orderBy: plan.primaryKey(target).map((name) => ({ [name]: 'asc' })),
          take: limit + 1,
        }));
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
            dialect,
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

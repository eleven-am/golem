import { DatamodelDocument, DatamodelField } from '../../src/datamodel';
import { GolemBatchDelegate, GolemBatchRuntime, GolemBatchTransaction } from '../../src/publisher';

type Row = Record<string, unknown>;

interface World {
  readonly datamodel?: DatamodelDocument;
  readonly store: Map<string, Row[]>;
}

function relationOf(world: World, model: string, name: string): DatamodelField | undefined {
  return world.datamodel?.models
    .find((candidate) => candidate.name === model)
    ?.fields.find((field) => field.name === name && field.kind === 'object');
}

function related(world: World, model: string, row: Row, field: DatamodelField): Row[] {
  const targets = world.store.get(field.type) ?? [];
  if (field.relationFromFields?.length) {
    return targets.filter((target) =>
      field.relationFromFields!.every((from, index) => target[field.relationToFields![index]] === row[from]));
  }
  const opposite = world.datamodel!.models
    .find((candidate) => candidate.name === field.type)!
    .fields.find((candidate) =>
      candidate.relationName === field.relationName && !(field.type === model && candidate.name === field.name))!;
  return targets.filter((target) =>
    opposite.relationFromFields!.every((from, index) => target[from] === row[opposite.relationToFields![index]]));
}

function matches(world: World, model: string, row: Row, where: unknown): boolean {
  if (!where || typeof where !== 'object') {
    return true;
  }
  return Object.entries(where as Record<string, unknown>).every(([key, expected]) => {
    if (key === 'OR') return (expected as unknown[]).some((branch) => matches(world, model, row, branch));
    if (key === 'AND') return (expected as unknown[]).every((branch) => matches(world, model, row, branch));
    const relation = relationOf(world, model, key);
    if (relation) {
      const filter = expected as { is?: unknown; some?: unknown };
      const rows = related(world, model, row, relation);
      if (filter.is !== undefined) return rows.some((target) => matches(world, relation.type, target, filter.is));
      return rows.some((target) => matches(world, relation.type, target, filter.some));
    }
    if (!(key in row) && expected && typeof expected === 'object') return matches(world, model, row, expected);
    if (expected && typeof expected === 'object') {
      const filter = expected as { in?: unknown[]; contains?: string };
      if (filter.in) return filter.in.includes(row[key]);
      if (filter.contains !== undefined) return String(row[key]).includes(filter.contains);
    }
    return row[key] === expected;
  });
}

function project(row: Row, select: unknown): Row {
  if (!select || typeof select !== 'object') {
    return { ...row };
  }
  return Object.fromEntries(Object.keys(select).map((key) => [key, row[key]]));
}

function scalarData(data: Row | undefined): Row {
  return Object.fromEntries(Object.entries(data ?? {}).filter(([, value]) => !value || typeof value !== 'object'));
}

function shape(row: Row, args: { select?: unknown; omit?: Record<string, boolean> } | undefined): Row {
  const result = project(row, args?.select);
  for (const [key, omitted] of Object.entries(args?.omit ?? {})) {
    if (omitted) delete result[key];
  }
  return result;
}

export interface FakeBatch {
  readonly tables: Map<string, Row[]>;
  readonly statements: string[];
  readonly values: unknown[][];
  readonly runtime: GolemBatchRuntime;
  readonly delegates: Map<string, Record<string, jest.Mock>>;
}

export function fakeBatch(
  tables: Record<string, Row[]>,
  model: string,
  overrides: Partial<Record<keyof GolemBatchDelegate, jest.Mock>> = {},
  datamodel?: DatamodelDocument,
): FakeBatch {
  const store = new Map(Object.entries(tables).map(([name, rows]) => [name, rows.map((row) => ({ ...row }))]));
  const world: World = { datamodel, store };
  const statements: string[] = [];
  const lockValues: unknown[][] = [];
  const delegates = new Map<string, Record<string, jest.Mock>>();
  const delegate = (name: string): Record<string, jest.Mock> => {
    const existing = delegates.get(name);
    if (existing) return existing;
    const rows = () => store.get(name) ?? [];
    const find = (where: unknown) => rows().filter((row) => matches(world, name, row, where));
    const created: Record<string, jest.Mock> = {
      findMany: jest.fn(async (args: { where?: unknown; select?: unknown; take?: number }) =>
        find(args?.where).slice(0, args?.take ?? Infinity).map((row) => project(row, args?.select))),
      findUnique: jest.fn(async (args: { where?: unknown; select?: unknown }) => {
        const found = find(args?.where)[0];
        return found ? project(found, args?.select) : null;
      }),
      updateManyAndReturn: jest.fn(),
      create: jest.fn(async (args: { data?: Row; select?: unknown; omit?: Record<string, boolean> }) => {
        const row = { ...(args?.data ?? {}) };
        store.set(name, [...rows(), row]);
        return shape(row, args);
      }),
      createMany: jest.fn(async (args: { data?: Row[] }) => {
        store.set(name, [...rows(), ...(args?.data ?? []).map((row) => ({ ...row }))]);
        return { count: (args?.data ?? []).length };
      }),
      update: jest.fn(async (args: { where?: unknown; data?: Row; select?: unknown; omit?: Record<string, boolean> }) => {
        const found = find(args?.where)[0];
        if (!found) throw Object.assign(new Error('missing'), { code: 'P2025' });
        Object.assign(found, scalarData(args?.data));
        return shape(found, args);
      }),
      upsert: jest.fn(async (args: { where?: unknown; create?: Row; update?: Row; select?: unknown; omit?: Record<string, boolean> }) => {
        const found = find(args?.where)[0];
        if (found) {
          Object.assign(found, scalarData(args?.update));
          return shape(found, args);
        }
        const row = { ...(args?.create ?? {}) };
        store.set(name, [...rows(), row]);
        return shape(row, args);
      }),
      updateMany: jest.fn(async (args: { where?: unknown; data?: Row }) => {
        const matched = find(args?.where);
        for (const row of matched) Object.assign(row, scalarData(args?.data));
        return { count: matched.length };
      }),
      delete: jest.fn(async (args: { where?: unknown; select?: unknown; omit?: Record<string, boolean> }) => {
        const found = find(args?.where)[0];
        if (!found) throw Object.assign(new Error('missing'), { code: 'P2025' });
        store.set(name, rows().filter((row) => row !== found));
        const result = project(found, args?.select);
        for (const [key, omitted] of Object.entries(args?.omit ?? {})) {
          if (omitted) delete result[key];
        }
        return result;
      }),
      deleteMany: jest.fn(async (args: { where?: unknown }) => {
        const removed = find(args?.where);
        store.set(name, rows().filter((row) => !removed.includes(row)));
        return { count: removed.length };
      }),
      ...(name === model ? overrides : {}),
    };
    delegates.set(name, created);
    return created;
  };
  const transaction: GolemBatchTransaction = {
    scope: store,
    delegate: (name) => delegate(name) as unknown as GolemBatchDelegate,
    queryRaw: async (sql, ...values) => {
      statements.push(sql);
      lockValues.push(values);
      return [{ locked: 1 }];
    },
  };
  for (const name of new Set([model, ...store.keys()])) {
    delegate(name);
  }
  return {
    tables: store,
    statements,
    values: lockValues,
    delegates,
    runtime: {
      suppressed: false,
      run: (work) => work(delegate(model) as unknown as GolemBatchDelegate, transaction),
    },
  };
}

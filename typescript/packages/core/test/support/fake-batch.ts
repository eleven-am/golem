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

export interface FakeBatch {
  readonly tables: Map<string, Row[]>;
  readonly statements: string[];
  readonly runtime: GolemBatchRuntime;
  readonly delegates: Map<string, Record<string, jest.Mock>>;
}

function lockedRows(world: World, sql: string, values: unknown[]): Row[] {
  const match = /FROM "([^"]+)" WHERE \(([^)]*)\) IN .* LIMIT (\d+) FOR UPDATE$/.exec(sql);
  if (!match || !world.datamodel) {
    throw new Error(`fake transaction cannot run ${sql}`);
  }
  const model = world.datamodel.models.find((candidate) => (candidate.dbName ?? candidate.name) === match[1])!;
  const fields = match[2].split(', ').map((column) => {
    const name = column.slice(1, -1);
    return model.fields.find((field) => (field.dbName ?? field.name) === name)!.name;
  });
  const tuples: unknown[][] = [];
  for (let index = 0; index < values.length; index += fields.length) {
    tuples.push(values.slice(index, index + fields.length));
  }
  return (world.store.get(model.name) ?? [])
    .filter((row) => tuples.some((tuple) => fields.every((name, position) => row[name] === tuple[position])))
    .slice(0, Number(match[3]))
    .map((row) => ({ ...row }));
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
      update: jest.fn(async (args: { where?: unknown }) => {
        const found = find(args?.where)[0];
        if (!found) throw Object.assign(new Error('missing'), { code: 'P2025' });
        return { ...found };
      }),
      upsert: jest.fn(async (args: { where?: unknown; create?: Row }) => {
        const found = find(args?.where)[0];
        return { ...(found ?? args?.create ?? {}) };
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
    delegate: (name) => delegate(name) as unknown as GolemBatchDelegate,
    queryRaw: async (sql, ...values) => {
      statements.push(sql);
      return lockedRows(world, sql, values);
    },
  };
  for (const name of new Set([model, ...store.keys()])) {
    delegate(name);
  }
  return {
    tables: store,
    statements,
    delegates,
    runtime: {
      suppressed: false,
      run: (work) => work(delegate(model) as unknown as GolemBatchDelegate, transaction),
    },
  };
}

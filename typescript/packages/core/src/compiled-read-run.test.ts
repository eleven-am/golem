import { CompiledReadBatch } from './compiled-read';
import { COMPILED_READ_BATCH_CHUNK, runCompiledBatches } from './compiled-read-run';

interface Call {
  readonly sql: string;
  readonly parameters: readonly unknown[];
}

interface Harness {
  readonly batch: CompiledReadBatch;
  readonly calls: Call[];
  readonly run: (sql: string, parameters: readonly unknown[]) => Promise<Record<string, unknown>[]>;
}

function harness(
  children: readonly Record<string, unknown>[],
  overrides: Partial<CompiledReadBatch> = {},
  extraParameters: readonly unknown[] = [],
): Harness {
  const calls: Call[] = [];
  const table = overrides.name ?? 'metrics';
  const batch: CompiledReadBatch = {
    path: table,
    name: table,
    parentKey: 'ownerId',
    childKey: 'ownerId',
    relations: [],
    batches: [],
    drop: [],
    reversed: false,
    ...overrides,
    build: (values) => ({
      sql: `select * from ${table} where owner_id in (${values.map(() => '?').join(', ')})`,
      parameters: [...extraParameters, ...values],
    }),
  };
  return {
    batch,
    calls,
    run: async (sql, parameters) => {
      calls.push({ sql, parameters });
      const wanted = new Set(parameters.slice(extraParameters.length).map((value) => String(value)));
      return children
        .filter((child) => wanted.has(String(child.ownerId)))
        .map((child) => ({ ...child }));
    },
  };
}

function parentsFor(count: number): Record<string, unknown>[] {
  return Array.from({ length: count }, (_, index) => ({ ownerId: index + 1 }));
}

function childrenFor(count: number, each: number): Record<string, unknown>[] {
  const rows: Record<string, unknown>[] = [];
  for (let owner = 1; owner <= count; owner += 1) {
    for (let index = 0; index < each; index += 1) {
      rows.push({ id: (owner - 1) * each + index + 1, ownerId: owner });
    }
  }
  return rows;
}

describe('the compiled batch runner', () => {
  it('binds no more ids per statement than one chunk holds', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK * 2 + 7);
    const subject = harness(childrenFor(parents.length, 1));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(3);
    expect(subject.calls.map((call) => call.parameters.length)).toEqual([
      COMPILED_READ_BATCH_CHUNK,
      COMPILED_READ_BATCH_CHUNK,
      7,
    ]);
    expect(subject.calls.every((call) => call.parameters.length <= COMPILED_READ_BATCH_CHUNK)).toBe(
      true,
    );
  });

  it('runs one statement when the ids fit inside a single chunk', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK);
    const subject = harness(childrenFor(parents.length, 1));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(1);
  });

  it('attaches every child to its own parent across a chunk boundary', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK + 5);
    const subject = harness(childrenFor(parents.length, 3));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(parents).toHaveLength(COMPILED_READ_BATCH_CHUNK + 5);
    for (const [index, parent] of parents.entries()) {
      const owner = index + 1;
      expect(parent.metrics).toEqual([
        { id: (owner - 1) * 3 + 1, ownerId: owner },
        { id: (owner - 1) * 3 + 2, ownerId: owner },
        { id: (owner - 1) * 3 + 3, ownerId: owner },
      ]);
    }
  });

  it('keeps a limit per parent rather than across the chunks it spans', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK + 5);
    const subject = harness(childrenFor(parents.length, 3), { limit: 2 });

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(2);
    const sizes = new Set(parents.map((parent) => (parent.metrics as unknown[]).length));
    expect([...sizes]).toEqual([2]);
    const last = parents[parents.length - 1]!;
    const owner = parents.length;
    expect(last.metrics).toEqual([
      { id: (owner - 1) * 3 + 1, ownerId: owner },
      { id: (owner - 1) * 3 + 2, ownerId: owner },
    ]);
  });

  it('keeps an offset and a reversal per parent across a chunk boundary', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK + 2);
    const subject = harness(childrenFor(parents.length, 4), {
      limit: 2,
      offset: 1,
      reversed: true,
    });

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    for (const [index, parent] of parents.entries()) {
      const owner = index + 1;
      expect(parent.metrics).toEqual([
        { id: (owner - 1) * 4 + 3, ownerId: owner },
        { id: (owner - 1) * 4 + 2, ownerId: owner },
      ]);
    }
  });

  it('answers every parent that holds a shared value, each with the whole relation', async () => {
    const parents: Record<string, unknown>[] = [
      { ownerId: 1 },
      { ownerId: 2 },
      { ownerId: 1 },
      { ownerId: 2 },
      { ownerId: 1 },
    ];
    const subject = harness([
      { id: 1, ownerId: 1 },
      { id: 2, ownerId: 1 },
      { id: 3, ownerId: 2 },
    ]);

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    const shared = [
      { id: 1, ownerId: 1 },
      { id: 2, ownerId: 1 },
    ];
    expect(parents[0]!.metrics).toEqual(shared);
    expect(parents[2]!.metrics).toEqual(shared);
    expect(parents[4]!.metrics).toEqual(shared);
    expect(parents[1]!.metrics).toEqual([{ id: 3, ownerId: 2 }]);
    expect(parents[3]!.metrics).toEqual([{ id: 3, ownerId: 2 }]);
  });

  it('binds a shared parent value once', async () => {
    const parents: Record<string, unknown>[] = [
      { ownerId: 1 },
      { ownerId: 2 },
      { ownerId: 1 },
      { ownerId: 2 },
      { ownerId: 1 },
    ];
    const subject = harness([
      { id: 1, ownerId: 1 },
      { id: 2, ownerId: 1 },
      { id: 3, ownerId: 2 },
    ]);

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(1);
    expect(subject.calls[0]!.parameters).toEqual([1, 2]);
  });

  it('keeps a per-parent page whole for parents that share a value', async () => {
    const parents: Record<string, unknown>[] = [{ ownerId: 1 }, { ownerId: 1 }, { ownerId: 2 }];
    const subject = harness(
      [
        { id: 1, ownerId: 1 },
        { id: 2, ownerId: 1 },
        { id: 3, ownerId: 1 },
        { id: 4, ownerId: 2 },
      ],
      { limit: 2 },
    );

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    const page = [
      { id: 1, ownerId: 1 },
      { id: 2, ownerId: 1 },
    ];
    expect(parents[0]!.metrics).toEqual(page);
    expect(parents[1]!.metrics).toEqual(page);
    expect(parents[2]!.metrics).toEqual([{ id: 4, ownerId: 2 }]);
  });

  it('chunks the values it kept, not the parents it read them from', async () => {
    const parents = Array.from({ length: COMPILED_READ_BATCH_CHUNK * 3 }, (_, index) => ({
      ownerId: (index % 4) + 1,
    }));
    const subject = harness(childrenFor(4, 1));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(1);
    expect(subject.calls[0]!.parameters).toEqual([1, 2, 3, 4]);
  });

  it('leaves out the parents whose value is null or missing', async () => {
    const parents: Record<string, unknown>[] = [{ ownerId: 1 }, { ownerId: null }, { ownerId: undefined }, {}, { ownerId: 2 }];
    const subject = harness(childrenFor(2, 1));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls[0]!.parameters).toEqual([1, 2]);
    expect(parents[1]!.metrics).toEqual([]);
    expect(parents[3]!.metrics).toEqual([]);
  });

  it('runs nothing at all when every parent value is null', async () => {
    const parents: Record<string, unknown>[] = [{ ownerId: null }, { ownerId: null }];
    const subject = harness(childrenFor(2, 1));

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(0);
    expect(parents.map((parent) => parent.metrics)).toEqual([[], []]);
  });

  it('separates values that share a printed form but not a type', async () => {
    const parents: Record<string, unknown>[] = [{ ownerId: 1 }, { ownerId: '1' }, { ownerId: 1n }, { ownerId: true }];
    const subject = harness([]);

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls[0]!.parameters).toEqual([1, '1', 1n, true]);
  });

  it('binds two dates of the same instant once', async () => {
    const parents: Record<string, unknown>[] = [
      { ownerId: new Date('2024-01-01T00:00:00.000Z') },
      { ownerId: new Date('2024-01-01T00:00:00.000Z') },
      { ownerId: new Date('2024-02-02T00:00:00.000Z') },
    ];
    const subject = harness([]);

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls[0]!.parameters).toEqual([
      new Date('2024-01-01T00:00:00.000Z'),
      new Date('2024-02-02T00:00:00.000Z'),
    ]);
  });

  it('carries the parameters the statement holds besides the ids into every chunk', async () => {
    const parents = parentsFor(COMPILED_READ_BATCH_CHUNK + 1);
    const subject = harness(childrenFor(parents.length, 1), {}, ['tenant', 7]);

    await runCompiledBatches(parents, [subject.batch], null, subject.run);

    expect(subject.calls).toHaveLength(2);
    expect(subject.calls[0]!.parameters.slice(0, 2)).toEqual(['tenant', 7]);
    expect(subject.calls[1]!.parameters).toEqual(['tenant', 7, COMPILED_READ_BATCH_CHUNK + 1]);
  });

  it('chunks a nested batch over the merged rows of the batch above it', async () => {
    const owners = parentsFor(COMPILED_READ_BATCH_CHUNK + 4);
    const outer = harness(childrenFor(owners.length, 1));
    const inner = harness(
      childrenFor(owners.length, 1).map((row) => ({ ...row, tag: `t${row.ownerId}` })),
      { name: 'tags' },
    );
    const batch: CompiledReadBatch = { ...outer.batch, batches: [inner.batch] };

    await runCompiledBatches(owners, [batch], null, (sql, parameters) =>
      sql.startsWith('select * from metrics')
        ? outer.run(sql, parameters)
        : inner.run(sql, parameters),
    );

    expect(outer.calls.map((call) => call.parameters.length)).toEqual([
      COMPILED_READ_BATCH_CHUNK,
      4,
    ]);
    expect(inner.calls.map((call) => call.parameters.length)).toEqual([
      COMPILED_READ_BATCH_CHUNK,
      4,
    ]);
    expect((owners[0]!.metrics as Record<string, unknown>[])[0]!.tags).toEqual([
      { id: 1, ownerId: 1, tag: 't1' },
    ]);
  });
});

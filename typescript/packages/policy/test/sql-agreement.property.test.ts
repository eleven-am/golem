import fc from 'fast-check';
import { sqliteDialect } from '../src/index';
import { Agreement, agree, explainAll } from './support/agreement';
import { DEMO_DATAMODEL } from './support/agreement-scope';
import { Row } from './support/measure';
import {
  SQL_LIST_HOPS,
  SQL_NUM_ROWS,
  SQL_NUM_RUNS,
  SQL_RELATION_HOPS,
  SQL_SEED,
  corpusSurface,
  demoInclude,
  hopDepthCounts,
  hopDepthOf,
  parentRowArbitrary,
  quantifierCount,
  quantifierDepthOf,
  relatedRowArbitrary,
  relationDepthCounts,
  relationDepthOf,
  relationOperandsAt,
  sampleCases,
  surfaceOf,
} from './support/sql-arbitraries';
import { SqliteHandle, openSqlite } from './support/sqlite';
import { VM_MODULES_ENABLED } from './support/vm-modules';

jest.setTimeout(600000);

const suite = VM_MODULES_ENABLED ? describe : describe.skip;

let handle: SqliteHandle;
let rows: Row[];
let agreements: Agreement[];

beforeAll(async () => {
  if (!VM_MODULES_ENABLED) {
    return;
  }
  handle = await openSqlite();
  const related = fc.sample(
    fc.tuple(relatedRowArbitrary(1), relatedRowArbitrary(2), relatedRowArbitrary(3)),
    { numRuns: 1, seed: SQL_SEED },
  )[0]!;
  const parents = Array.from({ length: SQL_NUM_ROWS }, (_unused, index) =>
    fc.sample(parentRowArbitrary(index + 1), { numRuns: 1, seed: SQL_SEED + index })[0]!,
  );
  await handle.prisma.related.createMany({ data: related as never });
  await handle.prisma.parent.createMany({ data: parents as never });
  rows = (await handle.prisma.parent.findMany({
    include: demoInclude(SQL_RELATION_HOPS) as never,
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  agreements = await agree(
    {
      datamodel: DEMO_DATAMODEL,
      model: 'Parent',
      dialect: sqliteDialect,
      db: handle.prisma as never,
      rows,
    },
    sampleCases(SQL_NUM_RUNS, SQL_SEED),
  );
});

afterAll(async () => {
  if (VM_MODULES_ENABLED) {
    await handle.close();
  }
});

suite('golem JS and golem SQL agree on generated conditions', () => {
  it('seeds a reproducible row set', () => {
    expect(rows).toHaveLength(SQL_NUM_ROWS);
    expect(rows.some((row) => row.related === null)).toBe(true);
    expect(rows.some((row) => row.related !== null)).toBe(true);
    expect(rows.some((row) => row.nameNull === null)).toBe(true);
    expect(rows.some((row) => row.bigNull === null)).toBe(true);
  });

  it('runs every generated condition against both backends', () => {
    expect(agreements).toHaveLength(SQL_NUM_RUNS);
    expect(agreements.filter((entry) => entry.sql.startsWith('error:'))).toEqual([]);
  });

  it('generates every supported operator, every combinator and a relation hop', () => {
    const surface = corpusSurface(sampleCases(SQL_NUM_RUNS, SQL_SEED));
    for (const name of [
      'equals', 'not', 'in', 'notIn', 'lt', 'lte', 'gt', 'gte', 'is', 'isNot',
      'some', 'every', 'none',
      'AND', 'OR', 'NOT', 'related', 'next', 'previous', 'empty-branch', 'empty-object',
    ]) {
      expect([name, surface.has(name)]).toEqual([name, true]);
    }
  });

  it('generates isNot bodies, not only is bodies, at the root hop and below it', () => {
    const cases = sampleCases(SQL_NUM_RUNS, SQL_SEED);
    const negated = cases.filter((entry) => surfaceOf(entry.where).has('isNot'));
    expect(negated.length).toBeGreaterThan(SQL_NUM_RUNS / 20);
    const rooted = negated.filter((entry) => relationOperandsAt(entry.where, 'related').has('isNot'));
    const nested = negated.filter((entry) => relationOperandsAt(entry.where, 'next').has('isNot'));
    expect(rooted.length).toBeGreaterThan(0);
    expect(nested.length).toBeGreaterThan(0);
  });

  it('runs the generated isNot bodies against rows that answer them', () => {
    const negated = sampleCases(SQL_NUM_RUNS, SQL_SEED).filter((entry) =>
      surfaceOf(entry.where).has('isNot'),
    );
    const answered = negated.filter((entry) => {
      const found = agreements.find((agreement) => agreement.id === entry.id);
      return found !== undefined && found.js !== '<none>' && found.js.split(',').length < rows.length;
    });
    expect(answered.length).toBeGreaterThan(0);
  });

  it('generates to-many hops, not only to-one ones, and stacks them', () => {
    const cases = sampleCases(SQL_NUM_RUNS, SQL_SEED);
    const quantified = cases.filter((entry) => quantifierCount(entry.where) > 0);
    expect(quantified.length).toBeGreaterThan(SQL_NUM_RUNS / 10);
    expect(Math.max(...cases.map((entry) => quantifierDepthOf(entry.where)))).toBe(SQL_LIST_HOPS);
    expect(
      quantified.filter((entry) => quantifierDepthOf(entry.where) === SQL_LIST_HOPS).length,
    ).toBeGreaterThan(0);
  });

  it('chains to-one and to-many hops together up to the generator bound, and never past it', () => {
    const cases = sampleCases(SQL_NUM_RUNS, SQL_SEED);
    const counts = relationDepthCounts(DEMO_DATAMODEL, 'Parent', cases);
    for (let hops = 1; hops <= SQL_RELATION_HOPS; hops += 1) {
      expect([hops, counts[hops] ?? 0]).not.toEqual([hops, 0]);
    }
    expect(Math.max(...Object.keys(counts).map(Number))).toBe(SQL_RELATION_HOPS);
    const mixed = cases.filter(
      (entry) =>
        quantifierCount(entry.where) > 0 &&
        relationDepthOf(DEMO_DATAMODEL, 'Parent', entry.where) >= 3,
    );
    expect(mixed.length).toBeGreaterThan(0);
  });

  it('runs the generated to-many hops against rows that answer them', () => {
    const quantified = sampleCases(SQL_NUM_RUNS, SQL_SEED).filter(
      (entry) => quantifierCount(entry.where) > 0,
    );
    const answered = quantified.filter((entry) => {
      const found = agreements.find((agreement) => agreement.id === entry.id);
      return found !== undefined && found.js !== '<none>' && found.js.split(',').length < rows.length;
    });
    expect(answered.length).toBeGreaterThan(0);
  });

  it('chains relation hops up to the generator bound, and never past it', () => {
    const counts = hopDepthCounts(sampleCases(SQL_NUM_RUNS, SQL_SEED));
    const reached = Object.keys(counts).map(Number);
    for (let hops = 1; hops <= SQL_RELATION_HOPS; hops += 1) {
      expect([hops, counts[hops] ?? 0]).not.toEqual([hops, 0]);
    }
    expect(Math.max(...reached)).toBe(SQL_RELATION_HOPS);
  });

  it('runs the chained conditions against rows that reach the end of the chain', () => {
    const chained = sampleCases(SQL_NUM_RUNS, SQL_SEED).filter(
      (entry) => hopDepthOf(entry.where) >= 2,
    );
    const answered = chained.filter((entry) => {
      const found = agreements.find((agreement) => agreement.id === entry.id);
      return found !== undefined && found.js !== '<none>';
    });
    expect(chained.length).toBeGreaterThan(SQL_NUM_RUNS / 20);
    expect(answered.length).toBeGreaterThan(0);
  });

  it('exercises conditions that select some rows but not all of them', () => {
    const partial = agreements.filter(
      (entry) => entry.js !== '<none>' && entry.js.split(',').length < rows.length,
    );
    expect(partial.length).toBeGreaterThan(SQL_NUM_RUNS / 4);
  });

  it('agrees on every generated condition', () => {
    expect(explainAll(agreements)).toBe('');
  });

  it('agrees on the negation of every generated condition', async () => {
    const negated = await agree(
      {
        datamodel: DEMO_DATAMODEL,
        model: 'Parent',
        dialect: sqliteDialect,
        db: handle.prisma as never,
        rows,
      },
      sampleCases(SQL_NUM_RUNS, SQL_SEED).map((entry) => ({
        id: `NOT(${entry.id})`,
        where: { NOT: entry.where },
      })),
    );
    expect(explainAll(negated)).toBe('');
  });

  it('reports the seed needed to reproduce this run', () => {
    expect(Number.isFinite(SQL_SEED)).toBe(true);
    expect(SQL_NUM_RUNS).toBeGreaterThan(0);
  });
});

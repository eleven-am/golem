import { UnsupportedConditionError, evaluateConditions, sqliteDialect } from '../src/index';
import {
  Agreement,
  agree,
  answerRecord,
  disagreementRecord,
  discriminating,
  explainAll,
  render,
} from './support/agreement';
import { DEMO_DATAMODEL, MAPPED_DATAMODEL } from './support/agreement-scope';
import {
  MAPPED_BARE_CASES,
  MAPPED_CASES,
  MAPPED_INCLUDE,
  OWNERS,
  REGIONS,
  SQLITE_MAPPED_DDL,
  TAGGED,
} from './support/mapped';
import { ALL_CASES, BARE_CASES } from './support/matrix';
import { Row } from './support/measure';
import { PARENTS, RELATED } from './support/rows';
import {
  AGREEMENT_STATEMENTS,
  DEMO_AGREEMENT_CASES,
  MAPPED_AGREEMENT_CASES,
  MAPPED_ANSWERS,
  SQLITE_DEMO_DISAGREEMENTS,
  SQLITE_MAPPED_DISAGREEMENTS,
} from './support/sql-agreement-record';
import { hopDepthOf } from './support/sql-arbitraries';
import { SqliteHandle, openSqlite } from './support/sqlite';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(300000);

let handle: SqliteHandle;
let demoRows: Row[];
let mappedRows: Row[];
let demo: Agreement[];
let mapped: Agreement[];

const suite = VM_MODULES_ENABLED ? describe : describe.skip;

beforeAll(async () => {
  if (!VM_MODULES_ENABLED) {
    return;
  }
  handle = await openSqlite();
  for (const statement of SQLITE_MAPPED_DDL) {
    await handle.prisma.$executeRawUnsafe(statement);
  }
  await handle.prisma.related.createMany({ data: RELATED as never });
  await handle.prisma.parent.createMany({ data: PARENTS as never });
  await handle.prisma.region.createMany({ data: REGIONS as never });
  await handle.prisma.owner.createMany({ data: OWNERS as never });
  for (const row of TAGGED) {
    await handle.prisma.tagged.create({ data: row as never });
  }
  demoRows = (await handle.prisma.parent.findMany({
    include: { related: true },
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  mappedRows = (await handle.prisma.tagged.findMany({
    include: MAPPED_INCLUDE as never,
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  demo = await agree(
    {
      datamodel: DEMO_DATAMODEL,
      model: 'Parent',
      dialect: sqliteDialect,
      db: handle.prisma as never,
      rows: demoRows,
    },
    ALL_CASES,
  );
  mapped = await agree(
    {
      datamodel: MAPPED_DATAMODEL,
      model: 'Tagged',
      dialect: sqliteDialect,
      db: handle.prisma as never,
      rows: mappedRows,
    },
    MAPPED_CASES,
  );
});

afterAll(async () => {
  if (VM_MODULES_ENABLED) {
    await handle.close();
  }
});

describe('the agreement oracle', () => {
  it('is checked against a live engine', () => {
    if (!VM_MODULES_ENABLED) {
      throw new Error(VM_MODULES_HINT);
    }
    expect(DEMO_AGREEMENT_CASES).toBe(BARE_CASES.length * 2);
  });
});

suite('golem JS versus golem SQL on SQLite', () => {
  it('covers both polarities of every case in the shared matrix', () => {
    expect(demo.length).toBe(DEMO_AGREEMENT_CASES);
    expect(mapped.length).toBe(MAPPED_AGREEMENT_CASES);
  });

  it('reads the same rows into both backends', () => {
    expect(demoRows.map((row) => row.id)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(mappedRows.map((row) => row.id)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(demoRows.map((row) => row.big)).toContain(9007199254740993n);
    expect(mappedRows.map((row) => row.big)).toContain(9007199254740993n);
    expect(mappedRows.map((row) => row.big)).toContain(-9007199254740993n);
  });

  it('agrees on every case in the shared matrix', () => {
    expect(explainAll(demo)).toBe('');
    expect(disagreementRecord(demo)).toEqual(SQLITE_DEMO_DISAGREEMENTS);
  });

  it('runs relation chains five hops deep, not just the two hops a pinned statement covers', () => {
    const depths = MAPPED_BARE_CASES.map((entry) => hopDepthOf(entry.where));
    expect(Math.max(...depths)).toBe(5);
    expect(depths.filter((depth) => depth >= 3).length).toBeGreaterThanOrEqual(10);
    const deep = MAPPED_BARE_CASES.filter((entry) => hopDepthOf(entry.where) >= 3).map(
      (entry) => entry.id,
    );
    const answered = deep.filter((id) => MAPPED_ANSWERS[id] !== '<none>');
    expect(answered.length).toBeGreaterThanOrEqual(deep.length - 1);
  });

  it('agrees on every case against mapped table and column names', () => {
    expect(explainAll(mapped)).toBe('');
    expect(disagreementRecord(mapped)).toEqual(SQLITE_MAPPED_DISAGREEMENTS);
  });

  it('pins the answer for every mapped case, so agreement cannot drift in step', () => {
    expect(answerRecord(mapped)).toEqual(MAPPED_ANSWERS);
  });

  it('selects a strict subset of the rows in most cases, so agreement is not vacuous', () => {
    expect(discriminating(demo, demoRows.length)).toBeGreaterThan(DEMO_AGREEMENT_CASES / 2);
    expect(discriminating(mapped, mappedRows.length)).toBeGreaterThan(MAPPED_AGREEMENT_CASES / 2);
  });

  it('never renders a condition it cannot execute', () => {
    const failures = demo
      .concat(mapped)
      .filter((entry) => entry.sql.startsWith('error:'));
    expect(failures.map((entry) => `${entry.id} ${entry.sql}`)).toEqual([]);
  });
});

suite('the oracle discriminates', () => {
  it('holds rows the join shapes drop, on the null foreign key case', async () => {
    const where = { OR: [{ owner: { is: { kind: 'red' } } }, { amount: 100 }] };
    const golem = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, where);
    const golemIds = (await handle.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const joined = (await handle.prisma.$queryRawUnsafe(
      `SELECT "t0"."row_pk" AS "id" FROM "tagged_rows" AS "t0" ` +
        `LEFT JOIN "owner_rows" AS "j0" ON "j0"."owner_pk" = "t0"."owner_ref" ` +
        `WHERE (("j0"."kind_text" IS ?) OR ("t0"."amount_value" IS ?))`,
      'red',
      100,
    )) as { id: number }[];
    expect(golemIds.map((entry) => entry.id).sort()).toEqual([1, 4, 6, 8]);
    expect(joined.map((entry) => entry.id).sort()).toEqual([1, 4, 6, 8]);
    const guarded = (await handle.prisma.$queryRawUnsafe(
      `SELECT "t0"."row_pk" AS "id" FROM "tagged_rows" AS "t0" ` +
        `INNER JOIN "owner_rows" AS "j0" ON "j0"."owner_pk" = "t0"."owner_ref" ` +
        `WHERE (("j0"."kind_text" IS ?) OR ("t0"."amount_value" IS ?))`,
      'red',
      100,
    )) as { id: number }[];
    expect(guarded.map((entry) => entry.id).sort()).toEqual([1, 6]);
  });

  it('reports a disagreement when the two backends are given different questions', async () => {
    const rendered = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, { amount: { gt: 0 } });
    const ids = (await handle.prisma.$queryRawUnsafe(
      rendered.statement,
      ...(rendered.parameters as unknown[]),
    )) as { id: number }[];
    const skewed = mappedRows.filter((row) =>
      evaluateConditions({ amount: { gt: 7 } }, row),
    );
    expect(ids.map((entry) => entry.id)).not.toEqual(skewed.map((row) => row.id));
    expect(
      disagreementRecord([
        { id: 'skew', sql: '3,6', js: '3', statement: '', parameters: [] },
      ]),
    ).toEqual({ skew: 'sql=3,6 js=3' });
  });
});

suite('the SQL golem itself emits', () => {
  it('targets the mapped table and the mapped column, never the logical name', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, { label: 'alpha' });
    expect(statement).toContain('FROM "tagged_rows" AS "t0"');
    expect(statement).toContain('"t0"."label_text"');
    expect(statement).toContain('SELECT "t0"."row_pk" AS "id"');
    expect(statement).not.toContain('"Tagged"');
    expect(statement).not.toContain('"label"');
  });

  it('resolves a mapped relation through both mapped key columns', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, {
      owner: { is: { kind: 'red' } },
    });
    expect(statement).toContain('EXISTS (SELECT 1 FROM "owner_rows" AS "t0_1"');
    expect(statement).toContain('"t0_1"."owner_pk" = "t0"."owner_ref"');
    expect(statement).toContain('"t0_1"."kind_text"');
  });

  it('gives a self relation an alias distinct from the root', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, {
      parent: { is: { label: 'alpha' } },
    });
    expect(statement).toContain('FROM "tagged_rows" AS "t0"');
    expect(statement).toContain('EXISTS (SELECT 1 FROM "tagged_rows" AS "t0_1"');
    expect(statement).toContain('"t0_1"."row_pk" = "t0"."parent_ref"');
  });

  it('collates both sides of a string foreign key, and neither side of an integer one', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, {
      owner: { is: { region: { is: { name: 'northern' } } } },
    });
    expect(statement).toContain('"t0_1"."owner_pk" = "t0"."owner_ref"');
    expect(statement).toContain(
      '"t0_2"."region_code" COLLATE BINARY = "t0_1"."region_ref" COLLATE BINARY',
    );
  });

  it('gives each hop in a chain an alias of its own, correlated to the hop above it', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, {
      parent: { is: { parent: { is: { label: 'alpha' } } } },
    });
    expect(statement).toContain('FROM "tagged_rows" AS "t0"');
    expect(statement).toContain('AS "t0_1"');
    expect(statement).toContain('AS "t0_2"');
    expect(statement).toContain('"t0_1"."row_pk" = "t0"."parent_ref"');
    expect(statement).toContain('"t0_2"."row_pk" = "t0_1"."parent_ref"');
  });

  it('opens a separate subquery for each sibling hop, so a shared alias cannot collide', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, {
      AND: [{ parent: { is: { label: 'alpha' } } }, { parent: { is: { amount: { gt: -1 } } } }],
    });
    expect(statement.split('EXISTS (SELECT 1 FROM "tagged_rows" AS "t0_1"')).toHaveLength(3);
    expect(statement).not.toContain('AS "t0_2"');
  });

  it('renders a to-one relation as a correlated EXISTS, never as a join', () => {
    const { statement } = render(DEMO_DATAMODEL, 'Parent', sqliteDialect, {
      OR: [{ related: { is: { tag: 'red' } } }, { count: 100 }],
    });
    expect(statement).toContain('EXISTS (SELECT 1 FROM "Related"');
    expect(statement).not.toContain('JOIN');
  });

  it('folds an empty disjunction at depth to a false predicate', () => {
    expect(render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, { AND: [{ OR: [] }] }).statement).toContain(
      'WHERE ((1 = 0))',
    );
    expect(
      render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, { NOT: { AND: [{ OR: [] }] } }).statement,
    ).toContain('WHERE NOT (((1 = 0)))');
  });

  it('forces byte-order collation on every string comparison and on nothing else', () => {
    expect(render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { name: { lt: 'Zulu' } }).statement).toContain(
      '"t0"."name" COLLATE BINARY < ?',
    );
    expect(render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { count: { lt: 3 } }).statement).not.toContain(
      'COLLATE',
    );
    expect(render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { at: { lt: new Date(0) } }).statement).not.toContain(
      'COLLATE',
    );
  });

  it('casts an enum to text before forcing byte-order collation on it', () => {
    const { statement } = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, { state: 'OPEN' });
    expect(statement).toContain('CAST("t0"."state_value" AS TEXT) COLLATE BINARY');
  });

  it('refuses loudly, rather than answering, for every shape it cannot render faithfully', () => {
    const refused: Record<string, string> = {};
    for (const [id, where] of Object.entries(REFUSED)) {
      try {
        render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, where);
        refused[id] = 'rendered';
      } catch (error) {
        refused[id] = (error as Error).name;
      }
    }
    expect(refused).toEqual({
      'field-absent-from-datamodel': 'UnsupportedConditionError',
      'relation-compared-as-scalar': 'UnsupportedConditionError',
      'scalar-scoped-as-relation': 'UnsupportedConditionError',
      'to-many-relation-under-is': 'UnsupportedConditionError',
    });
  });

  it('answers false in the JS evaluator for the shapes SQL refuses, so neither backend guesses', () => {
    for (const where of Object.values(REFUSED)) {
      expect(mappedRows.filter((row) => evaluateConditions(where, row))).toEqual([]);
    }
  });

  it('refuses a text operator on a non-text column, which SQLite would otherwise answer by coercion', async () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      expect(() =>
        render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { count: { [operator]: '0' } }),
      ).toThrow(UnsupportedConditionError);
      expect(() =>
        render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { count: { [operator]: '0' } }),
      ).toThrow('"Parent.count" is a Int column');
      expect(() =>
        render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { at: { [operator]: '2020' } }),
      ).toThrow('"Parent.at" is a DateTime column');
      expect(() =>
        render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { big: { [operator]: '9' } }),
      ).toThrow('"Parent.big" is a BigInt column');
      expect(() =>
        render(DEMO_DATAMODEL, 'Parent', sqliteDialect, { related: { is: { num: { [operator]: '1' } } } }),
      ).toThrow('"Related.num" is a Int column');
    }
  });

  it('measures the coercion the refusal exists to prevent, so the guard is not a formality', async () => {
    const coerced = (await handle.prisma.$queryRawUnsafe(
      `SELECT "t0"."id" AS "id" FROM "Parent" AS "t0" ` +
        `WHERE "t0"."count" IS NOT NULL AND instr("t0"."count", ?) > 0`,
      '0',
    )) as { id: number }[];
    expect(coerced.map((entry) => Number(entry.id)).sort((left, right) => left - right)).toEqual([
      1, 4, 5, 8,
    ]);
    expect(demoRows.filter((row) => evaluateConditions({ count: { contains: '0' } }, row))).toEqual([]);
  });

  it('binds every operand as a parameter, never as inlined text', () => {
    const rendered = render(DEMO_DATAMODEL, 'Parent', sqliteDialect, {
      name: "x'; DROP TABLE Parent; --",
    });
    expect(rendered.statement).not.toContain('DROP');
    expect(rendered.parameters).toEqual(["x'; DROP TABLE Parent; --"]);
  });

  it('pins the rendered statement for the cases most likely to regress', () => {
    const pinned: Record<string, string> = {};
    for (const [id, where] of Object.entries(PINNED)) {
      pinned[id] = render(MAPPED_DATAMODEL, 'Tagged', sqliteDialect, where).statement;
    }
    expect(pinned).toEqual(AGREEMENT_STATEMENTS);
  });
});

const REFUSED: Readonly<Record<string, Record<string, unknown>>> = {
  'field-absent-from-datamodel': { nope: 1 },
  'relation-compared-as-scalar': { owner: 'red' },
  'scalar-scoped-as-relation': { label: { is: { x: 1 } } },
  'to-many-relation-under-is': { children: { is: { label: 'alpha' } } },
};

const PINNED: Readonly<Record<string, unknown>> = {
  'mapped/scalar': { label: 'alpha' },
  'mapped/relation': { owner: { is: { kind: 'red' } } },
  'mapped/self-relation': { parent: { is: { label: 'alpha' } } },
  'mapped/null-fk-or': { OR: [{ owner: { is: { kind: 'red' } } }, { amount: 100 }] },
  'mapped/nested-is-under-not': { NOT: { owner: { is: { NOT: { kind: 'red' } } } } },
  'mapped/empty-or-at-depth': { AND: [{ OR: [] }] },
  'mapped/sibling-hops': {
    AND: [{ parent: { is: { label: 'alpha' } } }, { parent: { is: { amount: { gt: -1 } } } }],
  },
  'mapped/two-hop': { parent: { is: { parent: { is: { label: 'alpha' } } } } },
};

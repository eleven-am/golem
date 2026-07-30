import { ALL_CASES, BARE_CASES, isIdentityBranch, mentionsNullableColumn } from './support/matrix';
import { Measurement, Row, classCounts, divergenceRecord, measure } from './support/measure';
import { PARENTS, RELATED } from './support/rows';
import { BYTE_ORDER_DIVERGENCES, SQL_DIVERGENCE_CLASSES } from './support/sql-record';
import { SqliteHandle, openSqlite } from './support/sqlite';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(120000);

let handle: SqliteHandle;
let rows: Row[];
let measurements: Measurement[];

const suite = VM_MODULES_ENABLED ? describe : describe.skip;

beforeAll(async () => {
  if (!VM_MODULES_ENABLED) {
    return;
  }
  handle = await openSqlite();
  await handle.prisma.related.createMany({ data: RELATED as never });
  await handle.prisma.parent.createMany({ data: PARENTS as never });
  rows = (await handle.prisma.parent.findMany({
    include: { related: true },
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  measurements = await measure(handle.prisma.parent as never, rows, ALL_CASES);
});

afterAll(async () => {
  if (VM_MODULES_ENABLED) {
    await handle.close();
  }
});

async function sqlFor(where: unknown): Promise<string> {
  (handle.sql as string[]).length = 0;
  await handle.prisma.parent.findMany({ where: where as never, select: { id: true } });
  return handle.sql.filter((statement) => statement.startsWith('SELECT')).join('\n');
}

describe('the recorded divergence set', () => {
  it('is checked against a live engine', () => {
    if (!VM_MODULES_ENABLED) {
      throw new Error(VM_MODULES_HINT);
    }
    expect(Object.keys(BYTE_ORDER_DIVERGENCES)).toHaveLength(145);
  });
});

suite('golem evaluator versus Prisma-emitted SQLite', () => {
  it('covers both polarities of every case', () => {
    expect(BARE_CASES.length).toBe(290);
    expect(ALL_CASES.length).toBe(BARE_CASES.length * 2);
    expect(measurements.length).toBe(ALL_CASES.length);
  });

  it('round-trips BigInt past the safe-integer boundary', () => {
    expect(rows.map((row) => row.big)).toContain(9007199254740993n);
    expect(rows.map((row) => row.big)).toContain(-9007199254740993n);
    expect(rows.find((row) => row.id === 3)!.bigNull).toBe(9007199254740992n);
  });

  it('matches the recorded divergence set exactly', () => {
    expect(divergenceRecord(measurements)).toEqual(BYTE_ORDER_DIVERGENCES);
  });

  it('matches the recorded divergence classes', () => {
    expect(classCounts(measurements)).toEqual(SQL_DIVERGENCE_CLASSES);
  });

  it('never disagrees by producing a row the other engine excludes both ways', () => {
    const disjoint = measurements.filter(
      (entry) => entry.prisma !== entry.golem && classCounts([entry]).disjoint === 1,
    );
    expect(disjoint).toEqual([]);
  });

  it('agrees on every case whose where clause never touches a nullable column', () => {
    const diverging = new Set(Object.keys(divergenceRecord(measurements)));
    const unexplained = ALL_CASES.filter((entry) => diverging.has(entry.id)).filter(
      (entry) =>
        !mentionsNullableColumn(entry.where) &&
        !isIdentityBranch((entry.where as { NOT?: unknown }).NOT),
    );
    expect(unexplained.map((entry) => entry.id)).toEqual([]);
  });
});

suite('the SQL Prisma actually emits', () => {
  it('emits a bare <> for not, with no IS NULL guard', async () => {
    const sql = await sqlFor({ nameNull: { not: 'alpha' } });
    expect(sql).toContain('`nameNull` <> ?');
    expect(sql).not.toContain('IS NULL');
  });

  it('emits a bare NOT IN for notIn, with no IS NULL guard', async () => {
    const sql = await sqlFor({ nameNull: { notIn: ['alpha', ''] } });
    expect(sql).toContain('`nameNull` NOT IN (?,?)');
    expect(sql).not.toContain('IS NULL');
  });

  it('emits a bare comparison for lt, with no IS NOT NULL guard', async () => {
    const sql = await sqlFor({ countNull: { lt: 5 } });
    expect(sql).toContain('`countNull` < ?');
    expect(sql).not.toContain('IS NOT NULL');
  });

  it('wraps NOT around the bare comparison, leaving NULL rows unknown in both polarities', async () => {
    const sql = await sqlFor({ NOT: { countNull: { lt: 5 } } });
    expect(sql).toContain('(NOT `main`.`Parent`.`countNull` < ?)');
  });

  it('folds an empty in list to 1=0 and an empty notIn list to 1=1', async () => {
    expect(await sqlFor({ countNull: { in: [] } })).toContain('WHERE 1=0');
    expect(await sqlFor({ countNull: { notIn: [] } })).toContain('WHERE 1=1');
  });

  it('drops NOT of an identity condition entirely', async () => {
    expect(await sqlFor({ NOT: { AND: [] } })).toContain('WHERE 1=1');
    expect(await sqlFor({ NOT: {} })).toContain('WHERE 1=1');
    expect(await sqlFor({ NOT: [] })).toContain('WHERE 1=1');
  });

  it('expands NOT over an array into a conjunction of negated branches', async () => {
    const sql = await sqlFor({ NOT: [{ count: 0 }, { count: 7 }] });
    expect(sql).toContain('((NOT `main`.`Parent`.`count` = ?) AND (NOT `main`.`Parent`.`count` = ?))');
  });

  it('renders a to-one is as a LEFT JOIN guarded by the join key', async () => {
    const sql = await sqlFor({ related: { is: { tag: 'red' } } });
    expect(sql).toContain('LEFT JOIN `main`.`Related` AS `j0`');
    expect(sql).toContain('(`j0`.`tag` = ? AND (`j0`.`id` IS NOT NULL))');
  });

  it('negates the whole join guard, so a null relation satisfies NOT is', async () => {
    const sql = await sqlFor({ NOT: { related: { is: { tag: 'red' } } } });
    expect(sql).toContain('(NOT (`j0`.`tag` = ? AND (`j0`.`id` IS NOT NULL)))');
  });
});

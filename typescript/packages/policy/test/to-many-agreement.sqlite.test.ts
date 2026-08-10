import { evaluateConditions, sqliteDialect } from '../src/index';
import {
  Agreement,
  agree,
  answerRecord,
  disagreementRecord,
  discriminating,
  explainAll,
  render,
} from './support/agreement';
import {
  GROUP_CASES,
  LIBRARY_BARE_CASES,
  LIBRARY_CASES,
  LIBRARY_DATAMODEL,
  LIBRARY_DDL,
  NAIVE_EVERY_CASE,
  PRODUCTION_RULES,
  libraryGroupRows,
  libraryInserts,
  libraryMediaRows,
} from './support/library';
import { Row } from './support/measure';
import {
  GROUP_AGREEMENT_CASES,
  GROUP_ANSWERS,
  LIBRARY_AGREEMENT_CASES,
  LIBRARY_ANSWERS,
  LIBRARY_STATEMENTS,
  SQLITE_LIBRARY_DISAGREEMENTS,
} from './support/sql-agreement-record';
import { quantifierCount, relationDepthOf } from './support/sql-arbitraries';
import { SqliteHandle, openSqlite } from './support/sqlite';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(300000);

let handle: SqliteHandle;
let mediaRows: Row[];
let groupRows: Row[];
let media: Agreement[];
let groups: Agreement[];

const suite = VM_MODULES_ENABLED ? describe : describe.skip;

const NAIVE_EVERY =
  `SELECT "t0"."group_pk" AS "id" FROM "group_rows" AS "t0" ` +
  `WHERE NOT EXISTS (SELECT 1 FROM "policy_rows" AS "t0_1" ` +
  `WHERE "t0_1"."group_ref" = "t0"."group_pk" AND NOT ("t0_1"."access_value" = ?))`;

const NULL_SAFE_EVERY =
  `SELECT "t0"."group_pk" AS "id" FROM "group_rows" AS "t0" ` +
  `WHERE NOT EXISTS (SELECT 1 FROM "policy_rows" AS "t0_1" ` +
  `WHERE "t0_1"."group_ref" = "t0"."group_pk" AND ("t0_1"."access_value" = ?) IS NOT TRUE)`;

async function ids(statement: string, parameters: readonly unknown[]): Promise<string> {
  const found = (await handle.prisma.$queryRawUnsafe(statement, ...parameters)) as {
    id: number | bigint;
  }[];
  return found.length === 0
    ? '<none>'
    : found
        .map((entry) => Number(entry.id))
        .sort((left, right) => left - right)
        .join(',');
}

beforeAll(async () => {
  if (!VM_MODULES_ENABLED) {
    return;
  }
  handle = await openSqlite();
  for (const statement of LIBRARY_DDL) {
    await handle.prisma.$executeRawUnsafe(statement);
  }
  for (const entry of libraryInserts(sqliteDialect)) {
    await handle.prisma.$executeRawUnsafe(entry.sql, ...(entry.parameters as unknown[]));
  }
  mediaRows = libraryMediaRows();
  groupRows = libraryGroupRows();
  media = await agree(
    {
      datamodel: LIBRARY_DATAMODEL,
      model: 'Media',
      dialect: sqliteDialect,
      db: handle.prisma as never,
      rows: mediaRows,
    },
    LIBRARY_CASES,
  );
  groups = await agree(
    {
      datamodel: LIBRARY_DATAMODEL,
      model: 'MediaGroup',
      dialect: sqliteDialect,
      db: handle.prisma as never,
      rows: groupRows,
    },
    GROUP_CASES,
  );
});

afterAll(async () => {
  if (VM_MODULES_ENABLED) {
    await handle.close();
  }
});

describe('the to-many agreement oracle', () => {
  it('is checked against a live engine', () => {
    if (!VM_MODULES_ENABLED) {
      throw new Error(VM_MODULES_HINT);
    }
    expect(LIBRARY_AGREEMENT_CASES).toBe(LIBRARY_BARE_CASES.length * 2);
  });
});

suite('golem JS versus golem SQL on the production-shaped schema', () => {
  it('covers both polarities of every case', () => {
    expect(media).toHaveLength(LIBRARY_AGREEMENT_CASES);
    expect(groups).toHaveLength(GROUP_AGREEMENT_CASES);
  });

  it('reads the same rows into both backends', async () => {
    const counts = await handle.prisma.$queryRawUnsafe<{ table: string; total: number }[]>(
      `SELECT 'media' AS "table", count(*) AS total FROM "media_rows"
       UNION ALL SELECT 'group', count(*) FROM "group_rows"
       UNION ALL SELECT 'policy', count(*) FROM "policy_rows"
       UNION ALL SELECT 'usergroup', count(*) FROM "usergroup_rows"
       UNION ALL SELECT 'membership', count(*) FROM "membership_rows"
       UNION ALL SELECT 'video', count(*) FROM "video_rows"
       UNION ALL SELECT 'storage', count(*) FROM "storage_rows"`,
    );
    expect(counts.map((entry) => `${entry.table}=${Number(entry.total)}`)).toEqual([
      'media=7',
      'group=6',
      'policy=7',
      'usergroup=4',
      'membership=4',
      'video=6',
      'storage=2',
    ]);
    expect(mediaRows.map((row) => row.id)).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(groupRows.map((row) => row.id)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  it('expands the object graph deeper than the deepest case reaches', () => {
    const deeper = libraryMediaRows(16);
    for (const entry of LIBRARY_CASES) {
      expect([entry.id, deeper.filter((row) => evaluateConditions(entry.where, row)).map((row) => row.id)]).toEqual([
        entry.id,
        mediaRows.filter((row) => evaluateConditions(entry.where, row)).map((row) => row.id),
      ]);
    }
  });

  it('agrees on every case', () => {
    expect(explainAll(media)).toBe('');
    expect(explainAll(groups)).toBe('');
    expect(disagreementRecord(media)).toEqual(SQLITE_LIBRARY_DISAGREEMENTS);
  });

  it('pins the answer for every case, so agreement cannot drift in step', () => {
    expect(answerRecord(media)).toEqual(LIBRARY_ANSWERS);
    expect(answerRecord(groups)).toEqual(GROUP_ANSWERS);
  });

  it('selects a strict subset of the rows in most cases, so agreement is not vacuous', () => {
    expect(discriminating(media, mediaRows.length)).toBeGreaterThan(LIBRARY_AGREEMENT_CASES / 2);
  });

  it('never renders a condition it cannot execute', () => {
    const failures = media.concat(groups).filter((entry) => entry.sql.startsWith('error:'));
    expect(failures.map((entry) => `${entry.id} ${entry.sql}`)).toEqual([]);
  });

  it('runs the three production rules and answers them the same way in both backends', () => {
    const answers: Record<string, string> = {};
    for (const id of Object.keys(PRODUCTION_RULES)) {
      const entry = media.find((candidate) => candidate.id === id)!;
      expect([id, entry.sql]).toEqual([id, entry.js]);
      answers[id] = entry.sql;
    }
    expect(answers).toEqual({
      'production/readable-by-u1': '1,4',
      'production/readable-by-u2': '1',
      'production/not-denied-to-u1': '1,3,4,5,6,7',
      'production/not-denied-to-u2': '3,4,5,6,7',
      'production/owns-every-video-u1': '2,4,5,6,7',
      'production/owns-every-video-u2': '4,7',
    });
  });

  it('reaches six alternating hops, and runs them against rows that answer', () => {
    const depth = (entry: { where: unknown }): number =>
      relationDepthOf(LIBRARY_DATAMODEL, 'Media', entry.where);
    expect(Math.max(...LIBRARY_BARE_CASES.map(depth))).toBe(6);
    expect(LIBRARY_BARE_CASES.filter((entry) => depth(entry) >= 4).length).toBeGreaterThanOrEqual(10);
    const deep = LIBRARY_BARE_CASES.filter((entry) => depth(entry) >= 4);
    const answered = deep.filter((entry) => LIBRARY_ANSWERS[entry.id] !== '<none>');
    expect(answered.length).toBeGreaterThanOrEqual(deep.length - 1);
  });

  it('quantifies at every level of the deepest chain, not only at the top', () => {
    const six = LIBRARY_BARE_CASES.find((entry) => entry.id === 'depth/six-alternating')!;
    expect(relationDepthOf(LIBRARY_DATAMODEL, 'Media', six.where)).toBe(6);
    expect(quantifierCount(six.where)).toBe(4);
    expect(LIBRARY_BARE_CASES.filter((entry) => quantifierCount(entry.where) >= 3).length)
      .toBeGreaterThanOrEqual(8);
  });

  it('pins the rendered statement for the shapes most likely to regress', () => {
    const pinned: Record<string, string> = {};
    for (const id of Object.keys(LIBRARY_STATEMENTS)) {
      const where = LIBRARY_BARE_CASES.find((entry) => entry.id === id)!.where;
      pinned[id] = render(LIBRARY_DATAMODEL, 'Media', sqliteDialect, where).statement;
    }
    expect(pinned).toEqual(LIBRARY_STATEMENTS);
  });
});

suite('the null-safe negation inside every', () => {
  it('answers what the JS evaluator answers', async () => {
    const golem = render(LIBRARY_DATAMODEL, 'MediaGroup', sqliteDialect, NAIVE_EVERY_CASE);
    expect(golem.statement).toContain('IS NOT TRUE');
    expect(await ids(golem.statement, golem.parameters as unknown[])).toBe('4,6');
    expect(
      groupRows
        .filter((row) => evaluateConditions(NAIVE_EVERY_CASE, row))
        .map((row) => row.id)
        .join(','),
    ).toBe('4,6');
  });

  it('is the rendering the naive double negation gets wrong', async () => {
    expect(await ids(NAIVE_EVERY, ['ALLOW'])).toBe('2,4,5,6');
    expect(await ids(NULL_SAFE_EVERY, ['ALLOW'])).toBe('4,6');
  });

  it('lets exactly the groups whose policy is unknown through the naive form', async () => {
    const naive = new Set((await ids(NAIVE_EVERY, ['ALLOW'])).split(','));
    const safe = new Set((await ids(NULL_SAFE_EVERY, ['ALLOW'])).split(','));
    const slipped = [...naive].filter((id) => !safe.has(id));
    expect(slipped).toEqual(['2', '5']);
    const unknown = await handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT DISTINCT "group_ref" AS "id" FROM "policy_rows" WHERE "access_value" IS NULL ORDER BY 1`,
    );
    expect(unknown.map((entry) => String(Number(entry.id)))).toEqual(slipped);
  });

  it('is more permissive in the naive form, which is the direction that matters for a policy', async () => {
    const naive = (await ids(NAIVE_EVERY, ['ALLOW'])).split(',');
    const safe = (await ids(NULL_SAFE_EVERY, ['ALLOW'])).split(',');
    expect(safe.every((id) => naive.includes(id))).toBe(true);
    expect(naive.length).toBeGreaterThan(safe.length);
  });
});

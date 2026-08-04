import { evaluateConditions, postgresDialect } from '../src/index';
import {
  Agreement,
  agree,
  answerRecord,
  counting,
  disagreementRecord,
  discriminating,
  explainAll,
} from './support/agreement';
import { DEMO_DATAMODEL, MAPPED_DATAMODEL } from './support/agreement-scope';
import {
  MAPPED_CASES,
  MAPPED_INCLUDE,
  OWNERS,
  POSTGRES_MAPPED_DDL,
  REGIONS,
  TAGGED,
} from './support/mapped';
import {
  GROUP_CASES,
  LIBRARY_CASES,
  LIBRARY_DATAMODEL,
  LIBRARY_DDL,
  NAIVE_EVERY_CASE,
  PRODUCTION_RULES,
  libraryGroupRows,
  libraryInserts,
  libraryMediaRows,
} from './support/library';
import { ALL_CASES } from './support/matrix';
import { Row } from './support/measure';
import {
  POSTGRES_C_URL_ENV,
  POSTGRES_C_URL_HINT,
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  ensureDatabase,
  openPostgres,
  withDatabase,
} from './support/postgres';
import { PARENTS, RELATED } from './support/rows';
import {
  DEMO_AGREEMENT_CASES,
  GROUP_AGREEMENT_CASES,
  GROUP_ANSWERS,
  LIBRARY_AGREEMENT_CASES,
  LIBRARY_ANSWERS,
  MAPPED_AGREEMENT_CASES,
  MAPPED_ANSWERS,
  POSTGRES_DEMO_DISAGREEMENTS,
  POSTGRES_LIBRARY_DISAGREEMENTS,
  POSTGRES_MAPPED_DISAGREEMENTS,
} from './support/sql-agreement-record';
import {
  SQL_NUM_RUNS,
  SQL_RELATION_HOPS,
  SQL_SEED,
  demoInclude,
  sampleCases,
} from './support/sql-arbitraries';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(600000);

const AGREEMENT_DATABASE = 'golem_policy_agreement';

const url = process.env[POSTGRES_URL_ENV];
const cUrl = process.env[POSTGRES_C_URL_ENV];

const enabled = url !== undefined && VM_MODULES_ENABLED;
const suite = enabled ? describe : describe.skip;
const pair = enabled && cUrl !== undefined ? describe : describe.skip;

interface Server {
  readonly handle: PostgresHandle;
  readonly linguistic: boolean;
  readonly collation: string;
  readonly demo: Agreement[];
  readonly mapped: Agreement[];
  readonly generated: Agreement[];
  readonly library: Agreement[];
  readonly groups: Agreement[];
  readonly executed: number;
  readonly answered: number;
}

async function openServer(connection: string): Promise<Server> {
  await ensureDatabase(connection, AGREEMENT_DATABASE);
  const handle = await openPostgres(withDatabase(connection, AGREEMENT_DATABASE));
  for (const statement of POSTGRES_MAPPED_DDL) {
    await handle.prisma.$executeRawUnsafe(statement);
  }
  for (const statement of LIBRARY_DDL) {
    await handle.prisma.$executeRawUnsafe(statement);
  }
  for (const entry of libraryInserts(postgresDialect)) {
    await handle.prisma.$executeRawUnsafe(entry.sql, ...(entry.parameters as unknown[]));
  }
  await handle.prisma.related.createMany({ data: RELATED as never });
  await handle.prisma.parent.createMany({ data: PARENTS as never });
  await handle.prisma.region.createMany({ data: REGIONS as never });
  await handle.prisma.owner.createMany({ data: OWNERS as never });
  for (const row of TAGGED) {
    await handle.prisma.tagged.create({ data: row as never });
  }
  const demoRows = (await handle.prisma.parent.findMany({
    include: demoInclude(SQL_RELATION_HOPS) as never,
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  const mappedRows = (await handle.prisma.tagged.findMany({
    include: MAPPED_INCLUDE as never,
    orderBy: { id: 'asc' },
  })) as unknown as Row[];
  const probe = await handle.prisma.$queryRawUnsafe<{ linguistic: boolean; collation: string }[]>(
    `SELECT ('alpha' < 'Zulu') AS linguistic,
            (SELECT datcollate FROM pg_database WHERE datname = current_database()) AS collation`,
  );
  const db = counting(handle.prisma as never);
  const demo = await agree(
    {
      datamodel: DEMO_DATAMODEL,
      model: 'Parent',
      dialect: postgresDialect,
      db,
      rows: demoRows,
    },
    ALL_CASES,
  );
  const mapped = await agree(
    {
      datamodel: MAPPED_DATAMODEL,
      model: 'Tagged',
      dialect: postgresDialect,
      db,
      rows: mappedRows,
    },
    MAPPED_CASES,
  );
  const generated = await agree(
    {
      datamodel: DEMO_DATAMODEL,
      model: 'Parent',
      dialect: postgresDialect,
      db,
      rows: demoRows,
    },
    sampleCases(SQL_NUM_RUNS, SQL_SEED),
  );
  const library = await agree(
    {
      datamodel: LIBRARY_DATAMODEL,
      model: 'Media',
      dialect: postgresDialect,
      db,
      rows: libraryMediaRows(),
    },
    LIBRARY_CASES,
  );
  const groups = await agree(
    {
      datamodel: LIBRARY_DATAMODEL,
      model: 'MediaGroup',
      dialect: postgresDialect,
      db,
      rows: libraryGroupRows(),
    },
    GROUP_CASES,
  );
  return {
    handle,
    executed: db.executed,
    answered: db.answered,
    linguistic: probe[0]!.linguistic,
    collation: probe[0]!.collation,
    demo,
    mapped,
    generated,
    library,
    groups,
  };
}

let primary: Server;
let secondary: Server | undefined;

beforeAll(async () => {
  if (url === undefined || !VM_MODULES_ENABLED) {
    return;
  }
  primary = await openServer(url);
  if (cUrl !== undefined) {
    secondary = await openServer(cUrl);
  }
});

afterAll(async () => {
  if (primary !== undefined) {
    await primary.handle.close();
  }
  if (secondary !== undefined) {
    await secondary.handle.close();
  }
});

describe('the Postgres agreement suite', () => {
  it('is checked against a live server', () => {
    if (!VM_MODULES_ENABLED) {
      throw new Error(VM_MODULES_HINT);
    }
    if (url === undefined && !POSTGRES_OPTIONAL) {
      throw new Error(POSTGRES_URL_HINT);
    }
    expect(url === undefined ? POSTGRES_OPTIONAL : url.length > 0).toBe(true);
  });
});

suite('golem JS versus golem SQL on Postgres', () => {
  it('is checked against a byte-order server as well as this one', () => {
    if (cUrl === undefined) {
      if (!POSTGRES_OPTIONAL) {
        throw new Error(POSTGRES_C_URL_HINT);
      }
      return;
    }
    expect(secondary!.collation).toBe('C');
  });

  it('reports the collation family it is running under', () => {
    expect(typeof primary.linguistic).toBe('boolean');
    expect(primary.collation.length).toBeGreaterThan(0);
  });

  it('runs the whole shared matrix and the mapped matrix', () => {
    expect(primary.demo).toHaveLength(DEMO_AGREEMENT_CASES);
    expect(primary.mapped).toHaveLength(MAPPED_AGREEMENT_CASES);
    expect(primary.generated).toHaveLength(SQL_NUM_RUNS);
    expect(primary.library).toHaveLength(LIBRARY_AGREEMENT_CASES);
    expect(primary.groups).toHaveLength(GROUP_AGREEMENT_CASES);
  });

  it('sends one statement to the server, and reads one answer back, for every case', () => {
    const cases =
      DEMO_AGREEMENT_CASES +
      MAPPED_AGREEMENT_CASES +
      SQL_NUM_RUNS +
      LIBRARY_AGREEMENT_CASES +
      GROUP_AGREEMENT_CASES;
    expect(primary.executed).toBe(cases);
    expect(primary.answered).toBe(cases);
  });

  it('never renders a condition it cannot execute', () => {
    const failures = primary.demo
      .concat(primary.mapped, primary.generated, primary.library, primary.groups)
      .filter((entry) => entry.sql.startsWith('error:'));
    expect(failures.map((entry) => `${entry.id} ${entry.sql}`)).toEqual([]);
  });

  it('agrees on every case in the shared matrix', () => {
    expect(explainAll(primary.demo)).toBe('');
    expect(disagreementRecord(primary.demo)).toEqual(POSTGRES_DEMO_DISAGREEMENTS);
  });

  it('agrees on every case against mapped table and column names', () => {
    expect(explainAll(primary.mapped)).toBe('');
    expect(disagreementRecord(primary.mapped)).toEqual(POSTGRES_MAPPED_DISAGREEMENTS);
  });

  it('agrees on every generated condition', () => {
    expect(explainAll(primary.generated)).toBe('');
  });

  it('agrees on every case of the production-shaped to-many schema', () => {
    expect(explainAll(primary.library)).toBe('');
    expect(explainAll(primary.groups)).toBe('');
    expect(disagreementRecord(primary.library)).toEqual(POSTGRES_LIBRARY_DISAGREEMENTS);
  });

  it('reaches the same answer per mapped case as the SQLite record', () => {
    expect(answerRecord(primary.mapped)).toEqual(MAPPED_ANSWERS);
    expect(answerRecord(primary.library)).toEqual(LIBRARY_ANSWERS);
    expect(answerRecord(primary.groups)).toEqual(GROUP_ANSWERS);
  });

  it('answers the three production rules the same way the JS evaluator does', () => {
    for (const id of Object.keys(PRODUCTION_RULES)) {
      const entry = primary.library.find((candidate) => candidate.id === id)!;
      expect([id, entry.sql]).toEqual([id, entry.js]);
      expect([id, entry.sql]).toEqual([id, LIBRARY_ANSWERS[id]]);
    }
  });

  it('lets a row the condition leaves unknown violate every, where the naive rendering would not', async () => {
    const naive = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "t0"."group_pk" AS "id" FROM "group_rows" AS "t0"
       WHERE NOT EXISTS (SELECT 1 FROM "policy_rows" AS "t0_1"
                         WHERE "t0_1"."group_ref" = "t0"."group_pk"
                           AND NOT ("t0_1"."access_value" = $1))
       ORDER BY 1`,
      'ALLOW',
    );
    const safe = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "t0"."group_pk" AS "id" FROM "group_rows" AS "t0"
       WHERE NOT EXISTS (SELECT 1 FROM "policy_rows" AS "t0_1"
                         WHERE "t0_1"."group_ref" = "t0"."group_pk"
                           AND ("t0_1"."access_value" = $1) IS NOT TRUE)
       ORDER BY 1`,
      'ALLOW',
    );
    expect(naive.map((entry) => Number(entry.id)).join(',')).toBe('2,4,5,6');
    expect(safe.map((entry) => Number(entry.id)).join(',')).toBe('4,6');
    const golem = primary.groups.find((entry) => entry.id === 'group/every-access-allow')!;
    expect(golem.statement).toContain('IS NOT TRUE');
    expect([golem.sql, golem.js]).toEqual(['4,6', '4,6']);
    expect(
      libraryGroupRows()
        .filter((row) => evaluateConditions(NAIVE_EVERY_CASE, row))
        .map((row) => row.id)
        .join(','),
    ).toBe('4,6');
  });

  it('selects a strict subset of the rows in most cases, so agreement is not vacuous', () => {
    expect(discriminating(primary.demo, 8)).toBeGreaterThan(DEMO_AGREEMENT_CASES / 2);
    expect(discriminating(primary.mapped, 8)).toBeGreaterThan(MAPPED_AGREEMENT_CASES / 2);
  });

  it('is safe from the text-operator coercion only by accident, which is why golem refuses it', async () => {
    let refused = 'rendered';
    try {
      await primary.handle.prisma.$queryRawUnsafe(
        `SELECT "id" FROM "Parent" WHERE "count" IS NOT NULL AND "count" LIKE $1`,
        '%0%',
      );
    } catch (error) {
      refused = (error as Error).message.includes('operator does not exist')
        ? 'operator does not exist'
        : 'other';
    }
    expect(refused).toBe('operator does not exist');
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      expect(() =>
        evaluateConditions({ count: { [operator]: '0' } }, { count: 100 }, {
          datamodel: DEMO_DATAMODEL,
          model: 'Parent',
        }),
      ).toThrow('"Parent.count" is a Int column');
    }
  });

  it('orders strings by byte value even where the server collation is linguistic', async () => {
    const ids = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "id" FROM "Parent" WHERE ("name" COLLATE "C") < $1 ORDER BY "id"`,
      'Zulu',
    );
    expect(ids.map((entry) => entry.id)).toEqual([2, 5]);
  });

  it('would disagree with the JS evaluator without the forced collation', async () => {
    const forced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "id" FROM "Parent" WHERE ("name" COLLATE "C") < $1 ORDER BY "id"`,
      'Zulu',
    );
    const unforced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "id" FROM "Parent" WHERE "name" < $1 ORDER BY "id"`,
      'Zulu',
    );
    const same = JSON.stringify(forced) === JSON.stringify(unforced);
    expect(same).toBe(!primary.linguistic);
  });

  it('correlates a string foreign key on byte order, where a linguistic server would not', async () => {
    const forced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "t0"."row_pk" AS "id" FROM "tagged_rows" AS "t0"
       WHERE EXISTS (SELECT 1 FROM "owner_rows" AS "t0_1"
                     WHERE "t0_1"."owner_pk" = "t0"."owner_ref"
                       AND EXISTS (SELECT 1 FROM "region_rows" AS "t0_2"
                                   WHERE "t0_2"."region_code" COLLATE "C" = "t0_1"."region_ref" COLLATE "C"
                                     AND ("t0_2"."region_code" COLLATE "C") < $1))
       ORDER BY 1`,
      'north',
    );
    const unforced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "t0"."row_pk" AS "id" FROM "tagged_rows" AS "t0"
       WHERE EXISTS (SELECT 1 FROM "owner_rows" AS "t0_1"
                     WHERE "t0_1"."owner_pk" = "t0"."owner_ref"
                       AND EXISTS (SELECT 1 FROM "region_rows" AS "t0_2"
                                   WHERE "t0_2"."region_code" = "t0_1"."region_ref"
                                     AND "t0_2"."region_code" < $1))
       ORDER BY 1`,
      'north',
    );
    expect(forced.map((entry) => entry.id).join(',')).toBe(MAPPED_ANSWERS['region/is/code-lt-north']);
    const same = JSON.stringify(forced) === JSON.stringify(unforced);
    expect(same).toBe(!primary.linguistic);
  });

  it('keeps a non-ASCII comparison on byte order, where a linguistic server sorts differently', async () => {
    const forced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "row_pk" AS "id" FROM "tagged_rows" WHERE ("label_text" COLLATE "C") < $1 ORDER BY 1`,
      'Ångström',
    );
    const unforced = await primary.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
      `SELECT "row_pk" AS "id" FROM "tagged_rows" WHERE "label_text" < $1 ORDER BY 1`,
      'Ångström',
    );
    expect(forced.map((entry) => entry.id).join(',')).toBe(MAPPED_ANSWERS['label/lt-non-ascii']);
    const same = JSON.stringify(forced) === JSON.stringify(unforced);
    expect(same).toBe(!primary.linguistic);
  });
});

pair('the two Postgres collations', () => {
  it('runs one linguistic server and one byte-order server', () => {
    expect([primary.linguistic, secondary!.linguistic].sort()).toEqual([false, true]);
    expect(secondary!.collation).toBe('C');
    expect(primary.collation).not.toBe('C');
  });

  it('returns identical answers for the shared matrix', () => {
    expect(answerRecord(primary.demo)).toEqual(answerRecord(secondary!.demo));
  });

  it('returns identical answers for the mapped matrix', () => {
    expect(answerRecord(primary.mapped)).toEqual(answerRecord(secondary!.mapped));
  });

  it('returns identical answers for every generated condition', () => {
    expect(answerRecord(primary.generated)).toEqual(answerRecord(secondary!.generated));
  });

  it('returns identical answers for the to-many matrix', () => {
    expect(answerRecord(primary.library)).toEqual(answerRecord(secondary!.library));
    expect(answerRecord(primary.groups)).toEqual(answerRecord(secondary!.groups));
  });

  it('agrees with the JS evaluator on the byte-order server too', () => {
    expect(explainAll(secondary!.demo)).toBe('');
    expect(explainAll(secondary!.mapped)).toBe('');
    expect(explainAll(secondary!.generated)).toBe('');
    expect(explainAll(secondary!.library)).toBe('');
    expect(explainAll(secondary!.groups)).toBe('');
  });

  it('correlates a to-many relation on a string key by byte order on both servers', async () => {
    const statement = (collate: string): string =>
      `SELECT "t0"."usergroup_slug" AS "id" FROM "usergroup_rows" AS "t0"
       WHERE EXISTS (SELECT 1 FROM "membership_rows" AS "t0_1"
                     WHERE "t0_1"."usergroup_ref"${collate} = "t0"."usergroup_slug"${collate}
                       AND "t0_1"."user_ref"${collate} = $1)
       ORDER BY 1`;
    for (const server of [primary, secondary!]) {
      const forced = await server.handle.prisma.$queryRawUnsafe<{ id: string }[]>(
        statement(' COLLATE "C"'),
        'u1',
      );
      const unforced = await server.handle.prisma.$queryRawUnsafe<{ id: string }[]>(
        statement(''),
        'u1',
      );
      expect([server.collation, forced.map((entry) => entry.id).sort()]).toEqual([
        server.collation,
        ['core', 'côre'],
      ]);
      expect([server.collation, unforced.map((entry) => entry.id).sort()]).toEqual([
        server.collation,
        ['core', 'côre'],
      ]);
    }
  });

  it('orders the same string key differently without the forced collation, which is why golem forces it', async () => {
    const statement = (collate: string): string =>
      `SELECT "t0"."media_pk" AS "id" FROM "media_rows" AS "t0"
       WHERE EXISTS (SELECT 1 FROM "group_rows" AS "t0_1"
                     WHERE "t0_1"."media_ref" = "t0"."media_pk"
                       AND EXISTS (SELECT 1 FROM "policy_rows" AS "t0_2"
                                   WHERE "t0_2"."group_ref" = "t0_1"."group_pk"
                                     AND EXISTS (SELECT 1 FROM "usergroup_rows" AS "t0_3"
                                                 WHERE "t0_3"."usergroup_slug"${collate} = "t0_2"."usergroup_ref"${collate}
                                                   AND "t0_3"."usergroup_slug"${collate} < $1)))
       ORDER BY 1`;
    for (const server of [primary, secondary!]) {
      const forced = await server.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
        statement(' COLLATE "C"'),
        'core',
      );
      const unforced = await server.handle.prisma.$queryRawUnsafe<{ id: number }[]>(
        statement(''),
        'core',
      );
      expect([server.collation, forced.map((entry) => Number(entry.id)).join(',')]).toEqual([
        server.collation,
        LIBRARY_ANSWERS['string-fk/slug-lt'],
      ]);
      const same = JSON.stringify(forced) === JSON.stringify(unforced);
      expect([server.collation, same]).toEqual([server.collation, !server.linguistic]);
    }
  });
});

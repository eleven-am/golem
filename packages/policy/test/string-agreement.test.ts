import { SqlRenderError, evaluateConditions, mysqlDialect, postgresDialect, sqliteDialect } from '../src/index';
import { Agreement, agree, answerRecord, disagreementRecord, discriminating, explainAll, render } from './support/agreement';
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
import { SqliteHandle, openSqlite } from './support/sqlite';
import {
  STRING_CASES,
  STRING_DATAMODEL,
  STRING_DDL,
  STRING_INSENSITIVE_CASES,
  stringInserts,
  stringRows,
} from './support/strings';
import { STRING_ANSWERS, STRING_INSENSITIVE_ANSWERS } from './support/string-record';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(600000);

const STRING_DATABASE = 'golem_policy_strings';

const url = process.env[POSTGRES_URL_ENV];
const cUrl = process.env[POSTGRES_C_URL_ENV];

const suite = VM_MODULES_ENABLED ? describe : describe.skip;
const postgres = VM_MODULES_ENABLED && url !== undefined ? describe : describe.skip;
const pair = VM_MODULES_ENABLED && url !== undefined && cUrl !== undefined ? describe : describe.skip;

interface Engine {
  readonly sensitive: Agreement[];
  readonly insensitive: Agreement[];
  readonly collation: string;
  readonly linguistic: boolean;
}

interface Executor {
  $queryRawUnsafe(sql: string, ...parameters: unknown[]): Promise<unknown>;
  $executeRawUnsafe(sql: string, ...parameters: unknown[]): Promise<unknown>;
}

async function seed(db: Executor, dialect: typeof postgresDialect): Promise<void> {
  for (const statement of STRING_DDL) {
    await db.$executeRawUnsafe(statement);
  }
  for (const entry of stringInserts(dialect)) {
    await db.$executeRawUnsafe(entry.sql, ...(entry.parameters as unknown[]));
  }
}

async function run(
  db: Executor,
  dialect: typeof postgresDialect,
  cases: readonly { id: string; where: Record<string, unknown> }[],
): Promise<Agreement[]> {
  return agree(
    { datamodel: STRING_DATAMODEL, model: 'StringRow', dialect, db, rows: stringRows() },
    cases,
  );
}

let sqlite: SqliteHandle;
let sqliteEngine: Engine;
let primary: PostgresHandle | undefined;
let primaryEngine: Engine | undefined;
let secondary: PostgresHandle | undefined;
let secondaryEngine: Engine | undefined;

async function openServer(connection: string): Promise<{ handle: PostgresHandle; engine: Engine }> {
  await ensureDatabase(connection, STRING_DATABASE);
  const handle = await openPostgres(withDatabase(connection, STRING_DATABASE));
  await seed(handle.prisma as never, postgresDialect);
  const probe = await handle.prisma.$queryRawUnsafe<{ linguistic: boolean; collation: string }[]>(
    `SELECT ('alpha' < 'Zulu') AS linguistic,
            (SELECT datcollate FROM pg_database WHERE datname = current_database()) AS collation`,
  );
  return {
    handle,
    engine: {
      sensitive: await run(handle.prisma as never, postgresDialect, STRING_CASES),
      insensitive: await run(handle.prisma as never, postgresDialect, STRING_INSENSITIVE_CASES),
      collation: probe[0]!.collation,
      linguistic: probe[0]!.linguistic,
    },
  };
}

beforeAll(async () => {
  if (!VM_MODULES_ENABLED) {
    return;
  }
  sqlite = await openSqlite();
  await seed(sqlite.prisma as never, sqliteDialect);
  sqliteEngine = {
    sensitive: await run(sqlite.prisma as never, sqliteDialect, STRING_CASES),
    insensitive: [],
    collation: 'BINARY',
    linguistic: false,
  };
  if (url === undefined) {
    return;
  }
  const opened = await openServer(url);
  primary = opened.handle;
  primaryEngine = opened.engine;
  if (cUrl !== undefined) {
    const other = await openServer(cUrl);
    secondary = other.handle;
    secondaryEngine = other.engine;
  }
});

afterAll(async () => {
  if (sqlite !== undefined) {
    await sqlite.close();
  }
  if (primary !== undefined) {
    await primary.close();
  }
  if (secondary !== undefined) {
    await secondary.close();
  }
});

describe('the string agreement suite', () => {
  it('is checked against a live engine', () => {
    if (!VM_MODULES_ENABLED) {
      throw new Error(VM_MODULES_HINT);
    }
    if (url === undefined && !POSTGRES_OPTIONAL) {
      throw new Error(POSTGRES_URL_HINT);
    }
    if (url !== undefined && cUrl === undefined && !POSTGRES_OPTIONAL) {
      throw new Error(POSTGRES_C_URL_HINT);
    }
    expect(STRING_CASES.length).toBeGreaterThan(200);
  });
});

suite('golem JS versus golem SQL on SQLite', () => {
  it('agrees on every case-sensitive case', () => {
    expect(explainAll(sqliteEngine.sensitive)).toBe('');
    expect(disagreementRecord(sqliteEngine.sensitive)).toEqual({});
  });

  it('never renders a condition it cannot execute', () => {
    expect(sqliteEngine.sensitive.filter((entry) => entry.sql.startsWith('error:'))).toEqual([]);
  });

  it('pins the answer for every case, so both backends cannot drift in step', () => {
    expect(answerRecord(sqliteEngine.sensitive)).toEqual(STRING_ANSWERS);
  });

  it('selects a strict subset of the rows in most cases, so agreement is not vacuous', () => {
    expect(discriminating(sqliteEngine.sensitive, stringRows().length)).toBeGreaterThan(
      STRING_CASES.length / 2,
    );
  });

  it('matches case-sensitively, which a bare SQLite LIKE would not', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', sqliteDialect, { text: { contains: 'alpha' } });
    const ours = (await sqlite.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const naive = (await sqlite.prisma.$queryRawUnsafe(
      `SELECT "row_pk" AS "id" FROM "string_rows" WHERE "text_value" COLLATE BINARY LIKE '%alpha%'`,
    )) as { id: number }[];
    expect(ours.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([1, 16]);
    expect(naive.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([1, 2, 15, 16]);
  });

  it('treats a literal wildcard as a literal, which an unescaped pattern would not', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', sqliteDialect, { text: { contains: '%' } });
    const ours = (await sqlite.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const naive = (await sqlite.prisma.$queryRawUnsafe(
      `SELECT "row_pk" AS "id" FROM "string_rows" WHERE "text_value" LIKE '%' || '%' || '%'`,
    )) as { id: number }[];
    expect(ours.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([3, 7, 14]);
    expect(naive).toHaveLength(16);
  });

  it('refuses insensitive mode rather than matching case-sensitively behind the policy author', () => {
    expect(() =>
      render(STRING_DATAMODEL, 'StringRow', sqliteDialect, {
        text: { contains: 'a', mode: 'insensitive' },
      }),
    ).toThrow(SqlRenderError);
    expect(() =>
      render(STRING_DATAMODEL, 'StringRow', sqliteDialect, {
        text: { contains: 'a', mode: 'insensitive' },
      }),
    ).toThrow('sqlite');
    expect(() =>
      render(STRING_DATAMODEL, 'StringRow', mysqlDialect, {
        text: { contains: 'a', mode: 'insensitive' },
      }),
    ).toThrow('mysql');
  });

  it('refuses an empty insensitive operand too, so renderability never turns on the operand', () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      for (const dialect of [sqliteDialect, mysqlDialect]) {
        expect(() =>
          render(STRING_DATAMODEL, 'StringRow', dialect, {
            text: { [operator]: '', mode: 'insensitive' },
          }),
        ).toThrow(SqlRenderError);
        expect(() =>
          render(STRING_DATAMODEL, 'StringRow', dialect, {
            text: { [operator]: '', mode: 'insensitive' },
          }),
        ).toThrow(dialect.name);
      }
    }
    expect(
      render(STRING_DATAMODEL, 'StringRow', postgresDialect, {
        text: { contains: '', mode: 'insensitive' },
      }).statement,
    ).toContain('"t0"."text_value" COLLATE "C" IS NOT NULL');
  });
});

postgres('golem JS versus golem SQL on Postgres', () => {
  it('agrees on every case-sensitive case', () => {
    expect(explainAll(primaryEngine!.sensitive)).toBe('');
    expect(disagreementRecord(primaryEngine!.sensitive)).toEqual({});
  });

  it('agrees on every case-insensitive case', () => {
    expect(explainAll(primaryEngine!.insensitive)).toBe('');
    expect(disagreementRecord(primaryEngine!.insensitive)).toEqual({});
  });

  it('never renders a condition it cannot execute', () => {
    const failures = primaryEngine!.sensitive
      .concat(primaryEngine!.insensitive)
      .filter((entry) => entry.sql.startsWith('error:'));
    expect(failures.map((entry) => `${entry.id} ${entry.sql}`)).toEqual([]);
  });

  it('reaches the same answers as SQLite for every case-sensitive case', () => {
    expect(answerRecord(primaryEngine!.sensitive)).toEqual(STRING_ANSWERS);
  });

  it('pins the answer for every case-insensitive case', () => {
    expect(answerRecord(primaryEngine!.insensitive)).toEqual(STRING_INSENSITIVE_ANSWERS);
  });

  it('folds only ASCII case, because the forced collation is byte order', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', postgresDialect, {
      text: { contains: 'ÉCOLE', mode: 'insensitive' },
    });
    const forced = (await primary!.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const unforced = (await primary!.prisma.$queryRawUnsafe(
      `SELECT "row_pk" AS "id" FROM "string_rows" WHERE "text_value" IS NOT NULL AND "text_value" ILIKE $1 ESCAPE '\\'`,
      '%ÉCOLE%',
    )) as { id: number }[];
    expect(golem.statement).toContain('COLLATE "C" ILIKE');
    expect(forced.map((entry) => Number(entry.id))).toEqual([11]);
    expect(unforced.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual(
      primaryEngine!.linguistic ? [11, 12] : [11],
    );
  });

  it('escapes an insensitive equals, deliberately unlike Prisma, whose ILIKE treats it as a pattern', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', postgresDialect, {
      text: { equals: 'A%B', mode: 'insensitive' },
    });
    const ours = (await primary!.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const prismaShaped = (await primary!.prisma.$queryRawUnsafe(
      `SELECT "row_pk" AS "id" FROM "string_rows" WHERE "text_value" ILIKE $1`,
      'A%B',
    )) as { id: number }[];
    expect(golem.parameters).toEqual(['A\\%B']);
    expect(ours.map((entry) => Number(entry.id))).toEqual([3]);
    expect(prismaShaped.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([3, 4, 5, 6]);
    expect(
      stringRows()
        .filter((row) => evaluateConditions({ text: { equals: 'A%B', mode: 'insensitive' } }, row))
        .map((row) => row.id),
    ).toEqual([3]);
  });

  it('escapes an insensitive in list the same way, so no element becomes a pattern', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', postgresDialect, {
      text: { in: ['A%B', '100%'], mode: 'insensitive' },
    });
    const ours = (await primary!.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    expect(golem.parameters).toEqual(['A\\%B', '100\\%']);
    expect(ours.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([3, 7]);
  });

  it('escapes the pattern, where Prisma sends the operand through unescaped', async () => {
    const golem = render(STRING_DATAMODEL, 'StringRow', postgresDialect, { text: { contains: 'a%b' } });
    const ours = (await primary!.prisma.$queryRawUnsafe(
      golem.statement,
      ...(golem.parameters as unknown[]),
    )) as { id: number }[];
    const unescaped = (await primary!.prisma.$queryRawUnsafe(
      `SELECT "row_pk" AS "id" FROM "string_rows" WHERE "text_value" LIKE ('%' || $1 || '%')`,
      'a%b',
    )) as { id: number }[];
    expect(golem.parameters).toEqual(['%a\\%b%']);
    expect(ours.map((entry) => Number(entry.id))).toEqual([3]);
    expect(unescaped.map((entry) => Number(entry.id)).sort((a, b) => a - b)).toEqual([3, 4, 5, 6]);
  });
});

pair('the two Postgres collations', () => {
  it('runs one linguistic server and one byte-order server', () => {
    expect([primaryEngine!.linguistic, secondaryEngine!.linguistic].sort()).toEqual([false, true]);
    expect(secondaryEngine!.collation).toBe('C');
  });

  it('returns identical answers on both servers', () => {
    expect(answerRecord(secondaryEngine!.sensitive)).toEqual(STRING_ANSWERS);
    expect(answerRecord(secondaryEngine!.insensitive)).toEqual(STRING_INSENSITIVE_ANSWERS);
  });

  it('agrees with the JS evaluator on the byte-order server too', () => {
    expect(explainAll(secondaryEngine!.sensitive)).toBe('');
    expect(explainAll(secondaryEngine!.insensitive)).toBe('');
  });
});

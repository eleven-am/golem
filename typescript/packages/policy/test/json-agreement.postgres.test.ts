import {
  SqlParameter,
  UnsupportedConditionError,
  postgresDialect,
  validateConditions,
} from '../src/index';
import {
  JSON_CASES,
  JSON_DATAMODEL,
  JSON_OWNER_CASES,
  JSON_OWNER_TABLE,
  JSON_OWNERS,
  JSON_ROWS,
  JSON_TABLE,
  JsonCase,
  JsonSeed,
  POSTGRES_ONLY_JSON_CASES,
  PRECISION_ROWS,
  evaluateCondition,
  evaluateJson,
  evaluateOwners,
  filterFor,
  jsonOwnerOf,
  jsonOwnerRows,
  jsonRows,
  ownerWhereFor,
  renderConditionStatement,
  renderIds,
  renderJsonStatement,
  renderOwnerStatement,
} from './support/json-fixture';
import {
  POSTGRES_C_URL_ENV,
  POSTGRES_C_URL_HINT,
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  withDatabase,
} from './support/postgres';

jest.setTimeout(600000);

const AGREEMENT_DATABASE = 'golem_policy_json';

const url = process.env[POSTGRES_URL_ENV];

const byteOrderUrl = process.env[POSTGRES_C_URL_ENV];

const ALL_CASES: readonly JsonCase[] = [...JSON_CASES, ...POSTGRES_ONLY_JSON_CASES];

interface Server {
  query(sql: string, parameters: readonly SqlParameter[]): Promise<{ id: number }[]>;
  close(): Promise<void>;
}

async function openServer(connectionString: string): Promise<Server> {
  const { Client } = (await import('pg')) as unknown as {
    Client: new (options: { connectionString: string }) => {
      connect(): Promise<void>;
      query(text: string, values: unknown[]): Promise<{ rows: { id: number }[] }>;
      end(): Promise<void>;
    };
  };
  const client = new Client({ connectionString });
  await client.connect();
  return {
    query: async (sql, parameters) => (await client.query(sql, [...parameters])).rows,
    close: () => client.end(),
  };
}

async function seed(server: Server, rows: readonly JsonSeed[]): Promise<void> {
  await server.query(`DROP TABLE IF EXISTS "${JSON_TABLE}"`, []);
  await server.query(`DROP TABLE IF EXISTS "${JSON_OWNER_TABLE}"`, []);
  await server.query(`CREATE TABLE "${JSON_OWNER_TABLE}" ("id" INTEGER PRIMARY KEY, "label" TEXT)`, []);
  await server.query(
    `CREATE TABLE "${JSON_TABLE}" ("id" INTEGER PRIMARY KEY, "ownerId" INTEGER, "payload" JSONB)`,
    [],
  );
  for (const owner of JSON_OWNERS) {
    await server.query(`INSERT INTO "${JSON_OWNER_TABLE}" ("id", "label") VALUES ($1, $2)`, [
      owner.id,
      owner.label,
    ]);
  }
  for (const row of rows) {
    await server.query(
      `INSERT INTO "${JSON_TABLE}" ("id", "ownerId", "payload") VALUES ($1, $2, $3::jsonb)`,
      [row.id, jsonOwnerOf(row.id), row.document],
    );
  }
}

interface Answer {
  readonly id: string;
  readonly sql: string;
  readonly js: string;
  readonly statement: string;
}

async function answers(
  server: Server,
  cases: readonly JsonCase[],
  rows = jsonRows(),
): Promise<readonly Answer[]> {
  const collected: Answer[] = [];
  for (const entry of cases) {
    const filter = filterFor(entry, 'array');
    const statement = renderJsonStatement(filter, postgresDialect);
    const found = await server.query(statement.text, statement.parameters);
    collected.push({
      id: entry.id,
      sql: renderIds(found.map((row) => Number(row.id))),
      js: renderIds(evaluateJson(filter, rows)),
      statement: statement.text,
    });
  }
  return collected;
}

function disagreements(collected: readonly Answer[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const entry of collected) {
    if (entry.sql !== entry.js) {
      record[entry.id] = `sql=${entry.sql} js=${entry.js} :: ${entry.statement}`;
    }
  }
  return record;
}

function answerRecord(collected: readonly Answer[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const entry of collected) {
    record[entry.id] = entry.sql;
  }
  return record;
}

const describeSuite = url === undefined ? describe.skip : describe;

describe('json agreement on postgres', () => {
  if (url === undefined && !POSTGRES_OPTIONAL) {
    it('needs a live postgres server', () => {
      throw new Error(POSTGRES_URL_HINT);
    });
  }

  describeSuite('default collation', () => {
    let server: Server;
    let collected: readonly Answer[];

    beforeAll(async () => {
      await ensureDatabase(url!, AGREEMENT_DATABASE);
      server = await openServer(withDatabase(url!, AGREEMENT_DATABASE));
      await seed(server, JSON_ROWS);
      collected = await answers(server, ALL_CASES);
    });

    afterAll(async () => {
      await server?.close();
    });

    it('agrees with the evaluator on every case', () => {
      expect(disagreements(collected)).toEqual({});
    });

    it('answers each case the same way every run', () => {
      expect(answerRecord(collected)).toMatchSnapshot();
    });

    it('discriminates rather than answering all or nothing', () => {
      const answered = collected.filter(
        (entry) => entry.sql !== '<none>' && entry.sql.split(',').length < JSON_ROWS.length,
      );
      expect(answered.length).toBeGreaterThan(ALL_CASES.length / 2);
    });

    it('separates a path holding JSON null from a missing path and from a NULL column', () => {
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('equals-jsonnull-at-path')).toBe('2');
      expect(of('equals-dbnull-at-path')).toBe('3,4,5,6,7,8,9,10,11,13,14,15,16,17,18,19,21,22,23,24');
      expect(of('equals-anynull-at-path')).toBe('2,3,4,5,6,7,8,9,10,11,13,14,15,16,17,18,19,21,22,23,24');
      expect(of('equals-dbnull-whole')).toBe('9');
      expect(of('equals-jsonnull-whole')).toBe('8');
      expect(of('equals-anynull-whole')).toBe('8,9');
    });

    it('reads a reordered object as equal, because jsonb normalises key order', () => {
      const entry = collected.find((answer) => answer.id === 'equals-reordered-object')!;
      expect(entry.sql).toBe('12');
      expect(entry.js).toBe('12');
    });

    it('matches wildcard content literally rather than as a LIKE pattern', () => {
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('string-contains-percent')).toBe('7,22');
      expect(of('string-contains-underscore')).toBe('7');
      expect(of('string-contains-backslash')).toBe('7');
      expect(of('string-contains-percent-tail')).toBe('22');
      expect(of('string-starts-with-wildcard')).toBe('7');
    });

    it('orders extracted strings in byte order rather than in the server locale', () => {
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('gt-string-upper')).toBe('7,19,23,24');
      expect(of('lt-string')).toBe('7,16,17,19,22,24');
      expect(of('gte-string')).toBe('7,17,19,23,24');
    });

    it('folds ASCII case under insensitive mode and leaves non-ASCII alone', () => {
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('insensitive-ascii-fold')).toBe('23');
      expect(of('insensitive-leaves-non-ascii-alone')).toBe('<none>');
      expect(of('insensitive-starts-with')).toBe('19');
    });

    it('matches an astral character by code point on both sides', () => {
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('string-ends-with-astral')).toBe('24');
      expect(of('string-contains-astral')).toBe('24');
    });

    it('evaluates through compileConditions to the same answer the server gives', () => {
      const rows = jsonRows();
      const mismatched: Record<string, string> = {};
      for (const entry of ALL_CASES) {
        const filter = filterFor(entry, 'array');
        const dispatched = renderIds(evaluateCondition(filter, rows));
        const sql = collected.find((answer) => answer.id === entry.id)!.sql;
        if (dispatched !== sql) {
          mismatched[entry.id] = `compileConditions=${dispatched} sql=${sql}`;
        }
      }
      expect(mismatched).toEqual({});
    });

    it('reaches the Json renderer through renderConstraintSql', () => {
      const statement = renderConditionStatement({ path: ['a'], equals: 1 }, postgresDialect);
      expect(statement.text).toContain('"t0"."payload" #> ARRAY[$1]::text[]');
      expect(statement.text).toContain('::jsonb');
      expect(statement.parameters).toEqual(['a', 'a', '1']);
    });

    it('answers every Json case through renderConstraintSql exactly as the direct renderer does', async () => {
      const mismatched: Record<string, string> = {};
      for (const entry of ALL_CASES) {
        const filter = filterFor(entry, 'array');
        const statement = renderConditionStatement(filter, postgresDialect);
        const found = await server.query(statement.text, statement.parameters);
        const through = renderIds(found.map((row) => Number(row.id)));
        const direct = collected.find((answer) => answer.id === entry.id)!.sql;
        if (through !== direct) {
          mismatched[entry.id] = `renderConstraintSql=${through} renderJsonFilter=${direct}`;
        }
      }
      expect(mismatched).toEqual({});
    });

    it('answers a Json filter nested inside a to-many relation hop the way the evaluator does', async () => {
      const rows = jsonOwnerRows();
      const answers: Record<string, string> = {};
      const mismatched: Record<string, string> = {};
      for (const entry of JSON_OWNER_CASES) {
        const where = ownerWhereFor(entry, 'array');
        const statement = renderOwnerStatement(where, postgresDialect);
        expect(statement.text).toContain('EXISTS (SELECT 1 FROM "JsonDoc"');
        const found = await server.query(statement.text, statement.parameters);
        const sql = renderIds(found.map((row) => Number(row.id)));
        const js = renderIds(evaluateOwners(where, rows));
        answers[entry.id] = sql;
        if (sql !== js) {
          mismatched[entry.id] = `sql=${sql} js=${js} :: ${statement.text}`;
        }
      }
      expect(mismatched).toEqual({});
      expect(answers).toEqual({
        'owner/some-number': '1',
        'owner/some-string-contains': '2',
        'owner/some-nested-string': '1',
        'owner/some-jsonnull-at-path': '1',
        'owner/none-jsonnull-at-path': '2,3',
        'owner/none-missing-path': '1,2,3',
        'owner/every-missing-path': '3',
        'owner/every-dbnull-at-path': '1,2,3',
        'owner/some-array-index': '1',
        'owner/label-and-some-wildcard': '1',
        'owner/label-and-some-astral': '2',
      });
    });

    it('reads every document under the owner it was seeded with', async () => {
      const counted = await server.query(
        `SELECT "ownerId" AS "id" FROM "${JSON_TABLE}" WHERE "ownerId" <> $1`,
        [1],
      );
      expect(counted.every((row) => Number(row.id) === 2)).toBe(true);
      expect(JSON_ROWS.every((row) => jsonOwnerOf(row.id) === (row.id <= 12 ? 1 : 2))).toBe(true);
      expect(JSON_OWNERS.map((owner) => owner.id)).toEqual([1, 2, 3]);
      expect(jsonOwnerRows().map((owner) => owner.docs.length)).toEqual([12, 12, 0]);
    });

    it('validates a Json filter through the public entry point', () => {
      const context = { datamodel: JSON_DATAMODEL, model: 'JsonDoc' };
      expect(validateConditions({ payload: { path: ['a'], equals: 1 } }, context).supported).toBe(true);
      expect(validateConditions({ payload: { path: ['a'], string_contains: 'x' } }, context).supported).toBe(
        true,
      );
      const rejected = validateConditions({ payload: { path: ['a'], contains: 'x' } }, context);
      expect(rejected.supported).toBe(false);
      expect(rejected.issues[0]!.message).toContain('string_contains');
      expect(validateConditions({ payload: 5 }, context).issues[0]!.message).toContain(
        'no bare-value shorthand',
      );
    });

    it('refuses a number beyond the safe integer range rather than over-matching in memory', async () => {
      await seed(server, PRECISION_ROWS);
      const context = { datamodel: JSON_DATAMODEL, model: 'JsonDoc' };
      const filter = { path: ['a'], equals: 9007199254740993 };
      const refused = validateConditions({ payload: filter }, context);
      expect(refused.supported).toBe(false);
      expect(refused.issues[0]!.message).toContain('safe integer range');
      expect(() => renderConditionStatement(filter, postgresDialect)).toThrow(UnsupportedConditionError);
      expect(() => evaluateCondition(filter, jsonRows(PRECISION_ROWS))).toThrow(UnsupportedConditionError);

      const statement = renderJsonStatement(filter, postgresDialect);
      const found = await server.query(statement.text, statement.parameters);
      expect(renderIds(found.map((row) => Number(row.id)))).toBe('1');
      expect(renderIds(evaluateJson(filter, jsonRows(PRECISION_ROWS)))).toBe('1,2');
      await seed(server, JSON_ROWS);
    });

    it('agrees on the largest number it still accepts, one below the refusal', async () => {
      await seed(server, PRECISION_ROWS);
      const filter = { path: ['a'], equals: 9007199254740991 };
      expect(validateConditions({ payload: filter }, { datamodel: JSON_DATAMODEL, model: 'JsonDoc' }).supported).toBe(
        true,
      );
      const statement = renderConditionStatement(filter, postgresDialect);
      const found = await server.query(statement.text, statement.parameters);
      const sql = renderIds(found.map((row) => Number(row.id)));
      expect(sql).toBe('3');
      expect(renderIds(evaluateJson(filter, jsonRows(PRECISION_ROWS)))).toBe('3');
      expect(renderIds(evaluateCondition(filter, jsonRows(PRECISION_ROWS)))).toBe('3');
      await seed(server, JSON_ROWS);
    });
  });

  describeSuite('byte-order collation', () => {
    let server: Server | undefined;
    let collected: readonly Answer[] = [];

    beforeAll(async () => {
      if (byteOrderUrl === undefined) {
        return;
      }
      await ensureDatabase(byteOrderUrl, AGREEMENT_DATABASE);
      server = await openServer(withDatabase(byteOrderUrl, AGREEMENT_DATABASE));
      await seed(server, JSON_ROWS);
      collected = await answers(server, ALL_CASES);
    });

    afterAll(async () => {
      await server?.close();
    });

    it('needs the byte-order server', () => {
      if (byteOrderUrl === undefined && !POSTGRES_OPTIONAL) {
        throw new Error(POSTGRES_C_URL_HINT);
      }
      expect(byteOrderUrl === undefined || server !== undefined).toBe(true);
    });

    it('agrees with the evaluator under a byte-order collation too', () => {
      if (server === undefined) {
        return;
      }
      expect(disagreements(collected)).toEqual({});
    });

    it('orders extracted strings the same way on both servers', () => {
      if (server === undefined) {
        return;
      }
      const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
      expect(of('gt-string-upper')).toBe('7,19,23,24');
      expect(of('lt-string')).toBe('7,16,17,19,22,24');
      expect(of('gte-string')).toBe('7,17,19,23,24');
    });
  });
});

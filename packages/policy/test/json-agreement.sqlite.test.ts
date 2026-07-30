import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { SqlRenderError, sqliteDialect } from '../src/index';
import {
  JSON_CASES,
  JSON_OWNER_CASES,
  JSON_OWNER_TABLE,
  JSON_OWNERS,
  JSON_ROWS,
  JSON_TABLE,
  JsonCase,
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

jest.setTimeout(600000);

const STRUCTURAL_CASES: readonly string[] = [
  'equals-nested-object',
  'equals-nested-array',
  'equals-empty-array',
  'equals-whole-document',
  'equals-deep-object',
  'array-starts-with-array',
];

const SUPPORTED_CASES: readonly JsonCase[] = JSON_CASES.filter(
  (entry) => !STRUCTURAL_CASES.includes(entry.id),
);

interface Server {
  query(sql: string, parameters: readonly unknown[]): { id: number }[];
  run(sql: string, parameters: readonly unknown[]): void;
  close(): void;
}

function openServer(): Server {
  const Database = require('better-sqlite3') as new (path: string) => {
    prepare(sql: string): {
      all(...parameters: unknown[]): { id: number }[];
      run(...parameters: unknown[]): void;
    };
    close(): void;
  };
  const directory = mkdtempSync(join(tmpdir(), 'golem-policy-json-'));
  const database = new Database(join(directory, 'json.db'));
  return {
    query: (sql, parameters) => database.prepare(sql).all(...parameters),
    run: (sql, parameters) => database.prepare(sql).run(...parameters),
    close: () => {
      database.close();
      rmSync(directory, { recursive: true, force: true });
    },
  };
}

interface Answer {
  readonly id: string;
  readonly sql: string;
  readonly js: string;
  readonly statement: string;
}

describe('json agreement on sqlite', () => {
  let server: Server;
  let collected: readonly Answer[];

  beforeAll(() => {
    server = openServer();
    server.run(`DROP TABLE IF EXISTS "${JSON_TABLE}"`, []);
    server.run(`DROP TABLE IF EXISTS "${JSON_OWNER_TABLE}"`, []);
    server.run(`CREATE TABLE "${JSON_OWNER_TABLE}" ("id" INTEGER PRIMARY KEY, "label" TEXT)`, []);
    server.run(
      `CREATE TABLE "${JSON_TABLE}" ("id" INTEGER PRIMARY KEY, "ownerId" INTEGER, "payload" TEXT)`,
      [],
    );
    for (const owner of JSON_OWNERS) {
      server.run(`INSERT INTO "${JSON_OWNER_TABLE}" ("id", "label") VALUES (?, ?)`, [owner.id, owner.label]);
    }
    for (const row of JSON_ROWS) {
      server.run(`INSERT INTO "${JSON_TABLE}" ("id", "ownerId", "payload") VALUES (?, ?, ?)`, [
        row.id,
        jsonOwnerOf(row.id),
        row.document,
      ]);
    }
    const rows = jsonRows();
    collected = SUPPORTED_CASES.map((entry) => {
      const filter = filterFor(entry, 'jsonpath');
      const statement = renderJsonStatement(filter, sqliteDialect);
      const found = server.query(statement.text, statement.parameters);
      return {
        id: entry.id,
        sql: renderIds(found.map((row) => Number(row.id))),
        js: renderIds(evaluateJson(filter, rows)),
        statement: statement.text,
      };
    });
  });

  afterAll(() => {
    server?.close();
  });

  it('agrees with the evaluator on every case sqlite supports', () => {
    const record: Record<string, string> = {};
    for (const entry of collected) {
      if (entry.sql !== entry.js) {
        record[entry.id] = `sql=${entry.sql} js=${entry.js} :: ${entry.statement}`;
      }
    }
    expect(record).toEqual({});
  });

  it('answers each case the same way every run', () => {
    const record: Record<string, string> = {};
    for (const entry of collected) {
      record[entry.id] = entry.sql;
    }
    expect(record).toMatchSnapshot();
  });

  it('selects a strict subset of the rows in most cases, so agreement is not vacuous', () => {
    const discriminating = collected.filter(
      (entry) => entry.sql !== '<none>' && entry.sql.split(',').length < JSON_ROWS.length,
    );
    expect(discriminating.length).toBeGreaterThan(SUPPORTED_CASES.length / 2);
  });

  it('answers a Json filter nested inside a to-many relation hop the way the evaluator does', () => {
    const rows = jsonOwnerRows();
    const answers: Record<string, string> = {};
    const mismatched: Record<string, string> = {};
    for (const entry of JSON_OWNER_CASES) {
      const where = ownerWhereFor(entry, 'jsonpath');
      const statement = renderOwnerStatement(where, sqliteDialect);
      expect(statement.text).toContain('EXISTS (SELECT 1 FROM "JsonDoc"');
      const found = server.query(statement.text, statement.parameters);
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

  it('separates a path holding JSON null from a missing path and from a NULL column', () => {
    const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
    expect(of('equals-jsonnull-at-path')).toBe('2');
    expect(of('equals-dbnull-at-path')).toBe('3,4,5,6,7,8,9,10,11,13,14,15,16,17,18,19,21,22,23,24');
    expect(of('equals-dbnull-whole')).toBe('9');
    expect(of('equals-jsonnull-whole')).toBe('8');
    expect(of('equals-anynull-whole')).toBe('8,9');
  });

  it('matches wildcard content literally, where sqlite LIKE would fold case and glob', () => {
    const of = (id: string): string => collected.find((entry) => entry.id === id)!.sql;
    expect(of('string-contains-percent')).toBe('7,22');
    expect(of('string-contains-underscore')).toBe('7');
    expect(of('string-contains-backslash')).toBe('7');
    expect(of('string-contains-case')).toBe('<none>');
    expect(of('string-contains-case-exact')).toBe('17');
  });

  it('evaluates through compileConditions to the same answer the server gives', () => {
    const rows = jsonRows();
    const mismatched: Record<string, string> = {};
    for (const entry of SUPPORTED_CASES) {
      const filter = filterFor(entry, 'jsonpath');
      const dispatched = renderIds(evaluateCondition(filter, rows));
      const sql = collected.find((answer) => answer.id === entry.id)!.sql;
      if (dispatched !== sql) {
        mismatched[entry.id] = `compileConditions=${dispatched} sql=${sql}`;
      }
    }
    expect(mismatched).toEqual({});
  });

  it('reaches the Json renderer through renderConstraintSql', () => {
    const statement = renderConditionStatement({ path: '$."a"', equals: 1 }, sqliteDialect);
    expect(statement.text).toContain('"t0"."payload"');
    expect(statement.text).toContain('json_type');
  });

  it('answers every Json case through renderConstraintSql exactly as the direct renderer does', () => {
    const mismatched: Record<string, string> = {};
    for (const entry of SUPPORTED_CASES) {
      const filter = filterFor(entry, 'jsonpath');
      const statement = renderConditionStatement(filter, sqliteDialect);
      const found = server.query(statement.text, statement.parameters);
      const through = renderIds(found.map((row) => Number(row.id)));
      const direct = collected.find((answer) => answer.id === entry.id)!.sql;
      if (through !== direct) {
        mismatched[entry.id] = `renderConstraintSql=${through} renderJsonFilter=${direct}`;
      }
    }
    expect(mismatched).toEqual({});
  });

  it('refuses a document operand rather than comparing JSON as text', () => {
    for (const id of STRUCTURAL_CASES) {
      const entry = JSON_CASES.find((candidate) => candidate.id === id)!;
      expect(() => renderJsonStatement(filterFor(entry, 'jsonpath'), sqliteDialect)).toThrow(
        SqlRenderError,
      );
    }
  });

  it('refuses the operators Prisma does not generate for sqlite', () => {
    for (const filter of [
      { path: '$."a"', array_contains: 2 },
      { path: '$."a"', lt: 5 },
      { path: '$."a"', lte: 5 },
      { path: '$."a"', gt: 5 },
      { path: '$."a"', gte: 5 },
    ]) {
      expect(() => renderJsonStatement(filter, sqliteDialect)).toThrow(SqlRenderError);
    }
  });
});

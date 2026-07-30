import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  OPERATORS,
  SCALAR_LIST_OPERATORS,
  SqlDialect,
  SqlParameter,
  SqlRenderError,
  postgresDialect,
  sqliteDialect,
} from '../src/index';
import { renderIds } from './support/measure';
import {
  POSTGRES_C_URL_ENV,
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  ensureDatabase,
  withDatabase,
} from './support/postgres';
import {
  DECLARED_FOLDED_OPERATORS,
  DECLARED_OPERATORS,
  DECLARED_SCALAR_LIST_OPERATORS,
  INSENSITIVE_PROBES,
  OPERATOR_PROBES,
  OperatorProbe,
  SCALAR_LIST_PROBES,
  THREE_VALUED_CONTROL,
  TWO_VALUED_POSTGRES_DDL,
  TWO_VALUED_ROW_IDS,
  TWO_VALUED_SQLITE_DDL,
  coveredOperators,
  definitionOf,
  renderProbe,
  statementFor,
} from './support/two-valued';

jest.setTimeout(300000);

const TWO_VALUED_DATABASE = 'golem_policy_two_valued';

const url = process.env[POSTGRES_URL_ENV];

const byteOrderUrl = process.env[POSTGRES_C_URL_ENV];

interface Engine {
  readonly name: string;
  readonly dialect: SqlDialect;
  query(sql: string, parameters: readonly SqlParameter[]): Promise<number[]>;
  close(): Promise<void>;
}

function bindSqlite(value: SqlParameter): unknown {
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (typeof value === 'boolean') {
    return value ? 1 : 0;
  }
  return value;
}

async function openSqliteEngine(): Promise<Engine> {
  const Database = require('better-sqlite3') as new (path: string) => {
    prepare(sql: string): { all(...parameters: unknown[]): { id: number }[] };
    exec(sql: string): void;
    close(): void;
  };
  const directory = mkdtempSync(join(tmpdir(), 'golem-policy-two-valued-'));
  const database = new Database(join(directory, 'two-valued.db'));
  for (const statement of TWO_VALUED_SQLITE_DDL) {
    database.exec(statement);
  }
  return {
    name: 'sqlite',
    dialect: sqliteDialect,
    query: async (sql, parameters) =>
      database
        .prepare(sql)
        .all(...parameters.map(bindSqlite))
        .map((row) => Number(row.id)),
    close: async () => {
      database.close();
      rmSync(directory, { recursive: true, force: true });
    },
  };
}

async function openPostgresEngine(connection: string, name: string): Promise<Engine> {
  const { Client } = (await import('pg')) as unknown as {
    Client: new (options: { connectionString: string }) => {
      connect(): Promise<void>;
      query(text: string, values: unknown[]): Promise<{ rows: { id: number }[] }>;
      end(): Promise<void>;
    };
  };
  const client = new Client({ connectionString: connection });
  await client.connect();
  for (const statement of TWO_VALUED_POSTGRES_DDL) {
    await client.query(statement, []);
  }
  return {
    name,
    dialect: postgresDialect,
    query: async (sql, parameters) =>
      (await client.query(sql, [...parameters])).rows.map((row) => Number(row.id)),
    close: () => client.end(),
  };
}

async function unknownRows(engine: Engine, probe: OperatorProbe): Promise<string> {
  const fragment = renderProbe(probe, engine.dialect);
  const statement = statementFor('unknown-count', fragment.text, engine.dialect);
  return renderIds(await engine.query(statement, fragment.parameters));
}

async function polarities(engine: Engine, probe: OperatorProbe): Promise<[string, string]> {
  const fragment = renderProbe(probe, engine.dialect);
  const guarded = await engine.query(
    statementFor('guarded', fragment.text, engine.dialect),
    fragment.parameters,
  );
  const naive = await engine.query(
    statementFor('naive', fragment.text, engine.dialect),
    fragment.parameters,
  );
  return [renderIds(guarded), renderIds(naive)];
}

describe('the operator table declares its SQL two-valued', () => {
  it('declares the property on every operator, so a three-valued one cannot slip in unremarked', () => {
    const threeValued = Object.entries({ ...OPERATORS, ...SCALAR_LIST_OPERATORS })
      .filter(([, definition]) => definition.nullSemantics.sqlIsTwoValued !== true)
      .map(([name]) => name);
    expect(threeValued).toEqual([]);
  });

  it('renders every as a plain negation, which only a two-valued table makes correct', () => {
    expect(OPERATORS.every.nullSemantics.sqlIsTwoValued).toBe(true);
    expect(
      Object.values(OPERATORS).every((definition) => definition.nullSemantics.sqlIsTwoValued),
    ).toBe(true);
  });

  it('probes every operator the table declares, so a new one cannot go unmeasured', () => {
    expect(coveredOperators(OPERATOR_PROBES)).toEqual(DECLARED_OPERATORS);
    expect(coveredOperators(SCALAR_LIST_PROBES)).toEqual(DECLARED_SCALAR_LIST_OPERATORS);
  });

  it('probes every operator that folds case, since folding renders a different predicate', () => {
    expect(coveredOperators(INSENSITIVE_PROBES)).toEqual(DECLARED_FOLDED_OPERATORS);
  });

  it('names the same operator in the probe as in the table entry it renders', () => {
    for (const probe of [...OPERATOR_PROBES, ...SCALAR_LIST_PROBES]) {
      expect(`${probe.id}:${definitionOf(probe).name}`).toBe(`${probe.id}:${probe.operator}`);
    }
  });
});

describe('the two-valued invariant on a live engine', () => {
  it('needs a live Postgres, or says why it does not', () => {
    if (url === undefined && !POSTGRES_OPTIONAL) {
      throw new Error(POSTGRES_URL_HINT);
    }
    expect(url === undefined ? POSTGRES_OPTIONAL : url.length > 0).toBe(true);
  });
});

const engines: { readonly name: string; open(): Promise<Engine>; readonly lists: boolean }[] = [
  { name: 'sqlite', open: openSqliteEngine, lists: false },
];

if (url !== undefined) {
  engines.push({
    name: 'postgres',
    open: async () => {
      await ensureDatabase(url, TWO_VALUED_DATABASE);
      return openPostgresEngine(withDatabase(url, TWO_VALUED_DATABASE), 'postgres');
    },
    lists: true,
  });
}

if (byteOrderUrl !== undefined) {
  engines.push({
    name: 'postgres-byte-order',
    open: async () => {
      await ensureDatabase(byteOrderUrl, TWO_VALUED_DATABASE);
      return openPostgresEngine(withDatabase(byteOrderUrl, TWO_VALUED_DATABASE), 'postgres-byte-order');
    },
    lists: true,
  });
}

for (const entry of engines) {
  describe(`every rendered operator stays two-valued on ${entry.name}`, () => {
    let engine: Engine;
    let probes: readonly OperatorProbe[];

    beforeAll(async () => {
      engine = await entry.open();
      probes = entry.lists
        ? [...OPERATOR_PROBES, ...SCALAR_LIST_PROBES, ...INSENSITIVE_PROBES]
        : OPERATOR_PROBES;
    });

    afterAll(async () => {
      await engine?.close();
    });

    it('reads the seeded rows, half of them null in every nullable position', async () => {
      const all = await engine.query(
        statementFor('naive', '1 = 0', engine.dialect),
        [],
      );
      expect(all.sort((left, right) => left - right)).toEqual([...TWO_VALUED_ROW_IDS]);
    });

    it('leaves no row unknown for any rendering, against columns and relations that are null', async () => {
      const unknown: Record<string, string> = {};
      for (const probe of probes) {
        const rows = await unknownRows(engine, probe);
        if (rows !== '<none>') {
          unknown[probe.id] = rows;
        }
      }
      expect(unknown).toEqual({});
    });

    it('answers the null-safe negation and the naive negation identically for every rendering', async () => {
      const divergent: Record<string, string> = {};
      for (const probe of probes) {
        const [guarded, naive] = await polarities(engine, probe);
        if (guarded !== naive) {
          divergent[probe.id] = `IS NOT TRUE=${guarded} NOT=${naive}`;
        }
      }
      expect(divergent).toEqual({});
    });

    it('catches a three-valued predicate, so the two checks above are not vacuous', async () => {
      const unknown = await engine.query(
        statementFor('unknown-count', THREE_VALUED_CONTROL, engine.dialect),
        [],
      );
      const guarded = await engine.query(
        statementFor('guarded', THREE_VALUED_CONTROL, engine.dialect),
        [],
      );
      const naive = await engine.query(
        statementFor('naive', THREE_VALUED_CONTROL, engine.dialect),
        [],
      );
      expect(renderIds(unknown)).toBe('2,4');
      expect(renderIds(guarded)).toBe('2,3,4');
      expect(renderIds(naive)).toBe('3');
    });

    it('discriminates rows rather than answering all or nothing', async () => {
      const answers = new Set<string>();
      for (const probe of probes) {
        const [guarded] = await polarities(engine, probe);
        answers.add(guarded);
      }
      expect(answers.size).toBeGreaterThan(4);
    });
  });
}

describe('the scalar list operators reach only the provider that has list columns', () => {
  it('refuses every scalar list rendering on sqlite rather than answering it', () => {
    for (const probe of SCALAR_LIST_PROBES) {
      expect(() => renderProbe(probe, sqliteDialect)).toThrow(SqlRenderError);
    }
  });

  it('refuses every folded rendering on sqlite, whatever the operand', () => {
    const rendered: string[] = [];
    for (const probe of INSENSITIVE_PROBES) {
      try {
        renderProbe(probe, sqliteDialect);
        rendered.push(`${probe.id}/${sqliteDialect.name}`);
      } catch (error) {
        if (!(error instanceof SqlRenderError)) {
          rendered.push(`${probe.id}/${sqliteDialect.name}:${(error as Error).name}`);
        }
      }
    }
    expect(rendered).toEqual([]);
  });
});

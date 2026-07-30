import { PrismaPg } from '@prisma/adapter-pg';
import { SqlRenderError, mysqlDialect, postgresDialect, sqliteDialect } from '../src/index';
import { PrismaClient } from './prisma-arrays/generated/client';
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
  LIST_CASES,
  LIST_DATAMODEL,
  LIST_DDL,
  LIST_INSERTS,
  LIST_LEAF_CASES,
  LIST_NULL_COLUMN_ANSWERS,
  LIST_NULL_COLUMN_CASES,
  LIST_OWNER_CASES,
  LIST_OWNER_INSERTS,
  LIST_OWNER_ROWS,
  LIST_ROWS,
} from './support/lists';
import { Measurement, divergenceRecord, measure } from './support/measure';
import {
  POSTGRES_C_URL_ENV,
  POSTGRES_C_URL_HINT,
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
} from './support/postgres';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(600000);

const url = process.env[POSTGRES_URL_ENV];
const cUrl = process.env[POSTGRES_C_URL_ENV];

const enabled = url !== undefined && VM_MODULES_ENABLED;
const suite = enabled ? describe : describe.skip;
const pair = enabled && cUrl !== undefined ? describe : describe.skip;

interface Server {
  readonly prisma: PrismaClient;
  readonly collation: string;
  readonly agreements: Agreement[];
  readonly quantified: Agreement[];
  readonly measurements: Measurement[];
}

async function openServer(connection: string): Promise<Server> {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: connection }) });
  for (const statement of [...LIST_DDL, ...LIST_OWNER_INSERTS, ...LIST_INSERTS]) {
    await prisma.$executeRawUnsafe(statement);
  }
  const probe = await prisma.$queryRawUnsafe<{ collation: string }[]>(
    `SELECT (SELECT datcollate FROM pg_database WHERE datname = current_database()) AS collation`,
  );
  const agreements = await agree(
    {
      datamodel: LIST_DATAMODEL,
      model: 'ListRow',
      dialect: postgresDialect,
      db: prisma,
      rows: LIST_ROWS,
    },
    LIST_CASES,
  );
  const quantified = await agree(
    {
      datamodel: LIST_DATAMODEL,
      model: 'ListOwner',
      dialect: postgresDialect,
      db: prisma,
      rows: LIST_OWNER_ROWS,
    },
    LIST_OWNER_CASES,
  );
  const measurements = await measure(prisma.listRow as never, LIST_ROWS, LIST_LEAF_CASES);
  return { prisma, collation: probe[0]!.collation, agreements, quantified, measurements };
}

describe('the scalar list suites announce what they need', () => {
  it('runs against a live Postgres, or says why it does not', () => {
    if (url === undefined) {
      expect(POSTGRES_OPTIONAL).toBe(true);
      expect(POSTGRES_URL_HINT).toContain(POSTGRES_URL_ENV);
      return;
    }
    expect(VM_MODULES_ENABLED || VM_MODULES_HINT.length > 0).toBe(true);
  });

  it('runs the byte-order server, or says why it does not', () => {
    if (cUrl === undefined) {
      expect(POSTGRES_OPTIONAL).toBe(true);
      expect(POSTGRES_C_URL_HINT).toContain(POSTGRES_C_URL_ENV);
      return;
    }
    expect(cUrl.length).toBeGreaterThan(0);
  });
});

suite('scalar list operators agree with the evaluator on Postgres', () => {
  let server: Server;

  beforeAll(async () => {
    server = await openServer(url!);
  });

  afterAll(async () => {
    await server?.prisma.$disconnect();
  });

  it('answers every list case identically in SQL and in memory', () => {
    expect(disagreementRecord(server.agreements)).toEqual({});
  });

  it('renders SQL for every list case, leaving no case unanswered', () => {
    for (const entry of server.agreements) {
      expect(`${entry.id}:${entry.sql.startsWith('error:')}`).toBe(`${entry.id}:false`);
    }
  });

  it('answers a null list column equality on the server, now that the datamodel reaches the renderer', () => {
    const answers = answerRecord(server.agreements);
    const measured: Record<string, string> = {};
    for (const id of LIST_NULL_COLUMN_CASES) {
      measured[id] = answers[id]!;
    }
    expect(measured).toEqual(LIST_NULL_COLUMN_ANSWERS);
  });

  it('discriminates rows rather than answering all or nothing', () => {
    const answers = answerRecord(server.agreements);
    const distinct = new Set(Object.values(answers));
    expect(distinct.size).toBeGreaterThan(10);
  });

  it('pins the empty operand answers to what Postgres measured', () => {
    const answers = answerRecord(server.agreements);
    expect(answers['tags.hasSome-none']).toBe('<none>');
    expect(answers['tags.hasEvery-none']).toBe('1,3,4,5,6,7,8,9');
    expect(answers['tags.equals-none']).toBe('1');
    expect(answers['tags.isEmpty-true']).toBe('1');
    expect(answers['tags.isEmpty-false']).toBe('3,4,5,6,7,8,9');
    expect(answers['scores.hasSome-none']).toBe('<none>');
    expect(answers['scores.hasEvery-none']).toBe('1,3,4,5,6,7,8,9');
  });

  it('pins order and duplicate sensitivity in equals', () => {
    const answers = answerRecord(server.agreements);
    expect(answers['tags.equals-a-b']).toBe('6');
    expect(answers['tags.equals-b-a']).toBe('5');
    expect(answers['tags.equals-a-a-b']).toBe('4');
    expect(answers['tags.equals-a']).toBe('3');
  });

  it('never lets a null column answer a list filter', () => {
    const answers = answerRecord(server.agreements);
    for (const [id, answer] of Object.entries(answers)) {
      if (id.startsWith('NOT.') || id.includes('equals-null') || id.startsWith('or.') || id.startsWith('mixed.')) {
        continue;
      }
      expect(answer.split(',')).not.toContain('2');
    }
  });

  it('complements every leaf under NOT, because the leaves are two-valued', () => {
    const answers = answerRecord(server.agreements);
    const all = LIST_ROWS.map((row) => row.id);
    for (const entry of LIST_LEAF_CASES) {
      const positive = answers[entry.id]!;
      const negative = answers[`NOT.${entry.id}`]!;
      const matched = positive === '<none>' ? [] : positive.split(',').map(Number);
      const expected = all.filter((id) => !matched.includes(id));
      expect(`${entry.id}=${negative}`).toBe(`${entry.id}=${expected.length === 0 ? '<none>' : expected.join(',')}`);
    }
  });

  it('explains any disagreement at all', () => {
    expect(explainAll(server.agreements)).toBe('');
  });

  it('answers a list operator quantified over a to-many relation the way the evaluator does', () => {
    expect(explainAll(server.quantified)).toBe('');
    expect(disagreementRecord(server.quantified)).toEqual({});
    for (const entry of server.quantified) {
      expect(`${entry.id}:${entry.sql.startsWith('error:')}`).toBe(`${entry.id}:false`);
      expect(entry.statement).toContain('EXISTS (SELECT 1 FROM "ListRow"');
    }
  });

  it('pins the answer for every quantified list case', () => {
    expect(answerRecord(server.quantified)).toEqual({
      'owner/some-has-a': '1,2',
      'owner/some-has-zulu': '3',
      'owner/some-has-empty-string': '3',
      'owner/some-isEmpty-true': '1',
      'owner/some-hasEvery-a-b': '1,2',
      'owner/some-hasEvery-none': '1,2,3',
      'owner/some-hasSome-none': '<none>',
      'owner/some-equals-a-b': '2',
      'owner/some-equals-null': '1',
      'owner/some-scores-has-0': '3',
      'owner/some-bigs-has-beyond': '1,2',
      'owner/some-moments-hasEvery-d0-d1': '1,2',
      'owner/every-isEmpty-false': '2,3,4',
      'owner/every-has-a': '2,4',
      'owner/none-has-b': '3,4',
      'owner/none-hasSome-a-b': '3,4',
      'owner/label-and-some-has-a': '2',
      'owner/not-some-has-a': '3,4',
      'owner/some-has-a-and-flags-has-false': '1,2',
    });
  });

  it('discriminates owners rather than answering all or nothing', () => {
    const answers = answerRecord(server.quantified);
    expect(new Set(Object.values(answers)).size).toBeGreaterThan(6);
    expect(discriminating(server.quantified, LIST_OWNER_ROWS.length)).toBeGreaterThan(
      LIST_OWNER_CASES.length / 2,
    );
  });
});

suite('scalar list operators agree with Prisma on Postgres', () => {
  let server: Server;

  beforeAll(async () => {
    server = await openServer(url!);
  });

  afterAll(async () => {
    await server?.prisma.$disconnect();
  });

  it('answers every leaf case exactly as Prisma does', () => {
    expect(divergenceRecord(server.measurements)).toEqual({});
  });
});

pair('scalar list operators do not depend on the database collation', () => {
  let linguistic: Server;
  let byteOrder: Server;

  beforeAll(async () => {
    linguistic = await openServer(url!);
    byteOrder = await openServer(cUrl!);
  });

  afterAll(async () => {
    await linguistic?.prisma.$disconnect();
    await byteOrder?.prisma.$disconnect();
  });

  it('reads two servers with different collations', () => {
    expect(linguistic.collation).not.toBe(byteOrder.collation);
  });

  it('answers every list case identically on both servers', () => {
    expect(answerRecord(byteOrder.agreements)).toEqual(answerRecord(linguistic.agreements));
  });

  it('agrees with the evaluator on the byte-order server too', () => {
    expect(disagreementRecord(byteOrder.agreements)).toEqual({});
  });
});

describe('scalar list operators refuse the providers that have no list columns', () => {
  for (const dialect of [sqliteDialect, mysqlDialect]) {
    it(`refuses every list case on ${dialect.name}`, () => {
      for (const entry of LIST_LEAF_CASES) {
        expect(() => render(LIST_DATAMODEL, 'ListRow', dialect, entry.where)).toThrow(SqlRenderError);
      }
    });
  }
});

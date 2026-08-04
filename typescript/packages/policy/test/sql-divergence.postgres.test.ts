import { postgresDialect, validateConditions } from '../src/index';
import { render } from './support/agreement';
import { DEMO_DATAMODEL } from './support/agreement-scope';
import { ALL_CASES } from './support/matrix';
import { Measurement, Row, classCounts, divergenceRecord, measure, renderIds } from './support/measure';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  openPostgres,
} from './support/postgres';
import { PARENTS, RELATED } from './support/rows';
import {
  BYTE_ORDER_DIVERGENCES,
  LINGUISTIC_COLLATION_DIVERGENCES,
  SQL_DIVERGENCE_CLASSES,
} from './support/sql-record';
import { VM_MODULES_ENABLED, VM_MODULES_HINT } from './support/vm-modules';

jest.setTimeout(120000);

const MODE_MIXING_PROBES: Readonly<Record<string, Record<string, unknown>>> = {
  'equals+insensitive': { name: { equals: 'ALPHA', mode: 'insensitive' } },
  'not+insensitive': { name: { not: 'ALPHA', mode: 'insensitive' } },
  'in+insensitive': { name: { in: ['ALPHA', 'ZULU'], mode: 'insensitive' } },
  'notIn+insensitive': { name: { notIn: ['ALPHA'], mode: 'insensitive' } },
  'lt+insensitive': { name: { lt: 'ZULU', mode: 'insensitive' } },
  'gte+insensitive': { name: { gte: 'ZULU', mode: 'insensitive' } },
  'contains+gt+insensitive': { name: { contains: 'ALP', gt: 'A', mode: 'insensitive' } },
};

const MODE_MIXING_PRISMA_ANSWERS: Readonly<Record<string, string>> = {
  'equals+insensitive': '1,6 folded=true',
  'not+insensitive': '2,3,4,5,7,8 folded=true',
  'in+insensitive': '1,4,6 folded=true',
  'notIn+insensitive': '2,3,4,5,7,8 folded=true',
  'lt+insensitive': '1,2,3,5,6,7,8 folded=true',
  'gte+insensitive': '4 folded=true',
  'contains+gt+insensitive': '1,6 folded=true',
};

const MODE_MIXING_AGREEMENT: Readonly<Record<string, string>> = Object.fromEntries(
  Object.keys(MODE_MIXING_PROBES).map((id) => [id, 'agrees']),
);

const url = process.env[POSTGRES_URL_ENV];
const suite = url === undefined || !VM_MODULES_ENABLED ? describe.skip : describe;

describe('the Postgres divergence suite', () => {
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

suite('golem evaluator versus Prisma-emitted Postgres', () => {
  let handle: PostgresHandle;
  let rows: Row[];
  let measurements: Measurement[];
  let linguistic = false;

  beforeAll(async () => {
    handle = await openPostgres(url!);
    await handle.prisma.related.createMany({ data: RELATED as never });
    await handle.prisma.parent.createMany({ data: PARENTS as never });
    rows = (await handle.prisma.parent.findMany({
      include: { related: true },
      orderBy: { id: 'asc' },
    })) as unknown as Row[];
    const probe = await handle.prisma.$queryRawUnsafe<{ linguistic: boolean }[]>(
      `SELECT ('alpha' < 'Zulu') AS linguistic`,
    );
    linguistic = probe[0]!.linguistic;
    measurements = await measure(handle.prisma.parent as never, rows, ALL_CASES);
  });

  afterAll(async () => {
    await handle.close();
  });

  it('reports which collation family the server is using', () => {
    expect(typeof linguistic).toBe('boolean');
  });

  it('matches the recorded divergence set for its collation family', () => {
    const expected = linguistic
      ? { ...BYTE_ORDER_DIVERGENCES, ...LINGUISTIC_COLLATION_DIVERGENCES }
      : BYTE_ORDER_DIVERGENCES;
    expect(divergenceRecord(measurements)).toEqual(expected);
  });

  it('matches the recorded divergence classes, plus collation entries when linguistic', () => {
    const counts = classCounts(measurements);
    if (!linguistic) {
      expect(counts).toEqual(SQL_DIVERGENCE_CLASSES);
      return;
    }
    expect(counts['prisma-rejects']).toBe(SQL_DIVERGENCE_CLASSES['prisma-rejects']);
    const collationTotal = Object.keys(LINGUISTIC_COLLATION_DIVERGENCES).length;
    const total = Object.values(counts).reduce((sum, entry) => sum + entry, 0);
    expect(total).toBe(Object.values(SQL_DIVERGENCE_CLASSES).reduce((sum, e) => sum + e, 0) + collationTotal);
  });

  it('accepts mode insensitive beside every non-string operator, and folds case for all of them', async () => {
    const measured: Record<string, string> = {};
    for (const [id, where] of Object.entries(MODE_MIXING_PROBES)) {
      (handle.sql as string[]).length = 0;
      try {
        const found = await handle.prisma.parent.findMany({
          where: where as never,
          select: { id: true },
          orderBy: { id: 'asc' },
        });
        const emitted = handle.sql.filter((statement) => statement.startsWith('SELECT')).join(' ');
        const folded = emitted.includes('ILIKE') || emitted.includes('LOWER(');
        measured[id] = `${renderIds(found.map((entry) => entry.id))} folded=${folded}`;
      } catch (error) {
        measured[id] = `error:${(error as Error).name}`;
      }
    }
    expect(measured).toEqual(MODE_MIXING_PRISMA_ANSWERS);
  });

  it('accepts in golem every shape Prisma accepts, rather than refusing what Prisma answers', () => {
    for (const [id, where] of Object.entries(MODE_MIXING_PROBES)) {
      const result = validateConditions(where, { datamodel: DEMO_DATAMODEL, model: 'Parent' });
      expect(`${id}:${result.supported}`).toBe(`${id}:true`);
    }
  });

  it('answers each folded shape as Prisma does, through the forced byte-order collation', async () => {
    const measured: Record<string, string> = {};
    for (const [id, where] of Object.entries(MODE_MIXING_PROBES)) {
      const rendered = render(DEMO_DATAMODEL, 'Parent', postgresDialect, where);
      const found = (await handle.prisma.$queryRawUnsafe(
        rendered.statement,
        ...(rendered.parameters as unknown[]),
      )) as { id: number }[];
      const golemRows = renderIds(found.map((entry) => Number(entry.id)));
      const prismaRows = MODE_MIXING_PRISMA_ANSWERS[id]!.split(' ')[0]!;
      measured[id] = golemRows === prismaRows ? 'agrees' : `golem=${golemRows} prisma=${prismaRows}`;
    }
    expect(measured).toEqual(MODE_MIXING_AGREEMENT);
  });

  it('folds ASCII case only, where Prisma folds through the server collation', async () => {
    const rendered = render(DEMO_DATAMODEL, 'Parent', postgresDialect, {
      nameNull: { equals: 'ZULU', mode: 'insensitive' },
    });
    expect(rendered.statement).toContain('COLLATE "C" ILIKE');
    const golemRows = (await handle.prisma.$queryRawUnsafe(
      rendered.statement,
      ...(rendered.parameters as unknown[]),
    )) as { id: number }[];
    expect(renderIds(golemRows.map((entry) => Number(entry.id)))).toBe('4');
    const ordered = render(DEMO_DATAMODEL, 'Parent', postgresDialect, {
      name: { lt: 'BETA', mode: 'insensitive' },
    });
    expect(ordered.statement).toContain('lower("t0"."name" COLLATE "C") COLLATE "C" < $1');
    expect(ordered.parameters).toEqual(['beta']);
  });

  it('orders strings by collation, not by UTF-16 code unit, when the collation is linguistic', async () => {
    const ids = await handle.prisma.parent.findMany({
      where: { name: { lt: 'Zulu' } },
      select: { id: true },
      orderBy: { id: 'asc' },
    });
    expect(ids.map((entry) => entry.id)).toEqual(linguistic ? [1, 2, 3, 5, 6, 7, 8] : [2, 5]);
  });
});

import { CompiledReadEvent } from '../../src/compiled-read';
import { AggregateRequest, GolemEngine, GroupByRequest } from '../../src/operations';
import { context, engineFor } from './analytics';
import { describeShape } from './oracle';

export interface AggregateSubject {
  readonly provider: string;
  readonly client: Record<string, any>;
}

export interface AggregateRun {
  readonly compiled: unknown;
  readonly prisma: unknown;
  readonly events: readonly CompiledReadEvent[];
}

export const AVG_TOLERANCE = 1e-9;

async function settle(run: () => Promise<unknown>): Promise<unknown> {
  return run().then(
    (value) => value,
    (error: Error) => error,
  );
}

export async function runBothAggregate(
  engine: GolemEngine,
  request: Omit<AggregateRequest, 'context' | 'compiled'>,
): Promise<AggregateRun> {
  const events: CompiledReadEvent[] = [];
  const release = engine.observeCompiledRead((event) => events.push(event));
  const compiled = await settle(() => engine.aggregate({ ...request, context, compiled: true }));
  release();
  const prisma = await settle(() => engine.aggregate({ ...request, context }));
  return { compiled, prisma, events };
}

export async function runBothGroup(
  engine: GolemEngine,
  request: Omit<GroupByRequest, 'context' | 'compiled'>,
): Promise<AggregateRun> {
  const events: CompiledReadEvent[] = [];
  const release = engine.observeCompiledRead((event) => events.push(event));
  const compiled = await settle(() => engine.groupBy({ ...request, context, compiled: true }));
  release();
  const prisma = await settle(() => engine.groupBy({ ...request, context }));
  return { compiled, prisma, events };
}

export function expectAgreed(run: AggregateRun): void {
  if (run.compiled instanceof Error || run.prisma instanceof Error) {
    expect(run.compiled).toBeInstanceOf(Error);
    expect(run.prisma).toBeInstanceOf(Error);
    expect((run.compiled as Error).constructor).toBe((run.prisma as Error).constructor);
    expect((run.compiled as { code?: unknown }).code).toEqual(
      (run.prisma as { code?: unknown }).code,
    );
    return;
  }
  expect(describeShape(run.compiled)).toEqual(describeShape(run.prisma));
  expect(run.compiled).toStrictEqual(run.prisma);
}

export function expectCompiled(run: AggregateRun): CompiledReadEvent {
  expect(run.events.map((event) => `${event.outcome}:${event.reason ?? ''}`)).toEqual(['compiled:']);
  return run.events[0]!;
}

export function expectFellBack(run: AggregateRun, reason: string): void {
  expect(run.events).toEqual([expect.objectContaining({ outcome: 'fallback', reason })]);
  expectAgreed(run);
}

function values(run: AggregateRun): Record<string, any> {
  return run.compiled as Record<string, any>;
}

function rows(run: AggregateRun): Record<string, any>[] {
  return run.compiled as Record<string, any>[];
}

interface KeyType {
  readonly key: string;
  readonly groups: number;
  readonly holds: (value: unknown) => boolean;
}

const KEY_TYPES: readonly KeyType[] = [
  { key: 'recordedAt', groups: 5, holds: (value) => value instanceof Date },
  { key: 'hits', groups: 5, holds: (value) => typeof value === 'bigint' },
  {
    key: 'score',
    groups: 4,
    holds: (value) => value === null || (typeof value === 'object' && value !== null),
  },
  { key: 'ratio', groups: 5, holds: (value) => typeof value === 'number' },
  { key: 'label', groups: 5, holds: (value) => typeof value === 'string' },
];

export function aggregateOracleSuite(subject: () => AggregateSubject): void {
  const engineOn = (constraints: Record<string, unknown> = {}): GolemEngine =>
    engineFor(subject().client, subject().provider, constraints);

  const agree = async (
    request: Omit<AggregateRequest, 'context' | 'compiled'>,
    constraints: Record<string, unknown> = {},
  ): Promise<AggregateRun> => {
    const run = await runBothAggregate(engineOn(constraints), request);
    expectCompiled(run);
    expectAgreed(run);
    return run;
  };

  const agreeGrouped = async (
    request: Omit<GroupByRequest, 'context' | 'compiled'>,
    constraints: Record<string, unknown> = {},
  ): Promise<AggregateRun> => {
    const run = await runBothGroup(engineOn(constraints), request);
    expectCompiled(run);
    expectAgreed(run);
    return run;
  };

  it('counts every row and every non-null column identically on both paths', async () => {
    const scalar = await agree({ model: 'Metric', _count: true });
    expect(values(scalar)._count).toBe(5);

    const perField = await agree({
      model: 'Metric',
      _count: { _all: true, note: true, rank: true, score: true, label: true, recordedAt: true },
    });
    expect(values(perField)._count).toStrictEqual({
      _all: 5,
      note: 3,
      rank: 3,
      score: 3,
      label: 5,
      recordedAt: 5,
    });
  });

  it('sums an Int, a Float, a BigInt and a Decimal identically on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      _sum: { id: true, rank: true, ratio: true, hits: true, score: true },
    });
    const sum = values(run)._sum;
    expect(sum.id).toBe(15);
    expect(sum.rank).toBe(7);
    expect(typeof sum.rank).toBe('number');
    expect(sum.ratio).toBeCloseTo(7, 10);
    expect(sum.hits).toBe(43n);
    expect(typeof sum.hits).toBe('bigint');
    expect(sum.score.constructor.name).toBe(
      (run.prisma as Record<string, any>)._sum.score.constructor.name,
    );
  });

  it('averages an Int, a Float, a BigInt and a Decimal identically on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      _avg: { rank: true, ratio: true, hits: true, score: true },
    });
    const avg = values(run)._avg;
    expect(typeof avg.rank).toBe('number');
    expect(typeof avg.hits).toBe('number');
    expect(typeof avg.ratio).toBe('number');
  });

  it('holds the average of a non-Decimal column to the tolerance golem promises across engines', async () => {
    const run = await agree({ model: 'Metric', _avg: { rank: true, ratio: true, hits: true } });
    const avg = values(run)._avg;
    expect(Number.isInteger(avg.rank)).toBe(false);
    expect(Math.abs(avg.rank - 7 / 3)).toBeLessThanOrEqual(AVG_TOLERANCE * (7 / 3));
    expect(Math.abs(avg.ratio - 1.4)).toBeLessThanOrEqual(AVG_TOLERANCE * 1.4);
    expect(Math.abs(avg.hits - 8.6)).toBeLessThanOrEqual(AVG_TOLERANCE * 8.6);
  });

  it('takes the minimum and the maximum of every orderable column identically on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      _min: { id: true, rank: true, ratio: true, hits: true, score: true, label: true, recordedAt: true },
      _max: { id: true, rank: true, ratio: true, hits: true, score: true, label: true, recordedAt: true },
    });
    const { _min: min, _max: max } = values(run);
    expect(min.id).toBe(1);
    expect(max.id).toBe(5);
    expect(min.hits).toBe(-9007199254740993n);
    expect(max.hits).toBe(9007199254740993n);
    expect(min.recordedAt).toBeInstanceOf(Date);
    expect(min.recordedAt.toISOString()).toBe('2024-01-01T00:00:00.000Z');
    expect(max.recordedAt.toISOString()).toBe('2024-05-05T05:05:05.005Z');
    expect(typeof min.label).toBe('string');
    expect(String(max.score)).toBe(String((run.prisma as Record<string, any>)._max.score));
  });

  it('carries every measure of one request through a single compiled statement', async () => {
    const run = await agree({
      model: 'Metric',
      _count: { _all: true, note: true },
      _sum: { rank: true, hits: true },
      _avg: { rank: true },
      _min: { label: true, recordedAt: true },
      _max: { score: true },
    });
    expect(Object.keys(values(run)).sort()).toEqual(['_avg', '_count', '_max', '_min', '_sum']);
    expect(run.events[0]!.sql!.match(/count\(/gi)?.length).toBe(2);
  });

  it('returns nulls and zero counts over an empty set, on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      where: { id: -1 },
      _count: { _all: true, note: true },
      _sum: { rank: true, hits: true, score: true },
      _avg: { rank: true },
      _min: { label: true, recordedAt: true },
    });
    expect(values(run)._count).toStrictEqual({ _all: 0, note: 0 });
    expect(values(run)._sum).toStrictEqual({ rank: null, hits: null, score: null });
    expect(values(run)._min).toStrictEqual({ label: null, recordedAt: null });
  });

  it('aggregates only the rows the policy grants, and says so in the SQL', async () => {
    const scoped = await agree(
      { model: 'Metric', _count: { _all: true }, _sum: { rank: true } },
      { Metric: { ownerId: 2 } },
    );
    expect(values(scoped)._count._all).toBe(2);
    expect(scoped.events[0]!.sql).toContain('owner_id');

    const open = await agree({ model: 'Metric', _count: { _all: true } });
    expect(values(open)._count._all).toBe(5);
  });

  it('never lets a minimum escape the policy that hides the row holding it', async () => {
    const open = await agree({ model: 'Metric', _min: { label: true }, _max: { label: true } });
    expect(values(open)._min.label).toBe('alpha');

    const scoped = await agree(
      { model: 'Metric', _min: { label: true }, _max: { label: true } },
      { Metric: { ownerId: 3 } },
    );
    expect(values(scoped)._min.label).toBe('epsilon');
    expect(values(scoped)._max.label).toBe('epsilon');
  });

  it('answers a caller-controlled count through the policy, not around it', async () => {
    const denied = await agree(
      { model: 'Metric', where: { label: 'alpha' }, _count: { _all: true } },
      { Metric: { ownerId: 3 } },
    );
    expect(values(denied)._count._all).toBe(0);

    const granted = await agree(
      { model: 'Metric', where: { label: 'epsilon' }, _count: { _all: true } },
      { Metric: { ownerId: 3 } },
    );
    expect(values(granted)._count._all).toBe(1);
  });

  it('intersects the caller where with the policy predicate before it aggregates', async () => {
    const run = await agree(
      { model: 'Metric', where: { active: true }, _count: { _all: true }, _sum: { hits: true } },
      { Metric: { ownerId: { in: [2, 3] } } },
    );
    expect(values(run)._count._all).toBe(2);
    expect(values(run)._sum.hits).toBe(-9007199254740992n);
  });

  it('falls back to Prisma, and answers identically, when the aggregate carries a window', async () => {
    const engine = engineOn();
    expectFellBack(
      await runBothAggregate(engine, { model: 'Metric', _count: { _all: true }, take: 2 }),
      'take',
    );
    expectFellBack(
      await runBothAggregate(engine, { model: 'Metric', _count: { _all: true }, skip: 1 }),
      'take',
    );
    expectFellBack(
      await runBothAggregate(engine, {
        model: 'Metric',
        _count: { _all: true },
        orderBy: { id: 'asc' },
      }),
      'take',
    );
    expectFellBack(
      await runBothAggregate(engine, { model: 'Metric', _count: { _all: true }, cursor: { id: 2 } }),
      'cursor',
    );
  });

  it('falls back to Prisma, and answers identically, over a column it cannot order', async () => {
    expectFellBack(
      await runBothAggregate(engineOn(), { model: 'Metric', _min: { active: true } }),
      'measure',
    );
  });

  it('falls back to Prisma when the read policy is a flat denial', async () => {
    const run = await runBothAggregate(engineOn({ Metric: null }), {
      model: 'Metric',
      _count: { _all: true },
    });
    expectFellBack(run, 'where');
  });

  it('groups by one key and counts each group identically on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      orderBy: { ownerId: 'asc' },
    });
    expect(rows(run)).toStrictEqual([
      { ownerId: 1, _count: { _all: 2 } },
      { ownerId: 2, _count: { _all: 2 } },
      { ownerId: 3, _count: { _all: 1 } },
    ]);
  });

  it('groups with a scalar count identically on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: true,
      orderBy: { ownerId: 'asc' },
    });
    expect(rows(run)).toStrictEqual([
      { ownerId: 1, _count: 2 },
      { ownerId: 2, _count: 2 },
      { ownerId: 3, _count: 1 },
    ]);
  });

  it('counts a null grouping key as its own group, and counts the key itself as zero there', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['rank'],
      _count: { _all: true, rank: true, note: true },
      orderBy: { rank: 'asc' },
    });
    const nulled = rows(run).find((row) => row.rank === null);
    expect(nulled).toStrictEqual({ rank: null, _count: { _all: 2, rank: 0, note: 2 } });
    expect(rows(run)).toHaveLength(4);
  });

  it('carries a DateTime, a BigInt, a Decimal, a Float and a String grouping key out as Prisma carries it', async () => {
    const engine = engineOn();
    for (const { key, groups, holds } of KEY_TYPES) {
      const run = await runBothGroup(engine, {
        model: 'Metric',
        by: [key],
        _count: { _all: true },
        orderBy: { [key]: 'asc' },
      });
      expectCompiled(run);
      expectAgreed(run);
      expect(rows(run)).toHaveLength(groups);
      for (const row of rows(run)) {
        expect(Object.keys(row).sort()).toEqual(['_count', key].sort());
        expect(holds(row[key])).toBe(true);
      }
    }
  });

  it('groups by a Decimal key, counting the rows that carry no Decimal as their own group', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['score'],
      _count: { _all: true, score: true },
      orderBy: { score: 'asc' },
    });
    const nulled = rows(run).find((row) => row.score === null);
    expect(nulled).toStrictEqual({ score: null, _count: { _all: 2, score: 0 } });
    const scored = rows(run).filter((row) => row.score !== null);
    expect(scored).toHaveLength(3);
    expect(scored[0]!.score.constructor).toBe(
      (run.prisma as Record<string, any>[]).find((row) => row.score !== null)!.score.constructor,
    );
  });

  it('groups by two keys identically on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId', 'active'],
      _count: { _all: true },
      orderBy: [{ ownerId: 'asc' }, { active: 'asc' }],
    });
    expect(rows(run)).toHaveLength(5);
    expect(rows(run)[0]).toStrictEqual({ ownerId: 1, active: false, _count: { _all: 1 } });
  });

  it('carries every measure through a grouped statement identically on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      _sum: { hits: true, rank: true, ratio: true, score: true },
      _avg: { rank: true, hits: true },
      _min: { label: true, recordedAt: true },
      _max: { score: true },
      orderBy: { ownerId: 'asc' },
    });
    expect(rows(run)[0]!._sum.hits).toBe(9007199254740993n);
    expect(rows(run)[2]!._sum.score).toBeNull();
    expect(rows(run)[0]!._min.recordedAt).toBeInstanceOf(Date);
  });

  it('filters groups by a count in having, on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      having: { ownerId: { _count: { gt: 1 } } },
      orderBy: { ownerId: 'asc' },
    });
    expect(rows(run).map((row) => row.ownerId)).toEqual([1, 2]);
  });

  it('filters groups by a sum in having, on both paths', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _sum: { rank: true },
      having: { rank: { _sum: { gt: 1 } } },
      orderBy: { ownerId: 'asc' },
    });
    expect(rows(run).map((row) => row.ownerId)).toEqual([1, 2]);
  });

  it('filters groups by a DateTime measure against a Date operand, on both paths', async () => {
    const engine = engineOn();
    const from = await runBothGroup(engine, {
      model: 'Metric',
      by: ['ownerId'],
      _min: { recordedAt: true },
      having: { recordedAt: { _min: { gte: new Date('2024-03-01T00:00:00.000Z') } } },
      orderBy: { ownerId: 'asc' },
    });
    expectCompiled(from);
    expectAgreed(from);
    expect(rows(from).map((row) => row.ownerId)).toEqual([2, 3]);

    const exact = await runBothGroup(engine, {
      model: 'Metric',
      by: ['ownerId'],
      _min: { recordedAt: true },
      having: { recordedAt: { _min: { equals: new Date('2024-05-05T05:05:05.005Z') } } },
      orderBy: { ownerId: 'asc' },
    });
    expectCompiled(exact);
    expectAgreed(exact);
    expect(rows(exact).map((row) => row.ownerId)).toEqual([3]);
  });

  it('filters groups by a Decimal sum against a number operand, on both paths', async () => {
    const engine = engineOn();
    const above = await runBothGroup(engine, {
      model: 'Metric',
      by: ['ownerId'],
      _sum: { score: true },
      having: { score: { _sum: { gt: 11 } } },
      orderBy: { ownerId: 'asc' },
    });
    expectCompiled(above);
    expectAgreed(above);
    expect(rows(above).map((row) => row.ownerId)).toEqual([1]);

    const within = await runBothGroup(engine, {
      model: 'Metric',
      by: ['ownerId'],
      _sum: { score: true },
      having: { score: { _sum: { lte: 11 } } },
      orderBy: { ownerId: 'asc' },
    });
    expectCompiled(within);
    expectAgreed(within);
    expect(rows(within).map((row) => row.ownerId)).toEqual([2]);
  });

  it('compiles every having operator golem offers, agreeing with Prisma on each', async () => {
    const engine = engineOn();
    for (const having of [
      { ownerId: { _count: { equals: 2 } } },
      { ownerId: { _count: { not: 2 } } },
      { ownerId: { _count: { in: [1, 2] } } },
      { ownerId: { _count: { notIn: [1] } } },
      { ownerId: { _count: { lt: 2 } } },
      { ownerId: { _count: { lte: 1 } } },
      { ownerId: { _count: { gte: 2 } } },
      { rank: { _sum: { gte: 2 } } },
      { rank: { _sum: { not: 4 } } },
      { rank: { _sum: { notIn: [4] } } },
      { rank: { _sum: { in: [2, 4] } } },
      { rank: { _sum: { equals: 4 } } },
      { rank: { _sum: { lt: 4 } } },
      { rank: { _sum: { equals: null } } },
      { rank: { _sum: { not: null } } },
      { rank: { _min: { lt: 3 } } },
      { rank: { _max: { gte: 2 } } },
      { ratio: { _avg: { gt: 0 } } },
      { label: { _count: { gte: 1 } } },
      { recordedAt: { _count: { gt: 0 } } },
      { AND: [{ ownerId: { _count: { gte: 1 } } }, { rank: { _sum: { gt: 1 } } }] },
      { OR: [{ ownerId: { _count: { gt: 5 } } }, { rank: { _sum: { gte: 2 } } }] },
      { NOT: { ownerId: { _count: { gt: 1 } } } },
    ]) {
      const run = await runBothGroup(engine, {
        model: 'Metric',
        by: ['ownerId'],
        _count: { _all: true },
        _sum: { rank: true },
        having,
        orderBy: { ownerId: 'asc' },
      });
      expectCompiled(run);
      expectAgreed(run);
    }
  });

  it('orders groups by an aggregate, on both paths', async () => {
    const byCount = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      orderBy: [{ _count: { ownerId: 'desc' } }, { ownerId: 'asc' }],
    });
    expect(rows(byCount).map((row) => row.ownerId)).toEqual([1, 2, 3]);

    const bySum = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _sum: { hits: true },
      orderBy: [{ _sum: { hits: 'desc' } }],
    });
    expect(rows(bySum).map((row) => row.ownerId)).toEqual([1, 3, 2]);
  });

  it('pages a grouped result the way Prisma pages it', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      orderBy: { ownerId: 'asc' },
      take: 2,
      skip: 1,
    });
    expect(rows(run).map((row) => row.ownerId)).toEqual([2, 3]);

    const skipped = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      orderBy: { ownerId: 'asc' },
      skip: 2,
    });
    expect(rows(skipped).map((row) => row.ownerId)).toEqual([3]);
  });

  it('stops at the take it was given, so a cap above it still sees the overflow', async () => {
    const run = await agreeGrouped({
      model: 'Metric',
      by: ['ownerId'],
      _count: { _all: true },
      orderBy: { ownerId: 'asc' },
      take: 2,
    });
    expect(rows(run)).toHaveLength(2);
  });

  it('groups only the rows the policy grants', async () => {
    const run = await agreeGrouped(
      {
        model: 'Metric',
        by: ['ownerId'],
        _count: { _all: true },
        _min: { label: true },
        orderBy: { ownerId: 'asc' },
      },
      { Metric: { active: true } },
    );
    expect(rows(run)).toStrictEqual([
      { ownerId: 1, _count: { _all: 1 }, _min: { label: 'alpha' } },
      { ownerId: 2, _count: { _all: 1 }, _min: { label: 'gamma' } },
      { ownerId: 3, _count: { _all: 1 }, _min: { label: 'epsilon' } },
    ]);
  });

  it('returns an empty list, not a null group, when the policy denies every row', async () => {
    const run = await agreeGrouped(
      { model: 'Metric', by: ['ownerId'], _count: { _all: true }, orderBy: { ownerId: 'asc' } },
      { Metric: { ownerId: -1 } },
    );
    expect(rows(run)).toEqual([]);
  });

  it('falls back to Prisma, and answers identically, when it is grouped by a relation', async () => {
    expectFellBack(
      await runBothGroup(engineOn(), {
        model: 'Metric',
        by: ['owner'],
        _count: { _all: true },
      }),
      'group',
    );
  });

  it('falls back to Prisma, and answers identically, on a having operator it does not compile', async () => {
    expectFellBack(
      await runBothGroup(engineOn(), {
        model: 'Metric',
        by: ['ownerId'],
        _count: { _all: true },
        _min: { label: true },
        having: { label: { _min: { contains: 'a' } } },
        orderBy: { ownerId: 'asc' },
      }),
      'having',
    );
  });

  it('falls back to Prisma, and answers identically, when it is ordered by a column it does not group by', async () => {
    expectFellBack(
      await runBothGroup(engineOn(), {
        model: 'Metric',
        by: ['ownerId'],
        _count: { _all: true },
        orderBy: { label: 'asc' },
      }),
      'orderBy',
    );
  });
}

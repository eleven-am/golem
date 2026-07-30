import { CompiledReadEvent } from '../../src/compiled-read';
import { FindManyRequest, FindOneRequest, GolemEngine } from '../../src/operations';
import { context, engineFor, metrics } from './analytics';

export interface OracleSubject {
  readonly provider: string;
  readonly client: Record<string, any>;
}

export interface OracleRun {
  readonly compiled: unknown;
  readonly prisma: unknown;
  readonly events: readonly CompiledReadEvent[];
}

function describeValue(value: unknown): string {
  if (value === null) {
    return 'null';
  }
  if (value === undefined) {
    return 'undefined';
  }
  if (typeof value === 'object') {
    const name = (value as object).constructor?.name ?? 'object';
    return `${name}(${String(value)})`;
  }
  return `${typeof value}(${String(value)})`;
}

export function describeShape(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(describeShape);
  }
  if (value !== null && typeof value === 'object' && (value as object).constructor === Object) {
    return Object.fromEntries(
      Object.keys(value as Record<string, unknown>)
        .sort()
        .map((key) => [key, describeShape((value as Record<string, unknown>)[key])]),
    );
  }
  return describeValue(value);
}

export function expectIdentical(run: OracleRun): void {
  expect(describeShape(run.compiled)).toEqual(describeShape(run.prisma));
  expect(run.compiled).toStrictEqual(run.prisma);
}

export async function runBothMany(
  engine: GolemEngine,
  request: Omit<FindManyRequest, 'context' | 'compiled'>,
): Promise<OracleRun> {
  const events: CompiledReadEvent[] = [];
  const release = engine.observeCompiledRead((event) => events.push(event));
  let compiled: unknown;
  try {
    compiled = await engine.findMany({ ...request, context, compiled: true });
  } finally {
    release();
  }
  const prisma = await engine.findMany({ ...request, context });
  return { compiled, prisma, events };
}

export async function runBothOne(
  engine: GolemEngine,
  request: Omit<FindOneRequest, 'context' | 'compiled'>,
): Promise<OracleRun> {
  const events: CompiledReadEvent[] = [];
  const release = engine.observeCompiledRead((event) => events.push(event));
  let compiled: unknown;
  try {
    compiled = await engine.findOne({ ...request, context, compiled: true });
  } finally {
    release();
  }
  const prisma = await engine.findOne({ ...request, context });
  return { compiled, prisma, events };
}

export function expectCompiled(run: OracleRun): CompiledReadEvent {
  expect(run.events.map((event) => `${event.outcome}:${event.reason ?? ''}`)).toEqual(['compiled:']);
  return run.events[0]!;
}

const METRIC_COLUMNS = {
  id: true,
  label: true,
  ownerId: true,
  note: true,
  rank: true,
  score: true,
  hits: true,
  ratio: true,
  active: true,
  recordedAt: true,
} as const;

export function oracleSuite(subject: () => OracleSubject): void {
  const engineOn = (constraints: Record<string, unknown> = {}): GolemEngine =>
    engineFor(subject().client, subject().provider, constraints);

  const agree = async (
    request: Omit<FindManyRequest, 'context' | 'compiled'>,
    constraints: Record<string, unknown> = {},
  ): Promise<OracleRun> => {
    const run = await runBothMany(engineOn(constraints), request);
    expectCompiled(run);
    expectIdentical(run);
    return run;
  };

  it('reads every scalar column of every row identically on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      select: { ...METRIC_COLUMNS },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled as unknown[]).toHaveLength(metrics.length);
  });

  it('reads a nullable column as null, present as a key, on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, note: true, rank: true, score: true },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as Record<string, unknown>[];
    expect(rows[1]).toStrictEqual({ id: 2, note: null, rank: 1, score: null });
    expect(Object.keys(rows[1]).sort()).toEqual(['id', 'note', 'rank', 'score']);
  });

  it('carries a BigInt past 2^53 through the compiled path without loss', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, hits: true },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as { id: number; hits: bigint }[];
    expect(rows[0].hits).toBe(9007199254740993n);
    expect(rows[2].hits).toBe(-9007199254740993n);
    expect(typeof rows[0].hits).toBe('bigint');
  });

  it('carries a Decimal through the compiled path exactly as Prisma carries it', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, score: true },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as { id: number; score: unknown }[];
    expect(String(rows[3].score)).toBe('10.5');
    expect(rows[0].score?.constructor.name).toBe(
      (run.prisma as { score: unknown }[])[0].score?.constructor.name,
    );
  });

  it('round-trips a DateTime through the compiled path to the millisecond', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, recordedAt: true },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as { recordedAt: Date }[];
    expect(rows[1].recordedAt).toBeInstanceOf(Date);
    expect(rows[1].recordedAt.toISOString()).toBe('2024-02-02T12:30:45.123Z');
  });

  it('reads a Float and a Boolean identically on both paths', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, ratio: true, active: true },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as { ratio: number; active: boolean }[];
    expect(rows[1].ratio).toBe(-0.25);
    expect(rows[1].active).toBe(false);
  });

  it('orders nulls the way this engine orders them, ascending', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: 'asc' }, { id: 'asc' }],
    });
    expect((run.compiled as { rank: number | null }[]).map((row) => row.rank)).toEqual(
      (run.prisma as { rank: number | null }[]).map((row) => row.rank),
    );
  });

  it('orders nulls the way this engine orders them, descending', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: 'desc' }, { id: 'asc' }],
    });
  });

  it('orders a nullable string column the way this engine orders it', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, note: true },
      orderBy: [{ note: 'asc' }, { id: 'asc' }],
    });
    await agree({
      model: 'Metric',
      select: { id: true, note: true },
      orderBy: [{ note: 'desc' }, { id: 'asc' }],
    });
  });

  it('orders by several columns in the order they were asked for', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, ownerId: true, active: true },
      orderBy: [{ ownerId: 'desc' }, { active: 'asc' }, { id: 'asc' }],
    });
  });

  it('takes the last page when take is negative', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      take: -2,
    });
    expect(run.compiled).toEqual([{ id: 4 }, { id: 5 }]);
  });

  it('takes the last page when take is negative and the order is descending', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'desc' }],
      take: -2,
    });
    expect(run.compiled).toEqual([{ id: 2 }, { id: 1 }]);
  });

  it('skips from the far end when take is negative and skip is set', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      take: -2,
      skip: 1,
    });
    expect(run.compiled).toEqual([{ id: 3 }, { id: 4 }]);
  });

  it('pages with take and skip', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, label: true },
      orderBy: [{ id: 'asc' }],
      take: 2,
      skip: 2,
    });
    expect(run.compiled).toEqual([
      { id: 3, label: 'gamma' },
      { id: 4, label: 'delta' },
    ]);
  });

  it('skips without taking', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      skip: 3,
    });
    expect(run.compiled).toEqual([{ id: 4 }, { id: 5 }]);
  });

  it('pages an unordered read to the same rows, guaranteeing nothing about their order', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { id: true },
      take: 2,
      skip: 1,
    });
    expectCompiled(run);
    const ids = (rows: unknown): number[] =>
      (rows as { id: number }[]).map((row) => row.id).sort((left, right) => left - right);
    expect(ids(run.compiled)).toEqual(ids(run.prisma));
    expect((run.compiled as unknown[]).length).toBe(2);
  });

  it('returns nothing for take zero', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      take: 0,
    });
    expect(run.compiled).toEqual([]);
  });

  it('returns an empty array, not null, when nothing matches', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      where: { label: 'nothing-is-labelled-this' },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([]);
  });

  it('filters on a scalar where identically on both paths', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, ownerId: true, rank: true },
      where: { ownerId: { in: [1, 2] }, rank: { not: null } },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('filters on a string operator identically on both paths', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, label: true },
      where: { OR: [{ label: { startsWith: 'a' } }, { label: { contains: 'elt' } }] },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('filters on null identically on both paths', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, note: true },
      where: { note: null },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('gives a policy-scoped caller a strict subset, with the predicate doing the filtering', async () => {
    const run = await agree(
      {
        model: 'Metric',
        select: { id: true, ownerId: true },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { ownerId: 2 } },
    );
    expect(run.compiled).toEqual([
      { id: 3, ownerId: 2 },
      { id: 4, ownerId: 2 },
    ]);
    expect(run.events[0]!.sql).toContain('owner_id');

    const unscoped = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
    });
    expect((unscoped.compiled as unknown[]).length).toBeGreaterThan(
      (run.compiled as unknown[]).length,
    );
  });

  it('intersects the caller where with the policy predicate', async () => {
    const run = await agree(
      {
        model: 'Metric',
        select: { id: true },
        where: { active: true },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { ownerId: { in: [2, 3] } } },
    );
    expect(run.compiled).toEqual([{ id: 3 }, { id: 5 }]);
  });

  it('reads a policy-scoped row through findOne identically on both paths', async () => {
    const engine = engineOn({ Metric: { ownerId: 2 } });
    const visible = await runBothOne(engine, {
      model: 'Metric',
      where: { id: 3 },
      select: { id: true, label: true, ownerId: true },
    });
    expectCompiled(visible);
    expectIdentical(visible);
    expect(visible.compiled).toEqual({ id: 3, label: 'gamma', ownerId: 2 });

    const denied = await runBothOne(engine, {
      model: 'Metric',
      where: { id: 1 },
      select: { id: true, label: true },
    });
    expectCompiled(denied);
    expectIdentical(denied);
    expect(denied.compiled).toBeNull();
  });

  it('reads a row through findOne with no policy at all identically on both paths', async () => {
    const run = await runBothOne(engineOn(), {
      model: 'Metric',
      where: { id: 5 },
      select: { ...METRIC_COLUMNS },
    });
    expectCompiled(run);
    expectIdentical(run);
  });

  it('returns null from findOne when the row is absent, on both paths', async () => {
    const run = await runBothOne(engineOn(), {
      model: 'Metric',
      where: { id: 4242 },
      select: { id: true },
    });
    expectCompiled(run);
    expectIdentical(run);
    expect(run.compiled).toBeNull();
  });

  it('reads the mapped columns of a mapped model identically on both paths', async () => {
    const run = await agree({
      model: 'Post',
      select: { id: true, title: true, authorId: true, published: true, views: true },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.events[0]!.sql).toContain('"posts"');
    expect(run.events[0]!.sql).toContain('author_id');
  });

  it('reads a to-one relation as a nested object on both paths', async () => {
    const run = await agree({
      model: 'Post',
      select: { id: true, author: { select: { name: true, tenantId: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, author: { name: 'Ada', tenantId: 1 } },
      { id: 2, author: { name: 'Ada', tenantId: 1 } },
      { id: 3, author: { name: 'Ada', tenantId: 1 } },
      { id: 4, author: { name: 'Bob', tenantId: 1 } },
      { id: 5, author: { name: 'Bob', tenantId: 1 } },
      { id: 6, author: { name: 'Cleo', tenantId: 2 } },
    ]);
  });

  it('reads a to-one that matches nothing as null on both paths', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, profile: { select: { bio: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, profile: { bio: 'writes about analysis' } },
      { id: 2, profile: { bio: 'writes about nothing' } },
      { id: 3, profile: null },
      { id: 4, profile: null },
    ]);
  });

  it('reads a to-many that matches nothing as an empty array on both paths', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect((run.compiled as { id: number; posts: unknown[] }[])[3]).toStrictEqual({
      id: 4,
      posts: [],
    });
  });

  it('carries every scalar type through a nested array exactly as Prisma carries it', async () => {
    const run = await agree({
      model: 'User',
      select: {
        id: true,
        metrics: {
          select: {
            id: true,
            hits: true,
            score: true,
            recordedAt: true,
            active: true,
            note: true,
            rank: true,
            ratio: true,
            label: true,
          },
        },
      },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as {
      metrics: {
        id: number;
        hits: bigint;
        score: unknown;
        recordedAt: Date;
        active: boolean;
        note: string | null;
        rank: number | null;
        ratio: number;
      }[];
    }[];
    const first = rows[0]!.metrics[0]!;
    expect(first.hits).toBe(9007199254740993n);
    expect(typeof first.hits).toBe('bigint');
    expect(rows[1]!.metrics[0]!.hits).toBe(-9007199254740993n);
    expect(String(rows[1]!.metrics[1]!.score)).toBe('10.5');
    expect(first.score).toStrictEqual(
      (run.prisma as { metrics: { score: unknown }[] }[])[0]!.metrics[0]!.score,
    );
    expect(first.recordedAt).toBeInstanceOf(Date);
    expect(first.recordedAt.toISOString()).toBe('2024-01-01T00:00:00.000Z');
    expect(rows[0]!.metrics[1]!.recordedAt.toISOString()).toBe('2024-02-02T12:30:45.123Z');
    expect(first.active).toBe(true);
    expect(rows[0]!.metrics[1]!.active).toBe(false);
    expect(rows[0]!.metrics[1]!.note).toBeNull();
    expect(rows[0]!.metrics[1]!.ratio).toBe(-0.25);
    expect(rows[1]!.metrics[0]!.rank).toBeNull();
  });

  it('reads two levels of relation identically on both paths', async () => {
    const run = await agree({
      model: 'User',
      select: {
        id: true,
        posts: { select: { id: true, author: { select: { name: true } } } },
      },
      orderBy: [{ id: 'asc' }],
    });
    expect((run.compiled as { posts: unknown[] }[])[0]!.posts).toEqual([
      { id: 1, author: { name: 'Ada' } },
      { id: 2, author: { name: 'Ada' } },
      { id: 3, author: { name: 'Ada' } },
    ]);
  });

  it('carries the nested policy predicate into the subquery, filtering it to a subset', async () => {
    const run = await agree(
      {
        model: 'User',
        select: { id: true, metrics: { select: { id: true, active: true } } },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { active: true } },
    );
    expect(run.compiled).toEqual([
      { id: 1, metrics: [{ id: 1, active: true }] },
      { id: 2, metrics: [{ id: 3, active: true }] },
      { id: 3, metrics: [{ id: 5, active: true }] },
      { id: 4, metrics: [] },
    ]);
    expect(run.events[0]!.sql).toContain('active');
  });

  it('filters a nested relation by the where the caller asked for, on both paths', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, where: { published: false } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, posts: [{ id: 3 }] },
      { id: 2, posts: [] },
      { id: 3, posts: [] },
      { id: 4, posts: [] },
    ]);
  });

  it('intersects the caller where on a relation with the policy predicate on it', async () => {
    const run = await agree(
      {
        model: 'User',
        select: { id: true, posts: { select: { id: true }, where: { views: { gte: 20 } } } },
        orderBy: [{ id: 'asc' }],
      },
      { Post: { published: true } },
    );
    expect(run.compiled).toEqual([
      { id: 1, posts: [{ id: 2 }] },
      { id: 2, posts: [{ id: 4 }, { id: 5 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);
  });

  it('empties a nested relation the policy denies entirely, on both paths', async () => {
    const run = await agree(
      {
        model: 'User',
        select: { id: true, metrics: { select: { id: true } } },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { ownerId: -1 } },
    );
    expect(run.compiled).toEqual([
      { id: 1, metrics: [] },
      { id: 2, metrics: [] },
      { id: 3, metrics: [] },
      { id: 4, metrics: [] },
    ]);
  });

  it('carries the nested policy predicate two levels down, under a relation the policy leaves alone', async () => {
    const request: Omit<FindManyRequest, 'context' | 'compiled'> = {
      model: 'Post',
      select: {
        id: true,
        author: { select: { id: true, metrics: { select: { id: true, active: true } } } },
      },
      orderBy: [{ id: 'asc' }],
    };
    const run = await agree(request, { Metric: { active: true } });
    const expected = [
      { id: 1, author: { id: 1, metrics: [{ id: 1, active: true }] } },
      { id: 2, author: { id: 1, metrics: [{ id: 1, active: true }] } },
      { id: 3, author: { id: 1, metrics: [{ id: 1, active: true }] } },
      { id: 4, author: { id: 2, metrics: [{ id: 3, active: true }] } },
      { id: 5, author: { id: 2, metrics: [{ id: 3, active: true }] } },
      { id: 6, author: { id: 3, metrics: [{ id: 5, active: true }] } },
    ];
    expect(run.compiled).toEqual(expected);
    expect(run.prisma).toEqual(expected);

    const unscoped = await runBothMany(engineOn(), request);
    expectCompiled(unscoped);
    const nested = (rows: unknown): number[] =>
      (rows as { author: { metrics: { id: number }[] } }[])[0]!.author.metrics
        .map((metric) => metric.id)
        .sort((left, right) => left - right);
    expect(nested(unscoped.compiled)).toEqual([1, 2]);
    expect(nested(unscoped.prisma)).toEqual([1, 2]);
  });

  it('carries the nested policy predicate three levels down, on both paths', async () => {
    const run = await agree(
      {
        model: 'User',
        select: {
          id: true,
          posts: { select: { id: true, author: { select: { metrics: { select: { id: true } } } } } },
        },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { active: true } },
    );
    const expected = [
      {
        id: 1,
        posts: [
          { id: 1, author: { metrics: [{ id: 1 }] } },
          { id: 2, author: { metrics: [{ id: 1 }] } },
          { id: 3, author: { metrics: [{ id: 1 }] } },
        ],
      },
      {
        id: 2,
        posts: [
          { id: 4, author: { metrics: [{ id: 3 }] } },
          { id: 5, author: { metrics: [{ id: 3 }] } },
        ],
      },
      { id: 3, posts: [{ id: 6, author: { metrics: [{ id: 5 }] } }] },
      { id: 4, posts: [] },
    ];
    expect(run.compiled).toEqual(expected);
    expect(run.prisma).toEqual(expected);
  });

  it('KNOWN SHARP EDGE, pinned rather than endorsed: a nested constraint of null or false reads as no constraint at all and grants the whole relation on both paths', async () => {
    const request: Omit<FindManyRequest, 'context' | 'compiled'> = {
      model: 'User',
      select: { id: true, metrics: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
    };
    const everyMetric = [
      { id: 1, metrics: [{ id: 1 }, { id: 2 }] },
      { id: 2, metrics: [{ id: 3 }, { id: 4 }] },
      { id: 3, metrics: [{ id: 5 }] },
      { id: 4, metrics: [] },
    ];

    const nulled = await agree(request, { Metric: null });
    expect(nulled.compiled).toEqual(everyMetric);
    expect(nulled.prisma).toEqual(everyMetric);

    const denied = await agree(request, { Metric: false });
    expect(denied.compiled).toEqual(everyMetric);
    expect(denied.prisma).toEqual(everyMetric);
  });

  it('nulls a to-one the policy denies and strips what it hydrated, on both paths', async () => {
    const run = await agree(
      {
        model: 'Post',
        select: { id: true, author: { select: { name: true } } },
        orderBy: [{ id: 'asc' }],
      },
      { User: { tenantId: 2 } },
    );
    expect(run.compiled).toEqual([
      { id: 1, author: null },
      { id: 2, author: null },
      { id: 3, author: null },
      { id: 4, author: null },
      { id: 5, author: null },
      { id: 6, author: { name: 'Cleo' } },
    ]);
    const visible = (run.compiled as { author: Record<string, unknown> | null }[])[5]!.author!;
    expect(Object.keys(visible)).toEqual(['name']);
  });

  it('nulls a to-one two levels down and strips what it hydrated there', async () => {
    const run = await agree(
      {
        model: 'Post',
        select: {
          id: true,
          author: { select: { name: true, profile: { select: { bio: true } } } },
        },
        orderBy: [{ id: 'asc' }],
      },
      { Profile: { userId: 1 } },
    );
    const rows = run.compiled as {
      author: { name: string; profile: Record<string, unknown> | null };
    }[];
    expect(rows[0]!.author.profile).toEqual({ bio: 'writes about analysis' });
    expect(Object.keys(rows[0]!.author.profile!)).toEqual(['bio']);
    expect(rows[3]!.author.profile).toBeNull();
    expect(rows[5]!.author.profile).toBeNull();
  });

  it('reads a nested relation through include identically on both paths', async () => {
    const run = await agree({
      model: 'Post',
      omit: { secretNote: true },
      include: { author: { select: { name: true } } },
      orderBy: [{ id: 'asc' }],
    });
    const rows = run.compiled as Record<string, unknown>[];
    expect(Object.keys(rows[0]!).sort()).toEqual([
      'author',
      'authorId',
      'id',
      'published',
      'title',
      'views',
    ]);
  });

  it('falls back to Prisma, and answers identically, when a nested relation is ordered', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'User',
      select: { id: true, posts: { select: { id: true }, orderBy: { id: 'desc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'orderBy' }),
    ]);
    expectIdentical(run);
  });

  it('falls back to Prisma, and answers identically, when a nested relation is paged', async () => {
    const take = await runBothMany(engineOn(), {
      model: 'User',
      select: { id: true, posts: { select: { id: true }, take: 1 } },
      orderBy: [{ id: 'asc' }],
    });
    expect(take.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'take' }),
    ]);
    expectIdentical(take);

    const skip = await runBothMany(engineOn(), {
      model: 'User',
      select: { id: true, posts: { select: { id: true }, skip: 1 } },
      orderBy: [{ id: 'asc' }],
    });
    expect(skip.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'take' }),
    ]);
    expectIdentical(skip);
  });

  it('falls back to Prisma, and answers identically, when a nested relation is distinct', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'User',
      select: { id: true, posts: { select: { published: true }, distinct: ['published'] } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'distinct' }),
    ]);
    expectIdentical(run);
  });

  it('compiles a relation filter in the where and answers identically', async () => {
    const run = await agree({
      model: 'Post',
      select: { id: true, title: true },
      where: { author: { is: { name: 'Ada' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.events[0]!.sql).toContain('EXISTS');
    expect(run.compiled).toEqual([
      { id: 1, title: 'a1' },
      { id: 2, title: 'a2' },
      { id: 3, title: 'a3' },
    ]);
  });

  it('falls back to Prisma, and answers identically, when a cursor is asked for', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
      take: 2,
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'cursor' }),
    ]);
    expectIdentical(run);
    expect(run.compiled).toEqual([{ id: 3 }, { id: 4 }]);
  });

  it('falls back to Prisma, and answers identically, when distinct is asked for', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { ownerId: true },
      orderBy: [{ ownerId: 'asc' }],
      distinct: ['ownerId'],
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'distinct' }),
    ]);
    expectIdentical(run);
  });

  it('falls back to Prisma when take is negative and nothing orders the rows', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { id: true },
      take: -2,
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'take' }),
    ]);
    expectIdentical(run);
  });

  it('hands a flat denial to Prisma, which rejects it on both paths alike', async () => {
    const engine = engineOn({ Metric: null });
    const events: CompiledReadEvent[] = [];
    const release = engine.observeCompiledRead((event) => events.push(event));
    const compiled = await engine
      .findMany({ model: 'Metric', select: { id: true }, context, compiled: true })
      .then(() => null, (error: Error) => error);
    release();
    const prisma = await engine
      .findMany({ model: 'Metric', select: { id: true }, context })
      .then(() => null, (error: Error) => error);

    expect(events).toEqual([expect.objectContaining({ outcome: 'fallback', reason: 'where' })]);
    expect(compiled).toBeInstanceOf(Error);
    expect(prisma).toBeInstanceOf(Error);
    expect((compiled as Error).constructor).toBe((prisma as Error).constructor);
  });
}

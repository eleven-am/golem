import { CompiledReadEvent } from '../../src/compiled-read';
import { FindManyRequest, FindOneRequest, GolemEngine } from '../../src/operations';
import {
  FieldMaskSpec,
  context,
  engineFor,
  maskingEngineFor,
  metrics,
} from './analytics';

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

export function statements(run: OracleRun): readonly string[] {
  return expectCompiled(run).statements ?? [];
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

  it('counts a to-many relation identically on both paths, without reading its rows', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, _count: { select: { posts: true, metrics: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, _count: { posts: 3, metrics: 2 } },
      { id: 2, _count: { posts: 2, metrics: 2 } },
      { id: 3, _count: { posts: 1, metrics: 1 } },
      { id: 4, _count: { posts: 0, metrics: 0 } },
    ]);
    expect(statements(run)).toHaveLength(1);
    expect(statements(run)[0]).toContain('count(*)');
    const counted = (run.compiled as { _count: Record<string, number> }[])[0]!._count;
    expect(Object.values(counted).every((value) => typeof value === 'number')).toBe(true);
  });

  it('counts a relation the policy narrows, to the subset the caller may read, on both paths', async () => {
    const scoped = await agree(
      {
        model: 'User',
        select: { id: true, _count: { select: { posts: true } } },
        orderBy: [{ id: 'asc' }],
      },
      { Post: { published: true } },
    );
    expect(scoped.compiled).toEqual([
      { id: 1, _count: { posts: 2 } },
      { id: 2, _count: { posts: 2 } },
      { id: 3, _count: { posts: 1 } },
      { id: 4, _count: { posts: 0 } },
    ]);
    expect(statements(scoped)[0]).toContain('published');

    const unscoped = await agree({
      model: 'User',
      select: { id: true, _count: { select: { posts: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect((unscoped.compiled as { _count: { posts: number } }[])[0]!._count.posts).toBe(3);
  });

  it('counts nothing for a caller the policy allows nothing, on both paths', async () => {
    const run = await agree(
      {
        model: 'User',
        select: { id: true, _count: { select: { posts: true, metrics: true } } },
        orderBy: [{ id: 'asc' }],
      },
      { Post: { authorId: -1 }, Metric: { ownerId: -1 } },
    );
    expect(run.compiled).toEqual([
      { id: 1, _count: { posts: 0, metrics: 0 } },
      { id: 2, _count: { posts: 0, metrics: 0 } },
      { id: 3, _count: { posts: 0, metrics: 0 } },
      { id: 4, _count: { posts: 0, metrics: 0 } },
    ]);
  });

  it('counts a relation it also reads, agreeing with the rows it read, on both paths', async () => {
    const run = await agree(
      {
        model: 'User',
        select: {
          id: true,
          posts: { select: { id: true } },
          _count: { select: { posts: true } },
        },
        orderBy: [{ id: 'asc' }],
      },
      { Post: { published: true } },
    );
    for (const row of run.compiled as { posts: unknown[]; _count: { posts: number } }[]) {
      expect(row._count.posts).toBe(row.posts.length);
    }
  });

  it('counts a relation through findOne identically on both paths', async () => {
    const run = await runBothOne(engineOn({ Post: { published: true } }), {
      model: 'User',
      where: { id: 1 },
      select: { id: true, _count: { select: { posts: true } } },
    });
    expectCompiled(run);
    expectIdentical(run);
    expect(run.compiled).toEqual({ id: 1, _count: { posts: 2 } });
  });

  it('counts a relation beside a paged, ordered read of the parent, on both paths', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, _count: { select: { posts: true } } },
      orderBy: [{ id: 'desc' }],
      take: 2,
      skip: 1,
    });
    expect(run.compiled).toEqual([
      { id: 3, _count: { posts: 1 } },
      { id: 2, _count: { posts: 2 } },
    ]);
  });

  it('falls back rather than compiling a count under a relation, and still agrees', async () => {
    const run = await runBothMany(engineOn({ Post: { published: true } }), {
      model: 'Post',
      select: { id: true, author: { select: { id: true, _count: { select: { posts: true } } } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.events.map((event) => `${event.outcome}:${event.reason ?? ''}`)).toEqual([
      'fallback:relation',
    ]);
    expectIdentical(run);
    expect((run.compiled as { author: { _count: { posts: number } } }[])[0]!.author._count.posts).toBe(2);
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
    expect(statements(run).join(' ')).toContain('active');
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

  it('orders a nested relation the way Prisma orders it, per parent', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, orderBy: { id: 'desc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, posts: [{ id: 3 }, { id: 2 }, { id: 1 }] },
      { id: 2, posts: [{ id: 5 }, { id: 4 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);
  });

  it('orders a nested relation by several columns, in the order they were asked for', async () => {
    await agree({
      model: 'User',
      select: {
        id: true,
        posts: { select: { id: true }, orderBy: [{ published: 'asc' }, { id: 'desc' }] },
      },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('pages a nested relation per parent, not across the whole read', async () => {
    const take = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, take: 1 } },
      orderBy: [{ id: 'asc' }],
    });
    expect(take.compiled).toEqual([
      { id: 1, posts: [{ id: 1 }] },
      { id: 2, posts: [{ id: 4 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);

    const skip = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, skip: 1 } },
      orderBy: [{ id: 'asc' }],
    });
    expect(skip.compiled).toEqual([
      { id: 1, posts: [{ id: 2 }, { id: 3 }] },
      { id: 2, posts: [{ id: 5 }] },
      { id: 3, posts: [] },
      { id: 4, posts: [] },
    ]);

    const both = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, skip: 1, take: 2, orderBy: { id: 'desc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(both.compiled).toEqual([
      { id: 1, posts: [{ id: 2 }, { id: 1 }] },
      { id: 2, posts: [{ id: 4 }] },
      { id: 3, posts: [] },
      { id: 4, posts: [] },
    ]);
  });

  it('takes the last rows of each parent when the nested take is negative', async () => {
    const ordered = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, take: -2, orderBy: { id: 'asc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(ordered.compiled).toEqual([
      { id: 1, posts: [{ id: 2 }, { id: 3 }] },
      { id: 2, posts: [{ id: 4 }, { id: 5 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);

    const implied = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, take: -1 } },
      orderBy: [{ id: 'asc' }],
    });
    expect(implied.compiled).toEqual([
      { id: 1, posts: [{ id: 3 }] },
      { id: 2, posts: [{ id: 5 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);
  });

  it('empties a nested relation taken zero rows deep, and one skipped past its end', async () => {
    const zero = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, take: 0 } },
      orderBy: [{ id: 'asc' }],
    });
    expect((zero.compiled as { posts: unknown[] }[]).every((row) => row.posts.length === 0)).toBe(
      true,
    );

    const past = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true }, skip: 9 } },
      orderBy: [{ id: 'asc' }],
    });
    expect((past.compiled as { posts: unknown[] }[]).every((row) => row.posts.length === 0)).toBe(
      true,
    );
  });

  it('pages a nested relation the caller also narrowed with a where', async () => {
    const run = await agree({
      model: 'User',
      select: {
        id: true,
        posts: { select: { id: true }, where: { published: true }, take: 1 },
      },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 1, posts: [{ id: 1 }] },
      { id: 2, posts: [{ id: 4 }] },
      { id: 3, posts: [{ id: 6 }] },
      { id: 4, posts: [] },
    ]);
  });

  it('pages a relation it loads in a second statement, per parent and not across the batch', async () => {
    const forwards = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true }, take: 1, orderBy: { id: 'asc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(forwards)).toHaveLength(2);
    expect(forwards.compiled).toEqual([
      { id: 1, metrics: [{ id: 1 }] },
      { id: 2, metrics: [{ id: 3 }] },
      { id: 3, metrics: [{ id: 5 }] },
      { id: 4, metrics: [] },
    ]);

    const backwards = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true }, take: -1, orderBy: { id: 'asc' } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(backwards.compiled).toEqual([
      { id: 1, metrics: [{ id: 2 }] },
      { id: 2, metrics: [{ id: 4 }] },
      { id: 3, metrics: [{ id: 5 }] },
      { id: 4, metrics: [] },
    ]);

    await agree({
      model: 'User',
      select: {
        id: true,
        metrics: { select: { id: true }, skip: 1, take: 2, orderBy: { id: 'desc' } },
      },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('pages a nested relation the policy also narrows', async () => {
    await agree(
      {
        model: 'User',
        select: { id: true, metrics: { select: { id: true }, take: 1, orderBy: { id: 'desc' } } },
        orderBy: [{ id: 'asc' }],
      },
      { Metric: { active: true } },
    );
  });

  it('pages a nested relation two levels down, under a to-one', async () => {
    await agree({
      model: 'Post',
      select: {
        id: true,
        author: {
          select: { metrics: { select: { id: true }, orderBy: { id: 'desc' }, take: 1 } },
        },
      },
      orderBy: [{ id: 'asc' }],
    });
  });

  it('pages a nested relation of a nested relation', async () => {
    await agree({
      model: 'User',
      select: {
        id: true,
        posts: {
          select: { id: true, author: { select: { metrics: { select: { id: true }, take: 1 } } } },
          take: 2,
          orderBy: { id: 'desc' },
        },
      },
      orderBy: [{ id: 'asc' }],
    });
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

  it('starts a cursored read at the cursor row itself, which it keeps', async () => {
    const whole = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
    });
    expect(whole.compiled).toEqual([{ id: 3 }, { id: 4 }, { id: 5 }]);

    const taken = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
      take: 2,
    });
    expect(taken.compiled).toEqual([{ id: 3 }, { id: 4 }]);
  });

  it('skips past the cursor row when a cursored read also skips', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
      skip: 1,
      take: 2,
    });
    expect(run.compiled).toEqual([{ id: 4 }, { id: 5 }]);
  });

  it('walks backwards from the cursor row, keeping it, when the cursored take is negative', async () => {
    const back = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
      take: -2,
    });
    expect(back.compiled).toEqual([{ id: 2 }, { id: 3 }]);

    const skipped = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 3 },
      skip: 1,
      take: -2,
    });
    expect(skipped.compiled).toEqual([{ id: 1 }, { id: 2 }]);
  });

  it('walks a descending cursored read the way the order runs', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'desc' }],
      cursor: { id: 3 },
      take: 2,
    });
    expect(run.compiled).toEqual([{ id: 3 }, { id: 2 }]);
  });

  it('positions a cursored read with no order of its own by the primary key', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      cursor: { id: 3 },
      take: 2,
    });
    expect(run.compiled).toEqual([{ id: 3 }, { id: 4 }]);
  });

  it('positions a cursor across several ordering columns', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, ownerId: true, active: true },
      orderBy: [{ ownerId: 'asc' }, { active: 'desc' }, { id: 'asc' }],
      cursor: { id: 3 },
      take: 3,
    });
    await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ ownerId: 'desc' }, { id: 'desc' }],
      cursor: { id: 3 },
      take: 3,
    });
  });

  it('falls back to Prisma, and answers identically, when a cursor rests on a nullable order', async () => {
    const run = await runBothMany(engineOn(), {
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: 'asc' }, { id: 'asc' }],
      cursor: { id: 3 },
      take: 3,
    });
    expect(run.events).toEqual([
      expect.objectContaining({ outcome: 'fallback', reason: 'cursor' }),
    ]);
    expectIdentical(run);
  });

  it('positions a cursor on a unique column that is not the primary key', async () => {
    await agree({
      model: 'Profile',
      select: { id: true, userId: true },
      orderBy: [{ id: 'asc' }],
      cursor: { userId: 2 },
    });
  });

  it('returns nothing at all from a cursor no row answers', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 4242 },
      take: 2,
    });
    expect(run.compiled).toEqual([]);
  });

  it('positions a cursor at a row the where itself excludes', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      where: { active: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 2 },
      take: 3,
    });
    expect(run.compiled).toEqual([{ id: 3 }, { id: 5 }]);
  });

  it('positions a cursor at a row the policy hides', async () => {
    await agree(
      {
        model: 'Metric',
        select: { id: true },
        orderBy: [{ id: 'asc' }],
        cursor: { id: 2 },
        take: 3,
      },
      { Metric: { ownerId: 2 } },
    );
  });

  it('reads a cursored page of a relation-bearing row identically on both paths', async () => {
    await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 2 },
      take: 2,
    });
  });

  it('keeps the first row of each distinct group, in the order the read runs', async () => {
    const ascending = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
    });
    expect(ascending.compiled).toEqual([
      { id: 1, ownerId: 1 },
      { id: 3, ownerId: 2 },
      { id: 5, ownerId: 3 },
    ]);

    const descending = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'desc' }],
      distinct: ['ownerId'],
    });
    expect(descending.compiled).toEqual([
      { id: 5, ownerId: 3 },
      { id: 4, ownerId: 2 },
      { id: 2, ownerId: 1 },
    ]);
  });

  it('pages a distinct read after it has deduplicated, not before', async () => {
    const taken = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
      take: 2,
    });
    expect(taken.compiled).toEqual([
      { id: 1, ownerId: 1 },
      { id: 3, ownerId: 2 },
    ]);

    const skipped = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
      skip: 1,
    });
    expect(skipped.compiled).toEqual([
      { id: 3, ownerId: 2 },
      { id: 5, ownerId: 3 },
    ]);

    const backwards = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
      take: -2,
    });
    expect(backwards.compiled).toEqual([
      { id: 4, ownerId: 2 },
      { id: 5, ownerId: 3 },
    ]);
  });

  it('deduplicates on a column the caller never selected, and does not return it', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
    });
    expect(run.compiled).toEqual([{ id: 1 }, { id: 3 }, { id: 5 }]);
    expect(Object.keys((run.compiled as Record<string, unknown>[])[0]!)).toEqual(['id']);
  });

  it('treats null as a value of its own when it deduplicates', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, note: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['note'],
    });
    expect(run.compiled).toEqual([
      { id: 1, note: 'first' },
      { id: 2, note: null },
      { id: 3, note: 'third' },
      { id: 5, note: 'fifth' },
    ]);
  });

  it('deduplicates on several columns at once, and on columns of every scalar type', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, ownerId: true, active: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId', 'active'],
    });
    await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['hits'],
    });
    await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['score'],
    });
    await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['recordedAt'],
    });
    await agree({
      model: 'Metric',
      select: { id: true },
      orderBy: [{ id: 'asc' }],
      distinct: ['ratio'],
    });
  });

  it('deduplicates a cursored read after the cursor has cut it', async () => {
    const run = await agree({
      model: 'Metric',
      select: { id: true, ownerId: true },
      orderBy: [{ id: 'asc' }],
      cursor: { id: 2 },
      distinct: ['ownerId'],
    });
    expect(run.compiled).toEqual([
      { id: 2, ownerId: 1 },
      { id: 3, ownerId: 2 },
      { id: 5, ownerId: 3 },
    ]);
  });

  it('deduplicates a read that also reaches a relation', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, owner: { select: { name: true } } },
      orderBy: [{ id: 'asc' }],
      distinct: ['ownerId'],
    });
  });

  it('orders by a column of a to-one relation, the way Prisma joins to it', async () => {
    const run = await agree({
      model: 'Post',
      select: { id: true },
      orderBy: [{ author: { name: 'desc' } }, { id: 'asc' }],
    });
    expect(run.compiled).toEqual([
      { id: 6 },
      { id: 4 },
      { id: 5 },
      { id: 1 },
      { id: 2 },
      { id: 3 },
    ]);
    expect(statements(run)[0]).toContain('left join');
  });

  it('orders by a column two relations away', async () => {
    await agree({
      model: 'Post',
      select: { id: true },
      orderBy: [{ author: { profile: { bio: 'desc' } } }, { id: 'asc' }],
    });
  });

  it('orders by a nullable column of a relation no row reaches', async () => {
    await agree({
      model: 'User',
      select: { id: true },
      orderBy: [{ profile: { bio: 'asc' } }],
    });
  });

  it('orders by the same relation twice, on two different columns', async () => {
    await agree({
      model: 'Post',
      select: { id: true },
      orderBy: [{ author: { tenantId: 'desc' } }, { author: { name: 'asc' } }, { id: 'asc' }],
    });
  });

  it('orders by how many rows a to-many holds, counting an empty one as zero', async () => {
    const descending = await agree({
      model: 'User',
      select: { id: true },
      orderBy: [{ posts: { _count: 'desc' } }],
    });
    expect(descending.compiled).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }]);

    const ascending = await agree({
      model: 'User',
      select: { id: true },
      orderBy: [{ posts: { _count: 'asc' } }],
    });
    expect(ascending.compiled).toEqual([{ id: 4 }, { id: 3 }, { id: 2 }, { id: 1 }]);
  });

  it('pages and filters a read ordered by a relation column', async () => {
    const run = await agree({
      model: 'Post',
      select: { id: true },
      where: { published: true },
      orderBy: [{ author: { name: 'desc' } }, { id: 'asc' }],
      take: 2,
      skip: 1,
    });
    expect(run.compiled).toEqual([{ id: 4 }, { id: 5 }]);
  });

  it('orders a read by a relation column while the policy narrows both models', async () => {
    await agree(
      {
        model: 'Post',
        select: { id: true },
        orderBy: [{ author: { name: 'asc' } }, { id: 'asc' }],
      },
      { Post: { published: true }, User: { tenantId: 1 } },
    );
  });

  it('places nulls where the caller asked for them rather than where the engine puts them', async () => {
    const last = await agree({
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: { sort: 'asc', nulls: 'last' } }, { id: 'asc' }],
    });
    expect((last.compiled as { rank: number | null }[]).map((row) => row.rank)).toEqual([
      1,
      2,
      4,
      null,
      null,
    ]);

    const first = await agree({
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: { sort: 'desc', nulls: 'first' } }, { id: 'asc' }],
    });
    expect((first.compiled as { rank: number | null }[]).map((row) => row.rank)).toEqual([
      null,
      null,
      4,
      2,
      1,
    ]);
  });

  it('orders by a bare sort with no nulls placement of its own', async () => {
    await agree({
      model: 'Metric',
      select: { id: true, rank: true },
      orderBy: [{ rank: { sort: 'asc' } }, { id: 'asc' }],
    });
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

  it('reaches an indexed to-many in one statement and an unindexed one in two', async () => {
    const indexed = await agree({
      model: 'User',
      select: { id: true, posts: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(indexed)).toHaveLength(1);
    expect(expectCompiled(indexed).batched).toEqual([]);
    expect(statements(indexed)[0]).toContain('"t1"."author_id" = "t0"."user_id"');

    const unindexed = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(unindexed)).toHaveLength(2);
    expect(expectCompiled(unindexed).batched).toEqual(['metrics']);
    expect(statements(unindexed)[0]).not.toContain('metrics');
    expect(statements(unindexed)[1]).toContain('"t1"."owner_id" in (');
  });

  it('reaches a unique-backed to-one in one statement', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, profile: { select: { bio: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(run)).toHaveLength(1);
    expect(expectCompiled(run).batched).toEqual([]);
  });

  it('runs one statement per unindexed relation level, not one per row', async () => {
    const run = await agree({
      model: 'User',
      select: {
        id: true,
        posts: {
          select: { id: true, author: { select: { metrics: { select: { id: true } } } } },
        },
      },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(run)).toHaveLength(1);
    expect(expectCompiled(run).batched).toEqual([]);

    const batched = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true, owner: { select: { name: true } } } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(batched)).toHaveLength(2);
    expect(expectCompiled(batched).batched).toEqual(['metrics']);
  });

  it('adds the column it correlates a second statement on, and does not return it', async () => {
    const run = await agree({
      model: 'User',
      select: { metrics: { select: { id: true } } },
      orderBy: [{ id: 'asc' }],
    });
    expect(statements(run)).toHaveLength(2);
    expect(Object.keys((run.compiled as Record<string, unknown>[])[0]!)).toEqual(['metrics']);
    const nested = (run.compiled as { metrics: Record<string, unknown>[] }[])[0]!.metrics[0]!;
    expect(Object.keys(nested)).toEqual(['id']);
  });

  it('runs no second statement at all when the first one matched nothing', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true } } },
      where: { id: -1 },
      orderBy: [{ id: 'asc' }],
    });
    expect(run.compiled).toEqual([]);
    expect(statements(run)).toHaveLength(1);
    expect(expectCompiled(run).batched).toEqual(['metrics']);
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

  const maskedEngine = (
    masks: readonly FieldMaskSpec[],
    onCheckField?: (model: string, field: string) => void,
  ): GolemEngine =>
    maskingEngineFor({
      client: subject().client,
      provider: subject().provider,
      masks,
      onCheckField,
    });

  describe('a field the caller may read only on some rows', () => {
    const ownedByAda: FieldMaskSpec = {
      model: 'Metric',
      field: 'note',
      condition: { ownerId: 1 },
      requires: ['ownerId'],
    };

    it('nulls the same rows whether the database or golem masks it', async () => {
      const run = await runBothMany(maskedEngine([ownedByAda]), {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
      });

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['note']);
      expect(event.sql).toContain('case when');
      expect(run.compiled).toEqual([
        { id: 1, note: 'first' },
        { id: 2, note: null },
        { id: 3, note: null },
        { id: 4, note: null },
        { id: 5, note: null },
      ]);
    });

    it('answers each masked column of a row on its own condition, not its neighbour', async () => {
      const run = await runBothMany(
        maskedEngine([
          ownedByAda,
          { model: 'Metric', field: 'label', condition: { ownerId: 2 }, requires: ['ownerId'] },
        ]),
        {
          model: 'Metric',
          select: { id: true, label: true, note: true },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['label', 'note']);
      expect(event.deferred).toEqual([]);
      expect(run.compiled).toEqual([
        { id: 1, label: null, note: 'first' },
        { id: 2, label: null, note: null },
        { id: 3, label: 'gamma', note: null },
        { id: 4, label: 'delta', note: null },
        { id: 5, label: null, note: null },
      ]);
    });

    it('masks the same field of the same model at the root and two relations down', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'User', field: 'name', condition: { tenantId: 2 }, requires: ['tenantId'] },
        ]),
        {
          model: 'User',
          select: { id: true, name: true, profile: { select: { user: { select: { name: true } } } } },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['profile.user.name', 'name']);
      expect(event.deferred).toEqual([]);
      expect(run.compiled).toEqual([
        { id: 1, name: null, profile: { user: { name: null } } },
        { id: 2, name: null, profile: { user: { name: null } } },
        { id: 3, name: 'Cleo', profile: null },
        { id: 4, name: 'Dee', profile: null },
      ]);
    });

    it('still checks the root in memory when it masks the same field name in a relation', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'User',
            field: 'name',
            condition: { tenantId: 2 },
            requires: ['tenantId'],
            discharged: true,
          },
        ]),
        {
          model: 'User',
          select: { id: true, name: true, profile: { select: { user: { select: { name: true } } } } },
          orderBy: [{ id: 'asc' }],
          distinct: ['name'],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['profile.user.name']);
      expect(event.deferred).toEqual([
        expect.objectContaining({ path: 'the read', field: 'name', reason: 'distinct' }),
      ]);
      expect(run.compiled).toEqual([
        { id: 1, name: null, profile: { user: { name: null } } },
        { id: 2, name: null, profile: { user: { name: null } } },
        { id: 3, name: 'Cleo', profile: null },
        { id: 4, name: 'Dee', profile: null },
      ]);
    });

    it('masks a field of a relation the database reaches in one correlated statement', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Post',
            field: 'secretNote',
            condition: { published: true },
            requires: ['published'],
          },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            posts: { select: { id: true, secretNote: true }, orderBy: [{ id: 'asc' }] },
          },
          where: { id: { in: [1, 2] } },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.batched).toEqual([]);
      expect(statements(run)).toHaveLength(1);
      expect(event.masked).toEqual(['posts.secretNote']);
      expect(run.compiled).toEqual([
        {
          id: 1,
          posts: [
            { id: 1, secretNote: 'n1' },
            { id: 2, secretNote: 'n2' },
            { id: 3, secretNote: null },
          ],
        },
        {
          id: 2,
          posts: [
            { id: 4, secretNote: 'n4' },
            { id: 5, secretNote: 'n5' },
          ],
        },
      ]);
    });

    it('masks a field of a relation the database reaches in a second statement', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'Metric', field: 'note', condition: { ownerId: 1 }, requires: ['ownerId'] },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            metrics: { select: { id: true, note: true }, orderBy: [{ id: 'asc' }] },
          },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.batched).toEqual(['metrics']);
      expect(statements(run)).toHaveLength(2);
      expect(statements(run)[1]).toContain('case when');
      expect(event.masked).toEqual(['metrics.note']);
      expect(run.compiled).toEqual([
        { id: 1, metrics: [{ id: 1, note: 'first' }, { id: 2, note: null }] },
        { id: 2, metrics: [{ id: 3, note: null }, { id: 4, note: null }] },
        { id: 3, metrics: [{ id: 5, note: null }] },
        { id: 4, metrics: [] },
      ]);
    });

    it('masks a field two relations down, through a batch and into a json aggregate', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'User', field: 'name', condition: { tenantId: 2 }, requires: ['tenantId'] },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            metrics: {
              select: { id: true, owner: { select: { name: true } } },
              orderBy: [{ id: 'asc' }],
            },
          },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.batched).toEqual(['metrics']);
      expect(event.masked).toEqual(['metrics.owner.name']);
      expect(event.deferred).toEqual([]);
      expect(run.compiled).toEqual([
        { id: 1, metrics: [{ id: 1, owner: { name: null } }, { id: 2, owner: { name: null } }] },
        { id: 2, metrics: [{ id: 3, owner: { name: null } }, { id: 4, owner: { name: null } }] },
        { id: 3, metrics: [{ id: 5, owner: { name: 'Cleo' } }] },
        { id: 4, metrics: [] },
      ]);
    });

    it('keeps the hydration one mask of a relation still needs and drops the other', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Post',
            field: 'secretNote',
            condition: { published: true },
            requires: ['published'],
          },
          {
            model: 'Post',
            field: 'title',
            condition: { views: { gte: 20 } },
            requires: ['views'],
            withheld: true,
          },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            posts: { select: { id: true, title: true, secretNote: true }, orderBy: [{ id: 'asc' }] },
          },
          where: { id: 1 },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['posts.secretNote']);
      expect(event.deferred).toEqual([
        expect.objectContaining({ path: 'posts', field: 'title', reason: 'unconstrained' }),
      ]);
      expect(event.sql).toContain('as "views"');
      expect(event.sql).not.toContain('as "published"');
      expect(run.compiled).toEqual([
        {
          id: 1,
          posts: [
            { id: 1, title: null, secretNote: 'n1' },
            { id: 2, title: 'a2', secretNote: 'n2' },
            { id: 3, title: null, secretNote: null },
          ],
        },
      ]);
    });

    it('keeps a column of a relation the mask it defers there still reads', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Post',
            field: 'secretNote',
            condition: { published: true },
            requires: ['published'],
          },
          {
            model: 'Post',
            field: 'title',
            condition: { published: false },
            requires: ['published'],
            withheld: true,
          },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            posts: { select: { id: true, title: true, secretNote: true }, orderBy: [{ id: 'asc' }] },
          },
          where: { id: 1 },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['posts.secretNote']);
      expect(event.deferred).toEqual([
        expect.objectContaining({ path: 'posts', field: 'title', reason: 'unconstrained' }),
      ]);
      expect(event.sql).toContain('as "published"');
      expect(run.compiled).toEqual([
        {
          id: 1,
          posts: [
            { id: 1, title: null, secretNote: 'n1' },
            { id: 2, title: null, secretNote: 'n2' },
            { id: 3, title: 'a3', secretNote: null },
          ],
        },
      ]);
    });

    it('leaves the key a batched relation groups its rows by to the in-memory path', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'Metric', field: 'ownerId', condition: { rank: { not: null } }, requires: ['rank'] },
        ]),
        {
          model: 'User',
          select: {
            id: true,
            metrics: { select: { id: true, ownerId: true }, orderBy: [{ id: 'asc' }] },
          },
          where: { id: { in: [1, 2] } },
          orderBy: [{ id: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.batched).toEqual(['metrics']);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ path: 'metrics', field: 'ownerId', reason: 'correlated' }),
      ]);
      expect(run.compiled).toEqual([
        { id: 1, metrics: [{ id: 1, ownerId: 1 }, { id: 2, ownerId: 1 }] },
        { id: 2, metrics: [{ id: 3, ownerId: null }, { id: 4, ownerId: 2 }] },
      ]);
    });

    it('carries every column type it masks inside a json aggregate as Prisma carries it', async () => {
      const carried = [
        'label',
        'note',
        'rank',
        'score',
        'hits',
        'ratio',
        'active',
        'recordedAt',
      ] as const;
      for (const column of carried) {
        const run = await runBothMany(
          maskedEngine([
            { model: 'Metric', field: column, condition: { ownerId: 2 }, requires: ['ownerId'] },
          ]),
          {
            model: 'Profile',
            select: {
              id: true,
              user: {
                select: { metrics: { select: { [column]: true }, orderBy: [{ id: 'asc' }] } },
              },
            },
            orderBy: [{ id: 'asc' }],
          },
        );

        const event = expectCompiled(run);
        expect({ column, batched: event.batched }).toEqual({ column, batched: [] });
        expect({ column, masked: event.masked }).toEqual({
          column,
          masked: [`user.metrics.${column}`],
        });
        expect({ column, rows: describeShape(run.compiled) }).toEqual({
          column,
          rows: describeShape(run.prisma),
        });
      }
    });

    it('carries a column it masks inside a batch out under the type Prisma reads it as', async () => {
      const carried = subject().provider === 'sqlite'
        ? (['label', 'note', 'ratio', 'hits'] as const)
        : (['label', 'note', 'rank', 'score', 'hits', 'ratio', 'active', 'recordedAt'] as const);
      for (const column of carried) {
        const run = await runBothMany(
          maskedEngine([
            { model: 'Metric', field: column, condition: { ownerId: 2 }, requires: ['ownerId'] },
          ]),
          {
            model: 'User',
            select: {
              id: true,
              metrics: { select: { [column]: true }, orderBy: [{ id: 'asc' }] },
            },
            orderBy: [{ id: 'asc' }],
          },
        );

        const event = expectCompiled(run);
        expect({ column, batched: event.batched }).toEqual({ column, batched: ['metrics'] });
        expect({ column, masked: event.masked }).toEqual({
          column,
          masked: [`metrics.${column}`],
        });
        expect({ column, rows: describeShape(run.compiled) }).toEqual({
          column,
          rows: describeShape(run.prisma),
        });
      }
    });

    it('leaves a column sqlite would retype inside a batch to the in-memory path', async () => {
      const lossy = subject().provider === 'sqlite'
        ? (['rank', 'score', 'active', 'recordedAt'] as const)
        : ([] as const);
      for (const column of lossy) {
        const run = await runBothMany(
          maskedEngine([
            { model: 'Metric', field: column, condition: { ownerId: 2 }, requires: ['ownerId'] },
          ]),
          {
            model: 'User',
            select: {
              id: true,
              metrics: { select: { [column]: true }, orderBy: [{ id: 'asc' }] },
            },
            orderBy: [{ id: 'asc' }],
          },
        );

        const event = expectCompiled(run);
        expect({ column, masked: event.masked }).toEqual({ column, masked: [] });
        expect({ column, reason: event.deferred?.map((entry) => entry.reason) }).toEqual({
          column,
          reason: ['decoder'],
        });
        expect({ column, rows: describeShape(run.compiled) }).toEqual({
          column,
          rows: describeShape(run.prisma),
        });
      }
    });

    it('checks a field row by row when the condition handed over is a null denial', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Metric',
            field: 'note',
            condition: null,
            requires: ['ownerId'],
            evaluate: (row) => row.ownerId === 1,
          },
        ]),
        { model: 'Metric', select: { id: true, note: true }, orderBy: [{ id: 'asc' }] },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ field: 'note', reason: 'unconstrained' }),
      ]);
      expect(event.sql).not.toContain('case when');
      expect(run.compiled).toEqual([
        { id: 1, note: 'first' },
        { id: 2, note: null },
        { id: 3, note: null },
        { id: 4, note: null },
        { id: 5, note: null },
      ]);
    });

    it('keeps the key a batched relation correlates on whole, and masks it after the batch', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'User', field: 'id', condition: { tenantId: 1 }, requires: ['tenantId'] },
        ]),
        {
          model: 'User',
          select: { id: true, name: true, metrics: { select: { id: true } } },
          orderBy: [{ name: 'asc' }],
        },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.batched).toEqual(['metrics']);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ field: 'id', reason: 'correlated' }),
      ]);
      expect(run.compiled).toEqual([
        { id: 1, name: 'Ada', metrics: [{ id: 1 }, { id: 2 }] },
        { id: 2, name: 'Bob', metrics: [{ id: 3 }, { id: 4 }] },
        { id: null, name: 'Cleo', metrics: [{ id: 5 }] },
        { id: null, name: 'Dee', metrics: [] },
      ]);
    });

    it('asks the database for no column it only needed to run the check in memory', async () => {
      const run = await runBothMany(maskedEngine([ownedByAda]), {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
      });

      expectIdentical(run);
      expect(expectCompiled(run).sql).not.toContain('as "ownerId"');
      expect(statements(run)[0]).not.toContain('owner_id" as');
    });

    it('never asks the provider about a row once the database masks the field', async () => {
      const asked: string[] = [];
      const engine = maskedEngine([ownedByAda], (model, field) => asked.push(`${model}.${field}`));
      const request = {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
        context,
      } as const;

      const compiled = await engine.findMany({ ...request, compiled: true });
      expect(asked).toEqual([]);

      const prisma = await engine.findMany({ ...request });
      expect(asked).toEqual(
        metrics.filter((metric) => metric.note !== null).map(() => 'Metric.note'),
      );
      expect(compiled).toStrictEqual(prisma);
    });

    it('shows two callers of the same row different values', async () => {
      const request = {
        model: 'Metric',
        select: { id: true, note: true },
        where: { id: 3 },
        orderBy: [{ id: 'asc' }],
      } as const;
      const mine = await runBothMany(maskedEngine([ownedByAda]), { ...request });
      const theirs = await runBothMany(
        maskedEngine([{ ...ownedByAda, condition: { ownerId: 2 } }]),
        { ...request },
      );

      expectIdentical(mine);
      expectIdentical(theirs);
      expect(mine.compiled).toEqual([{ id: 3, note: null }]);
      expect(theirs.compiled).toEqual([{ id: 3, note: 'third' }]);
    });

    it('masks per row within one statement rather than per query', async () => {
      const run = await runBothMany(
        maskedEngine([{ model: 'Metric', field: 'note', condition: { rank: { not: null } }, requires: ['rank'] }]),
        { model: 'Metric', select: { id: true, note: true, rank: true }, orderBy: [{ id: 'asc' }] },
      );

      expectIdentical(run);
      expect(statements(run)).toHaveLength(1);
      expect(run.compiled).toEqual([
        { id: 1, note: 'first', rank: 2 },
        { id: 2, note: null, rank: 1 },
        { id: 3, note: null, rank: null },
        { id: 4, note: null, rank: 4 },
        { id: 5, note: null, rank: null },
      ]);
    });

    it('agrees on a condition that leaves a nullable column under a NOT', async () => {
      const run = await runBothMany(
        maskedEngine([
          { model: 'Metric', field: 'note', condition: { NOT: { rank: 2 } }, requires: ['rank'] },
        ]),
        { model: 'Metric', select: { id: true, note: true }, orderBy: [{ id: 'asc' }] },
      );

      expectIdentical(run);
      expect(run.compiled).toEqual([
        { id: 1, note: null },
        { id: 2, note: null },
        { id: 3, note: 'third' },
        { id: 4, note: null },
        { id: 5, note: 'fifth' },
      ]);
    });

    it('agrees on a condition that hops a relation', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Metric',
            field: 'note',
            condition: { owner: { is: { tenantId: 2 } } },
            requires: ['owner'],
            dependencies: { owner: { tenantId: true } },
          },
        ]),
        { model: 'Metric', select: { id: true, note: true }, orderBy: [{ id: 'asc' }] },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual(['note']);
      expect(event.sql).toContain('EXISTS');
      expect(event.sql).not.toContain('json');
      expect(run.compiled).toEqual([
        { id: 1, note: null },
        { id: 2, note: null },
        { id: 3, note: null },
        { id: 4, note: null },
        { id: 5, note: 'fifth' },
      ]);
    });

    it('orders by a column the database masks without disturbing the page', async () => {
      const run = await runBothMany(maskedEngine([{ ...ownedByAda, discharged: true }]), {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ note: 'asc' }, { id: 'asc' }],
        take: 3,
      });

      expectIdentical(run);
      expectCompiled(run);
    });

    it('refuses to order by a column the caller may not read on every row', async () => {
      const engine = maskedEngine([ownedByAda]);
      const request = {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ note: 'asc' }, { id: 'asc' }],
        context,
      } as const;

      await expect(engine.findMany({ ...request, compiled: true })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
      await expect(engine.findMany({ ...request })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
    });

    it('refuses to filter on a column the caller may not read on every row', async () => {
      const engine = maskedEngine([ownedByAda]);
      const request = {
        model: 'Metric',
        select: { id: true },
        where: { OR: [{ note: { startsWith: 'fi' } }] },
        context,
      } as const;

      await expect(engine.findMany({ ...request, compiled: true })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
      await expect(engine.findMany({ ...request })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
    });

    it('hands a field the read is distinct on back to the in-memory path', async () => {
      const run = await runBothMany(maskedEngine([{ ...ownedByAda, discharged: true }]), {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
        distinct: ['note'],
      });

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ field: 'note', reason: 'distinct' }),
      ]);
    });

    it('refuses to read distinct on a column the caller may not read on every row', async () => {
      const engine = maskedEngine([ownedByAda]);
      const request = {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
        distinct: ['note'],
        context,
      } as const;

      await expect(engine.findMany({ ...request, compiled: true })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
      await expect(engine.findMany({ ...request })).rejects.toThrow(
        'Cannot filter or order by field "note" on Metric',
      );
    });

    it('hands a field whose condition it cannot render back to the in-memory path', async () => {
      const run = await runBothMany(
        maskedEngine([
          {
            model: 'Metric',
            field: 'note',
            condition: { label: { search: 'alpha' } },
            requires: ['label'],
            evaluate: (row) => row.label === 'alpha',
          },
        ]),
        { model: 'Metric', select: { id: true, note: true }, orderBy: [{ id: 'asc' }] },
      );

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ field: 'note', reason: 'unrenderable' }),
      ]);
      expect(event.sql).toContain('as "label"');
      expect(run.compiled).toEqual([
        { id: 1, note: 'first' },
        { id: 2, note: null },
        { id: 3, note: null },
        { id: 4, note: null },
        { id: 5, note: null },
      ]);
    });

    it('carries every column type it masks out as Prisma carries it', async () => {
      const carried = subject().provider === 'sqlite'
        ? (['label', 'note', 'ratio', 'hits'] as const)
        : (['label', 'note', 'rank', 'score', 'hits', 'ratio', 'active', 'recordedAt'] as const);
      for (const column of carried) {
        const run = await runBothMany(
          maskedEngine([
            { model: 'Metric', field: column, condition: { ownerId: 2 }, requires: ['ownerId'] },
          ]),
          { model: 'Metric', select: { id: true, [column]: true }, orderBy: [{ id: 'asc' }] },
        );

        expect({ column, masked: expectCompiled(run).masked }).toEqual({ column, masked: [column] });
        expect({ column, rows: describeShape(run.compiled) }).toEqual({
          column,
          rows: describeShape(run.prisma),
        });
      }
    });

    it('leaves a column sqlite would hand back under another type to the in-memory path', async () => {
      const lossy = subject().provider === 'sqlite'
        ? (['rank', 'score', 'active', 'recordedAt'] as const)
        : ([] as const);
      for (const column of lossy) {
        const run = await runBothMany(
          maskedEngine([
            { model: 'Metric', field: column, condition: { ownerId: 2 }, requires: ['ownerId'] },
          ]),
          { model: 'Metric', select: { id: true, [column]: true }, orderBy: [{ id: 'asc' }] },
        );

        const event = expectCompiled(run);
        expect({ column, masked: event.masked }).toEqual({ column, masked: [] });
        expect({ column, reason: event.deferred?.map((entry) => entry.reason) }).toEqual({
          column,
          reason: ['decoder'],
        });
        expect({ column, rows: describeShape(run.compiled) }).toEqual({
          column,
          rows: describeShape(run.prisma),
        });
      }
    });

    it('hands a field the provider hands over no condition for back to the in-memory path', async () => {
      const run = await runBothMany(maskedEngine([{ ...ownedByAda, withheld: true }]), {
        model: 'Metric',
        select: { id: true, note: true },
        orderBy: [{ id: 'asc' }],
      });

      expectIdentical(run);
      const event = expectCompiled(run);
      expect(event.masked).toEqual([]);
      expect(event.deferred).toEqual([
        expect.objectContaining({ field: 'note', reason: 'unconstrained' }),
      ]);
      expect(event.sql).toContain('as "ownerId"');
      expect(run.compiled).toEqual([
        { id: 1, note: 'first' },
        { id: 2, note: null },
        { id: 3, note: null },
        { id: 4, note: null },
        { id: 5, note: null },
      ]);
    });
  });
}

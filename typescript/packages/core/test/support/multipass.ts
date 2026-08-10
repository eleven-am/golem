import { GolemEngine } from '../../src/operations';
import { ScopedQueryCreator, ScopedSelectBuilder } from '../../src/scoped';
import { context, engineFor, toNumber } from './analytics';
import { playModels } from './fixture';
import { AnalyticsSubject } from './expectations';

const FLOOR = 0.5;

const CEILING = 1.25;

const QUALIFYING_PLAYS = 2;

type Fluent = any;

interface Dialectal {
  year(eb: Fluent, reference: string): Fluent;
  clamp(eb: Fluent, value: Fluent): Fluent;
}

function dialectal(provider: string): Dialectal {
  const sqlite = provider === 'sqlite';
  return {
    year: (eb, reference) =>
      sqlite
        ? eb.cast(eb.fn('strftime', [eb.val('%Y'), reference]), 'integer')
        : eb.cast(eb.fn('date_part', [eb.val('year'), reference]), 'integer'),
    clamp: (eb, value) =>
      sqlite
        ? eb.fn('min', [eb.fn('max', [value, eb.lit(FLOOR)]), eb.lit(CEILING)])
        : eb.fn('least', [eb.fn('greatest', [value, eb.lit(FLOOR)]), eb.lit(CEILING)]),
  };
}

function aged(qb: ScopedSelectBuilder, sql: Dialectal): Fluent {
  return (qb as Fluent)
    .select(['Play.userId', 'Play.trackUri', 'Play.trackName', 'Play.msPlayed'])
    .select((eb: Fluent) =>
      eb(
        sql.year(eb, 'Play.ts'),
        '-',
        eb.fn
          .min(sql.year(eb, 'Play.ts'))
          .over((over: Fluent) => over.partitionBy(['Play.userId', 'Play.trackUri'])),
      ).as('age'),
    )
    .select((eb: Fluent) =>
      eb
        .case()
        .when('Play.reasonEnd', '=', 'trackdone')
        .then(eb.lit(1))
        .else(eb.lit(0))
        .end()
        .as('finished'),
    )
    .select((eb: Fluent) =>
      eb
        .case()
        .when(eb('Play.reasonStart', 'in', ['clickrow', 'playbtn']))
        .then(eb.lit(1))
        .else(eb.lit(0))
        .end()
        .as('pulled'),
    );
}

function baselines(creator: ScopedQueryCreator, sql: Dialectal, qb: ScopedSelectBuilder): Fluent {
  return (creator as Fluent)
    .with('aged', () => aged(qb, sql))
    .selectFrom('aged')
    .select('aged.age as age')
    .select((eb: Fluent) => eb.fn.countAll().as('plays'))
    .select((eb: Fluent) => eb.fn.avg('aged.finished').as('expFinish'))
    .select((eb: Fluent) => eb.fn.avg('aged.pulled').as('expPull'))
    .groupBy('aged.age')
    .orderBy('aged.age');
}

function affinity(creator: ScopedQueryCreator, sql: Dialectal, qb: ScopedSelectBuilder): Fluent {
  return (creator as Fluent)
    .with('aged', () => aged(qb, sql))
    .with('baseline', (c: Fluent) =>
      c
        .selectFrom('aged')
        .select('aged.age as age')
        .select((eb: Fluent) => eb.fn.avg('aged.finished').as('expFinish'))
        .select((eb: Fluent) => eb.fn.avg('aged.pulled').as('expPull'))
        .groupBy('aged.age'),
    )
    .with('indexed', (c: Fluent) =>
      c
        .selectFrom('aged')
        .innerJoin('baseline', 'baseline.age', 'aged.age')
        .select('aged.trackName as trackName')
        .select((eb: Fluent) => eb.fn.countAll().as('plays'))
        .select((eb: Fluent) => eb.fn.sum('aged.msPlayed').as('msTotal'))
        .select((eb: Fluent) =>
          sql
            .clamp(
              eb,
              eb(eb.fn.avg('aged.finished'), '/', eb.fn.avg('baseline.expFinish')),
            )
            .as('finishIdx'),
        )
        .select((eb: Fluent) =>
          sql
            .clamp(eb, eb(eb.fn.avg('aged.pulled'), '/', eb.fn.avg('baseline.expPull')))
            .as('pullIdx'),
        )
        .groupBy(['aged.trackUri', 'aged.trackName'])
        .having((eb: Fluent) => eb(eb.fn.countAll(), '>=', eb.lit(QUALIFYING_PLAYS))),
    )
    .with('scored', (c: Fluent) =>
      c
        .selectFrom('indexed')
        .select([
          'indexed.trackName as trackName',
          'indexed.plays as plays',
          'indexed.msTotal as msTotal',
          'indexed.finishIdx as finishIdx',
          'indexed.pullIdx as pullIdx',
        ])
        .select((eb: Fluent) =>
          eb
            .fn('sqrt', [eb(eb.ref('indexed.finishIdx'), '*', eb.ref('indexed.pullIdx'))])
            .as('score'),
        ),
    )
    .with('normal', (c: Fluent) =>
      c.selectFrom('scored').select((eb: Fluent) => eb.fn.avg('scored.score').as('meanScore')),
    )
    .selectFrom('scored')
    .crossJoin('normal')
    .select([
      'scored.trackName as trackName',
      'scored.plays as plays',
      'scored.msTotal as msTotal',
      'scored.finishIdx as finishIdx',
      'scored.pullIdx as pullIdx',
      'scored.score as score',
    ])
    .select((eb: Fluent) =>
      eb(eb.ref('scored.score'), '/', eb.ref('normal.meanScore')).as('normalised'),
    )
    .orderBy('scored.trackName');
}

function playsEngine(subject: AnalyticsSubject, constraint: unknown): GolemEngine {
  return engineFor(
    subject.client,
    subject.provider,
    { Play: constraint },
    undefined,
    playModels,
  );
}

function rowsOf(rows: unknown): Record<string, unknown>[] {
  return rows as Record<string, unknown>[];
}

export function multiPassSuite(subject: () => AnalyticsSubject): void {
  const run = async (constraint: unknown, shape: 'baselines' | 'affinity') => {
    const sql = dialectal(subject().provider);
    const rows = await playsEngine(subject(), constraint)
      .scoped({ model: 'Play', context })
      .query((qb, creator) =>
        (shape === 'baselines'
          ? baselines(creator, sql, qb)
          : affinity(creator, sql, qb)) as never,
      )
      .execute();
    return rowsOf(rows);
  };

  it('carries the policy predicate into a five-pass query the generated surface cannot express', async () => {
    const rows = await run({ userId: 1 }, 'affinity');

    expect(rows).toHaveLength(2);
    expect(rows.map((row) => row.trackName)).toEqual(['Alpha', 'Beta']);
    expect(rows.map((row) => toNumber(row.plays))).toEqual([4, 4]);
    expect(rows.map((row) => toNumber(row.msTotal))).toEqual([660000, 430000]);

    expect(toNumber(rows[0]!.finishIdx)).toBeCloseTo(1.25, 9);
    expect(toNumber(rows[0]!.pullIdx)).toBeCloseTo(0.9090909090909091, 9);
    expect(toNumber(rows[0]!.score)).toBeCloseTo(1.0660035817780522, 9);
    expect(toNumber(rows[0]!.normalised)).toBeCloseTo(1.0794456535687607, 9);

    expect(toNumber(rows[1]!.finishIdx)).toBeCloseTo(0.9090909090909091, 9);
    expect(toNumber(rows[1]!.pullIdx)).toBeCloseTo(0.9090909090909091, 9);
    expect(toNumber(rows[1]!.score)).toBeCloseTo(0.9090909090909091, 9);
    expect(toNumber(rows[1]!.normalised)).toBeCloseTo(0.9205543464312393, 9);

    expect(
      toNumber(rows[0]!.normalised) + toNumber(rows[1]!.normalised),
    ).toBeCloseTo(2, 9);
  });

  it('builds the per-age baselines from the scoped rows alone, never from another history', async () => {
    const rows = await run({ userId: 1 }, 'baselines');

    expect(rows.map((row) => toNumber(row.age))).toEqual([0, 1]);
    expect(rows.map((row) => toNumber(row.plays))).toEqual([5, 4]);
    expect(rows.map((row) => toNumber(row.expFinish))).toEqual([0.6, 0.5]);
    expect(rows.map((row) => toNumber(row.expPull))).toEqual([0.6, 0.5]);
  });

  it('shows those same baselines shifting once the predicate stops excluding the other user', async () => {
    const rows = await run({}, 'baselines');

    expect(rows.map((row) => toNumber(row.age))).toEqual([0, 1]);
    expect(rows.map((row) => toNumber(row.plays))).toEqual([8, 5]);
    expect(rows.map((row) => toNumber(row.expFinish))).toEqual([0.375, 0.4]);
    expect(rows.map((row) => toNumber(row.expPull))).toEqual([0.375, 0.4]);
  });

  it('gives the other user their own history and nothing of the first', async () => {
    const rows = await run({ userId: 2 }, 'baselines');

    expect(rows.map((row) => toNumber(row.age))).toEqual([0, 1]);
    expect(rows.map((row) => toNumber(row.plays))).toEqual([3, 1]);
    expect(rows.map((row) => toNumber(row.expFinish))).toEqual([0, 0]);
    expect(rows.map((row) => toNumber(row.expPull))).toEqual([0, 0]);
  });

  it('returns nothing at all from every pass when the policy denies every row', async () => {
    expect(await run(null, 'baselines')).toEqual([]);
    expect(await run(null, 'affinity')).toEqual([]);
  });
}

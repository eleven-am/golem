export interface EfficiencyPlay {
  readonly id: number;
  readonly userKey: number;
  readonly ts: string;
  readonly msPlayed: number;
  readonly reasonStart: string;
  readonly reasonEnd: string;
  readonly trackUri: string;
  readonly trackName: string;
  readonly artistName: string;
}

export const ADA = 1;

export const MOD = 2;

export const GUEST = 3;

export const QUALIFYING_PLAYS = 2;

export const INDEX_CEILING = 4;

const play = (
  id: number,
  userKey: number,
  ts: string,
  msPlayed: number,
  reasonStart: string,
  reasonEnd: string,
  trackUri: string,
  trackName: string,
  artistName: string,
): EfficiencyPlay => ({
  id,
  userKey,
  ts,
  msPlayed,
  reasonStart,
  reasonEnd,
  trackUri,
  trackName,
  artistName,
});

export const efficiencyPlays: readonly EfficiencyPlay[] = [
  play(1, ADA, '2020-06-15T12:00:00.000Z', 214000, 'fwdbtn', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(2, ADA, '2020-07-15T12:00:00.000Z', 31000, 'clickrow', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(3, ADA, '2020-08-15T12:00:00.000Z', 27000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(4, ADA, '2021-06-15T12:00:00.000Z', 19000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(5, ADA, '2021-06-15T12:00:00.000Z', 44000, 'clickrow', 'endplay', 'beta', 'Beta', 'Nova'),
  play(6, ADA, '2022-06-15T12:00:00.000Z', 198000, 'fwdbtn', 'trackdone', 'beta', 'Beta', 'Nova'),
  play(7, ADA, '2022-07-15T12:00:00.000Z', 22000, 'fwdbtn', 'endplay', 'beta', 'Beta', 'Nova'),
  play(8, ADA, '2022-08-15T12:00:00.000Z', 16000, 'backbtn', 'endplay', 'beta', 'Beta', 'Nova'),
  play(9, ADA, '2020-06-15T12:00:00.000Z', 187000, 'fwdbtn', 'trackdone', 'gamma', 'Gamma', 'Volt'),
  play(10, ADA, '2020-09-15T12:00:00.000Z', 33000, 'playbtn', 'endplay', 'gamma', 'Gamma', 'Volt'),
  play(11, ADA, '2021-06-15T12:00:00.000Z', 41000, 'clickrow', 'endplay', 'gamma', 'Gamma', 'Volt'),
  play(12, MOD, '2015-06-15T12:00:00.000Z', 203000, 'clickrow', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(13, MOD, '2016-06-15T12:00:00.000Z', 209000, 'playbtn', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(14, MOD, '2015-06-15T12:00:00.000Z', 12000, 'clickrow', 'endplay', 'zeta', 'Zeta', 'Volt'),
  play(15, MOD, '2015-07-15T12:00:00.000Z', 14000, 'fwdbtn', 'endplay', 'zeta', 'Zeta', 'Volt'),
  play(16, MOD, '2015-08-15T12:00:00.000Z', 11000, 'fwdbtn', 'endplay', 'zeta', 'Zeta', 'Volt'),
  play(17, MOD, '2015-06-15T12:00:00.000Z', 18000, 'fwdbtn', 'endplay', 'kappa', 'Kappa', 'Nova'),
  play(18, MOD, '2016-06-15T12:00:00.000Z', 21000, 'clickrow', 'endplay', 'kappa', 'Kappa', 'Nova'),
  play(19, MOD, '2016-07-15T12:00:00.000Z', 17000, 'fwdbtn', 'endplay', 'kappa', 'Kappa', 'Nova'),
  play(20, MOD, '2016-08-15T12:00:00.000Z', 15000, 'backbtn', 'endplay', 'kappa', 'Kappa', 'Nova'),
  play(21, GUEST, '2018-06-15T12:00:00.000Z', 24000, 'fwdbtn', 'endplay', 'omega', 'Omega', 'Void'),
  play(22, GUEST, '2018-07-15T12:00:00.000Z', 26000, 'fwdbtn', 'endplay', 'omega', 'Omega', 'Void'),
];

export type EfficiencyShape = 'baselines' | 'tracks' | 'artists';

export type EfficiencyPersona = 'ada' | 'mod' | 'guest' | 'everyone';

type Fluent = any;

export interface EfficiencyDialect {
  year(eb: Fluent, reference: string): Fluent;
  cap(eb: Fluent, value: Fluent): Fluent;
  real(eb: Fluent, value: Fluent): Fluent;
}

export function dialectFor(provider: string): EfficiencyDialect {
  const sqlite = provider === 'sqlite';
  return {
    year: (eb, reference) =>
      sqlite
        ? eb.cast(eb.fn('strftime', [eb.val('%Y'), reference]), 'integer')
        : eb.cast(eb.fn('date_part', [eb.val('year'), reference]), 'integer'),
    cap: (eb, value) =>
      sqlite
        ? eb.fn('min', [value, eb.lit(INDEX_CEILING)])
        : eb.fn('least', [value, eb.lit(INDEX_CEILING)]),
    real: (eb, value) =>
      sqlite ? eb.cast(value, 'real') : eb.cast(value, 'double precision'),
  };
}

function aged(qb: Fluent, sql: EfficiencyDialect): Fluent {
  return qb
    .select(['Play.userId', 'Play.trackUri', 'Play.trackName', 'Play.artistName'])
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
        .as('isFinish'),
    )
    .select((eb: Fluent) =>
      eb
        .case()
        .when(eb('Play.reasonStart', 'in', ['clickrow', 'playbtn']))
        .then(eb.lit(1))
        .else(eb.lit(0))
        .end()
        .as('isPull'),
    );
}

function baseline(c: Fluent): Fluent {
  return c
    .selectFrom('aged')
    .select('aged.age as age')
    .select((eb: Fluent) => eb.fn.countAll().as('plays'))
    .select((eb: Fluent) => eb.fn.avg('aged.isFinish').as('bf'))
    .select((eb: Fluent) => eb.fn.avg('aged.isPull').as('bp'))
    .groupBy('aged.age');
}

function index(eb: Fluent, sql: EfficiencyDialect, observed: string, expected: string): Fluent {
  return sql.cap(
    eb,
    eb(eb.fn.avg(observed), '/', eb.fn('nullif', [eb.fn.avg(expected), eb.lit(0)])),
  );
}

function efficiency(eb: Fluent, source: string): Fluent {
  return eb.fn('sqrt', [eb(eb.ref(`${source}.finishIdx`), '*', eb.ref(`${source}.pullIdx`))]);
}

function normalised(eb: Fluent, dial: string): Fluent {
  return eb(
    eb.ref(`shaped.${dial}`),
    '/',
    eb.fn('nullif', [
      eb.fn.max(`shaped.${dial}`).over((over: Fluent) => over.partitionBy('shaped.userId')),
      eb.lit(0),
    ]),
  );
}

function baselinesQuery(qb: Fluent, creator: Fluent, sql: EfficiencyDialect): Fluent {
  return creator
    .with('aged', () => aged(qb, sql))
    .selectFrom('aged')
    .select('aged.age as age')
    .select((eb: Fluent) => eb.fn.countAll().as('plays'))
    .select((eb: Fluent) => eb.fn.avg('aged.isFinish').as('bf'))
    .select((eb: Fluent) => eb.fn.avg('aged.isPull').as('bp'))
    .groupBy('aged.age')
    .orderBy('aged.age');
}

function tracksQuery(qb: Fluent, creator: Fluent, sql: EfficiencyDialect): Fluent {
  return creator
    .with('aged', () => aged(qb, sql))
    .with('baseline', (c: Fluent) => baseline(c))
    .with('indexed', (c: Fluent) =>
      c
        .selectFrom('aged')
        .innerJoin('baseline', 'baseline.age', 'aged.age')
        .select('aged.trackName as trackName')
        .select((eb: Fluent) => eb.fn.countAll().as('plays'))
        .select((eb: Fluent) =>
          index(eb, sql, 'aged.isFinish', 'baseline.bf').as('finishIdx'),
        )
        .select((eb: Fluent) => index(eb, sql, 'aged.isPull', 'baseline.bp').as('pullIdx'))
        .groupBy(['aged.trackUri', 'aged.trackName'])
        .having((eb: Fluent) => eb(eb.fn.countAll(), '>=', eb.lit(QUALIFYING_PLAYS))),
    )
    .with('scored', (c: Fluent) =>
      c
        .selectFrom('indexed')
        .select([
          'indexed.trackName as trackName',
          'indexed.plays as plays',
          'indexed.finishIdx as finishIdx',
          'indexed.pullIdx as pullIdx',
        ])
        .select((eb: Fluent) => efficiency(eb, 'indexed').as('efficiency')),
    )
    .selectFrom('scored')
    .select([
      'scored.trackName as trackName',
      'scored.plays as plays',
      'scored.finishIdx as finishIdx',
      'scored.pullIdx as pullIdx',
      'scored.efficiency as efficiency',
    ])
    .orderBy((eb: Fluent) => eb(eb.lit(0), '-', eb.ref('scored.efficiency')))
    .orderBy('scored.trackName');
}

function artistsQuery(qb: Fluent, creator: Fluent, sql: EfficiencyDialect): Fluent {
  return creator
    .with('aged', () => aged(qb, sql))
    .with('baseline', (c: Fluent) => baseline(c))
    .with('byArtist', (c: Fluent) =>
      c
        .selectFrom('aged')
        .innerJoin('baseline', 'baseline.age', 'aged.age')
        .select('aged.userId as userId')
        .select('aged.artistName as artistName')
        .select((eb: Fluent) => eb.fn.countAll().as('plays'))
        .select((eb: Fluent) => eb.fn.count('aged.trackUri').distinct().as('tracks'))
        .select((eb: Fluent) =>
          index(eb, sql, 'aged.isFinish', 'baseline.bf').as('finishIdx'),
        )
        .select((eb: Fluent) => index(eb, sql, 'aged.isPull', 'baseline.bp').as('pullIdx'))
        .groupBy(['aged.userId', 'aged.artistName'])
        .having((eb: Fluent) => eb(eb.fn.countAll(), '>=', eb.lit(QUALIFYING_PLAYS))),
    )
    .with('shaped', (c: Fluent) =>
      c
        .selectFrom('byArtist')
        .select([
          'byArtist.userId as userId',
          'byArtist.artistName as artistName',
          'byArtist.plays as plays',
          'byArtist.tracks as tracks',
          'byArtist.finishIdx as finishIdx',
          'byArtist.pullIdx as pullIdx',
        ])
        .select((eb: Fluent) =>
          eb(
            sql.real(eb, eb.ref('byArtist.plays')),
            '/',
            eb.ref('byArtist.tracks'),
          ).as('intensity'),
        )
        .select((eb: Fluent) => efficiency(eb, 'byArtist').as('efficiency')),
    )
    .selectFrom('shaped')
    .select([
      'shaped.userId as userId',
      'shaped.artistName as artistName',
      'shaped.plays as plays',
      'shaped.tracks as tracks',
      'shaped.finishIdx as finishIdx',
      'shaped.pullIdx as pullIdx',
      'shaped.intensity as intensity',
      'shaped.efficiency as efficiency',
    ])
    .select((eb: Fluent) => normalised(eb, 'finishIdx').as('finishDial'))
    .select((eb: Fluent) => normalised(eb, 'pullIdx').as('pullDial'))
    .select((eb: Fluent) => normalised(eb, 'intensity').as('intensityDial'))
    .select((eb: Fluent) => normalised(eb, 'efficiency').as('efficiencyDial'))
    .orderBy('shaped.userId')
    .orderBy('shaped.artistName');
}

export function efficiencyQuery(
  shape: EfficiencyShape,
  sql: EfficiencyDialect,
): (qb: Fluent, creator: Fluent) => Fluent {
  return (qb, creator) => {
    if (shape === 'baselines') {
      return baselinesQuery(qb, creator, sql);
    }
    if (shape === 'tracks') {
      return tracksQuery(qb, creator, sql);
    }
    return artistsQuery(qb, creator, sql);
  };
}

export function numeric(value: unknown): number | null {
  if (value === null || value === undefined) {
    return null;
  }
  if (typeof value === 'number') {
    return value;
  }
  if (typeof value === 'bigint') {
    return Number(value);
  }
  if (typeof value === 'string') {
    return Number(value);
  }
  if (typeof value === 'object') {
    return Number(String(value));
  }
  throw new Error(`the database returned ${String(value)} where a number was expected`);
}

export interface EfficiencySubject {
  readonly provider: string;
  run(persona: EfficiencyPersona, shape: EfficiencyShape): Promise<Record<string, unknown>[]>;
  userId(key: number): unknown;
}

const DIGITS = 9;

function dials(rows: Record<string, unknown>[], key: string): (number | null)[] {
  return rows.map((row) => numeric(row[key]));
}

function closeTo(actual: (number | null)[], expected: (number | null)[]): void {
  expect(actual).toHaveLength(expected.length);
  expected.forEach((value, position) => {
    if (value === null) {
      expect(actual[position]).toBeNull();
      return;
    }
    expect(actual[position]).toBeCloseTo(value, DIGITS);
  });
}

export function efficiencySuite(subject: () => EfficiencySubject): void {
  const run = (persona: EfficiencyPersona, shape: EfficiencyShape) =>
    subject().run(persona, shape);

  const forUser = (rows: Record<string, unknown>[], key: number) =>
    rows.filter((row) => String(row.userId) === String(subject().userId(key)));

  it('derives the grouping key from a window function and averages over it', async () => {
    const rows = await run('ada', 'baselines');

    expect(dials(rows, 'age')).toEqual([0, 1]);
    expect(dials(rows, 'plays')).toEqual([6, 5]);
    closeTo(dials(rows, 'bf'), [1 / 3, 1 / 5]);
    closeTo(dials(rows, 'bp'), [1 / 2, 1 / 5]);
  });

  it('divides one aggregate by another across the join to those baselines', async () => {
    const rows = await run('ada', 'tracks');

    expect(rows.map((row) => row.trackName)).toEqual(['Gamma', 'Beta', 'Alpha']);
    expect(dials(rows, 'plays')).toEqual([3, 4, 4]);
    closeTo(dials(rows, 'finishIdx'), [15 / 13, 15 / 14, 5 / 6]);
    closeTo(dials(rows, 'pullIdx'), [5 / 3, 10 / 11, 10 / 17]);
    closeTo(dials(rows, 'efficiency'), [
      Math.sqrt(25 / 13),
      Math.sqrt(75 / 77),
      Math.sqrt(25 / 51),
    ]);
  });

  it('holds a runaway index at the ceiling for the second caller', async () => {
    const rows = await run('mod', 'tracks');

    expect(rows.map((row) => row.trackName)).toEqual(['Alpha', 'Kappa', 'Zeta']);
    expect(dials(rows, 'plays')).toEqual([2, 4, 3]);
    closeTo(dials(rows, 'finishIdx'), [4, 0, 0]);
    closeTo(dials(rows, 'pullIdx'), [20 / 9, 10 / 19, 5 / 6]);
    closeTo(dials(rows, 'efficiency'), [Math.sqrt(80 / 9), 0, 0]);
  });

  it('divides by a zero baseline, which the two engines cap differently', async () => {
    const baselines = await run('guest', 'baselines');

    expect(dials(baselines, 'age')).toEqual([0]);
    expect(dials(baselines, 'plays')).toEqual([2]);
    closeTo(dials(baselines, 'bf'), [0]);
    closeTo(dials(baselines, 'bp'), [0]);

    const rows = await run('guest', 'tracks');
    const capped = subject().provider === 'sqlite' ? null : INDEX_CEILING;

    expect(rows.map((row) => row.trackName)).toEqual(['Omega']);
    expect(dials(rows, 'plays')).toEqual([2]);
    closeTo(dials(rows, 'finishIdx'), [capped]);
    closeTo(dials(rows, 'pullIdx'), [capped]);
    closeTo(dials(rows, 'efficiency'), [capped]);
  });

  it('gives an unrestricted caller the wider baselines the larger set implies', async () => {
    const rows = await run('everyone', 'baselines');

    expect(dials(rows, 'age')).toEqual([0, 1]);
    expect(dials(rows, 'plays')).toEqual([13, 9]);
    closeTo(dials(rows, 'bf'), [3 / 13, 2 / 9]);
    closeTo(dials(rows, 'bp'), [5 / 13, 1 / 3]);
  });

  it('answers the same tracks differently once another history is in the average', async () => {
    const rows = await run('everyone', 'tracks');

    expect(rows.map((row) => row.trackName)).toEqual([
      'Alpha',
      'Gamma',
      'Beta',
      'Kappa',
      'Omega',
      'Zeta',
    ]);
    expect(dials(rows, 'plays')).toEqual([6, 3, 4, 4, 2, 3]);
    closeTo(dials(rows, 'finishIdx'), [351 / 160, 117 / 80, 39 / 35, 0, 0, 0]);
    closeTo(dials(rows, 'pullIdx'), [117 / 86, 78 / 43, 13 / 18, 13 / 18, 0, 13 / 15]);
    closeTo(dials(rows, 'efficiency'), [
      Math.sqrt((351 / 160) * (117 / 86)),
      Math.sqrt((117 / 80) * (78 / 43)),
      Math.sqrt((39 / 35) * (13 / 18)),
      0,
      0,
      0,
    ]);
  });

  it('counts the distinct tracks behind an artist and normalises against the caller alone', async () => {
    const rows = await run('ada', 'artists');

    expect(rows.map((row) => row.artistName)).toEqual(['Nova', 'Volt']);
    expect(dials(rows, 'plays')).toEqual([8, 3]);
    expect(dials(rows, 'tracks')).toEqual([2, 1]);
    closeTo(dials(rows, 'intensity'), [4, 3]);
    closeTo(dials(rows, 'finishIdx'), [15 / 16, 15 / 13]);
    closeTo(dials(rows, 'pullIdx'), [5 / 7, 5 / 3]);
    closeTo(dials(rows, 'efficiency'), [Math.sqrt(75 / 112), Math.sqrt(25 / 13)]);
    closeTo(dials(rows, 'finishDial'), [13 / 16, 1]);
    closeTo(dials(rows, 'pullDial'), [3 / 7, 1]);
    closeTo(dials(rows, 'intensityDial'), [1, 3 / 4]);
    closeTo(dials(rows, 'efficiencyDial'), [Math.sqrt(39 / 112), 1]);
  });

  it('normalises each caller against their own maximum when every history is visible', async () => {
    const rows = await run('everyone', 'artists');

    expect(rows).toHaveLength(5);

    const ada = forUser(rows, ADA);
    expect(ada.map((row) => row.artistName)).toEqual(['Nova', 'Volt']);
    expect(dials(ada, 'tracks')).toEqual([2, 1]);
    closeTo(dials(ada, 'intensity'), [4, 3]);
    closeTo(dials(ada, 'finishIdx'), [117 / 106, 117 / 80]);
    closeTo(dials(ada, 'pullIdx'), [39 / 56, 78 / 43]);
    closeTo(dials(ada, 'finishDial'), [40 / 53, 1]);
    closeTo(dials(ada, 'pullDial'), [43 / 112, 1]);
    closeTo(dials(ada, 'intensityDial'), [1, 3 / 4]);
    closeTo(dials(ada, 'efficiencyDial'), [Math.sqrt(215 / 742), 1]);

    const mod = forUser(rows, MOD);
    expect(mod.map((row) => row.artistName)).toEqual(['Nova', 'Volt']);
    expect(dials(mod, 'plays')).toEqual([6, 3]);
    expect(dials(mod, 'tracks')).toEqual([2, 1]);
    closeTo(dials(mod, 'intensity'), [3, 3]);
    closeTo(dials(mod, 'finishIdx'), [117 / 79, 0]);
    closeTo(dials(mod, 'pullIdx'), [117 / 82, 13 / 15]);
    closeTo(dials(mod, 'finishDial'), [1, 0]);
    closeTo(dials(mod, 'pullDial'), [1, 82 / 135]);
    closeTo(dials(mod, 'intensityDial'), [1, 1]);
    closeTo(dials(mod, 'efficiencyDial'), [1, 0]);

    const guest = forUser(rows, GUEST);
    expect(guest.map((row) => row.artistName)).toEqual(['Void']);
    closeTo(dials(guest, 'finishIdx'), [0]);
    closeTo(dials(guest, 'pullIdx'), [0]);
    closeTo(dials(guest, 'intensity'), [2]);
    closeTo(dials(guest, 'finishDial'), [null]);
    closeTo(dials(guest, 'pullDial'), [null]);
    closeTo(dials(guest, 'intensityDial'), [1]);
    closeTo(dials(guest, 'efficiencyDial'), [null]);
  });
}

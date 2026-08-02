import { DatamodelModel } from '../../src/datamodel';
import { GolemEngine } from '../../src/operations';
import { scopedModels } from './fixture';
import { AuthorizationProvider } from '../../src/authorization';

export interface SeedRow {
  readonly id: number;
  readonly title: string;
  readonly authorId: number;
  readonly published: boolean;
  readonly views: number;
  readonly secretNote: string;
}

export const users = [
  { id: 1, name: 'Ada', tenantId: 1 },
  { id: 2, name: 'Bob', tenantId: 1 },
  { id: 3, name: 'Cleo', tenantId: 2 },
  { id: 4, name: 'Dee', tenantId: 2 },
];

export const posts: readonly SeedRow[] = [
  { id: 1, title: 'a1', authorId: 1, published: true, views: 10, secretNote: 'n1' },
  { id: 2, title: 'a2', authorId: 1, published: true, views: 30, secretNote: 'n2' },
  { id: 3, title: 'a3', authorId: 1, published: false, views: 5, secretNote: 'n3' },
  { id: 4, title: 'b1', authorId: 2, published: true, views: 20, secretNote: 'n4' },
  { id: 5, title: 'b2', authorId: 2, published: true, views: 20, secretNote: 'n5' },
  { id: 6, title: 'c1', authorId: 3, published: true, views: 99, secretNote: 'n6' },
];

export const secrets = [
  { id: 1, value: 'never' },
  { id: 2, value: 'ever' },
];

export const profiles = [
  { id: 1, bio: 'writes about analysis', userId: 1 },
  { id: 2, bio: 'writes about nothing', userId: 2 },
];

export const context = { caller: 'analyst' };

export interface MetricSeed {
  readonly id: number;
  readonly label: string;
  readonly ownerId: number;
  readonly note: string | null;
  readonly rank: number | null;
  readonly score: string | null;
  readonly hits: bigint;
  readonly ratio: number;
  readonly active: boolean;
  readonly recordedAt: Date;
}

export const metrics: readonly MetricSeed[] = [
  {
    id: 1,
    label: 'alpha',
    ownerId: 1,
    note: 'first',
    rank: 2,
    score: '1234567890.1234567890123',
    hits: 9007199254740993n,
    ratio: 1.5,
    active: true,
    recordedAt: new Date('2024-01-01T00:00:00.000Z'),
  },
  {
    id: 2,
    label: 'beta',
    ownerId: 1,
    note: null,
    rank: 1,
    score: null,
    hits: 0n,
    ratio: -0.25,
    active: false,
    recordedAt: new Date('2024-02-02T12:30:45.123Z'),
  },
  {
    id: 3,
    label: 'gamma',
    ownerId: 2,
    note: 'third',
    rank: null,
    score: '0.000000000000000001',
    hits: -9007199254740993n,
    ratio: 3.25,
    active: true,
    recordedAt: new Date('2024-03-03T23:59:59.999Z'),
  },
  {
    id: 4,
    label: 'delta',
    ownerId: 2,
    note: null,
    rank: 4,
    score: '10.5',
    hits: 42n,
    ratio: 0,
    active: false,
    recordedAt: new Date('2024-04-04T01:02:03.004Z'),
  },
  {
    id: 5,
    label: 'epsilon',
    ownerId: 3,
    note: 'fifth',
    rank: null,
    score: null,
    hits: 1n,
    ratio: 2.5,
    active: true,
    recordedAt: new Date('2024-05-05T05:05:05.005Z'),
  },
];

export async function seedMetrics(client: {
  metric: { createMany(args: { data: MetricSeed[] }): Promise<unknown> };
}): Promise<void> {
  await client.metric.createMany({ data: metrics.map((metric) => ({ ...metric })) });
}

export function tuples(
  count: number,
  width: number,
  placeholder: (position: number) => string,
): string {
  return Array.from(
    { length: count },
    (_, row) =>
      `(${Array.from({ length: width }, (_, column) =>
        placeholder(row * width + column + 1),
      ).join(', ')})`,
  ).join(', ');
}

export async function seed(client: {
  $executeRawUnsafe(sql: string, ...values: unknown[]): Promise<unknown>;
}, placeholder: (position: number) => string): Promise<void> {
  const rows = (count: number, width: number) => tuples(count, width, placeholder);
  await client.$executeRawUnsafe(
    `INSERT INTO "users" ("user_id", "name", "tenant_id") VALUES ${rows(users.length, 3)}`,
    ...users.flatMap((user) => [user.id, user.name, user.tenantId]),
  );
  await Promise.all([
    client.$executeRawUnsafe(
      `INSERT INTO "posts" ("post_id", "title", "author_id", "published", "views", "secret_note") ` +
        `VALUES ${rows(posts.length, 6)}`,
      ...posts.flatMap((post) => [
        post.id,
        post.title,
        post.authorId,
        post.published,
        post.views,
        post.secretNote,
      ]),
    ),
    client.$executeRawUnsafe(
      `INSERT INTO "secrets" ("id", "value") VALUES ${rows(secrets.length, 2)}`,
      ...secrets.flatMap((secret) => [secret.id, secret.value]),
    ),
    client.$executeRawUnsafe(
      `INSERT INTO "profiles" ("profile_id", "bio", "user_id") VALUES ${rows(profiles.length, 3)}`,
      ...profiles.flatMap((profile) => [profile.id, profile.bio, profile.userId]),
    ),
  ]);
}

export interface PlaySeed {
  readonly id: number;
  readonly userId: number;
  readonly ts: string;
  readonly msPlayed: number;
  readonly reasonStart: string;
  readonly reasonEnd: string;
  readonly trackUri: string;
  readonly trackName: string;
  readonly artistName: string;
}

const play = (
  id: number,
  userId: number,
  ts: string,
  msPlayed: number,
  reasonStart: string,
  reasonEnd: string,
  trackUri: string,
  trackName: string,
  artistName: string,
): PlaySeed => ({
  id,
  userId,
  ts,
  msPlayed,
  reasonStart,
  reasonEnd,
  trackUri,
  trackName,
  artistName,
});

export const plays: readonly PlaySeed[] = [
  play(1, 1, '2020-01-05 09:00:00', 200000, 'clickrow', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(2, 1, '2020-06-05 09:00:00', 210000, 'fwdbtn', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(3, 1, '2021-02-05 09:00:00', 220000, 'playbtn', 'trackdone', 'alpha', 'Alpha', 'Nova'),
  play(4, 1, '2021-08-05 09:00:00', 30000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(5, 1, '2021-03-05 09:00:00', 180000, 'fwdbtn', 'trackdone', 'beta', 'Beta', 'Nova'),
  play(6, 1, '2021-09-05 09:00:00', 40000, 'clickrow', 'endplay', 'beta', 'Beta', 'Nova'),
  play(7, 1, '2022-01-05 09:00:00', 190000, 'playbtn', 'trackdone', 'beta', 'Beta', 'Nova'),
  play(8, 1, '2022-07-05 09:00:00', 20000, 'fwdbtn', 'endplay', 'beta', 'Beta', 'Nova'),
  play(9, 1, '2019-05-05 09:00:00', 15000, 'clickrow', 'endplay', 'gamma', 'Gamma', 'Nova'),
  play(10, 2, '2015-01-01 09:00:00', 11000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(11, 2, '2015-02-01 09:00:00', 12000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(12, 2, '2016-01-01 09:00:00', 13000, 'fwdbtn', 'endplay', 'alpha', 'Alpha', 'Nova'),
  play(13, 2, '2015-03-01 09:00:00', 14000, 'fwdbtn', 'endplay', 'zeta', 'Zeta', 'Nova'),
];

export async function seedPlays(
  client: { $executeRawUnsafe(sql: string, ...values: unknown[]): Promise<unknown> },
  placeholder: (position: number) => string,
): Promise<void> {
  await client.$executeRawUnsafe(
    `INSERT INTO "plays" ("play_id", "user_id", "ts", "ms_played", "reason_start", "reason_end", ` +
      `"track_uri", "track_name", "artist_name") VALUES ${tuples(plays.length, 9, placeholder)}`,
    ...plays.flatMap((row) => [
      row.id,
      row.userId,
      row.ts,
      row.msPlayed,
      row.reasonStart,
      row.reasonEnd,
      row.trackUri,
      row.trackName,
      row.artistName,
    ]),
  );
}

export function toNumber(value: unknown): number {
  if (typeof value === 'number') {
    return value;
  }
  if (typeof value === 'bigint') {
    return Number(value);
  }
  if (typeof value === 'string') {
    return Number(value);
  }
  if (value !== null && value !== undefined && typeof value === 'object') {
    return Number(String(value));
  }
  throw new Error(`the database returned ${String(value)} where a number was expected`);
}

export function satisfies(entity: unknown, constraint: unknown): boolean {
  if (!constraint || typeof constraint !== 'object' || !entity || typeof entity !== 'object') {
    return true;
  }
  const row = entity as Record<string, unknown>;
  for (const [key, expected] of Object.entries(constraint as Record<string, unknown>)) {
    const actual = row[key];
    if (expected !== null && typeof expected === 'object' && !Array.isArray(expected)) {
      const operators = expected as Record<string, unknown>;
      if (Array.isArray(operators.in) && operators.in.includes(actual)) {
        continue;
      }
      return false;
    }
    if (actual !== expected) {
      return false;
    }
  }
  return true;
}

export function engineFor(
  client: Record<string, any>,
  provider: string,
  constraints: Record<string, unknown>,
  hiddenFields?: ReadonlyMap<string, ReadonlySet<string>>,
  models: readonly DatamodelModel[] = scopedModels,
): GolemEngine {
  const authorization: AuthorizationProvider = {
    authorize: async () => undefined,
    constrain: async (_action, model) => constraints[model],
    check: async (_action, model, entity) => satisfies(entity, constraints[model]),
  };
  return new GolemEngine(client, models, {
    provider,
    authorization,
    hiddenFields,
    checkWriteResults: false,
    checkReadFields: false,
  });
}

export function integers(row: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(row)) {
    out[key] = typeof value === 'bigint' ? Number(value) : value;
  }
  return out;
}

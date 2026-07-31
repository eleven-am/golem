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
    rank: 3,
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
    rank: 2,
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

export async function seed(client: {
  $executeRawUnsafe(sql: string, ...values: unknown[]): Promise<unknown>;
}, placeholder: (position: number) => string): Promise<void> {
  const rows = (count: number, width: number) =>
    Array.from(
      { length: count },
      (_, row) =>
        `(${Array.from({ length: width }, (_, column) =>
          placeholder(row * width + column + 1),
        ).join(', ')})`,
    ).join(', ');
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
): GolemEngine {
  const authorization: AuthorizationProvider = {
    authorize: async () => undefined,
    constrain: async (_action, model) => constraints[model],
    check: async (_action, model, entity) => satisfies(entity, constraints[model]),
  };
  return new GolemEngine(client, scopedModels, {
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

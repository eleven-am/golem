import { PrismaPg } from '@prisma/adapter-pg';
import { DatamodelModel } from '../../src/datamodel';
import { field } from '../../src/testing';
import { PrismaClient } from '../prisma-postgres/generated/client';
import { engineFor } from './analytics';
import {
  ADA,
  EfficiencyPersona,
  EfficiencyShape,
  GUEST,
  MOD,
  dialectFor,
  efficiencyPlays,
  efficiencyQuery,
} from './efficiency';

export const efficiencyModels: readonly DatamodelModel[] = [
  {
    name: 'Play',
    dbName: 'efficiency_plays',
    fields: [
      field({ name: 'id', dbName: 'play_id', type: 'Int', isId: true }),
      field({ name: 'userId', dbName: 'user_id', type: 'Int' }),
      field({ name: 'ts', dbName: 'ts', type: 'DateTime' }),
      field({ name: 'msPlayed', dbName: 'ms_played', type: 'Int' }),
      field({ name: 'reasonStart', dbName: 'reason_start', type: 'String' }),
      field({ name: 'reasonEnd', dbName: 'reason_end', type: 'String' }),
      field({ name: 'trackUri', dbName: 'track_uri', type: 'String' }),
      field({ name: 'trackName', dbName: 'track_name', type: 'String' }),
      field({ name: 'artistName', dbName: 'artist_name', type: 'String' }),
    ],
  },
];

const SQLITE_DDL = `CREATE TABLE "efficiency_plays" (
  "play_id" INTEGER PRIMARY KEY,
  "user_id" INTEGER NOT NULL,
  "ts" TEXT NOT NULL,
  "ms_played" INTEGER NOT NULL,
  "reason_start" TEXT NOT NULL,
  "reason_end" TEXT NOT NULL,
  "track_uri" TEXT NOT NULL,
  "track_name" TEXT NOT NULL,
  "artist_name" TEXT NOT NULL
)`;

const POSTGRES_DDL = `CREATE TABLE "efficiency_plays" (
  "play_id" INTEGER PRIMARY KEY,
  "user_id" INTEGER NOT NULL,
  "ts" TIMESTAMP(3) NOT NULL,
  "ms_played" INTEGER NOT NULL,
  "reason_start" TEXT NOT NULL,
  "reason_end" TEXT NOT NULL,
  "track_uri" TEXT NOT NULL,
  "track_name" TEXT NOT NULL,
  "artist_name" TEXT NOT NULL
)`;

interface RawClient {
  $executeRawUnsafe(sql: string, ...values: unknown[]): Promise<unknown>;
}

export async function seedEfficiencyPlays(client: RawClient, provider: string): Promise<void> {
  const sqlite = provider === 'sqlite';
  const placeholder = (position: number) => (sqlite ? '?' : `$${position}`);
  await client.$executeRawUnsafe(`DROP TABLE IF EXISTS "efficiency_plays"`);
  await client.$executeRawUnsafe(sqlite ? SQLITE_DDL : POSTGRES_DDL);
  const width = 9;
  const tuples = efficiencyPlays
    .map(
      (_, row) =>
        `(${Array.from({ length: width }, (_, column) =>
          placeholder(row * width + column + 1),
        ).join(', ')})`,
    )
    .join(', ');
  await client.$executeRawUnsafe(
    `INSERT INTO "efficiency_plays" ("play_id", "user_id", "ts", "ms_played", "reason_start", ` +
      `"reason_end", "track_uri", "track_name", "artist_name") VALUES ${tuples}`,
    ...efficiencyPlays.flatMap((row) => [
      row.id,
      row.userKey,
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

export interface EfficiencyPostgresHandle {
  readonly prisma: PrismaClient;
  close(): Promise<void>;
}

export async function openEfficiencyPostgres(url: string): Promise<EfficiencyPostgresHandle> {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: url }) });
  await seedEfficiencyPlays(prisma, 'postgresql');
  return {
    prisma,
    close: async () => {
      await prisma.$executeRawUnsafe(`DROP TABLE IF EXISTS "efficiency_plays"`);
      await prisma.$disconnect();
    },
  };
}

const constraints: Record<EfficiencyPersona, unknown> = {
  ada: { userId: ADA },
  mod: { userId: MOD },
  guest: { userId: GUEST },
  everyone: {},
};

export function efficiencyRunner(
  client: Record<string, any>,
  provider: string,
): (persona: EfficiencyPersona, shape: EfficiencyShape) => Promise<Record<string, unknown>[]> {
  const sql = dialectFor(provider);
  return async (persona, shape) => {
    const rows = await engineFor(
      client,
      provider,
      { Play: constraints[persona] },
      undefined,
      efficiencyModels,
    )
      .scoped({ model: 'Play', context: { caller: persona } })
      .query(efficiencyQuery(shape, sql) as never)
      .execute();
    return rows as Record<string, unknown>[];
  };
}

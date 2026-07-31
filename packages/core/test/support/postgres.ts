import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '../prisma-postgres/generated/client';

export const POSTGRES_URL_ENV = 'GOLEM_POLICY_PG_URL';

export const POSTGRES_OPTIONAL_ENV = 'GOLEM_POLICY_PG_OPTIONAL';

export const POSTGRES_OPTIONAL = (process.env[POSTGRES_OPTIONAL_ENV] ?? '') !== '';

export const POSTGRES_URL_HINT =
  `the Postgres suite needs a live server in ${POSTGRES_URL_ENV}, and skipping it silently would hide the ` +
  'divergence it exists to catch: ' +
  'docker run -d --name golem-policy-pg-default -e POSTGRES_PASSWORD=golem -e POSTGRES_DB=golem ' +
  '-e POSTGRES_INITDB_ARGS=--locale=en_US.utf8 -p 55432:5432 postgres:17, then export ' +
  `${POSTGRES_URL_ENV}=postgresql://postgres:golem@127.0.0.1:55432/golem. ` +
  `Set ${POSTGRES_OPTIONAL_ENV}=1 to skip the Postgres suite while iterating locally.`;

const DDL = [
  `DROP TABLE IF EXISTS "posts", "profiles", "metrics", "users", "secrets"`,
  `CREATE TABLE "users" (
     "user_id" INTEGER PRIMARY KEY,
     "name" TEXT NOT NULL,
     "tenant_id" INTEGER NOT NULL
   )`,
  `CREATE TABLE "posts" (
     "post_id" INTEGER PRIMARY KEY,
     "title" TEXT NOT NULL,
     "author_id" INTEGER NOT NULL REFERENCES "users"("user_id"),
     "published" BOOLEAN NOT NULL,
     "views" INTEGER NOT NULL,
     "secret_note" TEXT NOT NULL
   )`,
  `CREATE TABLE "secrets" (
     "id" INTEGER PRIMARY KEY,
     "value" TEXT NOT NULL
   )`,
  `CREATE TABLE "profiles" (
     "profile_id" INTEGER PRIMARY KEY,
     "bio" TEXT NOT NULL,
     "user_id" INTEGER NOT NULL UNIQUE REFERENCES "users"("user_id")
   )`,
  `CREATE TABLE "metrics" (
     "metric_id" INTEGER PRIMARY KEY,
     "label" TEXT NOT NULL,
     "owner_id" INTEGER NOT NULL,
     "note" TEXT,
     "rank_value" INTEGER,
     "score" DECIMAL(65,30),
     "hits" BIGINT NOT NULL,
     "ratio" DOUBLE PRECISION NOT NULL,
     "active" BOOLEAN NOT NULL,
     "recorded_at" TIMESTAMP(3) NOT NULL,
     FOREIGN KEY ("owner_id") REFERENCES "users"("user_id")
   )`,
];

export interface PostgresHandle {
  readonly prisma: PrismaClient;
  close(): Promise<void>;
}

export function withDatabase(url: string, name: string): string {
  const parsed = new URL(url);
  parsed.pathname = `/${name}`;
  return parsed.toString();
}

export async function ensureDatabase(url: string, name: string): Promise<string> {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: url }) });
  const found = await prisma.$queryRawUnsafe<{ present: number }[]>(
    `SELECT count(*)::int AS present FROM pg_database WHERE datname = $1`,
    name,
  );
  if (found[0]!.present === 0) {
    await prisma.$executeRawUnsafe(`CREATE DATABASE "${name}"`);
  }
  await prisma.$disconnect();
  return withDatabase(url, name);
}

export async function openPostgres(url: string): Promise<PostgresHandle> {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: url }) });
  for (const statement of DDL) {
    await prisma.$executeRawUnsafe(statement);
  }
  return {
    prisma,
    close: async () => {
      await prisma.$disconnect();
    },
  };
}

import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '../prisma-postgres/generated/client';

export const POSTGRES_URL_ENV = 'GOLEM_POLICY_PG_URL';

export const POSTGRES_C_URL_ENV = 'GOLEM_POLICY_PG_C_URL';

export const POSTGRES_OPTIONAL_ENV = 'GOLEM_POLICY_PG_OPTIONAL';

export const POSTGRES_OPTIONAL = (process.env[POSTGRES_OPTIONAL_ENV] ?? '') !== '';

export const POSTGRES_URL_HINT =
  `the Postgres suites need a live server in ${POSTGRES_URL_ENV}, and skipping them silently would hide every ` +
  'disagreement they exist to catch: ' +
  'docker run -d --name golem-policy-pg-default -e POSTGRES_PASSWORD=golem -e POSTGRES_DB=golem ' +
  '-e POSTGRES_INITDB_ARGS=--locale=en_US.utf8 -p 55432:5432 postgres:17, then export ' +
  `${POSTGRES_URL_ENV}=postgresql://postgres:golem@127.0.0.1:55432/golem. ` +
  `Set ${POSTGRES_OPTIONAL_ENV}=1 to skip the Postgres suites while iterating locally.`;

export const POSTGRES_C_URL_HINT =
  `the cross-collation tests need a second, byte-order server in ${POSTGRES_C_URL_ENV}; without it the ` +
  'strongest tests in this file skip inside an otherwise passing suite: ' +
  'docker run -d --name golem-policy-pg-c -e POSTGRES_PASSWORD=golem -e POSTGRES_DB=golem ' +
  '-e POSTGRES_INITDB_ARGS=--locale=C -p 55433:5432 postgres:17, then export ' +
  `${POSTGRES_C_URL_ENV}=postgresql://postgres:golem@127.0.0.1:55433/golem. ` +
  `Set ${POSTGRES_OPTIONAL_ENV}=1 to skip the Postgres suites while iterating locally.`;

const DDL = [
  `DROP TABLE IF EXISTS "Parent"`,
  `DROP TABLE IF EXISTS "Related"`,
  `CREATE TABLE "Related" (
     "id" INTEGER PRIMARY KEY,
     "tag" TEXT NOT NULL,
     "tagNull" TEXT,
     "num" INTEGER NOT NULL,
     "numNull" INTEGER,
     "big" BIGINT NOT NULL,
     "at" TIMESTAMP(3) NOT NULL,
     "nextId" INTEGER REFERENCES "Related"("id")
   )`,
  `CREATE TABLE "Parent" (
     "id" INTEGER PRIMARY KEY,
     "name" TEXT NOT NULL,
     "nameNull" TEXT,
     "count" INTEGER NOT NULL,
     "countNull" INTEGER,
     "big" BIGINT NOT NULL,
     "bigNull" BIGINT,
     "at" TIMESTAMP(3) NOT NULL,
     "atNull" TIMESTAMP(3),
     "relatedId" INTEGER REFERENCES "Related"("id")
   )`,
];

export function withDatabase(url: string, name: string): string {
  const parsed = new URL(url);
  parsed.pathname = `/${name}`;
  return parsed.toString();
}

export async function ensureDatabase(url: string, name: string): Promise<void> {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: url }) });
  const found = await prisma.$queryRawUnsafe<{ present: number }[]>(
    `SELECT count(*)::int AS present FROM pg_database WHERE datname = $1`,
    name,
  );
  if (found[0]!.present === 0) {
    await prisma.$executeRawUnsafe(`CREATE DATABASE "${name}"`);
  }
  await prisma.$disconnect();
  const created = new PrismaClient({
    adapter: new PrismaPg({ connectionString: withDatabase(url, name) }),
  });
  await created.$queryRawUnsafe(`SELECT 1 AS ready`);
  await created.$disconnect();
}

export interface PostgresHandle {
  readonly prisma: PrismaClient;
  readonly sql: readonly string[];
  close(): Promise<void>;
}

export async function openPostgres(url: string): Promise<PostgresHandle> {
  const sql: string[] = [];
  const prisma = new PrismaClient({
    adapter: new PrismaPg({ connectionString: url }),
    log: [{ emit: 'event', level: 'query' }],
  });
  (prisma as unknown as { $on(event: 'query', handler: (e: { query: string }) => void): void }).$on(
    'query',
    (event) => {
      sql.push(event.query);
    },
  );
  for (const statement of DDL) {
    await prisma.$executeRawUnsafe(statement);
  }
  return {
    prisma,
    sql,
    close: async () => {
      await prisma.$disconnect();
    },
  };
}

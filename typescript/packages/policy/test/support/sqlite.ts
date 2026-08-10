import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from '../prisma/generated/client';

const DDL = [
  `CREATE TABLE "Related" (
     "id" INTEGER PRIMARY KEY,
     "tag" TEXT NOT NULL,
     "tagNull" TEXT,
     "num" INTEGER NOT NULL,
     "numNull" INTEGER,
     "big" BIGINT NOT NULL,
     "at" DATETIME NOT NULL,
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
     "at" DATETIME NOT NULL,
     "atNull" DATETIME,
     "relatedId" INTEGER REFERENCES "Related"("id")
   )`,
];

export interface SqliteHandle {
  readonly prisma: PrismaClient;
  readonly sql: readonly string[];
  close(): Promise<void>;
}

export async function openSqlite(): Promise<SqliteHandle> {
  const directory = mkdtempSync(join(tmpdir(), 'golem-policy-sqlite-'));
  const file = join(directory, 'policy.db');
  const sql: string[] = [];
  const prisma = new PrismaClient({
    adapter: new PrismaBetterSqlite3({ url: `file:${file}` }),
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
      rmSync(directory, { recursive: true, force: true });
    },
  };
}

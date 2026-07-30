import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from '../prisma/generated/client';

const DDL = [
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
  `CREATE TABLE "metrics" (
     "metric_id" INTEGER PRIMARY KEY,
     "label" TEXT NOT NULL,
     "owner_id" INTEGER NOT NULL,
     "note" TEXT,
     "rank_value" INTEGER,
     "score" DECIMAL,
     "hits" BIGINT NOT NULL,
     "ratio" REAL NOT NULL,
     "active" BOOLEAN NOT NULL,
     "recorded_at" DATETIME NOT NULL
   )`,
];

export interface SqliteHandle {
  readonly prisma: PrismaClient;
  close(): Promise<void>;
}

export async function openSqlite(): Promise<SqliteHandle> {
  const directory = mkdtempSync(join(tmpdir(), 'golem-core-sqlite-'));
  const file = join(directory, 'scoped.db');
  const prisma = new PrismaClient({ adapter: new PrismaBetterSqlite3({ url: `file:${file}` }) });
  for (const statement of DDL) {
    await prisma.$executeRawUnsafe(statement);
  }
  return {
    prisma,
    close: async () => {
      await prisma.$disconnect();
      rmSync(directory, { recursive: true, force: true });
    },
  };
}

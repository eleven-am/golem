import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from '../prisma/generated/client';

const DDL = [
  `CREATE TABLE "_golem_upsert_guard" (
     "stripe" INTEGER NOT NULL PRIMARY KEY,
     "seq" BIGINT NOT NULL DEFAULT 0
   )`,
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
     "score" DECIMAL,
     "hits" BIGINT NOT NULL,
     "ratio" REAL NOT NULL,
     "active" BOOLEAN NOT NULL,
     "recorded_at" DATETIME NOT NULL,
     FOREIGN KEY ("owner_id") REFERENCES "users"("user_id")
   )`,
  `CREATE TABLE "plays" (
     "play_id" INTEGER PRIMARY KEY,
     "user_id" INTEGER NOT NULL,
     "ts" TEXT NOT NULL,
     "ms_played" INTEGER NOT NULL,
     "reason_start" TEXT NOT NULL,
     "reason_end" TEXT NOT NULL,
     "track_uri" TEXT NOT NULL,
     "track_name" TEXT NOT NULL,
     "artist_name" TEXT NOT NULL
   )`,
  `CREATE INDEX "posts_author_id_idx" ON "posts" ("author_id")`,
  `CREATE TABLE "threads" (
     "id" INTEGER PRIMARY KEY,
     "title" TEXT NOT NULL
   )`,
  `CREATE TABLE "replies" (
     "id" INTEGER PRIMARY KEY,
     "thread_id" INTEGER NOT NULL REFERENCES "threads"("id") ON DELETE CASCADE,
     "body" TEXT NOT NULL,
     "amount" DECIMAL,
     "posted_at" DATETIME NOT NULL
   )`,
  `CREATE TABLE "watches" (
     "id" INTEGER PRIMARY KEY,
     "thread_id" INTEGER REFERENCES "threads"("id") ON DELETE SET NULL
   )`,
  `CREATE TABLE "channels" (
     "slug" TEXT NOT NULL UNIQUE,
     "title" TEXT NOT NULL
   )`,
  `CREATE TABLE "messages" (
     "id" INTEGER PRIMARY KEY,
     "channel_slug" TEXT NOT NULL REFERENCES "channels"("slug") ON DELETE CASCADE
   )`,
  `CREATE TABLE "people" (
     "id" INTEGER PRIMARY KEY,
     "buddy_id" INTEGER REFERENCES "people"("id") ON DELETE SET NULL
   )`,
  `CREATE TABLE "pins" (
     "id" INTEGER PRIMARY KEY,
     "channel_slug" TEXT REFERENCES "channels"("slug") ON DELETE SET NULL
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

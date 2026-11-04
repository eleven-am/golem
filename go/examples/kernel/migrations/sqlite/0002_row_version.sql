CREATE TABLE IF NOT EXISTS "golem_row_version" ("value" INTEGER NOT NULL CHECK ("value" >= 0)) STRICT;
INSERT INTO "golem_row_version" ("value") SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM "golem_row_version");
ALTER TABLE "notes" ADD COLUMN "golem_version" INTEGER NOT NULL DEFAULT 0;

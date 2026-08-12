CREATE TABLE "versioned_notes" (
  "id" TEXT NOT NULL,
  "body" TEXT NOT NULL,
  "version" INTEGER NOT NULL,
  CONSTRAINT "pk_versioned_notes" PRIMARY KEY ("id"),
  CONSTRAINT "ck_versioned_notes_id_uuid_2f0ba666c6" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;

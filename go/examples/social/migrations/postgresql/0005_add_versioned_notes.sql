CREATE TABLE "public"."versioned_notes" (
  "id" uuid NOT NULL,
  "body" text NOT NULL,
  "version" bigint NOT NULL,
  CONSTRAINT "pk_versioned_notes" PRIMARY KEY ("id")
);

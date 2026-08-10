CREATE SCHEMA IF NOT EXISTS "public";
CREATE SCHEMA IF NOT EXISTS "_golem";
CREATE TABLE "_golem"."_golem_outbox" (
  "event_id" text PRIMARY KEY,
  "fact_version" integer NOT NULL CHECK ("fact_version" > 0),
  "codec_identity" text NOT NULL,
  "generation_fingerprint" text NOT NULL,
  "model_id" text NOT NULL,
  "action" text NOT NULL CHECK ("action" IN ('created','updated','deleted')),
  "before_identity" bytea,
  "after_identity" bytea,
  "causation_id" text NOT NULL,
  "transaction_ordinal" integer NOT NULL CHECK ("transaction_ordinal" >= 0),
  "metadata" bytea NOT NULL,
  "delete_snapshot" bytea,
  "recorded_at" timestamptz(6) NOT NULL,
  UNIQUE ("causation_id", "transaction_ordinal"),
  CHECK (("action" = 'created' AND "before_identity" IS NULL AND "after_identity" IS NOT NULL) OR ("action" = 'updated' AND "before_identity" IS NOT NULL AND "after_identity" IS NOT NULL) OR ("action" = 'deleted' AND "before_identity" IS NOT NULL AND "after_identity" IS NULL))
);
CREATE INDEX "_golem_outbox_pending" ON "_golem"."_golem_outbox" ("recorded_at", "event_id");
CREATE TABLE "_golem"."_golem_outbox_delivery" ("causation_id" text NOT NULL, "status" text NOT NULL, "first_recorded_at" timestamptz(6) NOT NULL, "attempt_count" bigint NOT NULL, "available_at" timestamptz(6) NOT NULL, "lease_token" text, "lease_until" timestamptz(6), "delivered_at" timestamptz(6), "last_failure_code" text, "blocked_at" timestamptz(6), "retired_at" timestamptz(6), "updated_at" timestamptz(6) NOT NULL, PRIMARY KEY ("causation_id"), CHECK ("causation_id" ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'), CHECK ("status" IN ('pending','leased','delivered','blocked','retired')), CHECK ("attempt_count" >= 0), CHECK ("lease_token" IS NULL OR "lease_token" ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'), CHECK ("last_failure_code" IS NULL OR "last_failure_code" ~ '^[a-z0-9][a-z0-9._-]{0,127}$'), CHECK (("status" = 'pending' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'leased' AND "lease_token" IS NOT NULL AND "lease_until" IS NOT NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'delivered' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NOT NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'blocked' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NOT NULL AND "retired_at" IS NULL AND "last_failure_code" IS NOT NULL) OR ("status" = 'retired' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NOT NULL)));
CREATE INDEX "_golem_outbox_delivery_pending" ON "_golem"."_golem_outbox_delivery" ("status", "available_at", "first_recorded_at", "causation_id");
CREATE TABLE "_golem"."_golem_migrations" (
  "migration_id" text PRIMARY KEY,
  "parent_chain_hash" text NOT NULL,
  "chain_hash" text NOT NULL,
  "file_checksums" jsonb NOT NULL,
  "before_physical_fingerprint" text NOT NULL,
  "after_physical_fingerprint" text NOT NULL,
  "phases" jsonb NOT NULL,
  "applied_at" timestamptz(6) NOT NULL
);
CREATE TABLE "public"."p7_event_posts" (
  "id" uuid NOT NULL,
  "owner_id" uuid NOT NULL,
  "title" text NOT NULL,
  CONSTRAINT "pk_p7_event_posts" PRIMARY KEY ("id"),
  CONSTRAINT "ck_max_length_9df162382f90" CHECK ((char_length("title") <= 80))
);

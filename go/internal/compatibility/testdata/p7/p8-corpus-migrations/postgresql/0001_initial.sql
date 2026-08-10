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
CREATE TABLE "public"."comments" (
  "id" uuid NOT NULL,
  "post_id" uuid NOT NULL,
  "author_id" uuid NOT NULL,
  "parent_id" uuid,
  "body" text NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_comments" PRIMARY KEY ("id")
);
CREATE TABLE "public"."friendships" (
  "user_id" uuid NOT NULL,
  "friend_id" uuid NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_friendships" PRIMARY KEY ("user_id", "friend_id")
);
CREATE TABLE "public"."post_tags" (
  "post_id" uuid NOT NULL,
  "tag_name" text NOT NULL,
  CONSTRAINT "pk_post_tags" PRIMARY KEY ("post_id", "tag_name"),
  CONSTRAINT "ck_max_length_fa82c8c112ca" CHECK ((char_length("tag_name") <= 64))
);
CREATE TABLE "public"."posts" (
  "id" uuid NOT NULL,
  "author_id" uuid NOT NULL,
  "title" text NOT NULL,
  "body" text NOT NULL,
  "search" text GENERATED ALWAYS AS (lower("title")) STORED NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_posts" PRIMARY KEY ("id"),
  CONSTRAINT "uq_posts_author_title" UNIQUE ("author_id", "title"),
  CONSTRAINT "ck_max_length_aba60b903f0f" CHECK ((char_length("title") <= 160)),
  CONSTRAINT "ck_posts_title" CHECK (("title" IS NOT NULL)),
  CONSTRAINT "ck_max_length_bb2c56eec7ee" CHECK ((char_length("search") <= 160))
);
CREATE TABLE "public"."tags" (
  "id" uuid NOT NULL,
  "name" text NOT NULL,
  CONSTRAINT "pk_tags" PRIMARY KEY ("id"),
  CONSTRAINT "uq_tags_name" UNIQUE ("name"),
  CONSTRAINT "ck_max_length_9e7ec6a9df2a" CHECK ((char_length("name") <= 64))
);
CREATE TABLE "public"."users" (
  "id" uuid NOT NULL,
  "handle" text NOT NULL,
  "email" text NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_users" PRIMARY KEY ("id"),
  CONSTRAINT "uq_users_handle" UNIQUE ("handle"),
  CONSTRAINT "ck_max_length_3e45b69ed15f" CHECK ((char_length("email") <= 255)),
  CONSTRAINT "ck_max_length_b6b01e59e6db" CHECK ((char_length("handle") <= 40))
);
ALTER TABLE "public"."comments" ADD CONSTRAINT "fk_comments_parent_id" FOREIGN KEY ("parent_id") REFERENCES "public"."comments" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."comments" ADD CONSTRAINT "fk_comments_post_id" FOREIGN KEY ("post_id") REFERENCES "public"."posts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."comments" ADD CONSTRAINT "fk_comments_author_id" FOREIGN KEY ("author_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."friendships" ADD CONSTRAINT "fk_friendships_user_id" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."friendships" ADD CONSTRAINT "fk_friendships_friend_id" FOREIGN KEY ("friend_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."post_tags" ADD CONSTRAINT "fk_post_tags_post_id" FOREIGN KEY ("post_id") REFERENCES "public"."posts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."post_tags" ADD CONSTRAINT "fk_post_tags_tag_name" FOREIGN KEY ("tag_name") REFERENCES "public"."tags" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE;
ALTER TABLE "public"."posts" ADD CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
CREATE INDEX "idx_comments_post_parent" ON "public"."comments" USING BTREE ("post_id" ASC, "parent_id" ASC);
CREATE INDEX "idx_friendships_friend_user" ON "public"."friendships" USING BTREE ("friend_id" ASC, "user_id" ASC);
CREATE INDEX "idx_posts_lower_title" ON "public"."posts" USING BTREE ((lower("title")) DESC) WHERE ("title" IS NOT NULL);
CREATE INDEX "idx_posts_author_created" ON "public"."posts" USING BTREE ("author_id" ASC, "created_at" ASC);
CREATE INDEX "idx_users_created" ON "public"."users" USING BTREE ("handle" ASC, "created_at" ASC);

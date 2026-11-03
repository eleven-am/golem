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
CREATE TABLE "public"."article_audio" (
  "id" uuid NOT NULL,
  "generation_status" text DEFAULT 'PENDING' NOT NULL,
  "source_content_hash" text NOT NULL,
  "voice" text NOT NULL,
  "tts_model" text NOT NULL,
  "content_type" text DEFAULT 'audio/mpeg' NOT NULL,
  "storage_path" text,
  "specification_version" text DEFAULT 'readable-audio-v1' NOT NULL,
  "generation_error" text,
  "generated_at" timestamp(6) with time zone,
  "created_at" timestamp(6) with time zone NOT NULL,
  "updated_at" timestamp(6) with time zone NOT NULL,
  "user_id" uuid NOT NULL,
  "article_id" uuid NOT NULL,
  CONSTRAINT "pk_article_audio" PRIMARY KEY ("id"),
  CONSTRAINT "uq_article_audio_article" UNIQUE ("article_id"),
  CONSTRAINT "ck_enum_8e6f86cdb8ab" CHECK (("generation_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED')))
);
CREATE TABLE "public"."articles" (
  "id" uuid NOT NULL,
  "source_type" text DEFAULT 'URL' NOT NULL,
  "url" text,
  "title" text,
  "author" text,
  "site_name" text,
  "published_date" timestamp(6) with time zone,
  "markdown_content" text,
  "plain_text_content" text,
  "screenshot_path" text,
  "cover_image_path" text,
  "hero_image_url" text,
  "original_language" text,
  "summary" text,
  "suggested_preamble" text,
  "roleplay_character" text,
  "roleplay_user_character" text,
  "roleplay_scene" text,
  "word_count" integer,
  "reading_time_minutes" integer,
  "content_hash" text,
  "status" text DEFAULT 'PENDING' NOT NULL,
  "extract_error" text,
  "screenshot_status" text DEFAULT 'IDLE' NOT NULL,
  "analysis_status" text DEFAULT 'IDLE' NOT NULL,
  "preamble_status" text DEFAULT 'IDLE' NOT NULL,
  "embedding_status" text DEFAULT 'IDLE' NOT NULL,
  "audio_status" text DEFAULT 'IDLE' NOT NULL,
  "audio_file_path" text,
  "audio_voice_used" text,
  "audio_error" text,
  "audio_progress" double precision DEFAULT 0 NOT NULL,
  "last_read_at" timestamp(6) with time zone,
  "reading_progress" double precision DEFAULT 0 NOT NULL,
  "is_archived" boolean DEFAULT FALSE NOT NULL,
  "preference" text DEFAULT 'NEUTRAL' NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  "updated_at" timestamp(6) with time zone NOT NULL,
  "user_id" uuid NOT NULL,
  CONSTRAINT "pk_articles" PRIMARY KEY ("id"),
  CONSTRAINT "uq_articles_user_url" UNIQUE ("user_id", "url"),
  CONSTRAINT "ck_enum_509f5e2d6aed" CHECK (("embedding_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED'))),
  CONSTRAINT "ck_enum_7aad611a0cb0" CHECK (("analysis_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED'))),
  CONSTRAINT "ck_enum_825dc954d6d8" CHECK (("preamble_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED'))),
  CONSTRAINT "ck_enum_8836b064f3ba" CHECK (("status" IN ('PENDING', 'EXTRACTING', 'READY', 'FAILED'))),
  CONSTRAINT "ck_enum_99e5ee321adc" CHECK (("screenshot_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED'))),
  CONSTRAINT "ck_enum_9dc0e105e236" CHECK (("source_type" IN ('URL', 'YOUTUBE', 'PDF', 'EPUB'))),
  CONSTRAINT "ck_enum_b91c23c42acc" CHECK (("preference" IN ('NEUTRAL', 'LIKE', 'DISLIKE'))),
  CONSTRAINT "ck_enum_c789503365eb" CHECK (("audio_status" IN ('IDLE', 'PENDING', 'RUNNING', 'READY', 'FAILED')))
);
CREATE TABLE "public"."chat_messages" (
  "id" uuid NOT NULL,
  "role" text NOT NULL,
  "content" text NOT NULL,
  "status" text DEFAULT 'COMPLETED' NOT NULL,
  "error" text,
  "completed_at" timestamp(6) with time zone,
  "thread_id" uuid NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_chat_messages" PRIMARY KEY ("id"),
  CONSTRAINT "ck_enum_5985450f72ed" CHECK (("status" IN ('PENDING', 'STREAMING', 'COMPLETED', 'FAILED', 'CANCELLED')))
);
CREATE TABLE "public"."chat_threads" (
  "id" uuid NOT NULL,
  "preamble" text,
  "article_id" uuid NOT NULL,
  "user_id" uuid NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_chat_threads" PRIMARY KEY ("id"),
  CONSTRAINT "uq_chat_threads_article" UNIQUE ("article_id")
);
CREATE TABLE "public"."reading_sessions" (
  "id" uuid NOT NULL,
  "article_id" uuid NOT NULL,
  "started_at" timestamp(6) with time zone NOT NULL,
  "ended_at" timestamp(6) with time zone,
  "duration_sec" integer DEFAULT 0 NOT NULL,
  "progress_start" double precision DEFAULT 0 NOT NULL,
  "progress_end" double precision DEFAULT 0 NOT NULL,
  "session_type" text DEFAULT 'reading' NOT NULL,
  CONSTRAINT "pk_reading_sessions" PRIMARY KEY ("id")
);
CREATE TABLE "public"."users" (
  "id" uuid NOT NULL,
  "email" text NOT NULL,
  "name" text DEFAULT '' NOT NULL,
  "created_at" timestamp(6) with time zone NOT NULL,
  CONSTRAINT "pk_users" PRIMARY KEY ("id")
);
ALTER TABLE "public"."article_audio" ADD CONSTRAINT "fk_article_audio_article_id" FOREIGN KEY ("article_id") REFERENCES "public"."articles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."article_audio" ADD CONSTRAINT "fk_article_audio_user_id" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."articles" ADD CONSTRAINT "fk_articles_user_id" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."chat_messages" ADD CONSTRAINT "fk_chat_messages_thread_id" FOREIGN KEY ("thread_id") REFERENCES "public"."chat_threads" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."chat_threads" ADD CONSTRAINT "fk_chat_threads_article_id" FOREIGN KEY ("article_id") REFERENCES "public"."articles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."chat_threads" ADD CONSTRAINT "fk_chat_threads_user_id" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
ALTER TABLE "public"."reading_sessions" ADD CONSTRAINT "fk_reading_sessions_article_id" FOREIGN KEY ("article_id") REFERENCES "public"."articles" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE;
CREATE INDEX "idx_article_audio_user_status" ON "public"."article_audio" USING BTREE ("user_id" ASC, "generation_status" ASC);
CREATE INDEX "idx_articles_user_archived" ON "public"."articles" USING BTREE ("user_id" ASC, "is_archived" ASC);
CREATE INDEX "idx_articles_user_last_read" ON "public"."articles" USING BTREE ("user_id" ASC, "last_read_at" ASC);
CREATE INDEX "idx_articles_user_status" ON "public"."articles" USING BTREE ("user_id" ASC, "status" ASC);
CREATE INDEX "idx_articles_user_preference" ON "public"."articles" USING BTREE ("user_id" ASC, "preference" ASC);
CREATE INDEX "idx_chat_messages_thread_created" ON "public"."chat_messages" USING BTREE ("thread_id" ASC, "created_at" ASC);

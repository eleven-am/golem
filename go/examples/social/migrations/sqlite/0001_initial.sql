CREATE TABLE "_golem_upsert_guard" ("guard_token" BLOB NOT NULL, PRIMARY KEY ("guard_token")) STRICT;
CREATE TABLE "_golem_outbox" ("event_id" TEXT NOT NULL, "fact_version" INTEGER NOT NULL, "codec_identity" TEXT NOT NULL, "generation_fingerprint" TEXT NOT NULL, "model_id" TEXT NOT NULL, "action" TEXT NOT NULL, "before_identity" BLOB, "after_identity" BLOB, "causation_id" TEXT NOT NULL, "transaction_ordinal" INTEGER NOT NULL, "metadata" BLOB NOT NULL, "delete_snapshot" BLOB, "recorded_at" INTEGER NOT NULL, PRIMARY KEY ("event_id"), UNIQUE ("causation_id", "transaction_ordinal"), CHECK ("fact_version" > 0), CHECK ("transaction_ordinal" >= 0), CHECK ("action" IN ('created','updated','deleted')), CHECK (("action" = 'created' AND "before_identity" IS NULL AND "after_identity" IS NOT NULL) OR ("action" = 'updated' AND "before_identity" IS NOT NULL AND "after_identity" IS NOT NULL) OR ("action" = 'deleted' AND "before_identity" IS NOT NULL AND "after_identity" IS NULL))) STRICT;
CREATE INDEX "_golem_outbox_pending" ON "_golem_outbox" ("recorded_at", "event_id");
CREATE TABLE "_golem_outbox_delivery" ("causation_id" TEXT NOT NULL, "status" TEXT NOT NULL, "first_recorded_at" INTEGER NOT NULL, "attempt_count" INTEGER NOT NULL, "available_at" INTEGER NOT NULL, "lease_token" TEXT, "lease_until" INTEGER, "delivered_at" INTEGER, "last_failure_code" TEXT, "blocked_at" INTEGER, "retired_at" INTEGER, "updated_at" INTEGER NOT NULL, PRIMARY KEY ("causation_id"), CHECK (length("causation_id") = 36 AND "causation_id" NOT GLOB '*[^0-9a-f-]*' AND substr("causation_id",9,1) = '-' AND substr("causation_id",14,1) = '-' AND substr("causation_id",19,1) = '-' AND substr("causation_id",24,1) = '-' AND length(replace("causation_id",'-','')) = 32), CHECK ("status" IN ('pending','leased','delivered','blocked','retired')), CHECK ("attempt_count" >= 0), CHECK ("first_recorded_at" >= 0 AND "available_at" >= 0 AND "updated_at" >= 0), CHECK ("lease_token" IS NULL OR (length("lease_token") = 36 AND "lease_token" NOT GLOB '*[^0-9a-f-]*' AND substr("lease_token",9,1) = '-' AND substr("lease_token",14,1) = '-' AND substr("lease_token",19,1) = '-' AND substr("lease_token",24,1) = '-' AND length(replace("lease_token",'-','')) = 32)), CHECK ("lease_until" IS NULL OR "lease_until" >= 0), CHECK ("delivered_at" IS NULL OR "delivered_at" >= 0), CHECK ("blocked_at" IS NULL OR "blocked_at" >= 0), CHECK ("retired_at" IS NULL OR "retired_at" >= 0), CHECK ("last_failure_code" IS NULL OR (length("last_failure_code") BETWEEN 1 AND 128 AND substr("last_failure_code",1,1) GLOB '[a-z0-9]' AND "last_failure_code" NOT GLOB '*[^a-z0-9._-]*')), CHECK (("status" = 'pending' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'leased' AND "lease_token" IS NOT NULL AND "lease_until" IS NOT NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'delivered' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NOT NULL AND "blocked_at" IS NULL AND "retired_at" IS NULL) OR ("status" = 'blocked' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NOT NULL AND "retired_at" IS NULL AND "last_failure_code" IS NOT NULL) OR ("status" = 'retired' AND "lease_token" IS NULL AND "lease_until" IS NULL AND "delivered_at" IS NULL AND "blocked_at" IS NULL AND "retired_at" IS NOT NULL))) STRICT;
CREATE INDEX "_golem_outbox_delivery_pending" ON "_golem_outbox_delivery" ("status", "available_at", "first_recorded_at", "causation_id");
CREATE TABLE "_golem_migrations" ("migration_id" TEXT NOT NULL, "parent_chain_hash" TEXT NOT NULL, "chain_hash" TEXT NOT NULL, "file_checksums" TEXT NOT NULL, "before_physical_fingerprint" TEXT NOT NULL, "after_physical_fingerprint" TEXT NOT NULL, "phases" TEXT NOT NULL, "applied_at" TEXT NOT NULL, PRIMARY KEY ("migration_id")) STRICT;
CREATE TABLE "_golem_migration_lock" ("lock_id" INTEGER NOT NULL, PRIMARY KEY ("lock_id"), CHECK ("lock_id" = 1)) STRICT;
CREATE TABLE "comments" (
  "id" TEXT NOT NULL,
  "post_id" TEXT NOT NULL,
  "author_id" TEXT NOT NULL,
  "parent_id" TEXT,
  "body" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_comments" PRIMARY KEY ("id"),
  CONSTRAINT "fk_comments_parent_id" FOREIGN KEY ("parent_id") REFERENCES "comments" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "fk_comments_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "fk_comments_post_id" FOREIGN KEY ("post_id") REFERENCES "posts" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "ck_comments_created_at_datetime_6c8e07dff3" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0)),
  CONSTRAINT "ck_comments_post_id_uuid_b716a9529f" CHECK (("post_id" IS NULL OR (length("post_id") = 36 AND lower("post_id") = "post_id" AND substr("post_id", 9, 1) = '-' AND substr("post_id", 14, 1) = '-' AND substr("post_id", 19, 1) = '-' AND substr("post_id", 24, 1) = '-' AND "post_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_parent_id_uuid_c375d5a06e" CHECK (("parent_id" IS NULL OR (length("parent_id") = 36 AND lower("parent_id") = "parent_id" AND substr("parent_id", 9, 1) = '-' AND substr("parent_id", 14, 1) = '-' AND substr("parent_id", 19, 1) = '-' AND substr("parent_id", 24, 1) = '-' AND "parent_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_author_id_uuid_e11e8fbd10" CHECK (("author_id" IS NULL OR (length("author_id") = 36 AND lower("author_id") = "author_id" AND substr("author_id", 9, 1) = '-' AND substr("author_id", 14, 1) = '-' AND substr("author_id", 19, 1) = '-' AND substr("author_id", 24, 1) = '-' AND "author_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_id_uuid_fe8003d097" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "post_tags" (
  "post_id" TEXT NOT NULL,
  "tag_name" TEXT NOT NULL,
  CONSTRAINT "pk_post_tags" PRIMARY KEY ("post_id", "tag_name"),
  CONSTRAINT "fk_post_tags_tag_name" FOREIGN KEY ("tag_name") REFERENCES "tags" ("name") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "fk_post_tags_post_id" FOREIGN KEY ("post_id") REFERENCES "posts" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "ck_post_tags_tag_name_string_length_ab0f322a68" CHECK (("tag_name" IS NULL OR length("tag_name") <= 64)),
  CONSTRAINT "ck_post_tags_post_id_uuid_c1893f2571" CHECK (("post_id" IS NULL OR (length("post_id") = 36 AND lower("post_id") = "post_id" AND substr("post_id", 9, 1) = '-' AND substr("post_id", 14, 1) = '-' AND substr("post_id", 19, 1) = '-' AND substr("post_id", 24, 1) = '-' AND "post_id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "posts" (
  "id" TEXT NOT NULL,
  "author_id" TEXT NOT NULL,
  "title" TEXT NOT NULL,
  "body" TEXT NOT NULL,
  "published" INTEGER NOT NULL DEFAULT 0,
  "reactions" INTEGER NOT NULL DEFAULT 0,
  "priority" INTEGER NOT NULL DEFAULT 0,
  "views" INTEGER NOT NULL DEFAULT 0,
  "momentum" REAL NOT NULL DEFAULT 0,
  "rating" REAL NOT NULL DEFAULT 0,
  "budget" INTEGER NOT NULL DEFAULT 0,
  "live_date" TEXT NOT NULL,
  "live_time" TEXT NOT NULL,
  "metadata" TEXT NOT NULL,
  "visibility" TEXT NOT NULL DEFAULT 'PUBLIC',
  "topics" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  CONSTRAINT "pk_posts" PRIMARY KEY ("id"),
  CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "ck_posts_author_id_uuid_001811e780" CHECK (("author_id" IS NULL OR (length("author_id") = 36 AND lower("author_id") = "author_id" AND substr("author_id", 9, 1) = '-' AND substr("author_id", 14, 1) = '-' AND substr("author_id", 19, 1) = '-' AND substr("author_id", 24, 1) = '-' AND "author_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_posts_topics_json_array_0cce8d257b" CHECK (("topics" IS NULL OR (json_valid("topics") AND json_type("topics") = 'array'))),
  CONSTRAINT "ck_posts_published_bool_1cb1938a6d" CHECK (("published" IS NULL OR "published" IN (0, 1))),
  CONSTRAINT "ck_posts_id_uuid_4f339bf57d" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_posts_title_string_length_5d3782dc98" CHECK (("title" IS NULL OR length("title") <= 160)),
  CONSTRAINT "ck_posts_reactions_int16_703515526e" CHECK (("reactions" IS NULL OR "reactions" BETWEEN -32768 AND 32767)),
  CONSTRAINT "ck_posts_metadata_json_8447dc7615" CHECK (("metadata" IS NULL OR json_valid("metadata"))),
  CONSTRAINT "ck_posts_budget_decimal_93840ff4c1" CHECK (("budget" IS NULL OR "budget" BETWEEN -999999999999999999 AND 999999999999999999)),
  CONSTRAINT "ck_posts_priority_int32_a1b1c7b499" CHECK (("priority" IS NULL OR "priority" BETWEEN -2147483648 AND 2147483647)),
  CONSTRAINT "ck_posts_live_time_time_a433d0446d" CHECK (("live_time" IS NULL OR (length("live_time") = 15 AND substr("live_time", 1, 8) GLOB '[0-2][0-9]:[0-5][0-9]:[0-5][0-9]' AND CAST(substr("live_time", 1, 2) AS INTEGER) <= 23 AND substr("live_time", 9, 1) = '.' AND substr("live_time", 10, 6) NOT GLOB '*[^0-9]*'))),
  CONSTRAINT "ck_posts_created_at_datetime_ae545f5ed1" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0)),
  CONSTRAINT "ck_posts_visibility_enum_b0f0530e24" CHECK (("visibility" IS NULL OR "visibility" IN ('PUBLIC', 'FOLLOWERS', 'PRIVATE'))),
  CONSTRAINT "ck_posts_live_date_date_bfac115841" CHECK (("live_date" IS NULL OR (length("live_date") = 10 AND substr("live_date", 5, 1) = '-' AND substr("live_date", 8, 1) = '-' AND date("live_date") = "live_date"))),
  CONSTRAINT "ck_posts_updated_at_datetime_c5cbbb2e74" CHECK (("updated_at" IS NULL OR "updated_at" % 1 = 0))
) STRICT;
CREATE TABLE "sessions" (
  "id" TEXT NOT NULL,
  "user_id" TEXT NOT NULL,
  "token_hash" BLOB NOT NULL,
  "expires_at" INTEGER NOT NULL,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_sessions" PRIMARY KEY ("id"),
  CONSTRAINT "uq_sessions_token_hash" UNIQUE ("token_hash"),
  CONSTRAINT "fk_sessions_user_id" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "ck_sessions_id_uuid_2133dce2ac" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_sessions_expires_at_datetime_221cf2458d" CHECK (("expires_at" IS NULL OR "expires_at" % 1 = 0)),
  CONSTRAINT "ck_sessions_user_id_uuid_c9f91dfda8" CHECK (("user_id" IS NULL OR (length("user_id") = 36 AND lower("user_id") = "user_id" AND substr("user_id", 9, 1) = '-' AND substr("user_id", 14, 1) = '-' AND substr("user_id", 19, 1) = '-' AND substr("user_id", 24, 1) = '-' AND "user_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_sessions_created_at_datetime_d799572a51" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0))
) STRICT;
CREATE TABLE "tags" (
  "id" TEXT NOT NULL,
  "name" TEXT NOT NULL,
  CONSTRAINT "pk_tags" PRIMARY KEY ("id"),
  CONSTRAINT "uq_tags_name" UNIQUE ("name"),
  CONSTRAINT "ck_tags_name_string_length_421b07a784" CHECK (("name" IS NULL OR length("name") <= 64)),
  CONSTRAINT "ck_tags_id_uuid_9c35b31d14" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "users" (
  "id" TEXT NOT NULL,
  "handle" TEXT NOT NULL,
  "email" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_users" PRIMARY KEY ("id"),
  CONSTRAINT "uq_users_handle" UNIQUE ("handle"),
  CONSTRAINT "ck_users_handle_string_length_2edf316da4" CHECK (("handle" IS NULL OR length("handle") <= 40)),
  CONSTRAINT "ck_users_email_string_length_6984cb4cac" CHECK (("email" IS NULL OR length("email") <= 255)),
  CONSTRAINT "ck_users_id_uuid_898a2abbfe" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_users_created_at_datetime_eb0f9ab8f1" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0))
) STRICT;
CREATE INDEX "idx_comments_post_parent_created" ON "comments" ("post_id" ASC, "parent_id" ASC, "created_at" ASC, "id" ASC);
CREATE INDEX "idx_post_tags_tag_post" ON "post_tags" ("tag_name" ASC, "post_id" ASC);
CREATE INDEX "idx_posts_feed" ON "posts" ("published" ASC, "created_at" ASC, "id" ASC);
CREATE INDEX "idx_posts_author_created" ON "posts" ("author_id" ASC, "created_at" ASC, "id" ASC);
CREATE INDEX "idx_sessions_user_expires" ON "sessions" ("user_id" ASC, "expires_at" ASC);
CREATE INDEX "idx_users_created" ON "users" ("created_at" ASC, "id" ASC);

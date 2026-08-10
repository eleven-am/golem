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
  CONSTRAINT "fk_comments_parent_id" FOREIGN KEY ("parent_id") REFERENCES "comments" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "fk_comments_post_id" FOREIGN KEY ("post_id") REFERENCES "posts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "fk_comments_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "ck_comments_post_id_uuid_2782335579" CHECK (("post_id" IS NULL OR (length("post_id") = 36 AND lower("post_id") = "post_id" AND substr("post_id", 9, 1) = '-' AND substr("post_id", 14, 1) = '-' AND substr("post_id", 19, 1) = '-' AND substr("post_id", 24, 1) = '-' AND "post_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_parent_id_uuid_810cac3148" CHECK (("parent_id" IS NULL OR (length("parent_id") = 36 AND lower("parent_id") = "parent_id" AND substr("parent_id", 9, 1) = '-' AND substr("parent_id", 14, 1) = '-' AND substr("parent_id", 19, 1) = '-' AND substr("parent_id", 24, 1) = '-' AND "parent_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_created_at_datetime_92604f618c" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0)),
  CONSTRAINT "ck_comments_author_id_uuid_dc2ab5a7e5" CHECK (("author_id" IS NULL OR (length("author_id") = 36 AND lower("author_id") = "author_id" AND substr("author_id", 9, 1) = '-' AND substr("author_id", 14, 1) = '-' AND substr("author_id", 19, 1) = '-' AND substr("author_id", 24, 1) = '-' AND "author_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_comments_id_uuid_fe376b506f" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "friendships" (
  "user_id" TEXT NOT NULL,
  "friend_id" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_friendships" PRIMARY KEY ("user_id", "friend_id"),
  CONSTRAINT "fk_friendships_user_id" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "fk_friendships_friend_id" FOREIGN KEY ("friend_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "ck_friendships_created_at_datetime_3ef0f2d50b" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0)),
  CONSTRAINT "ck_friendships_friend_id_uuid_4cf6620aa1" CHECK (("friend_id" IS NULL OR (length("friend_id") = 36 AND lower("friend_id") = "friend_id" AND substr("friend_id", 9, 1) = '-' AND substr("friend_id", 14, 1) = '-' AND substr("friend_id", 19, 1) = '-' AND substr("friend_id", 24, 1) = '-' AND "friend_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_friendships_user_id_uuid_8a885a11a8" CHECK (("user_id" IS NULL OR (length("user_id") = 36 AND lower("user_id") = "user_id" AND substr("user_id", 9, 1) = '-' AND substr("user_id", 14, 1) = '-' AND substr("user_id", 19, 1) = '-' AND substr("user_id", 24, 1) = '-' AND "user_id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "post_tags" (
  "post_id" TEXT NOT NULL,
  "tag_name" TEXT NOT NULL,
  CONSTRAINT "pk_post_tags" PRIMARY KEY ("post_id", "tag_name"),
  CONSTRAINT "fk_post_tags_post_id" FOREIGN KEY ("post_id") REFERENCES "posts" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "fk_post_tags_tag_name" FOREIGN KEY ("tag_name") REFERENCES "tags" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT DEFERRABLE,
  CONSTRAINT "ck_post_tags_tag_name_string_length_046c226ed7" CHECK (("tag_name" IS NULL OR length("tag_name") <= 64)),
  CONSTRAINT "ck_post_tags_post_id_uuid_3611b62b0f" CHECK (("post_id" IS NULL OR (length("post_id") = 36 AND lower("post_id") = "post_id" AND substr("post_id", 9, 1) = '-' AND substr("post_id", 14, 1) = '-' AND substr("post_id", 19, 1) = '-' AND substr("post_id", 24, 1) = '-' AND "post_id" NOT GLOB '*[^0-9a-f-]*')))
) STRICT;
CREATE TABLE "posts" (
  "id" TEXT NOT NULL,
  "author_id" TEXT NOT NULL,
  "title" TEXT NOT NULL,
  "body" TEXT NOT NULL,
  "search" TEXT NOT NULL GENERATED ALWAYS AS (lower("title")) STORED,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_posts" PRIMARY KEY ("id"),
  CONSTRAINT "uq_posts_author_title" UNIQUE ("author_id", "title"),
  CONSTRAINT "fk_posts_author_id" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE NOT DEFERRABLE,
  CONSTRAINT "ck_posts_title_string_length_0ca770be66" CHECK (("title" IS NULL OR length("title") <= 160)),
  CONSTRAINT "ck_posts_author_id_uuid_47e84b1fb1" CHECK (("author_id" IS NULL OR (length("author_id") = 36 AND lower("author_id") = "author_id" AND substr("author_id", 9, 1) = '-' AND substr("author_id", 14, 1) = '-' AND substr("author_id", 19, 1) = '-' AND substr("author_id", 24, 1) = '-' AND "author_id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_posts_id_uuid_90c4b16cd4" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_posts_title" CHECK (("title" IS NOT NULL)),
  CONSTRAINT "ck_posts_search_string_length_e300aecead" CHECK (("search" IS NULL OR length("search") <= 160)),
  CONSTRAINT "ck_posts_created_at_datetime_f4943f9fb8" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0))
) STRICT;
CREATE TABLE "tags" (
  "id" TEXT NOT NULL,
  "name" TEXT NOT NULL,
  CONSTRAINT "pk_tags" PRIMARY KEY ("id"),
  CONSTRAINT "uq_tags_name" UNIQUE ("name"),
  CONSTRAINT "ck_tags_id_uuid_963d40c6ca" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_tags_name_string_length_faca1c6f41" CHECK (("name" IS NULL OR length("name") <= 64))
) STRICT;
CREATE TABLE "users" (
  "id" TEXT NOT NULL,
  "handle" TEXT NOT NULL,
  "email" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  CONSTRAINT "pk_users" PRIMARY KEY ("id"),
  CONSTRAINT "uq_users_handle" UNIQUE ("handle"),
  CONSTRAINT "ck_users_id_uuid_5fecde2830" CHECK (("id" IS NULL OR (length("id") = 36 AND lower("id") = "id" AND substr("id", 9, 1) = '-' AND substr("id", 14, 1) = '-' AND substr("id", 19, 1) = '-' AND substr("id", 24, 1) = '-' AND "id" NOT GLOB '*[^0-9a-f-]*'))),
  CONSTRAINT "ck_users_handle_string_length_67a46a1870" CHECK (("handle" IS NULL OR length("handle") <= 40)),
  CONSTRAINT "ck_users_created_at_datetime_98efca0373" CHECK (("created_at" IS NULL OR "created_at" % 1 = 0)),
  CONSTRAINT "ck_users_email_string_length_db1af001bb" CHECK (("email" IS NULL OR length("email") <= 255))
) STRICT;
CREATE INDEX "idx_comments_post_parent" ON "comments" ("post_id" ASC, "parent_id" ASC);
CREATE INDEX "idx_friendships_friend_user" ON "friendships" ("friend_id" ASC, "user_id" ASC);
CREATE INDEX "idx_posts_lower_title" ON "posts" ((lower("title")) DESC) WHERE ("title" IS NOT NULL);
CREATE INDEX "idx_posts_author_created" ON "posts" ("author_id" ASC, "created_at" ASC);
CREATE INDEX "idx_users_created" ON "users" ("handle" ASC, "created_at" ASC);

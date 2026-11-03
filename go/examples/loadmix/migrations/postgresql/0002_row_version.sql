CREATE SEQUENCE IF NOT EXISTS "_golem"."golem_row_version" AS bigint INCREMENT BY 1 MINVALUE 1 START WITH 1 CACHE 1 NO CYCLE;
ALTER TABLE "public"."article_audio" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;
ALTER TABLE "public"."chat_messages" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;
ALTER TABLE "public"."articles" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;
ALTER TABLE "public"."chat_threads" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;

CREATE SEQUENCE IF NOT EXISTS "_golem"."golem_row_version" AS bigint INCREMENT BY 1 MINVALUE 1 START WITH 1 CACHE 1 NO CYCLE;
ALTER TABLE "public"."posts" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;
ALTER TABLE "public"."sessions" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;
ALTER TABLE "public"."comments" ADD COLUMN "golem_version" bigint DEFAULT 0 NOT NULL;

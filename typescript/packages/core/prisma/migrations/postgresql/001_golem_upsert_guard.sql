CREATE TABLE "_golem_upsert_guard" (
  "stripe" INTEGER NOT NULL,
  "seq" BIGINT NOT NULL DEFAULT 0,
  CONSTRAINT "_golem_upsert_guard_pkey" PRIMARY KEY ("stripe")
);

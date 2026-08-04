import assert from 'node:assert/strict';
import { PrismaPg } from '@prisma/adapter-pg';
import { PrismaClient } from '../postgres/generated/client';
import { PrismaJobStore } from '../../src/prisma-job-store';
import type { ClaimInput } from '../../src/job-store';

/**
 * Opt-in: set GOLEM_QUEUE_PG_URL and run `npm run test:postgres`.
 *
 * The SQLite suite proves the guards enforce; this proves they *run* on a second
 * engine. A previous release shipped a claim path that could not parse on
 * Postgres at all, because nothing executed it there.
 *
 *   docker run -d --name golem-queue-pg -e POSTGRES_PASSWORD=golem \
 *     -e POSTGRES_DB=queue -p 55432:5432 postgres:16-alpine
 *   GOLEM_QUEUE_PG_URL=postgresql://postgres:golem@127.0.0.1:55432/queue \
 *     npm run test:postgres
 */
const URL = process.env.GOLEM_QUEUE_PG_URL;
if (!URL) {
  console.log('GOLEM_QUEUE_PG_URL is not set; skipping the Postgres suite');
  process.exit(0);
}

const DDL = [
  `DROP TABLE IF EXISTS "Job"`,
  `DROP TYPE IF EXISTS "JobStatus"`,
  `CREATE TYPE "JobStatus" AS ENUM ('PENDING','RUNNING','SUCCEEDED','FAILED')`,
  `DROP TABLE IF EXISTS "JobGuard"`,
  `CREATE TABLE "Job" (
     "id" TEXT PRIMARY KEY, "type" TEXT NOT NULL, "payload" TEXT NOT NULL,
     "scopeType" TEXT, "scopeId" TEXT, "status" "JobStatus" NOT NULL DEFAULT 'PENDING',
     "runAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
     "attempts" INTEGER NOT NULL DEFAULT 0, "maxAttempts" INTEGER NOT NULL DEFAULT 3,
     "lastError" TEXT, "dedupeKey" TEXT UNIQUE, "leaseOwner" TEXT,
     "leaseExpiresAt" TIMESTAMP(3), "startedAt" TIMESTAMP(3),
     "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
     "updatedAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
  `CREATE TABLE "JobGuard" ("key" TEXT PRIMARY KEY, "seq" BIGINT NOT NULL DEFAULT 0,
     "windowStart" TIMESTAMP(3), "spent" BIGINT NOT NULL DEFAULT 0)`,
];

(async () => {
  const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: URL }) });
  for (const s of DDL) await prisma.$executeRawUnsafe(s);
  const store = new PrismaJobStore(prisma as never);
  let failures = 0;
  const check = async (name: string, run: () => Promise<void>) => {
    try { await run(); console.log(`  ok  ${name}`); }
    catch (e) { failures++; console.log(`  FAIL ${name}`); console.log(String((e as Error).message).slice(0, 400)); }
  };
  const seed = (id: string, type = 'hydrate') =>
    prisma.job.create({ data: { id, type, payload: '{}' } });
  const claimOf = (id: string, extra: Partial<ClaimInput> = {}): ClaimInput => ({
    id, fromStatus: 'PENDING', now: new Date(), leaseOwner: `w-${id}`,
    leaseExpiresAt: new Date(Date.now() + 60_000), ...extra,
  } as ClaimInput);

  await check('runs every guard combination on Postgres', async () => {
    for (const [i, extra] of ([
      { guardKeys: ['scope:Article'], serializeScope: true, scopeType: 'Article', scopeId: 's0' },
      { guardKeys: ['order:other'], waitsForTypes: ['other'] },
      { guardKeys: ['order:x'], notWhileRunningTypes: ['x'] },
      { guardKeys: ['pool:api'], pool: { name: 'api', types: ['hydrate'], costs: {}, cost: 1, limit: 8 } },
    ] as Partial<ClaimInput>[]).entries()) {
      await seed(`c${i}`);
      assert.equal(await store.claim(claimOf(`c${i}`, extra)), true, `combination ${i}`);
    }
  });

  await check('bounds a pool under concurrent claims', async () => {
    await prisma.job.deleteMany({});
    for (let i = 0; i < 6; i++) await seed(`p${i}`);
    const r = await Promise.all([...Array(6)].map((_, i) =>
      store.claim(claimOf(`p${i}`, {
        guardKeys: ['pool:api'],
        pool: { name: 'api', types: ['hydrate'], costs: {}, cost: 1, limit: 2 },
      }))));
    assert.equal(r.filter(Boolean).length, 2);
  });

  await check('charges every retry against the budget', async () => {
    await prisma.job.deleteMany({});
    await prisma.jobGuard.deleteMany({});
    await seed('retry');
    let admitted = 0;
    for (let a = 0; a < 10; a++) {
      const won = await store.claim(claimOf('retry', {
        guardKeys: ['pool:api'],
        pool: { name: 'api', types: ['hydrate'], costs: {}, cost: 1,
                rateLimit: 2, rateWindowMs: 60_000, rateCost: 1 },
      }));
      if (!won) break;
      admitted++;
      await store.retry({ id: 'retry', leaseOwner: 'w-retry', attempts: a + 1,
                          lastError: 'x', runAt: new Date() });
    }
    assert.equal(admitted, 2, `admitted ${admitted}`);
  });

  await prisma.$disconnect();
  console.log(failures ? `\n${failures} failed` : '\nall passed');
  process.exit(failures ? 1 : 0);
})();

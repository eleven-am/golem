import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from '../prisma7/generated/client';
import { PrismaJobStore } from '../../src/prisma-job-store';

const [, , dbFile, jobId, startAtMs] = process.argv;

(async () => {
  const prisma = new PrismaClient({
    adapter: new PrismaBetterSqlite3({ url: `file:${dbFile}` }),
  });
  const store = new PrismaJobStore(prisma as never);
  await prisma.$queryRawUnsafe('SELECT 1');
  await new Promise((resolve) =>
    setTimeout(resolve, Math.max(0, Number(startAtMs) - Date.now())),
  );
  let out = 'false';
  try {
    out = String(
      await store.claim({
        id: jobId,
        fromStatus: 'PENDING',
        now: new Date(),
        leaseOwner: `worker-${process.pid}`,
        leaseExpiresAt: new Date(Date.now() + 60_000),
        guardKeys: ['pool:api'],
        pool: { name: 'api', types: ['hydrate'], costs: {}, limit: 2, cost: 1 },
      }),
    );
  } catch (error) {
    out = `ERR:${(error as { code?: string }).code ?? String(error).slice(0, 60)}`;
  }
  console.log(out);
  await prisma.$disconnect();
})();

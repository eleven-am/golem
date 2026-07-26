import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from '../prisma7/generated/client';
import { PrismaJobStore } from '../../src/prisma-job-store';
import type { ClaimInput } from '../../src/job-store';

/**
 * Executes the store against a real SQLite engine, including across processes.
 *
 * The mocked store tests assert what the code intends to send. Nothing there
 * runs it, which is how a claim path that could not parse on two of the three
 * supported engines reached a release. These cases run the statements.
 */

const DDL = [
  `CREATE TABLE "Job" (
     "id" TEXT PRIMARY KEY,
     "type" TEXT NOT NULL,
     "payload" TEXT NOT NULL,
     "scopeType" TEXT,
     "scopeId" TEXT,
     "status" TEXT NOT NULL DEFAULT 'PENDING',
     "runAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
     "attempts" INTEGER NOT NULL DEFAULT 0,
     "maxAttempts" INTEGER NOT NULL DEFAULT 3,
     "lastError" TEXT,
     "dedupeKey" TEXT UNIQUE,
     "leaseOwner" TEXT,
     "leaseExpiresAt" DATETIME,
     "startedAt" DATETIME,
     "createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
     "updatedAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
   )`,
  `CREATE TABLE "JobGuard" ("key" TEXT PRIMARY KEY, "seq" INTEGER NOT NULL DEFAULT 0)`,
];

let failures = 0;
let directory = '';
let dbFile = '';
let prisma: PrismaClient;
let store: PrismaJobStore;

async function open(): Promise<void> {
  directory = mkdtempSync(join(tmpdir(), 'golem-queue-sqlite-'));
  dbFile = join(directory, 'queue.db');
  prisma = new PrismaClient({
    adapter: new PrismaBetterSqlite3({ url: `file:${dbFile}` }),
  });
  for (const statement of DDL) await prisma.$executeRawUnsafe(statement);
  store = new PrismaJobStore(prisma as never);
}

async function close(): Promise<void> {
  await prisma.$disconnect();
  rmSync(directory, { recursive: true, force: true });
}

async function test(name: string, run: () => Promise<void>): Promise<void> {
  await open();
  try {
    await run();
    console.log(`  ok  ${name}`);
  } catch (error) {
    failures += 1;
    console.log(`  FAIL ${name}`);
    console.log(`       ${(error as Error).message.split('\n')[0]}`);
  } finally {
    await close();
  }
}

async function seed(
  id: string,
  overrides: Partial<{ type: string; scopeType: string; scopeId: string }> = {},
): Promise<void> {
  await prisma.job.create({
    data: {
      id,
      type: overrides.type ?? 'hydrate',
      payload: '{}',
      scopeType: overrides.scopeType ?? null,
      scopeId: overrides.scopeId ?? null,
    },
  });
}

function claimOf(id: string, extra: Partial<ClaimInput> = {}): ClaimInput {
  return {
    id,
    fromStatus: 'PENDING',
    now: new Date(),
    leaseOwner: `worker-${id}`,
    leaseExpiresAt: new Date(Date.now() + 60_000),
    ...extra,
  } as ClaimInput;
}

const running = () => prisma.job.count({ where: { status: 'RUNNING' } });

async function main(): Promise<void> {
  console.log('PrismaJobStore against a real SQLite engine');

  await test('runs an unguarded claim', async () => {
    await seed('a');
    assert.equal(await store.claim(claimOf('a')), true);
    assert.equal(await running(), 1);
  });

  await test('runs every guard combination without a syntax error', async () => {
    const combinations: Array<Partial<ClaimInput>> = [
      {
        guardKeys: ['scope:Article'],
        serializeScope: true,
        scopeType: 'Article',
        scopeId: 's0',
      },
      { guardKeys: ['order:other'], waitsForTypes: ['other'] },
      {
        guardKeys: ['pool:api'],
        pool: { types: ['hydrate'], costs: {}, limit: 8, cost: 1 },
      },
      {
        guardKeys: ['order:other', 'pool:api', 'scope:Article'],
        serializeScope: true,
        scopeType: 'Article',
        scopeId: 's3',
        waitsForTypes: ['other'],
        pool: { types: ['hydrate'], costs: { hydrate: 2 }, limit: 8, cost: 2 },
      },
    ];
    for (const [index, extra] of combinations.entries()) {
      const id = `combo-${index}`;
      await seed(id, { scopeType: 'Article', scopeId: `s${index}` });
      assert.equal(await store.claim(claimOf(id, extra)), true, `combination ${index}`);
    }
  });

  await test('bounds a pool under concurrent claims', async () => {
    for (let i = 0; i < 6; i += 1) await seed(`p${i}`);
    const results = await Promise.all(
      Array.from({ length: 6 }, (_, i) =>
        store.claim(
          claimOf(`p${i}`, {
            guardKeys: ['pool:api'],
            pool: { types: ['hydrate'], costs: {}, limit: 2, cost: 1 },
          }),
        ),
      ),
    );
    assert.equal(results.filter(Boolean).length, 2);
    assert.equal(await running(), 2);
  });

  await test('keeps a scope to one live lease', async () => {
    for (let i = 0; i < 4; i += 1) {
      await seed(`s${i}`, { scopeType: 'Article', scopeId: 'same' });
    }
    await Promise.all(
      Array.from({ length: 4 }, (_, i) =>
        store.claim(
          claimOf(`s${i}`, {
            guardKeys: ['scope:Article'],
            serializeScope: true,
            scopeType: 'Article',
            scopeId: 'same',
          }),
        ),
      ),
    );
    assert.equal(await running(), 1);
  });

  await test('refuses claims while an awaited type is pending', async () => {
    await seed('blocker', { type: 'import' });
    for (let i = 0; i < 3; i += 1) await seed(`e${i}`);
    const results = await Promise.all(
      Array.from({ length: 3 }, (_, i) =>
        store.claim(
          claimOf(`e${i}`, {
            guardKeys: ['order:hydrate|import'],
            waitsForTypes: ['import'],
          }),
        ),
      ),
    );
    assert.equal(results.filter(Boolean).length, 0);
  });

  await test('records startedAt so a rate window can be derived', async () => {
    await seed('t');
    await store.claim(claimOf('t', { guardKeys: ['pool:api'] }));
    const row = await prisma.job.findUnique({ where: { id: 't' } });
    assert.ok(row?.startedAt instanceof Date);
  });

  await test('frees pool capacity once a lease expires', async () => {
    await seed('dead');
    await store.claim({
      ...claimOf('dead'),
      leaseExpiresAt: new Date(Date.now() - 1),
      guardKeys: ['pool:api'],
      pool: { types: ['hydrate'], costs: {}, limit: 1, cost: 1 },
    } as ClaimInput);
    await seed('next');
    assert.equal(
      await store.claim(
        claimOf('next', {
          guardKeys: ['pool:api'],
          pool: { types: ['hydrate'], costs: {}, limit: 1, cost: 1 },
        }),
      ),
      true,
    );
  });

  await test('bounds a rate budget, counting jobs that already finished', async () => {
    for (let i = 0; i < 5; i += 1) await seed(`r${i}`);
    const windowStart = new Date(Date.now() - 60_000);
    const admitted: boolean[] = [];
    for (let i = 0; i < 5; i += 1) {
      const won = await store.claim(
        claimOf(`r${i}`, {
          guardKeys: ['pool:api'],
          pool: {
            types: ['hydrate'],
            costs: {},
            cost: 1,
            rateLimit: 2,
            rateWindowStart: windowStart,
            rateCosts: {},
            rateCost: 1,
          },
        }),
      );
      admitted.push(won);
      if (won) {
        // Finish it. A completed job still spent its budget, so the next claim
        // must still be refused.
        await prisma.job.update({
          where: { id: `r${i}` },
          data: { status: 'SUCCEEDED', leaseOwner: null, leaseExpiresAt: null },
        });
      }
    }
    assert.equal(admitted.filter(Boolean).length, 2, 'budget admitted too many');
  });

  await test('withholds rows inside the rate window when pruning', async () => {
    await seed('kept');
    await store.claim(claimOf('kept', { guardKeys: ['pool:api'] }));
    await prisma.job.update({
      where: { id: 'kept' },
      data: { status: 'SUCCEEDED' },
    });
    const removed = await store.deleteTerminalBefore({
      statuses: ['SUCCEEDED', 'FAILED'],
      before: new Date(Date.now() + 60_000),
      keepStartedAfter: new Date(Date.now() - 60_000),
    });
    assert.equal(removed, 0);
  });

  // The case no in-process test can reach: separate workers contending for one
  // pool on one database file. Reading before writing the guard row leaves a
  // deferred reader that cannot upgrade while another worker holds the writer
  // lock, which surfaces as a timeout rather than a refusal.
  await test('bounds a pool across worker processes without timeouts', async () => {
    const WORKERS = 8;
    const ids = Array.from({ length: WORKERS }, (_, i) => `x${i}`);
    for (const id of ids) await seed(id);
    await prisma.$disconnect();

    const startAt = Date.now() + 2000;
    const outputs = await Promise.all(
      ids.map(
        (id) =>
          new Promise<string>((resolve) => {
            const child = spawn(
              'node',
              [join(__dirname, 'worker.js'), dbFile, id, String(startAt)],
              { stdio: ['ignore', 'pipe', 'inherit'] },
            );
            let buffer = '';
            child.stdout.on('data', (chunk) => (buffer += chunk));
            child.on('close', () => resolve(buffer.trim()));
          }),
      ),
    );

    prisma = new PrismaClient({
      adapter: new PrismaBetterSqlite3({ url: `file:${dbFile}` }),
    });
    const admitted = await running();
    const errors = outputs.filter((line) => line.startsWith('ERR'));

    assert.equal(errors.length, 0, `workers errored: ${errors.join(', ')}`);
    assert.equal(admitted, 2, `admitted ${admitted}, expected 2`);
    assert.equal(outputs.filter((line) => line === 'true').length, 2);
  });

  if (failures > 0) {
    console.log(`\n${failures} failed`);
    process.exit(1);
  }
  console.log('\nall passed');
}

void main();

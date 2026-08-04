import { copyFileSync, existsSync, mkdirSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import { basename, join, resolve } from 'path';
import { INestApplication } from '@nestjs/common';
import { Test, TestingModule } from '@nestjs/testing';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { PrismaClient } from '../src/generated/prisma/client';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { seed } from '../src/seed';
import type { DemoGolemOptions } from '../src/app.module';

const SCHEMA_TEMPLATE = resolve(__dirname, '../prisma/dev.db');
const ARTIFACT_ROOT = join(tmpdir(), 'golem-demo-e2e');

export interface DemoTestContext {
  moduleRef: TestingModule;
  app: INestApplication;
  prisma: GolemPrismaService;
}

export function databaseFileFor(testPath: string): string {
  const suite = basename(testPath).replace(/\.e2e\.test\.ts$/, '');
  return join(ARTIFACT_ROOT, `${suite}.db`);
}

export function removeDatabaseFiles(databaseFile: string): void {
  for (const suffix of ['', '-journal', '-wal', '-shm']) {
    rmSync(`${databaseFile}${suffix}`, { force: true });
  }
}

export function provisionDatabase(testPath: string): string {
  if (!existsSync(SCHEMA_TEMPLATE)) {
    throw new Error(`Demo schema template missing at ${SCHEMA_TEMPLATE}; run "npm run push -w demo" first`);
  }
  mkdirSync(ARTIFACT_ROOT, { recursive: true });
  const databaseFile = databaseFileFor(testPath);
  removeDatabaseFiles(databaseFile);
  copyFileSync(SCHEMA_TEMPLATE, databaseFile);
  return databaseFile;
}

export async function bootDemoApp(
  testPath: string,
  options: { golem?: DemoGolemOptions; seedBeforeBootstrap?: boolean } = {},
): Promise<DemoTestContext> {
  const databaseFile = provisionDatabase(testPath);
  if (options.seedBeforeBootstrap) {
    const raw = new PrismaClient({
      adapter: new PrismaBetterSqlite3({ url: `file:${databaseFile}` }),
    });
    try {
      await seed(raw);
    } finally {
      await raw.$disconnect();
    }
  }
  const moduleRef = await Test.createTestingModule({
    imports: [AppModule.forDatabase(`file:${databaseFile}`, {}, options.golem)],
  }).compile();
  const app = moduleRef.createNestApplication();
  await app.init();
  const prisma = app.get(GolemPrismaService);
  if (!options.seedBeforeBootstrap) await seed(prisma);
  return { moduleRef, app, prisma };
}

export async function shutdownDemoApp(app: INestApplication, testPath: string): Promise<void> {
  await app.close();
  removeDatabaseFiles(databaseFileFor(testPath));
}

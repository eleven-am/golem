import type { Prisma, PrismaClient } from './generated/client';
import { PrismaJobStore } from '../../src/prisma-job-store';

declare const prisma: PrismaClient;
declare const transaction: Prisma.TransactionClient;

const store = new PrismaJobStore(prisma);
store.withClient(transaction);

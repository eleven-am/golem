import type {
  CancellableJob,
  ClaimCandidate,
  ClaimInput,
  CreateJobInput,
  DedupeQuery,
  FailExpiredLeaseInput,
  FailInput,
  JobQuery,
  JobStatus,
  JobStore,
  JobSummary,
  OwnedJob,
  PruneInput,
  RequeueInput,
  RetryInput,
  ScopeQuery,
} from './job-store';

type Where = Record<string, unknown>;
type Data = Record<string, unknown>;

interface PrismaJobDelegate {
  create(args: { data: Data }): Promise<unknown>;
  findMany(args: {
    where: Where;
    orderBy?: Data;
    take?: number;
    skip?: number;
    select: Data;
  }): Promise<Record<string, unknown>[]>;
  findFirst(args: {
    where: Where;
    select: Data;
  }): Promise<Record<string, unknown> | null>;
  updateMany(args: { where: Where; data: Data }): Promise<{ count: number }>;
  deleteMany(args: { where: Where }): Promise<{ count: number }>;
}

export interface PrismaClientLike {
  readonly job: PrismaJobDelegate;
}

const UNIQUE_VIOLATION = 'P2002';

const CANCELLABLE_SELECT = {
  id: true,
  type: true,
  payload: true,
  scopeType: true,
  scopeId: true,
} as const;

const SUMMARY_SELECT = {
  ...CANCELLABLE_SELECT,
  status: true,
  attempts: true,
  maxAttempts: true,
  lastError: true,
  runAt: true,
  createdAt: true,
  updatedAt: true,
} as const;

function isUniqueViolation(error: unknown): boolean {
  return (error as { code?: string })?.code === UNIQUE_VIOLATION;
}

function ids(rows: Record<string, unknown>[]): string[] {
  return rows.map((row) => row.id as string);
}

export class PrismaJobStore implements JobStore {
  constructor(private readonly prisma: PrismaClientLike) {}

  withClient(client: PrismaClientLike): PrismaJobStore {
    return new PrismaJobStore(client);
  }

  async create(input: CreateJobInput): Promise<boolean> {
    try {
      await this.prisma.job.create({
        data: {
          ...(input.id ? { id: input.id } : {}),
          type: input.type,
          payload: input.payload,
          scopeType: input.scopeType,
          scopeId: input.scopeId,
          runAt: input.runAt,
          dedupeKey: input.dedupeKey,
          maxAttempts: input.maxAttempts,
        },
      });
      return true;
    } catch (error) {
      if (isUniqueViolation(error)) return false;
      throw error;
    }
  }

  async findClaimCandidates(input: {
    type: string;
    now: Date;
    limit: number;
  }): Promise<ClaimCandidate[]> {
    const rows = await this.prisma.job.findMany({
      where: {
        type: input.type,
        OR: [
          { status: 'PENDING', runAt: { lte: input.now } },
          { status: 'RUNNING', leaseExpiresAt: { lte: input.now } },
        ],
      },
      orderBy: { runAt: 'asc' },
      take: input.limit,
      select: {
        id: true,
        payload: true,
        scopeType: true,
        scopeId: true,
        status: true,
        attempts: true,
        maxAttempts: true,
      },
    });
    return rows as unknown as ClaimCandidate[];
  }

  async claim(input: ClaimInput): Promise<boolean> {
    const guard =
      input.fromStatus === 'PENDING'
        ? { status: 'PENDING', runAt: { lte: input.now } }
        : { status: 'RUNNING', leaseExpiresAt: { lte: input.now } };
    const result = await this.prisma.job.updateMany({
      where: { id: input.id, ...guard },
      data: {
        status: 'RUNNING',
        leaseOwner: input.leaseOwner,
        leaseExpiresAt: input.leaseExpiresAt,
        ...(input.attempts === undefined ? {} : { attempts: input.attempts }),
        ...(input.lastError === undefined
          ? {}
          : { lastError: input.lastError }),
      },
    });
    return result.count === 1;
  }

  async failExpiredLease(input: FailExpiredLeaseInput): Promise<boolean> {
    const result = await this.prisma.job.updateMany({
      where: {
        id: input.id,
        status: 'RUNNING',
        leaseExpiresAt: { lte: input.now },
      },
      data: {
        status: 'FAILED',
        attempts: input.attempts,
        lastError: input.lastError,
        dedupeKey: null,
        leaseOwner: null,
        leaseExpiresAt: null,
      },
    });
    return result.count === 1;
  }

  async findOwnedRunningIds(input: {
    ids: readonly string[];
    leaseOwner: string;
  }): Promise<string[]> {
    const rows = await this.prisma.job.findMany({
      where: {
        id: { in: [...input.ids] },
        status: 'RUNNING',
        leaseOwner: input.leaseOwner,
      },
      select: { id: true },
    });
    return ids(rows);
  }

  async complete(input: { id: string; leaseOwner: string }): Promise<boolean> {
    const result = await this.prisma.job.updateMany({
      where: { id: input.id, status: 'RUNNING', leaseOwner: input.leaseOwner },
      data: {
        status: 'SUCCEEDED',
        dedupeKey: null,
        leaseOwner: null,
        leaseExpiresAt: null,
        lastError: null,
      },
    });
    return result.count === 1;
  }

  async findOwned(input: {
    id: string;
    leaseOwner: string;
  }): Promise<OwnedJob | null> {
    const row = await this.prisma.job.findFirst({
      where: { id: input.id, status: 'RUNNING', leaseOwner: input.leaseOwner },
      select: {
        type: true,
        payload: true,
        scopeType: true,
        scopeId: true,
        attempts: true,
        maxAttempts: true,
      },
    });
    return row as unknown as OwnedJob | null;
  }

  async fail(input: FailInput): Promise<boolean> {
    const result = await this.prisma.job.updateMany({
      where: { id: input.id, status: 'RUNNING', leaseOwner: input.leaseOwner },
      data: {
        status: 'FAILED',
        attempts: input.attempts,
        lastError: input.lastError,
        dedupeKey: null,
        leaseOwner: null,
        leaseExpiresAt: null,
      },
    });
    return result.count === 1;
  }

  async retry(input: RetryInput): Promise<boolean> {
    const result = await this.prisma.job.updateMany({
      where: { id: input.id, status: 'RUNNING', leaseOwner: input.leaseOwner },
      data: {
        status: 'PENDING',
        attempts: input.attempts,
        lastError: input.lastError,
        runAt: input.runAt,
        leaseOwner: null,
        leaseExpiresAt: null,
      },
    });
    return result.count === 1;
  }

  async findByScope(query: ScopeQuery): Promise<CancellableJob[]> {
    const rows = await this.prisma.job.findMany({
      where: {
        scopeType: query.scopeType,
        scopeId: query.scopeId,
        status: { in: [...query.statuses] },
      },
      select: CANCELLABLE_SELECT,
    });
    return rows as unknown as CancellableJob[];
  }

  async findByDedupeKeys(query: DedupeQuery): Promise<CancellableJob[]> {
    if (query.dedupeKeys.length === 0) return [];
    const rows = await this.prisma.job.findMany({
      where: {
        type: query.type,
        dedupeKey: { in: [...query.dedupeKeys] },
        status: { in: [...query.statuses] },
      },
      select: CANCELLABLE_SELECT,
    });
    return rows as unknown as CancellableJob[];
  }

  async deleteByIds(jobIds: readonly string[]): Promise<number> {
    if (jobIds.length === 0) return 0;
    const result = await this.prisma.job.deleteMany({
      where: { id: { in: [...jobIds] } },
    });
    return result.count;
  }

  async findJobs(query: JobQuery): Promise<JobSummary[]> {
    const rows = await this.prisma.job.findMany({
      where: queryWhere(query),
      orderBy: { runAt: 'desc' },
      take: query.limit ?? 100,
      skip: query.skip,
      select: SUMMARY_SELECT,
    });
    return rows as unknown as JobSummary[];
  }

  async countByStatus(query: JobQuery): Promise<Record<JobStatus, number>> {
    const rows = await this.prisma.job.findMany({
      where: queryWhere(query),
      select: { status: true },
    });
    const counts: Record<JobStatus, number> = {
      PENDING: 0,
      RUNNING: 0,
      SUCCEEDED: 0,
      FAILED: 0,
    };
    for (const row of rows) {
      counts[row.status as JobStatus] += 1;
    }
    return counts;
  }

  async requeue(input: RequeueInput): Promise<number> {
    if (input.ids.length === 0) return 0;
    const result = await this.prisma.job.updateMany({
      where: { id: { in: [...input.ids] } },
      data: {
        status: 'PENDING',
        attempts: 0,
        lastError: null,
        runAt: input.runAt,
        leaseOwner: null,
        leaseExpiresAt: null,
      },
    });
    return result.count;
  }

  async deleteTerminalBefore(input: PruneInput): Promise<number> {
    const result = await this.prisma.job.deleteMany({
      where: {
        status: { in: [...input.statuses] },
        updatedAt: { lt: input.before },
      },
    });
    return result.count;
  }
}

function queryWhere(query: JobQuery): Where {
  const where: Where = {};
  if (query.scopeType !== undefined) where.scopeType = query.scopeType;
  if (query.scopeId !== undefined) where.scopeId = query.scopeId;
  if (query.types?.length) where.type = { in: [...query.types] };
  if (query.statuses?.length) where.status = { in: [...query.statuses] };
  return where;
}

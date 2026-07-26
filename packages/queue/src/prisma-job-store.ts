import type {
  CancellableJob,
  ClaimCandidate,
  ClaimInput,
  RenewLeaseInput,
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
  count(args: { where: Where }): Promise<number>;
  updateMany(args: { where: Where; data: Data }): Promise<{ count: number }>;
  deleteMany(args: { where: Where }): Promise<{ count: number }>;
  readonly groupBy: unknown;
}

interface PrismaStatusGroupByDelegate {
  groupBy(args: {
    by: ['status'];
    where: Where;
    _count: { _all: true };
  }): Promise<Array<{ status: JobStatus; _count: { _all: number } }>>;
}

export interface PrismaGuardDelegate {
  create(args: { data: Data }): Promise<unknown>;
  update(args: { where: Where; data: Data }): Promise<unknown>;
}

export interface PrismaClientLike {
  readonly job: PrismaJobDelegate;
  /**
   * Required only when a handler uses a claim guard: `serializeByScope`,
   * `waitsFor`, `notWhileRunning`, or a resource pool. Guards need a
   * serialization point, and the JobGuard row is it.
   */
  readonly jobGuard?: PrismaGuardDelegate;
  /**
   * Required only when a handler uses a claim guard. The guard reads and the
   * claim have to commit together, and the guard row has to be written first
   * so competitors queue on a lock rather than failing to upgrade a reader.
   */
  $transaction?<T>(fn: (tx: PrismaClientLike) => Promise<T>): Promise<T>;
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
  readonly enforcesClaimGuards = true;


  constructor(
    private readonly prisma: PrismaClientLike,
  ) {
  }

  private readonly knownGuards = new Set<string>();

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
    const guardKeys = input.guardKeys ?? [];
    if (guardKeys.length === 0) {
      return this.claimUnguarded(this.prisma, input);
    }
    const runTransaction = this.prisma.$transaction?.bind(this.prisma);
    const guards = this.prisma.jobGuard;
    if (!runTransaction || !guards) {
      throw new Error(
        'A handler uses a claim guard (serializeByScope, waitsFor, notWhileRunning, or a resource pool), which needs $transaction and a jobGuard delegate on the Prisma client passed to PrismaJobStore. Add the JobGuard model to your schema and migrate.',
      );
    }
    await this.ensureGuardRows(guardKeys);
    return runTransaction(async (tx) => {
      const guardDelegate = tx.jobGuard;
      if (!guardDelegate) {
        throw new Error('The transaction client is missing the jobGuard delegate');
      }
      // Write the guard rows before reading anything. Competing claimers then
      // queue on a row lock instead of racing: on Postgres and MySQL this turns
      // a read-write conflict the engine would not serialize into a write-write
      // one it must, and on SQLite it takes the writer lock up front rather
      // than failing to upgrade a deferred reader mid-transaction.
      for (const key of guardKeys) {
        await guardDelegate.update({
          where: { key },
          data: { seq: { increment: 1 } },
        });
      }
      if (!(await this.guardsAllow(tx, input))) {
        return false;
      }
      return this.claimUnguarded(tx, input);
    });
  }

  private async ensureGuardRows(keys: readonly string[]): Promise<void> {
    for (const key of keys) {
      if (this.knownGuards.has(key)) continue;
      try {
        await this.prisma.jobGuard?.create({ data: { key } });
      } catch (error) {
        if (!isUniqueViolation(error)) throw error;
      }
      this.knownGuards.add(key);
    }
  }

  private async guardsAllow(
    tx: PrismaClientLike,
    input: ClaimInput,
  ): Promise<boolean> {
    if (input.serializeScope && input.scopeType !== null && input.scopeType !== undefined) {
      const holders = await tx.job.count({
        where: {
          id: { not: input.id },
          scopeType: input.scopeType,
          scopeId: input.scopeId ?? null,
          status: 'RUNNING',
          leaseExpiresAt: { gt: input.now },
        },
      });
      if (holders > 0) return false;
    }
    const waitsFor = input.waitsForTypes ?? [];
    if (waitsFor.length > 0) {
      const outstanding = await tx.job.count({
        where: {
          type: { in: [...waitsFor] },
          OR: [
            { status: 'PENDING', runAt: { lte: input.now } },
            { status: 'RUNNING' },
          ],
        },
      });
      if (outstanding > 0) return false;
    }
    const notWhileRunning = input.notWhileRunningTypes ?? [];
    if (notWhileRunning.length > 0) {
      const live = await tx.job.count({
        where: {
          id: { not: input.id },
          type: { in: [...notWhileRunning] },
          status: 'RUNNING',
          leaseExpiresAt: { gt: input.now },
        },
      });
      if (live > 0) return false;
    }
    const pool = input.pool;
    if (pool !== undefined && pool.limit !== undefined) {
      const members = await tx.job.findMany({
        where: {
          type: { in: [...pool.types] },
          status: 'RUNNING',
          leaseExpiresAt: { gt: input.now },
        },
        select: { type: true },
      });
      const used = members.reduce(
        (total, row) => total + (pool.costs[row.type as string] ?? 1),
        0,
      );
      if (used + pool.cost > pool.limit) return false;
    }
    if (
      pool !== undefined &&
      pool.rateLimit !== undefined &&
      pool.rateWindowStart !== undefined
    ) {
      // No status filter: a job that started inside the window spent its budget
      // whether or not it has finished since.
      const started = await tx.job.findMany({
        where: {
          type: { in: [...pool.types] },
          startedAt: { gt: pool.rateWindowStart },
        },
        select: { type: true },
      });
      const spent = started.reduce(
        (total, row) => total + (pool.rateCosts?.[row.type as string] ?? 1),
        0,
      );
      if (spent + (pool.rateCost ?? 1) > pool.rateLimit) return false;
    }
    return true;
  }

  private async claimUnguarded(
    client: PrismaClientLike,
    input: ClaimInput,
  ): Promise<boolean> {
    const guard =
      input.fromStatus === 'PENDING'
        ? { status: 'PENDING', runAt: { lte: input.now } }
        : { status: 'RUNNING', leaseExpiresAt: { lte: input.now } };
    const result = await client.job.updateMany({
      where: { id: input.id, ...guard },
      data: {
        status: 'RUNNING',
        leaseOwner: input.leaseOwner,
        leaseExpiresAt: input.leaseExpiresAt,
        startedAt: input.now,
        ...(input.attempts === undefined ? {} : { attempts: input.attempts }),
        ...(input.lastError === undefined ? {} : { lastError: input.lastError }),
      },
    });
    return result.count === 1;
  }

  async hasActiveOfTypes(input: {
    waitsFor?: readonly string[];
    notWhileRunning?: readonly string[];
    now: Date;
  }): Promise<boolean> {
    const clauses: Where[] = [];
    if ((input.waitsFor?.length ?? 0) > 0) {
      clauses.push({
        type: { in: [...(input.waitsFor ?? [])] },
        OR: [
          { status: 'PENDING', runAt: { lte: input.now } },
          { status: 'RUNNING' },
        ],
      });
    }
    if ((input.notWhileRunning?.length ?? 0) > 0) {
      clauses.push({
        type: { in: [...(input.notWhileRunning ?? [])] },
        status: 'RUNNING',
        leaseExpiresAt: { gt: input.now },
      });
    }
    if (clauses.length === 0) return false;
    const rows = await this.prisma.job.findMany({
      where: clauses.length === 1 ? clauses[0] : { OR: clauses },
      select: { id: true },
      take: 1,
    });
    return rows.length > 0;
  }

  async poolUsage(input: {
    types: readonly string[];
    costs: Readonly<Record<string, number>>;
    rateCosts: Readonly<Record<string, number>>;
    rateWindowStart?: Date;
    now: Date;
  }): Promise<{ concurrency: number; rate: number }> {
    if (input.types.length === 0) return { concurrency: 0, rate: 0 };
    const live = await this.prisma.job.findMany({
      where: {
        type: { in: [...input.types] },
        status: 'RUNNING',
        leaseExpiresAt: { gt: input.now },
      },
      select: { type: true },
    });
    const concurrency = live.reduce(
      (total, row) => total + (input.costs[row.type as string] ?? 1),
      0,
    );
    if (input.rateWindowStart === undefined) return { concurrency, rate: 0 };
    const started = await this.prisma.job.findMany({
      where: {
        type: { in: [...input.types] },
        startedAt: { gt: input.rateWindowStart },
      },
      select: { type: true },
    });
    const rate = started.reduce(
      (total, row) => total + (input.rateCosts[row.type as string] ?? 1),
      0,
    );
    return { concurrency, rate };
  }

  async renewLease(input: RenewLeaseInput): Promise<boolean> {
    const result = await this.prisma.job.updateMany({
      where: {
        id: input.id,
        status: 'RUNNING',
        leaseOwner: input.leaseOwner,
        leaseExpiresAt: { gt: input.now },
      },
      data: { leaseExpiresAt: input.leaseExpiresAt },
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

  async findJobIds(query: JobQuery): Promise<string[]> {
    const rows = await this.prisma.job.findMany({
      where: queryWhere(query),
      orderBy: { runAt: 'desc' },
      ...(query.limit === undefined ? {} : { take: query.limit }),
      skip: query.skip,
      select: { id: true },
    });
    return ids(rows);
  }

  async countByStatus(query: JobQuery): Promise<Record<JobStatus, number>> {
    // Prisma's generated groupBy is a conditional generic that cannot satisfy a
    // version-neutral structural interface, although this call is valid in
    // supported Prisma clients. Keep that compatibility cast local to groupBy.
    const statusGroupBy = this.prisma.job as PrismaJobDelegate &
      PrismaStatusGroupByDelegate;
    const rows = await statusGroupBy.groupBy({
      by: ['status'],
      where: queryWhere(query),
      _count: { _all: true },
    });
    const counts: Record<JobStatus, number> = {
      PENDING: 0,
      RUNNING: 0,
      SUCCEEDED: 0,
      FAILED: 0,
    };
    for (const row of rows) {
      counts[row.status] = row._count._all;
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
        ...(input.keepStartedAfter === undefined
          ? {}
          : {
              OR: [
                { startedAt: null },
                { startedAt: { lte: input.keepStartedAfter } },
              ],
            }),
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

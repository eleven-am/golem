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

export interface PrismaClientLike {
  readonly job: PrismaJobDelegate;
  /**
   * Required only when a handler sets `serializeByScope`. The scope predicate
   * and the claim have to land in one statement, and a same-table NOT EXISTS
   * is not expressible through the delegate's where-shape.
   */
  $executeRawUnsafe?(query: string, ...values: unknown[]): Promise<number>;
}

export interface PrismaJobStoreOptions {
  /** Physical table name, when the Job model is mapped to something else. */
  readonly table?: string;
}

const UNIQUE_VIOLATION = 'P2002';

function costExpression(
  alias: string,
  costs: Readonly<Record<string, number>>,
): { sql: string; values: unknown[] } {
  const weighted = Object.entries(costs).filter(([, cost]) => cost !== 1);
  if (weighted.length === 0) {
    return { sql: '1', values: [] };
  }
  const values: unknown[] = [];
  const branches = weighted
    .map(([type, cost]) => {
      values.push(type, cost);
      return 'WHEN ? THEN ?';
    })
    .join(' ');
  return { sql: `CASE ${alias}."type" ${branches} ELSE 1 END`, values };
}

function quoteIdentifier(name: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
    throw new Error(`Unsupported job table name: ${name}`);
  }
  return `"${name}"`;
}

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
  private readonly table: string;

  constructor(
    private readonly prisma: PrismaClientLike,
    options: PrismaJobStoreOptions = {},
  ) {
    this.table = options.table ?? 'Job';
  }

  withClient(client: PrismaClientLike): PrismaJobStore {
    return new PrismaJobStore(client, { table: this.table });
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
    if (
      input.serializeScope ||
      (input.excludeTypes?.length ?? 0) > 0 ||
      input.pool !== undefined
    ) {
      return this.claimGuarded(input);
    }
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
        startedAt: input.now,
        ...(input.attempts === undefined ? {} : { attempts: input.attempts }),
        ...(input.lastError === undefined
          ? {}
          : { lastError: input.lastError }),
      },
    });
    return result.count === 1;
  }

  private async claimGuarded(input: ClaimInput): Promise<boolean> {
    const execute = this.prisma.$executeRawUnsafe;
    if (!execute) {
      const flag = input.serializeScope
        ? 'serializeByScope'
        : input.pool !== undefined
          ? 'a resource pool'
          : 'excludes';
      throw new Error(
        `A handler sets ${flag}, which needs $executeRawUnsafe on the Prisma client passed to PrismaJobStore. The guard predicate and the claim must be one statement.`,
      );
    }
    const table = quoteIdentifier(this.table);
    const guard =
      input.fromStatus === 'PENDING'
        ? `"status" = 'PENDING' AND "runAt" <= ?`
        : `"status" = 'RUNNING' AND "leaseExpiresAt" <= ?`;
    const sets = [
      `"status" = 'RUNNING'`,
      `"leaseOwner" = ?`,
      `"leaseExpiresAt" = ?`,
      `"startedAt" = ?`,
    ];
    const values: unknown[] = [input.leaseOwner, input.leaseExpiresAt, input.now];
    if (input.attempts !== undefined) {
      sets.push(`"attempts" = ?`);
      values.push(input.attempts);
    }
    if (input.lastError !== undefined) {
      sets.push(`"lastError" = ?`);
      values.push(input.lastError);
    }
    values.push(input.id, input.now);
    const predicates: string[] = [];
    if (input.serializeScope) {
      predicates.push(`AND NOT EXISTS (
           SELECT 1 FROM ${table} AS active
           WHERE active."id" <> ${table}."id"
             AND active."scopeType" IS NOT NULL
             AND active."scopeType" = ${table}."scopeType"
             AND active."scopeId" = ${table}."scopeId"
             AND active."status" = 'RUNNING'
             AND active."leaseExpiresAt" > ?
         )`);
      values.push(input.now);
    }
    const excluded = input.excludeTypes ?? [];
    if (excluded.length > 0) {
      predicates.push(`AND NOT EXISTS (
           SELECT 1 FROM ${table} AS blocker
           WHERE blocker."type" IN (${excluded.map(() => '?').join(', ')})
             AND blocker."status" IN ('PENDING', 'RUNNING')
         )`);
      values.push(...excluded);
    }
    const pool = input.pool;
    if (pool !== undefined) {
      const cost = costExpression('pooled', pool.costs);
      predicates.push(`AND (
           SELECT COALESCE(SUM(${cost.sql}), 0)
           FROM ${table} AS pooled
           WHERE pooled."type" IN (${pool.types.map(() => '?').join(', ')})
             AND pooled."status" = 'RUNNING'
             AND pooled."leaseExpiresAt" > ?
         ) + ? <= ?`);
      values.push(
        ...cost.values,
        ...pool.types,
        input.now,
        pool.cost,
        pool.limit,
      );
    }
    const affected = await execute.call(
      this.prisma,
      `UPDATE ${table} SET ${sets.join(', ')}
       WHERE "id" = ?
         AND ${guard}
         ${predicates.join('\n         ')}`,
      ...values,
    );
    return affected === 1;
  }

  async poolUsage(input: {
    types: readonly string[];
    costs: Readonly<Record<string, number>>;
    now: Date;
  }): Promise<number> {
    if (input.types.length === 0) return 0;
    const rows = await this.prisma.job.findMany({
      where: {
        type: { in: [...input.types] },
        status: 'RUNNING',
        leaseExpiresAt: { gt: input.now },
      },
      select: { type: true },
    });
    return rows.reduce(
      (total, row) => total + (input.costs[row.type as string] ?? 1),
      0,
    );
  }

  async hasActiveOfTypes(input: { types: readonly string[] }): Promise<boolean> {
    if (input.types.length === 0) return false;
    const rows = await this.prisma.job.findMany({
      where: {
        type: { in: [...input.types] },
        status: { in: ['PENDING', 'RUNNING'] },
      },
      select: { id: true },
      take: 1,
    });
    return rows.length > 0;
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

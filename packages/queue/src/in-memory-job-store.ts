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

interface StoredJob {
  id: string;
  type: string;
  payload: string;
  scopeType: string | null;
  scopeId: string | null;
  status: JobStatus;
  runAt: Date;
  attempts: number;
  maxAttempts: number;
  lastError: string | null;
  dedupeKey: string | null;
  leaseOwner: string | null;
  leaseExpiresAt: Date | null;
  createdAt: Date;
  updatedAt: Date;
}

export class InMemoryJobStore implements JobStore {
  private readonly jobs = new Map<string, StoredJob>();
  private sequence = 0;

  all(): StoredJob[] {
    return [...this.jobs.values()];
  }

  get(id: string): StoredJob | undefined {
    return this.jobs.get(id);
  }

  create(input: CreateJobInput): Promise<boolean> {
    if (
      input.dedupeKey !== null &&
      this.all().some((job) => job.dedupeKey === input.dedupeKey)
    ) {
      return Promise.resolve(false);
    }
    const id = input.id ?? `job-${++this.sequence}`;
    if (this.jobs.has(id)) return Promise.resolve(false);
    this.jobs.set(id, {
      id,
      type: input.type,
      payload: input.payload,
      scopeType: input.scopeType,
      scopeId: input.scopeId,
      status: 'PENDING',
      runAt: input.runAt,
      attempts: 0,
      maxAttempts: input.maxAttempts,
      lastError: null,
      dedupeKey: input.dedupeKey,
      leaseOwner: null,
      leaseExpiresAt: null,
      createdAt: new Date(),
      updatedAt: new Date(),
    });
    return Promise.resolve(true);
  }

  findClaimCandidates(input: {
    type: string;
    now: Date;
    limit: number;
  }): Promise<ClaimCandidate[]> {
    const claimable = this.all()
      .filter(
        (job) =>
          job.type === input.type &&
          ((job.status === 'PENDING' && job.runAt <= input.now) ||
            (job.status === 'RUNNING' &&
              job.leaseExpiresAt !== null &&
              job.leaseExpiresAt <= input.now)),
      )
      .sort((a, b) => a.runAt.getTime() - b.runAt.getTime())
      .slice(0, input.limit)
      .map(
        ({ id, payload, scopeType, scopeId, status, attempts, maxAttempts }) => ({
          id,
          payload,
          scopeType,
          scopeId,
          status,
          attempts,
          maxAttempts,
        }),
      );
    return Promise.resolve(claimable);
  }

  claim(input: ClaimInput): Promise<boolean> {
    const job = this.jobs.get(input.id);
    if (!job) return Promise.resolve(false);
    const claimable =
      input.fromStatus === 'PENDING'
        ? job.status === 'PENDING' && job.runAt <= input.now
        : job.status === 'RUNNING' &&
          job.leaseExpiresAt !== null &&
          job.leaseExpiresAt <= input.now;
    if (!claimable) return Promise.resolve(false);
    job.status = 'RUNNING';
    job.leaseOwner = input.leaseOwner;
    job.leaseExpiresAt = input.leaseExpiresAt;
    if (input.attempts !== undefined) job.attempts = input.attempts;
    if (input.lastError !== undefined) job.lastError = input.lastError;
    return Promise.resolve(true);
  }

  failExpiredLease(input: FailExpiredLeaseInput): Promise<boolean> {
    const job = this.jobs.get(input.id);
    if (
      !job ||
      job.status !== 'RUNNING' ||
      job.leaseExpiresAt === null ||
      job.leaseExpiresAt > input.now
    ) {
      return Promise.resolve(false);
    }
    job.status = 'FAILED';
    job.attempts = input.attempts;
    job.lastError = input.lastError;
    job.dedupeKey = null;
    job.leaseOwner = null;
    job.leaseExpiresAt = null;
    return Promise.resolve(true);
  }

  findOwnedRunningIds(input: {
    ids: readonly string[];
    leaseOwner: string;
  }): Promise<string[]> {
    return Promise.resolve(
      input.ids.filter((id) => {
        const job = this.jobs.get(id);
        return (
          job?.status === 'RUNNING' && job.leaseOwner === input.leaseOwner
        );
      }),
    );
  }

  complete(input: { id: string; leaseOwner: string }): Promise<boolean> {
    const job = this.owned(input.id, input.leaseOwner);
    if (!job) return Promise.resolve(false);
    job.status = 'SUCCEEDED';
    job.dedupeKey = null;
    job.leaseOwner = null;
    job.leaseExpiresAt = null;
    job.lastError = null;
    return Promise.resolve(true);
  }

  findOwned(input: {
    id: string;
    leaseOwner: string;
  }): Promise<OwnedJob | null> {
    const job = this.owned(input.id, input.leaseOwner);
    if (!job) return Promise.resolve(null);
    const { type, payload, scopeType, scopeId, attempts, maxAttempts } = job;
    return Promise.resolve({
      type,
      payload,
      scopeType,
      scopeId,
      attempts,
      maxAttempts,
    });
  }

  fail(input: FailInput): Promise<boolean> {
    const job = this.owned(input.id, input.leaseOwner);
    if (!job) return Promise.resolve(false);
    job.status = 'FAILED';
    job.attempts = input.attempts;
    job.lastError = input.lastError;
    job.dedupeKey = null;
    job.leaseOwner = null;
    job.leaseExpiresAt = null;
    return Promise.resolve(true);
  }

  retry(input: RetryInput): Promise<boolean> {
    const job = this.owned(input.id, input.leaseOwner);
    if (!job) return Promise.resolve(false);
    job.status = 'PENDING';
    job.attempts = input.attempts;
    job.lastError = input.lastError;
    job.runAt = input.runAt;
    job.leaseOwner = null;
    job.leaseExpiresAt = null;
    return Promise.resolve(true);
  }

  findByScope(query: ScopeQuery): Promise<CancellableJob[]> {
    return Promise.resolve(
      this.all()
        .filter(
          (job) =>
            job.scopeType === query.scopeType &&
            job.scopeId === query.scopeId &&
            query.statuses.includes(job.status),
        )
        .map(toCancellable),
    );
  }

  findByDedupeKeys(query: DedupeQuery): Promise<CancellableJob[]> {
    return Promise.resolve(
      this.all()
        .filter(
          (job) =>
            job.type === query.type &&
            job.dedupeKey !== null &&
            query.dedupeKeys.includes(job.dedupeKey) &&
            query.statuses.includes(job.status),
        )
        .map(toCancellable),
    );
  }

  deleteByIds(ids: readonly string[]): Promise<number> {
    let deleted = 0;
    for (const id of ids) {
      if (this.jobs.delete(id)) deleted += 1;
    }
    return Promise.resolve(deleted);
  }

  findJobs(query: JobQuery): Promise<JobSummary[]> {
    const matched = this.matching(query)
      .sort((a, b) => b.runAt.getTime() - a.runAt.getTime())
      .slice(query.skip ?? 0, (query.skip ?? 0) + (query.limit ?? 100))
      .map(toSummary);
    return Promise.resolve(matched);
  }

  findJobIds(query: JobQuery): Promise<string[]> {
    const start = query.skip ?? 0;
    const matched = this.matching(query)
      .sort((a, b) => b.runAt.getTime() - a.runAt.getTime());
    const page = query.limit === undefined
      ? matched.slice(start)
      : matched.slice(start, start + query.limit);
    return Promise.resolve(page.map(({ id }) => id));
  }

  countByStatus(query: JobQuery): Promise<Record<JobStatus, number>> {
    const counts: Record<JobStatus, number> = {
      PENDING: 0,
      RUNNING: 0,
      SUCCEEDED: 0,
      FAILED: 0,
    };
    for (const job of this.matching(query)) {
      counts[job.status] += 1;
    }
    return Promise.resolve(counts);
  }

  requeue(input: RequeueInput): Promise<number> {
    let requeued = 0;
    for (const id of input.ids) {
      const job = this.jobs.get(id);
      if (!job) continue;
      job.status = 'PENDING';
      job.attempts = 0;
      job.lastError = null;
      job.runAt = input.runAt;
      job.leaseOwner = null;
      job.leaseExpiresAt = null;
      job.updatedAt = new Date();
      requeued += 1;
    }
    return Promise.resolve(requeued);
  }

  deleteTerminalBefore(input: PruneInput): Promise<number> {
    const doomed = this.all().filter(
      (job) =>
        input.statuses.includes(job.status) && job.updatedAt < input.before,
    );
    for (const job of doomed) this.jobs.delete(job.id);
    return Promise.resolve(doomed.length);
  }

  private matching(query: JobQuery): StoredJob[] {
    return this.all().filter(
      (job) =>
        (query.scopeType === undefined || job.scopeType === query.scopeType) &&
        (query.scopeId === undefined || job.scopeId === query.scopeId) &&
        (!query.types?.length || query.types.includes(job.type)) &&
        (!query.statuses?.length || query.statuses.includes(job.status)),
    );
  }

  private owned(id: string, leaseOwner: string): StoredJob | undefined {
    const job = this.jobs.get(id);
    if (!job || job.status !== 'RUNNING' || job.leaseOwner !== leaseOwner) {
      return undefined;
    }
    return job;
  }
}

function toCancellable(job: StoredJob): CancellableJob {
  const { id, type, payload, scopeType, scopeId } = job;
  return { id, type, payload, scopeType, scopeId };
}

function toSummary(job: StoredJob): JobSummary {
  return {
    ...toCancellable(job),
    status: job.status,
    attempts: job.attempts,
    maxAttempts: job.maxAttempts,
    lastError: job.lastError,
    runAt: job.runAt,
    createdAt: job.createdAt,
    updatedAt: job.updatedAt,
  };
}

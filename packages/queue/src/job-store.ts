export type JobStatus = 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED';

export interface CreateJobInput {
  readonly id?: string;
  readonly type: string;
  readonly payload: string;
  readonly scopeType: string | null;
  readonly scopeId: string | null;
  readonly runAt: Date;
  readonly dedupeKey: string | null;
  readonly maxAttempts: number;
}

export interface ClaimCandidate {
  readonly id: string;
  readonly payload: string;
  readonly scopeType: string | null;
  readonly scopeId: string | null;
  readonly status: JobStatus;
  readonly attempts: number;
  readonly maxAttempts: number;
}

export interface OwnedJob {
  readonly type: string;
  readonly payload: string;
  readonly scopeType: string | null;
  readonly scopeId: string | null;
  readonly attempts: number;
  readonly maxAttempts: number;
}

export interface ClaimInput {
  readonly id: string;
  readonly fromStatus: 'PENDING' | 'RUNNING';
  readonly now: Date;
  readonly leaseOwner: string;
  readonly leaseExpiresAt: Date;
  readonly attempts?: number;
  readonly lastError?: string;
  /**
   * Refuse the claim when another job in the same scope holds a live lease.
   *
   * A store MUST evaluate this predicate and the claim itself atomically. Two
   * workers polling the same scope will otherwise both observe no live holder
   * and both claim, which is the exact outcome the flag exists to prevent.
   * Reading and then writing in separate statements is not sufficient on any
   * engine, including SQLite, whose single writer serializes statements rather
   * than transactions with reads interleaved.
   *
   * "Live" means `status = 'RUNNING' AND leaseExpiresAt > now`. A crashed
   * worker leaves a RUNNING row behind; treating that as a holder would block
   * the whole scope until lease recovery ran, defeating the recovery. The
   * candidate's own row is never its own blocker, or reclaiming an expired
   * lease would block on itself.
   */
  readonly serializeScope?: boolean;
  /**
   * Refuse the claim while any job of these types is PENDING or RUNNING.
   *
   * Like `serializeScope`, a store MUST evaluate this predicate and the claim
   * in one statement. Checking first and claiming after leaves the window the
   * flag exists to close: an excluded job enqueued between the two is invisible
   * to the check and runs concurrently anyway.
   *
   * A RUNNING row whose lease has expired still blocks. Its work is not done —
   * lease recovery will reclaim and rerun it — so treating it as finished would
   * admit exactly the overlap the caller asked to prevent. This is the opposite
   * of `serializeScope`, where an expired lease must not block, because there
   * the blocked work and the expired row are the same job.
   */
  readonly waitsForTypes?: readonly string[];
  readonly notWhileRunningTypes?: readonly string[];
  /**
   * Refuse the claim when the resource pool this job draws on is already at
   * its limit.
   *
   * Occupancy is derived, not held: it is the summed cost of pool members that
   * are RUNNING with a live lease, counted at claim time. Nothing is acquired,
   * so nothing has to be released, and a worker that dies frees its units when
   * its lease expires through the same recovery that reclaims the job.
   *
   * Like the other guards this MUST be evaluated in the statement that claims.
   * A separate read admits two workers that both saw room.
   *
   * The candidate cannot block itself: a PENDING row is not RUNNING, and a row
   * being reclaimed has an expired lease.
   */
  readonly pool?: ClaimPool;
  /** Scope of the candidate, needed to evaluate `serializeScope`. */
  readonly scopeType?: string | null;
  readonly scopeId?: string | null;
  /**
   * Rows every competing claimer of this guard group must write before reading.
   * Sorted, so a claim needing several guards cannot deadlock against one
   * acquiring the same guards in another order. Empty means an unguarded claim,
   * which is a same-row compare-and-set and needs no serialization point.
   */
  readonly guardKeys?: readonly string[];
}

export interface ClaimPool {
  readonly types: readonly string[];
  readonly costs: Readonly<Record<string, number>>;
  readonly limit: number;
  readonly cost: number;
}

export interface FailExpiredLeaseInput {
  readonly id: string;
  readonly now: Date;
  readonly attempts: number;
  readonly lastError: string;
}

export interface RenewLeaseInput {
  readonly id: string;
  readonly leaseOwner: string;
  readonly leaseExpiresAt: Date;
  readonly now: Date;
}

export interface FailInput {
  readonly id: string;
  readonly leaseOwner: string;
  readonly attempts: number;
  readonly lastError: string;
}

export interface RetryInput extends FailInput {
  readonly runAt: Date;
}

export interface ScopeQuery {
  readonly scopeType: string;
  readonly scopeId: string;
  readonly statuses: readonly JobStatus[];
}

export interface DedupeQuery {
  readonly type: string;
  readonly dedupeKeys: readonly string[];
  readonly statuses: readonly JobStatus[];
}

export interface CancellableJob {
  readonly id: string;
  readonly type: string;
  readonly payload: string;
  readonly scopeType: string | null;
  readonly scopeId: string | null;
}

export interface JobSummary extends CancellableJob {
  readonly status: JobStatus;
  readonly attempts: number;
  readonly maxAttempts: number;
  readonly lastError: string | null;
  readonly runAt: Date;
  readonly createdAt: Date;
  readonly updatedAt: Date;
}

export interface JobQuery {
  readonly scopeType?: string;
  readonly scopeId?: string;
  readonly types?: readonly string[];
  readonly statuses?: readonly JobStatus[];
  readonly limit?: number;
  readonly skip?: number;
}

export interface RequeueInput {
  readonly ids: readonly string[];
  readonly runAt: Date;
}

export interface PruneInput {
  readonly statuses: readonly JobStatus[];
  readonly before: Date;
}

/**
 * TODO: Implement a Postgres-native store using SKIP LOCKED once an app needs
 * throughput beyond what poll-and-CAS claiming provides.
 */
export interface JobStore {
  /**
   * Set to true once the store evaluates every guard on `ClaimInput` inside the
   * statement or transaction that claims. The dispatcher refuses to start a
   * guarded handler against a store that does not, because the guards are
   * optional fields a store can silently ignore.
   */
  readonly enforcesClaimGuards?: boolean;
  create(input: CreateJobInput): Promise<boolean>;
  findClaimCandidates(input: {
    type: string;
    now: Date;
    limit: number;
  }): Promise<ClaimCandidate[]>;
  claim(input: ClaimInput): Promise<boolean>;
  renewLease?(input: RenewLeaseInput): Promise<boolean>;
  /**
   * Whether any job of these types is PENDING or RUNNING.
   *
   * Optional. The dispatcher uses it to skip a handler whose exclusions are
   * active rather than fetching candidates it cannot claim, and to report a
   * handler that has been blocked for a long time. Correctness does not depend
   * on it: `ClaimInput.excludeTypes` is the enforcing predicate.
   */
  hasActiveOfTypes?(input: {
    waitsFor?: readonly string[];
    notWhileRunning?: readonly string[];
    now: Date;
  }): Promise<boolean>;
  /**
   * Summed cost of pool members holding a live lease.
   *
   * Optional. The dispatcher uses it to skip a handler whose pool is full and
   * to bound how many candidates it fetches, rather than issuing claims that
   * cannot succeed. Correctness rests on `ClaimInput.pool`.
   */
  poolUsage?(input: {
    types: readonly string[];
    costs: Readonly<Record<string, number>>;
    now: Date;
  }): Promise<number>;
  failExpiredLease(input: FailExpiredLeaseInput): Promise<boolean>;
  findOwnedRunningIds(input: {
    ids: readonly string[];
    leaseOwner: string;
  }): Promise<string[]>;
  complete(input: { id: string; leaseOwner: string }): Promise<boolean>;
  findOwned(input: {
    id: string;
    leaseOwner: string;
  }): Promise<OwnedJob | null>;
  fail(input: FailInput): Promise<boolean>;
  retry(input: RetryInput): Promise<boolean>;
  findByScope(query: ScopeQuery): Promise<CancellableJob[]>;
  findByDedupeKeys(query: DedupeQuery): Promise<CancellableJob[]>;
  deleteByIds(ids: readonly string[]): Promise<number>;
  findJobs(query: JobQuery): Promise<JobSummary[]>;
  findJobIds(query: JobQuery): Promise<string[]>;
  countByStatus(query: JobQuery): Promise<Record<JobStatus, number>>;
  requeue(input: RequeueInput): Promise<number>;
  deleteTerminalBefore(input: PruneInput): Promise<number>;
}

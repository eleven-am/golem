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
  create(input: CreateJobInput): Promise<boolean>;
  findClaimCandidates(input: {
    type: string;
    now: Date;
    limit: number;
  }): Promise<ClaimCandidate[]>;
  claim(input: ClaimInput): Promise<boolean>;
  renewLease?(input: RenewLeaseInput): Promise<boolean>;
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

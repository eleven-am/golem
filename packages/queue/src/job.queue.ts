import { Inject, Injectable } from '@nestjs/common';
import type {
  CancellableJob,
  JobQuery,
  JobStatus,
  JobStore,
  JobSummary,
} from './job-store';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import {
  GOLEM_QUEUE_OPTIONS,
  JOB_STORE,
  type EnqueueOptions,
  type JobScope,
  type ResolvedGolemQueueOptions,
} from './job.types';

const PENDING_ONLY: readonly JobStatus[] = ['PENDING'];
const PENDING_AND_RUNNING: readonly JobStatus[] = ['PENDING', 'RUNNING'];
const FAILED_ONLY: readonly JobStatus[] = ['FAILED'];
const TERMINAL: readonly JobStatus[] = ['SUCCEEDED', 'FAILED'];

@Injectable()
export class JobQueue {
  constructor(
    @Inject(JOB_STORE) private readonly store: JobStore,
    private readonly cancellations: JobCancellationRegistry,
    @Inject(GOLEM_QUEUE_OPTIONS)
    private readonly options: ResolvedGolemQueueOptions,
    private readonly observers: JobObserverRegistry,
  ) {}

  add(
    type: string,
    payload: Record<string, unknown>,
    options: EnqueueOptions = {},
  ): Promise<boolean> {
    return (options.store ?? this.store).create({
      id: options.id,
      type,
      payload: JSON.stringify(payload),
      scopeType: options.scope?.type ?? null,
      scopeId: options.scope?.id ?? null,
      runAt: options.runAt ?? new Date(),
      dedupeKey: options.dedupeKey ?? null,
      maxAttempts: options.maxAttempts ?? this.options.defaultMaxAttempts,
    });
  }

  async cancelPendingForScope(scope: JobScope, reason: string): Promise<number> {
    const jobs = await this.store.findByScope({
      scopeType: scope.type,
      scopeId: scope.id,
      statuses: PENDING_ONLY,
    });
    return this.discard(jobs, reason, false);
  }

  async cancelForScope(scope: JobScope, reason: string): Promise<number> {
    const jobs = await this.store.findByScope({
      scopeType: scope.type,
      scopeId: scope.id,
      statuses: PENDING_AND_RUNNING,
    });
    this.cancellations.abortForScope(scope, reason);
    return this.discard(jobs, reason, true);
  }

  find(query: JobQuery = {}): Promise<JobSummary[]> {
    return this.store.findJobs(query);
  }

  findForScope(
    scope: JobScope,
    query: Omit<JobQuery, 'scopeType' | 'scopeId'> = {},
  ): Promise<JobSummary[]> {
    return this.store.findJobs({
      ...query,
      scopeType: scope.type,
      scopeId: scope.id,
    });
  }

  countByStatus(query: JobQuery = {}): Promise<Record<JobStatus, number>> {
    return this.store.countByStatus(query);
  }

  async retryFailed(
    query: Omit<JobQuery, 'statuses'> = {},
  ): Promise<number> {
    const failed = await this.store.findJobs({
      ...query,
      statuses: FAILED_ONLY,
    });
    if (failed.length === 0) return 0;
    return this.store.requeue({
      ids: failed.map(({ id }) => id),
      runAt: new Date(),
    });
  }

  prune(input: {
    olderThan: Date;
    statuses?: readonly JobStatus[];
  }): Promise<number> {
    return this.store.deleteTerminalBefore({
      statuses: input.statuses ?? TERMINAL,
      before: input.olderThan,
    });
  }

  async cancelByDedupeKeys(
    type: string,
    dedupeKeys: readonly string[],
    reason: string,
  ): Promise<number> {
    if (dedupeKeys.length === 0) return 0;
    const jobs = await this.store.findByDedupeKeys({
      type,
      dedupeKeys,
      statuses: PENDING_AND_RUNNING,
    });
    this.cancellations.abortJobs(
      jobs.map(({ id }) => id),
      reason,
    );
    return this.discard(jobs, reason, true);
  }

  private async discard(
    jobs: readonly CancellableJob[],
    reason: string,
    awaitInFlight: boolean,
  ): Promise<number> {
    if (jobs.length === 0) return 0;
    const jobIds = jobs.map(({ id }) => id);
    const deleted = await this.store.deleteByIds(jobIds);
    if (awaitInFlight) {
      await this.cancellations.waitForJobs(jobIds);
    }
    for (const job of jobs) {
      await this.observers.notify({
        jobId: job.id,
        type: job.type,
        payload: job.payload,
        scope:
          job.scopeType && job.scopeId
            ? { type: job.scopeType, id: job.scopeId }
            : null,
        outcome: 'cancelled',
        error: reason,
      });
    }
    return deleted;
  }
}

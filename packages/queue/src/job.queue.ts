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
  QueuePayloadError,
  type EnqueueOptions,
  type JobScope,
  type ResolvedGolemQueueOptions,
} from './job.types';

const PENDING_ONLY: readonly JobStatus[] = ['PENDING'];
const PENDING_AND_RUNNING: readonly JobStatus[] = ['PENDING', 'RUNNING'];
const FAILED_ONLY: readonly JobStatus[] = ['FAILED'];
const TERMINAL: readonly JobStatus[] = ['SUCCEEDED', 'FAILED'];

type PayloadFailure = QueuePayloadError['reason'];

class InvalidPayloadValue extends Error {
  constructor(readonly reason: PayloadFailure, options?: ErrorOptions) {
    super(reason, options);
  }
}

function validateJsonValue(value: unknown, ancestors: Set<object>): void {
  if (typeof value === 'bigint') throw new InvalidPayloadValue('BigInt');
  if (
    value === undefined ||
    typeof value === 'function' ||
    typeof value === 'symbol' ||
    (typeof value === 'number' && !Number.isFinite(value))
  ) {
    throw new InvalidPayloadValue('unsupported non-JSON values');
  }
  if (value === null || typeof value !== 'object') return;
  if (ancestors.has(value)) throw new InvalidPayloadValue('circular references');
  ancestors.add(value);
  try {
    const toJSON = (value as { toJSON?: unknown }).toJSON;
    if (typeof toJSON === 'function') {
      validateJsonValue(toJSON.call(value), ancestors);
      return;
    }
    const prototype = Object.getPrototypeOf(value);
    if (!Array.isArray(value) && prototype !== Object.prototype && prototype !== null) {
      throw new InvalidPayloadValue('unsupported non-JSON values');
    }
    for (const entry of Object.values(value as Record<string, unknown>)) {
      validateJsonValue(entry, ancestors);
    }
  } catch (error) {
    if (error instanceof InvalidPayloadValue) throw error;
    throw new InvalidPayloadValue('unsupported non-JSON values', { cause: error });
  } finally {
    ancestors.delete(value);
  }
}

function serializePayload(type: string, payload: Record<string, unknown>): string {
  try {
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
      throw new InvalidPayloadValue('unsupported non-JSON values');
    }
    validateJsonValue(payload, new Set());
    const serialized = JSON.stringify(payload);
    if (serialized === undefined) {
      throw new InvalidPayloadValue('unsupported non-JSON values');
    }
    const serializedPayload = JSON.parse(serialized) as unknown;
    if (
      !serializedPayload ||
      typeof serializedPayload !== 'object' ||
      Array.isArray(serializedPayload)
    ) {
      throw new InvalidPayloadValue('unsupported non-JSON values');
    }
    return serialized;
  } catch (error) {
    const reason = error instanceof InvalidPayloadValue
      ? error.reason
      : 'unsupported non-JSON values';
    throw new QueuePayloadError(type, reason, { cause: error });
  }
}

function requireNonEmpty(name: string, value: string): void {
  if (value.trim().length === 0) {
    throw new Error(`Invalid enqueue option ${name}=${JSON.stringify(value)}: must not be empty`);
  }
}

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
    requireNonEmpty('type', type);
    const maxAttempts = options.maxAttempts ?? this.options.defaultMaxAttempts;
    if (!Number.isInteger(maxAttempts) || maxAttempts < 1) {
      throw new Error(`Invalid enqueue option maxAttempts=${String(maxAttempts)}: must be an integer of at least 1`);
    }
    if (options.scope) {
      requireNonEmpty('scope.type', options.scope.type);
      requireNonEmpty('scope.id', options.scope.id);
    }
    if (options.dedupeKey !== undefined) {
      requireNonEmpty('dedupeKey', options.dedupeKey);
    }
    if (options.runAt !== undefined && (!(options.runAt instanceof Date) || Number.isNaN(options.runAt.getTime()))) {
      throw new Error(`Invalid enqueue option runAt=${String(options.runAt)}: must be a valid Date`);
    }
    const serialized = serializePayload(type, payload);
    return (options.store ?? this.store).create({
      id: options.id,
      type,
      payload: serialized,
      scopeType: options.scope?.type ?? null,
      scopeId: options.scope?.id ?? null,
      runAt: options.runAt ?? new Date(),
      dedupeKey: options.dedupeKey ?? null,
      maxAttempts,
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
    const failedIds = await this.store.findJobIds({
      ...query,
      statuses: FAILED_ONLY,
    });
    if (failedIds.length === 0) return 0;
    return this.store.requeue({
      ids: failedIds,
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

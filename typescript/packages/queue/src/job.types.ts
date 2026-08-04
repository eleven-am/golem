import type { JobStore } from './job-store';
import type { JobPayload, JobType } from './register';

export interface JobScope {
  readonly type: string;
  readonly id: string;
}

export interface JobExecution {
  readonly signal: AbortSignal;
}

export interface JobEvent<TType extends JobType = JobType> {
  readonly id: string;
  readonly type: TType;
  readonly payload: JobPayload<TType>;
  readonly attempt: number;
  readonly maxAttempts: number;
  readonly scope: JobScope | null;
  readonly signal: AbortSignal;
}

export interface JobWork<TType extends JobType = JobType> {
  handle(job: JobEvent<TType>): Promise<void>;
}

export interface JobHandler<TType extends JobType = JobType> extends JobWork<TType> {
  readonly type: TType;
  readonly concurrency: number;
  readonly timeoutMs: number;
  readonly serializeByScope?: boolean;
  readonly waitsFor?: readonly string[];
  readonly notWhileRunning?: readonly string[];
}

export class TerminalJobError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = TerminalJobError.name;
  }
}

export class RetryableJobError extends Error {
  constructor(
    message: string,
    readonly retryAfterMs?: number,
    readonly consumesAttempt = true,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = RetryableJobError.name;
  }
}

export class QueuePayloadError extends Error {
  constructor(
    readonly jobType: string,
    readonly reason: 'BigInt' | 'circular references' | 'unsupported non-JSON values',
    options?: ErrorOptions,
  ) {
    super(`Cannot enqueue job "${jobType}": payload contains ${reason}`, options);
    this.name = QueuePayloadError.name;
  }
}

export function retryAfterMs(error: unknown): number | undefined {
  if (!(error instanceof RetryableJobError)) return undefined;
  return error.retryAfterMs;
}

export function consumesRetryAttempt(error: unknown): boolean {
  return !(error instanceof RetryableJobError) || error.consumesAttempt;
}

export function isTerminalJobError(error: unknown): boolean {
  return (
    error instanceof TerminalJobError ||
    (typeof error === 'object' &&
      error !== null &&
      'retryable' in error &&
      error.retryable === false)
  );
}

export const JOB_HANDLER = 'GOLEM_QUEUE_JOB_HANDLER';

export const JOB_LIFECYCLE_OBSERVER = 'GOLEM_QUEUE_JOB_LIFECYCLE_OBSERVER';

export const JOB_STORE = 'GOLEM_QUEUE_JOB_STORE';

export const GOLEM_QUEUE_OPTIONS = 'GOLEM_QUEUE_OPTIONS';

export type JobLifecycleOutcome =
  | 'started'
  | 'succeeded'
  | 'retry-scheduled'
  | 'failed-terminal'
  | 'cancelled';

export interface JobLifecycleTransition {
  readonly jobId: string;
  readonly type: string;
  readonly payload: string;
  readonly scope: JobScope | null;
  readonly outcome: JobLifecycleOutcome;
  readonly attempts?: number;
  readonly maxAttempts?: number;
  readonly error?: string;
  readonly durationMs?: number;
}

export interface JobLifecycleObserver {
  onTransition(transition: JobLifecycleTransition): Promise<void>;
}

export interface EnqueueOptions {
  id?: string;
  runAt?: Date;
  dedupeKey?: string;
  maxAttempts?: number;
  scope?: JobScope;
  store?: JobStore;
}

export interface JobRetentionOptions {
  readonly statuses?: readonly ('SUCCEEDED' | 'FAILED')[];
  readonly olderThanMs: number;
  readonly sweepIntervalMs?: number;
}

export interface JobResourceOptions<TType extends string = JobType> {
  /** Maximum summed cost of pool members holding a live lease at once. */
  readonly concurrency?: number;
  /**
   * Maximum summed rate cost of pool members *started* in the last minute.
   *
   * Bounds throughput rather than parallelism, and counts jobs that have since
   * finished: a completed job still spent whatever it called. Declare this
   * instead of, or alongside, `concurrency`.
   */
  readonly ratePerMinute?: number;
  /**
   * Every job type drawing on this resource, including types whose handler
   * runs in another process. A worker counts only the types it is told about,
   * so a member missing here is a member the pool does not bound.
   */
  readonly types: readonly TType[];
  /** Concurrency slots a type occupies while running. Defaults to 1. */
  readonly costs?: Partial<Record<TType, number>>;
  /**
   * Budget a type spends when it starts. Defaults to 1.
   *
   * Separate from `costs` because the two measure different things: a job that
   * holds one connection while making fifty calls occupies one slot and spends
   * fifty units.
   */
  readonly rateCosts?: Partial<Record<TType, number>>;
}

export interface GolemQueueOptions {
  resources?: Readonly<Record<string, JobResourceOptions>>;
  pollIntervalMs?: number;
  baseBackoffMs?: number;
  maxBackoffMs?: number;
  leaseGraceMs?: number;
  leaseDurationMs?: number;
  leaseRenewIntervalMs?: number;
  abandonGraceMs?: number;
  shutdownGraceMs?: number;
  defaultMaxAttempts?: number;
  workerId?: string;
  retention?: JobRetentionOptions;
}

export interface ResolvedGolemQueueOptions {
  readonly resources?: Readonly<Record<string, JobResourceOptions>>;
  readonly pollIntervalMs: number;
  readonly baseBackoffMs: number;
  readonly maxBackoffMs: number;
  readonly leaseGraceMs: number;
  readonly leaseDurationMs?: number;
  readonly leaseRenewIntervalMs?: number;
  readonly abandonGraceMs: number;
  readonly shutdownGraceMs: number;
  readonly defaultMaxAttempts: number;
  readonly workerId?: string;
  readonly retention?: JobRetentionOptions;
}

export const GOLEM_QUEUE_DEFAULTS: ResolvedGolemQueueOptions = {
  pollIntervalMs: 500,
  baseBackoffMs: 2000,
  maxBackoffMs: 300_000,
  leaseGraceMs: 30_000,
  abandonGraceMs: 5000,
  shutdownGraceMs: 15_000,
  defaultMaxAttempts: 3,
};

export function resolveQueueOptions(
  options: GolemQueueOptions = {},
): ResolvedGolemQueueOptions {
  const resolved: ResolvedGolemQueueOptions = {
    pollIntervalMs:
      options.pollIntervalMs ?? GOLEM_QUEUE_DEFAULTS.pollIntervalMs,
    baseBackoffMs: options.baseBackoffMs ?? GOLEM_QUEUE_DEFAULTS.baseBackoffMs,
    maxBackoffMs: options.maxBackoffMs ?? GOLEM_QUEUE_DEFAULTS.maxBackoffMs,
    leaseGraceMs: options.leaseGraceMs ?? GOLEM_QUEUE_DEFAULTS.leaseGraceMs,
    leaseDurationMs: options.leaseDurationMs,
    leaseRenewIntervalMs:
      options.leaseRenewIntervalMs ??
      (options.leaseDurationMs === undefined
        ? undefined
        : Math.max(1, Math.floor(options.leaseDurationMs / 3))),
    abandonGraceMs:
      options.abandonGraceMs ?? GOLEM_QUEUE_DEFAULTS.abandonGraceMs,
    shutdownGraceMs:
      options.shutdownGraceMs ?? GOLEM_QUEUE_DEFAULTS.shutdownGraceMs,
    defaultMaxAttempts:
      options.defaultMaxAttempts ?? GOLEM_QUEUE_DEFAULTS.defaultMaxAttempts,
    workerId: options.workerId,
    retention: options.retention,
    resources: options.resources,
  };
  validateQueueOptions(resolved);
  resolveJobPools(resolved.resources);
  return resolved;
}

function invalidOption(name: string, value: unknown, expectation: string): never {
  throw new Error(`Invalid Golem queue option ${name}=${String(value)}: ${expectation}`);
}

function finiteNumber(name: string, value: number): void {
  if (!Number.isFinite(value)) invalidOption(name, value, 'must be a finite number');
}

export interface ResolvedJobPool {
  readonly name: string;
  readonly types: readonly string[];
  readonly limit?: number;
  readonly costs: Readonly<Record<string, number>>;
  readonly rateLimit?: number;
  readonly rateWindowMs?: number;
  readonly rateCosts: Readonly<Record<string, number>>;
}

export function resolveJobPools(
  resources: Readonly<Record<string, JobResourceOptions>> | undefined,
): Map<string, ResolvedJobPool> {
  const byType = new Map<string, ResolvedJobPool>();
  const owner = new Map<string, string>();
  for (const [name, resource] of Object.entries(resources ?? {})) {
    if (resource.concurrency === undefined && resource.ratePerMinute === undefined) {
      invalidOption(
        `resources.${name}`,
        'no limit',
        'must declare concurrency, ratePerMinute, or both; a resource with neither bounds nothing',
      );
    }
    if (
      resource.concurrency !== undefined &&
      (!Number.isInteger(resource.concurrency) || resource.concurrency < 1)
    ) {
      invalidOption(
        `resources.${name}.concurrency`,
        resource.concurrency,
        'must be an integer of at least 1',
      );
    }
    if (
      resource.ratePerMinute !== undefined &&
      (!Number.isInteger(resource.ratePerMinute) || resource.ratePerMinute < 1)
    ) {
      invalidOption(
        `resources.${name}.ratePerMinute`,
        resource.ratePerMinute,
        'must be an integer of at least 1',
      );
    }
    if (resource.costs !== undefined && resource.concurrency === undefined) {
      invalidOption(
        `resources.${name}.costs`,
        Object.keys(resource.costs).join(', '),
        `weighs a limit that resources.${name} does not declare; add concurrency`,
      );
    }
    if (resource.rateCosts !== undefined && resource.ratePerMinute === undefined) {
      invalidOption(
        `resources.${name}.rateCosts`,
        Object.keys(resource.rateCosts).join(', '),
        `weighs a budget that resources.${name} does not declare; add ratePerMinute`,
      );
    }
    if (resource.types.length === 0) {
      invalidOption(
        `resources.${name}.types`,
        '[]',
        'must name at least one job type, including types handled by other workers',
      );
    }
    const costs: Record<string, number> = {};
    for (const [type, rawCost] of Object.entries(resource.costs ?? {})) {
      const cost = rawCost as number;
      if (!resource.types.includes(type)) {
        invalidOption(
          `resources.${name}.costs.${type}`,
          cost,
          `must name a type listed in resources.${name}.types`,
        );
      }
      if (!Number.isInteger(cost) || cost < 1) {
        invalidOption(
          `resources.${name}.costs.${type}`,
          cost,
          'must be an integer of at least 1',
        );
      }
      if (
        resource.concurrency !== undefined &&
        cost > resource.concurrency
      ) {
        invalidOption(
          `resources.${name}.costs.${type}`,
          cost,
          `exceeds resources.${name}.concurrency (${resource.concurrency}); a job costing more than the pool holds could never be claimed`,
        );
      }
      costs[type] = cost;
    }
    const rateCosts: Record<string, number> = {};
    for (const [type, rawCost] of Object.entries(resource.rateCosts ?? {})) {
      const cost = rawCost as number;
      if (!resource.types.includes(type)) {
        invalidOption(
          `resources.${name}.rateCosts.${type}`,
          cost,
          `must name a type listed in resources.${name}.types`,
        );
      }
      if (!Number.isInteger(cost) || cost < 1) {
        invalidOption(
          `resources.${name}.rateCosts.${type}`,
          cost,
          'must be an integer of at least 1',
        );
      }
      if (
        resource.ratePerMinute !== undefined &&
        cost > resource.ratePerMinute
      ) {
        invalidOption(
          `resources.${name}.rateCosts.${type}`,
          cost,
          `exceeds resources.${name}.ratePerMinute (${resource.ratePerMinute}); a job spending more than the budget holds could never be claimed`,
        );
      }
      rateCosts[type] = cost;
    }
    const pool: ResolvedJobPool = {
      name,
      types: [...new Set(resource.types)],
      limit: resource.concurrency,
      costs,
      rateLimit: resource.ratePerMinute,
      rateWindowMs: resource.ratePerMinute === undefined ? undefined : 60_000,
      rateCosts,
    };
    for (const type of pool.types) {
      const claimed = owner.get(type);
      if (claimed !== undefined) {
        invalidOption(
          `resources.${name}.types`,
          type,
          `is already claimed by resources.${claimed}; a job type may draw on one resource`,
        );
      }
      owner.set(type, name);
      byType.set(type, pool);
    }
  }
  return byType;
}

export function validateQueueOptions(options: ResolvedGolemQueueOptions): void {
  for (const key of [
    'pollIntervalMs',
    'baseBackoffMs',
    'maxBackoffMs',
    'leaseGraceMs',
    'abandonGraceMs',
    'shutdownGraceMs',
    'defaultMaxAttempts',
  ] as const) {
    finiteNumber(key, options[key]);
  }
  if (options.pollIntervalMs <= 0) invalidOption('pollIntervalMs', options.pollIntervalMs, 'must be greater than 0');
  if (options.baseBackoffMs < 0) invalidOption('baseBackoffMs', options.baseBackoffMs, 'must be at least 0');
  if (options.maxBackoffMs < 0) invalidOption('maxBackoffMs', options.maxBackoffMs, 'must be at least 0');
  if (options.maxBackoffMs < options.baseBackoffMs) {
    invalidOption('maxBackoffMs', options.maxBackoffMs, `must be at least baseBackoffMs (${options.baseBackoffMs})`);
  }
  if (options.leaseGraceMs < 0) invalidOption('leaseGraceMs', options.leaseGraceMs, 'must be at least 0');
  if (options.leaseDurationMs !== undefined) {
    finiteNumber('leaseDurationMs', options.leaseDurationMs);
    if (options.leaseDurationMs <= 0) {
      invalidOption('leaseDurationMs', options.leaseDurationMs, 'must be greater than 0');
    }
  }
  if (options.leaseRenewIntervalMs !== undefined) {
    finiteNumber('leaseRenewIntervalMs', options.leaseRenewIntervalMs);
    if (options.leaseRenewIntervalMs <= 0) {
      invalidOption('leaseRenewIntervalMs', options.leaseRenewIntervalMs, 'must be greater than 0');
    }
    if (options.leaseDurationMs === undefined) {
      invalidOption('leaseRenewIntervalMs', options.leaseRenewIntervalMs, 'requires leaseDurationMs to be set');
    }
    if (options.leaseRenewIntervalMs >= options.leaseDurationMs) {
      invalidOption(
        'leaseRenewIntervalMs',
        options.leaseRenewIntervalMs,
        `must be less than leaseDurationMs (${options.leaseDurationMs})`,
      );
    }
  }
  if (options.abandonGraceMs < 0) invalidOption('abandonGraceMs', options.abandonGraceMs, 'must be at least 0');
  if (options.shutdownGraceMs < 0) invalidOption('shutdownGraceMs', options.shutdownGraceMs, 'must be at least 0');
  if (!Number.isInteger(options.defaultMaxAttempts) || options.defaultMaxAttempts < 1) {
    invalidOption('defaultMaxAttempts', options.defaultMaxAttempts, 'must be an integer of at least 1');
  }
  if (options.workerId !== undefined && options.workerId.trim().length === 0) {
    invalidOption('workerId', JSON.stringify(options.workerId), 'must not be empty');
  }
  const retention = options.retention;
  if (!retention) return;
  finiteNumber('retention.olderThanMs', retention.olderThanMs);
  if (retention.olderThanMs <= 0) {
    invalidOption('retention.olderThanMs', retention.olderThanMs, 'must be greater than 0');
  }
  if (retention.sweepIntervalMs !== undefined) {
    finiteNumber('retention.sweepIntervalMs', retention.sweepIntervalMs);
    if (retention.sweepIntervalMs <= 0) {
      invalidOption('retention.sweepIntervalMs', retention.sweepIntervalMs, 'must be greater than 0');
    }
  }
  if (retention.statuses !== undefined && retention.statuses.length === 0) {
    invalidOption('retention.statuses', '[]', 'must contain at least one terminal status');
  }
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  return String(error);
}

import {
  Inject,
  Injectable,
  Logger,
  Optional,
  OnModuleDestroy,
} from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import type { ClaimCandidate, JobStatus, JobStore } from './job-store';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import {
  consumesRetryAttempt,
  errorMessage,
  GOLEM_QUEUE_OPTIONS,
  isTerminalJobError,
  JOB_HANDLER,
  JOB_STORE,
  retryAfterMs,
  TerminalJobError,
  type JobHandler,
  type JobLifecycleTransition,
  type JobScope,
  type ResolvedGolemQueueOptions,
} from './job.types';

const DEFAULT_SWEEP_INTERVAL_MS = 60_000;
const TERMINAL_STATUSES: readonly JobStatus[] = ['SUCCEEDED', 'FAILED'];

function toScope(job: {
  scopeType: string | null;
  scopeId: string | null;
}): JobScope | null {
  return job.scopeType && job.scopeId
    ? { type: job.scopeType, id: job.scopeId }
    : null;
}

function describeScope(scope: JobScope | null): string {
  return scope ? `${scope.type}:${scope.id}` : 'none';
}

@Injectable()
export class JobDispatcher implements OnModuleDestroy {
  private readonly logger = new Logger(JobDispatcher.name);
  private readonly handlers = new Map<string, JobHandler>();
  private readonly inFlight = new Map<string, number>();
  private readonly executions = new Map<
    string,
    { controller: AbortController; completion: Promise<void> }
  >();
  private readonly workerId: string;
  private timer: NodeJS.Timeout | null = null;
  private tickCompletion: Promise<void> | null = null;
  private stopped = false;
  private lastSweepAt = 0;

  constructor(
    @Inject(JOB_STORE) private readonly store: JobStore,
    @Optional() @Inject(JOB_HANDLER) handlers: JobHandler[] = [],
    private readonly cancellations: JobCancellationRegistry,
    @Inject(GOLEM_QUEUE_OPTIONS)
    private readonly options: ResolvedGolemQueueOptions,
    private readonly observers: JobObserverRegistry,
  ) {
    this.workerId = options.workerId ?? randomUUID();
    for (const handler of handlers) {
      this.register(handler);
    }
  }

  register(handler: JobHandler): void {
    const identity = handler.constructor?.name || '<anonymous provider>';
    if (typeof handler.type !== 'string' || handler.type.trim().length === 0) {
      throw new Error(`Invalid queue handler type=${JSON.stringify(handler.type)} on ${identity}: must not be empty`);
    }
    if (!Number.isInteger(handler.concurrency) || handler.concurrency < 1) {
      throw new Error(`Invalid queue handler concurrency=${String(handler.concurrency)} for "${handler.type}" on ${identity}: must be an integer of at least 1`);
    }
    if (!Number.isFinite(handler.timeoutMs) || handler.timeoutMs <= 0) {
      throw new Error(`Invalid queue handler timeoutMs=${String(handler.timeoutMs)} for "${handler.type}" on ${identity}: must be greater than 0`);
    }
    const previous = this.handlers.get(handler.type);
    if (previous) {
      const previousIdentity = previous.constructor?.name || '<anonymous provider>';
      throw new Error(
        `Duplicate queue handler type "${handler.type}": ${previousIdentity} and ${identity}`,
      );
    }
    this.handlers.set(handler.type, handler);
    this.inFlight.set(handler.type, this.inFlight.get(handler.type) ?? 0);
  }

  registeredTypes(): string[] {
    return [...this.handlers.keys()];
  }

  start(): void {
    if (this.timer || this.stopped) return;
    this.schedule();
  }

  async onModuleDestroy(): Promise<void> {
    this.stopped = true;
    if (this.timer) clearTimeout(this.timer);
    await this.tickCompletion;
    const inFlight = [...this.executions.values()].map(
      ({ completion }) => completion,
    );
    if (inFlight.length === 0) return;
    const drained = await this.settledWithin(
      Promise.allSettled(inFlight),
      this.options.shutdownGraceMs,
    );
    if (drained) return;
    this.logger.warn(
      `Shutdown grace of ${this.options.shutdownGraceMs}ms elapsed with ${this.executions.size} job(s) still running; aborting`,
    );
    for (const execution of this.executions.values()) {
      execution.controller.abort(
        new Error('Application shutdown interrupted the job'),
      );
    }
    await Promise.allSettled(
      [...this.executions.values()].map(({ completion }) => completion),
    );
  }

  private settledWithin(work: Promise<unknown>, ms: number): Promise<boolean> {
    return new Promise((resolve) => {
      const timer = setTimeout(() => resolve(false), ms);
      void work.then(() => {
        clearTimeout(timer);
        resolve(true);
      });
    });
  }

  private schedule(): void {
    if (this.stopped) return;
    this.timer = setTimeout(() => {
      this.tickCompletion = this.tick().finally(() => {
        this.tickCompletion = null;
        this.schedule();
      });
    }, this.options.pollIntervalMs);
  }

  private async tick(): Promise<void> {
    await this.reconcileCancellations();
    await this.sweepRetention();
    for (const [type, handler] of this.handlers) {
      if (this.stopped) return;
      const capacity = handler.concurrency - (this.inFlight.get(type) ?? 0);
      if (capacity <= 0) continue;
      const claimed = await this.claim(handler, capacity);
      for (const job of claimed) this.startExecution(handler, job);
    }
  }

  private async claim(
    handler: JobHandler,
    limit: number,
  ): Promise<ClaimCandidate[]> {
    const now = new Date();
    const candidates = await this.store.findClaimCandidates({
      type: handler.type,
      now,
      limit,
    });
    const claimed: ClaimCandidate[] = [];
    for (const candidate of candidates) {
      const recoveringExpiredLease = candidate.status === 'RUNNING';
      const recoveredAttempts = candidate.attempts + 1;
      if (
        recoveringExpiredLease &&
        recoveredAttempts >= candidate.maxAttempts
      ) {
        const expired = await this.store.failExpiredLease({
          id: candidate.id,
          now,
          attempts: recoveredAttempts,
          lastError: 'The worker lease expired before the job completed',
        });
        if (expired) {
          this.logger.warn(
            `Job failed: type=${handler.type} scope=${describeScope(toScope(candidate))} jobId=${candidate.id} reason=worker-lease-expired attempts=${recoveredAttempts}/${candidate.maxAttempts}`,
          );
          await this.notifyObservers({
            jobId: candidate.id,
            type: handler.type,
            payload: candidate.payload,
            scope: toScope(candidate),
            outcome: 'failed-terminal',
            attempts: recoveredAttempts,
            maxAttempts: candidate.maxAttempts,
            error: 'The worker lease expired before the job completed',
          });
        }
        continue;
      }
      const leaseExpiresAt = new Date(
        Date.now() + handler.timeoutMs + this.options.leaseGraceMs,
      );
      const won = await this.store.claim({
        id: candidate.id,
        fromStatus: recoveringExpiredLease ? 'RUNNING' : 'PENDING',
        now,
        leaseOwner: this.workerId,
        leaseExpiresAt,
        ...(recoveringExpiredLease
          ? {
              attempts: recoveredAttempts,
              lastError: 'The previous worker lease expired',
            }
          : {}),
      });
      if (won) claimed.push(candidate);
    }
    return claimed;
  }

  private startExecution(handler: JobHandler, job: ClaimCandidate): void {
    const controller = new AbortController();
    this.cancellations.register(job.id, toScope(job), controller);
    this.inFlight.set(handler.type, (this.inFlight.get(handler.type) ?? 0) + 1);
    const completion = this.run(handler, job, controller)
      .catch((error: unknown) => {
        this.logger.error(
          `Could not persist job ${job.id} outcome: ${errorMessage(error)}`,
        );
      })
      .finally(() => {
        this.cancellations.unregister(job.id);
        this.executions.delete(job.id);
        this.inFlight.set(
          handler.type,
          Math.max(0, (this.inFlight.get(handler.type) ?? 1) - 1),
        );
      });
    this.executions.set(job.id, { controller, completion });
  }

  private async sweepRetention(): Promise<void> {
    const retention = this.options.retention;
    if (!retention) return;
    const interval = retention.sweepIntervalMs ?? DEFAULT_SWEEP_INTERVAL_MS;
    const now = Date.now();
    if (now - this.lastSweepAt < interval) return;
    this.lastSweepAt = now;
    try {
      const pruned = await this.store.deleteTerminalBefore({
        statuses: retention.statuses ?? TERMINAL_STATUSES,
        before: new Date(now - retention.olderThanMs),
      });
      if (pruned > 0) {
        this.logger.log(`Pruned ${pruned} terminal job(s)`);
      }
    } catch (error) {
      this.logger.error(`Retention sweep failed: ${errorMessage(error)}`);
    }
  }

  private async reconcileCancellations(): Promise<void> {
    const ids = this.cancellations.ids();
    if (ids.length === 0) return;
    const active = await this.store.findOwnedRunningIds({
      ids,
      leaseOwner: this.workerId,
    });
    this.cancellations.abortJobsMissingFrom(new Set(active));
  }

  private async run(
    handler: JobHandler,
    job: ClaimCandidate,
    controller: AbortController,
  ): Promise<void> {
    const startedAt = Date.now();
    const scope = toScope(job);
    this.logger.log(
      `Job started: type=${handler.type} scope=${describeScope(scope)} jobId=${job.id}`,
    );
    await this.notifyObservers({
      jobId: job.id,
      type: handler.type,
      payload: job.payload,
      scope,
      outcome: 'started',
      attempts: job.attempts,
      maxAttempts: job.maxAttempts,
    });
    try {
      const payload = this.parsePayload(job.payload);
      await this.executeWithTimeout(handler, payload, controller);
      const completed = await this.store.complete({
        id: job.id,
        leaseOwner: this.workerId,
      });
      if (completed) {
        const durationMs = Date.now() - startedAt;
        this.logger.log(
          `Job completed: type=${handler.type} scope=${describeScope(scope)} jobId=${job.id} durationMs=${durationMs}`,
        );
        await this.notifyObservers({
          jobId: job.id,
          type: handler.type,
          payload: job.payload,
          scope,
          outcome: 'succeeded',
          attempts: job.attempts,
          maxAttempts: job.maxAttempts,
          durationMs,
        });
      }
    } catch (error) {
      await this.fail(job.id, error, startedAt);
    }
  }

  private parsePayload(payload: string): Record<string, unknown> {
    try {
      const parsed: unknown = JSON.parse(payload);
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error('payload must be a JSON object');
      }
      return parsed as Record<string, unknown>;
    } catch (error) {
      throw new TerminalJobError('Job payload is invalid JSON', {
        cause: error,
      });
    }
  }

  private async executeWithTimeout(
    handler: JobHandler,
    payload: Record<string, unknown>,
    controller: AbortController,
  ): Promise<void> {
    let timer: NodeJS.Timeout | undefined;
    const timeout = new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        controller.abort(
          new Error(
            `Job ${handler.type} timed out after ${handler.timeoutMs}ms`,
          ),
        );
        resolve();
      }, handler.timeoutMs);
    });
    const work = handler.handle(payload, { signal: controller.signal });
    try {
      await Promise.race([work, timeout]);
      if (controller.signal.aborted) {
        await this.settleOrAbandon(work, handler.type);
        throw controller.signal.reason;
      }
      await work;
    } catch (error) {
      if (controller.signal.aborted) {
        await this.settleOrAbandon(work, handler.type);
        throw controller.signal.reason;
      }
      throw error;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  private async settleOrAbandon(
    work: Promise<void>,
    type: string,
  ): Promise<void> {
    const settled = await this.settledWithin(
      work.then(
        () => undefined,
        () => undefined,
      ),
      this.options.abandonGraceMs,
    );
    if (!settled) {
      this.logger.warn(
        `Job ${type} ignored its abort signal for ${this.options.abandonGraceMs}ms; abandoning it to free the slot`,
      );
    }
  }

  private async fail(
    id: string,
    error: unknown,
    startedAt?: number,
  ): Promise<void> {
    const message = errorMessage(error);
    const job = await this.store.findOwned({ id, leaseOwner: this.workerId });
    if (!job) return;
    const scope = toScope(job);
    const attempts = job.attempts + (consumesRetryAttempt(error) ? 1 : 0);
    const durationMs = startedAt ? Date.now() - startedAt : undefined;
    if (isTerminalJobError(error) || attempts >= job.maxAttempts) {
      this.logger.warn(
        `Job failed: type=${job.type} scope=${describeScope(scope)} jobId=${id} durationMs=${durationMs ?? 'unknown'} attempts=${attempts}/${job.maxAttempts} error=${message}`,
      );
      const failed = await this.store.fail({
        id,
        leaseOwner: this.workerId,
        attempts,
        lastError: message,
      });
      if (failed) {
        await this.notifyObservers({
          jobId: id,
          type: job.type,
          payload: job.payload,
          scope,
          outcome: 'failed-terminal',
          attempts,
          maxAttempts: job.maxAttempts,
          error: message,
          durationMs,
        });
      }
      return;
    }
    const exponentialBackoff =
      this.options.baseBackoffMs * 2 ** attempts +
      Math.floor(Math.random() * 500);
    const backoff = Math.max(
      Math.min(exponentialBackoff, this.options.maxBackoffMs),
      retryAfterMs(error) ?? 0,
    );
    const retried = await this.store.retry({
      id,
      leaseOwner: this.workerId,
      attempts,
      lastError: message,
      runAt: new Date(Date.now() + backoff),
    });
    if (retried) {
      this.logger.warn(
        `Job retry scheduled: type=${job.type} scope=${describeScope(scope)} jobId=${id} attempt=${attempts}/${job.maxAttempts} retryInMs=${backoff} error=${message}`,
      );
      await this.notifyObservers({
        jobId: id,
        type: job.type,
        payload: job.payload,
        scope,
        outcome: 'retry-scheduled',
        attempts,
        maxAttempts: job.maxAttempts,
        error: message,
        durationMs,
      });
    }
  }

  private notifyObservers(
    transition: JobLifecycleTransition,
  ): Promise<void> {
    return this.observers.notify(transition);
  }
}

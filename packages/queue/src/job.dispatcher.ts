import {
  Inject,
  Injectable,
  Logger,
  Optional,
  OnModuleDestroy,
} from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import type { ClaimCandidate, ClaimPool, JobStatus, JobStore } from './job-store';
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
  type JobEvent,
  type JobHandler,
  type JobLifecycleTransition,
  type JobScope,
  type ResolvedGolemQueueOptions,
  type ResolvedJobPool,
  resolveJobPools,
} from './job.types';

const DEFAULT_SWEEP_INTERVAL_MS = 60_000;
const TERMINAL_STATUSES: readonly JobStatus[] = ['SUCCEEDED', 'FAILED'];
const BLOCKED_TICKS_BEFORE_WARNING = 60;

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
  private readonly blockedTicks = new Map<string, number>();
  private readonly pools: Map<string, ResolvedJobPool>;
  private readonly exclusionGroups = new Map<string, string>();
  private tickOffset = 0;
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
    this.pools = resolveJobPools(options.resources);
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

  private poolFor(type: string): ResolvedJobPool | undefined {
    return this.pools.get(type);
  }

  private claimPool(handler: JobHandler): ClaimPool | undefined {
    const pool = this.poolFor(handler.type);
    if (pool === undefined) return undefined;
    return {
      types: pool.types,
      limit: pool.limit,
      costs: pool.costs,
      cost: pool.costs[handler.type] ?? 1,
      rateLimit: pool.rateLimit,
      rateWindowStart:
        pool.rateWindowMs === undefined
          ? undefined
          : new Date(Date.now() - pool.rateWindowMs),
      rateCosts: pool.rateCosts,
      rateCost: pool.rateCosts[handler.type] ?? 1,
    };
  }

  /**
   * Claimers that can block one another must serialize on the same guard row,
   * so every type joined by an exclusion edge shares a group. The group name is
   * derived from its members rather than assigned, so every worker computes the
   * same key without coordinating.
   */
  private resolveExclusionGroups(): void {
    const parent = new Map<string, string>();
    const find = (type: string): string => {
      const seen = parent.get(type);
      if (seen === undefined || seen === type) {
        parent.set(type, type);
        return type;
      }
      const root = find(seen);
      parent.set(type, root);
      return root;
    };
    const union = (a: string, b: string): void => {
      const rootA = find(a);
      const rootB = find(b);
      if (rootA !== rootB) parent.set(rootA, rootB);
    };
    for (const [type, handler] of this.handlers) {
      for (const other of handler.waitsFor ?? []) union(type, other);
      for (const other of handler.notWhileRunning ?? []) union(type, other);
    }
    const members = new Map<string, string[]>();
    for (const type of parent.keys()) {
      const root = find(type);
      members.set(root, [...(members.get(root) ?? []), type]);
    }
    for (const [root, group] of members) {
      const name = [...group].sort().join('|');
      for (const type of group) this.exclusionGroups.set(type, name);
      void root;
    }
  }

  /**
   * A store is free to omit the optional port methods, but not to accept a
   * guarded claim it will not enforce. ClaimInput carries guards as optional
   * fields, so a store written before they existed still type-checks and would
   * silently run every guarded job unguarded.
   */
  private assertStoreEnforcesGuards(): void {
    const guarded = [...this.handlers.values()].filter(
      (handler) =>
        (handler.waitsFor?.length ?? 0) > 0 ||
        (handler.notWhileRunning?.length ?? 0) > 0 ||
        handler.serializeByScope === true ||
        this.pools.has(handler.type),
    );
    if (guarded.length === 0) return;
    const store = this.store as { enforcesClaimGuards?: boolean };
    if (store.enforcesClaimGuards !== true) {
      throw new Error(
        `${guarded.map((handler) => handler.type).sort().join(', ')} use claim guards, but ${this.store.constructor?.name ?? 'the configured JobStore'} does not declare enforcesClaimGuards. A store that ignores waitsFor, notWhileRunning, serializeByScope or a resource pool would run those jobs unguarded. Implement the guards and set enforcesClaimGuards = true.`,
      );
    }
  }

  start(): void {
    if (this.timer || this.stopped) return;
    this.assertNoExclusionCycle();
    this.resolveExclusionGroups();
    this.assertStoreEnforcesGuards();
    this.schedule();
  }

  private assertNoExclusionCycle(): void {
    const visiting = new Set<string>();
    const settled = new Set<string>();
    const walk = (type: string, path: string[]): void => {
      if (settled.has(type)) return;
      if (visiting.has(type)) {
        const cycle = [...path.slice(path.indexOf(type)), type];
        throw new Error(
          `Queue handlers wait for each other in a cycle: ${cycle.join(' -> ')}. No job of these types could ever be claimed. Use notWhileRunning if the types must not overlap; unlike waitsFor it is safe in both directions.`,
        );
      }
      visiting.add(type);
      for (const next of this.handlers.get(type)?.waitsFor ?? []) {
        walk(next, [...path, type]);
      }
      visiting.delete(type);
      settled.add(type);
    };
    for (const type of this.handlers.keys()) walk(type, []);
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
    const entries = [...this.handlers.entries()];
    this.tickOffset = entries.length === 0 ? 0 : (this.tickOffset + 1) % entries.length;
    const rotated = [
      ...entries.slice(this.tickOffset),
      ...entries.slice(0, this.tickOffset),
    ];
    for (const [type, handler] of rotated) {
      if (this.stopped) return;
      let capacity = handler.concurrency - (this.inFlight.get(type) ?? 0);
      if (capacity <= 0) continue;
      if (await this.isOrderBlocked(handler)) {
        this.reportBlocked(
          handler,
          `waiting on ${[...(handler.waitsFor ?? []), ...(handler.notWhileRunning ?? [])].join(', ')}`,
        );
        // Recovery still has to run: a blocked handler owns expired leases of
        // its own, and leaving them RUNNING would stall any type waiting on it.
        await this.claim(handler, capacity, true);
        continue;
      }
      const room = await this.poolRoom(handler);
      if (room !== undefined && room <= 0) {
        this.reportBlocked(handler, `resource pool ${this.poolFor(type)?.name} is full`);
        await this.claim(handler, capacity, true);
        continue;
      }
      if (room !== undefined) capacity = Math.min(capacity, room);
      this.blockedTicks.delete(type);
      const claimed = await this.claim(handler, capacity);
      for (const job of claimed) this.startExecution(handler, job);
    }
  }

  private async claim(
    handler: JobHandler,
    limit: number,
    recoverOnly = false,
  ): Promise<ClaimCandidate[]> {
    const now = new Date();
    const claimPool = this.claimPool(handler);
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
      if (recoverOnly) continue;
      const leaseExpiresAt = new Date(Date.now() + this.leaseMs(handler));
      const won = await this.store.claim({
        id: candidate.id,
        fromStatus: recoveringExpiredLease ? 'RUNNING' : 'PENDING',
        now,
        leaseOwner: this.workerId,
        leaseExpiresAt,
        ...(handler.serializeByScope ? { serializeScope: true } : {}),
        ...((handler.waitsFor?.length ?? 0) > 0
          ? { waitsForTypes: handler.waitsFor }
          : {}),
        ...((handler.notWhileRunning?.length ?? 0) > 0
          ? { notWhileRunningTypes: handler.notWhileRunning }
          : {}),
        ...(claimPool === undefined ? {} : { pool: claimPool }),
        scopeType: candidate.scopeType,
        scopeId: candidate.scopeId,
        guardKeys: this.guardKeysFor(handler, candidate),
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

  private guardKeysFor(
    handler: JobHandler,
    candidate: ClaimCandidate,
  ): string[] {
    const keys: string[] = [];
    const pool = this.poolFor(handler.type);
    if (pool !== undefined) keys.push(`pool:${pool.name}`);
    const group = this.exclusionGroups.get(handler.type);
    if (group !== undefined) keys.push(`order:${group}`);
    if (handler.serializeByScope && candidate.scopeType !== null) {
      keys.push(`scope:${candidate.scopeType}`);
    }
    return keys.sort();
  }

  private async poolRoom(handler: JobHandler): Promise<number | undefined> {
    const pool = this.poolFor(handler.type);
    const usage = this.store.poolUsage?.bind(this.store);
    if (pool === undefined || usage === undefined) return undefined;
    const now = new Date();
    const used = await usage({
      types: pool.types,
      costs: pool.costs,
      rateCosts: pool.rateCosts,
      rateWindowStart:
        pool.rateWindowMs === undefined
          ? undefined
          : new Date(now.getTime() - pool.rateWindowMs),
      now,
    });
    const rooms: number[] = [];
    if (pool.limit !== undefined) {
      rooms.push(
        Math.floor((pool.limit - used.concurrency) / (pool.costs[handler.type] ?? 1)),
      );
    }
    if (pool.rateLimit !== undefined) {
      rooms.push(
        Math.floor(
          (pool.rateLimit - used.rate) / (pool.rateCosts[handler.type] ?? 1),
        ),
      );
    }
    return rooms.length === 0 ? undefined : Math.min(...rooms);
  }

  private async isOrderBlocked(handler: JobHandler): Promise<boolean> {
    const waitsFor = handler.waitsFor ?? [];
    const notWhileRunning = handler.notWhileRunning ?? [];
    const hasActive = this.store.hasActiveOfTypes?.bind(this.store);
    if (
      (waitsFor.length === 0 && notWhileRunning.length === 0) ||
      hasActive === undefined
    ) {
      return false;
    }
    return hasActive({ waitsFor, notWhileRunning, now: new Date() });
  }

  /**
   * A handler that cannot claim reports it, whatever the reason. Waiting on
   * another type or on a full pool is legitimate and often brief, so this stays
   * quiet until it has lasted long enough to look like starvation rather than
   * scheduling.
   */
  private reportBlocked(handler: JobHandler, reason: string): void {
    const ticks = (this.blockedTicks.get(handler.type) ?? 0) + 1;
    this.blockedTicks.set(handler.type, ticks);
    if (ticks === BLOCKED_TICKS_BEFORE_WARNING) {
      this.logger.warn(
        `Job type blocked: type=${handler.type} forTicks=${ticks} reason=${reason}`,
      );
    }
  }

  private leaseMs(handler: JobHandler): number {
    return (
      this.options.leaseDurationMs ??
      handler.timeoutMs + this.options.leaseGraceMs
    );
  }

  private startHeartbeat(
    handler: JobHandler,
    job: ClaimCandidate,
    controller: AbortController,
  ): NodeJS.Timeout | undefined {
    const interval = this.options.leaseRenewIntervalMs;
    const renew = this.store.renewLease?.bind(this.store);
    if (interval === undefined || renew === undefined) {
      return undefined;
    }
    const timer = setInterval(() => {
      const now = new Date();
      void renew({
        id: job.id,
        leaseOwner: this.workerId,
        leaseExpiresAt: new Date(now.getTime() + this.leaseMs(handler)),
        now,
      })
        .then((held) => {
          if (held) {
            return;
          }
          clearInterval(timer);
          this.logger.warn(
            `Job lease lost: type=${handler.type} jobId=${job.id} reason=another-worker-may-own-it`,
          );
          controller.abort(
            new Error(`Lost the lease for job ${job.id}; another worker may own it`),
          );
        })
        .catch((error: unknown) => {
          clearInterval(timer);
          this.logger.warn(
            `Job lease renewal failed: type=${handler.type} jobId=${job.id} error=${errorMessage(error)}`,
          );
          controller.abort(
            new Error(`Could not renew the lease for job ${job.id}`),
          );
        });
    }, interval);
    timer.unref?.();
    return timer;
  }

  private startExecution(handler: JobHandler, job: ClaimCandidate): void {
    const controller = new AbortController();
    const heartbeat = this.startHeartbeat(handler, job, controller);
    this.cancellations.register(job.id, toScope(job), controller);
    this.inFlight.set(handler.type, (this.inFlight.get(handler.type) ?? 0) + 1);
    const completion = this.run(handler, job, controller)
      .catch((error: unknown) => {
        this.logger.error(
          `Could not persist job ${job.id} outcome: ${errorMessage(error)}`,
        );
      })
      .finally(() => {
        if (heartbeat !== undefined) clearInterval(heartbeat);
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
      await this.executeWithTimeout(
        handler,
        {
          id: job.id,
          type: handler.type,
          payload,
          attempt: job.attempts + 1,
          maxAttempts: job.maxAttempts,
          scope,
          signal: controller.signal,
        },
        controller,
      );
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
    event: JobEvent,
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
    const work = handler.handle(event);
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

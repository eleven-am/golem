import { InMemoryJobStore } from './in-memory-job-store';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import { JobDispatcher } from './job.dispatcher';
import {
  RetryableJobError,
  TerminalJobError,
  resolveQueueOptions,
  type GolemQueueOptions,
  type JobExecution,
  type JobHandler,
  type JobLifecycleObserver,
  type JobLifecycleTransition,
} from './job.types';

const WORKER = 'worker-under-test';
const SCOPE = { type: 'Article', id: 'a1' } as const;

type Handle = (
  payload: Record<string, unknown>,
  execution: JobExecution,
) => Promise<void>;

function handler(handle: Handle, overrides: Partial<JobHandler> = {}): JobHandler {
  return {
    type: 'article.extract',
    concurrency: 1,
    timeoutMs: 1000,
    handle,
    ...overrides,
  };
}

function observerRegistry(observers: JobLifecycleObserver[]) {
  const registry = new JobObserverRegistry();
  for (const observer of observers) registry.register(observer);
  return registry;
}

function build(
  handlers: JobHandler[],
  options: GolemQueueOptions = {},
  observers: JobLifecycleObserver[] = [],
) {
  const store = new InMemoryJobStore();
  const cancellations = new JobCancellationRegistry();
  const dispatcher = new JobDispatcher(
    store,
    handlers,
    cancellations,
    resolveQueueOptions({ workerId: WORKER, ...options }),
    observerRegistry(observers),
  );
  return { store, cancellations, dispatcher };
}

async function tick(dispatcher: JobDispatcher): Promise<void> {
  const internals = dispatcher as unknown as {
    tick(): Promise<void>;
    executions: Map<string, { completion: Promise<void> }>;
  };
  await internals.tick();
  await Promise.allSettled(
    [...internals.executions.values()].map(({ completion }) => completion),
  );
}

function recorder() {
  const transitions: JobLifecycleTransition[] = [];
  const observer: JobLifecycleObserver = {
    onTransition: (transition) => {
      transitions.push(transition);
      return Promise.resolve();
    },
  };
  const outcomes = () => transitions.map(({ outcome }) => outcome);
  return { transitions, observer, outcomes };
}

async function seed(
  store: InMemoryJobStore,
  overrides: Partial<{
    type: string;
    payload: string;
    maxAttempts: number;
  }> = {},
): Promise<string> {
  await store.create({
    type: overrides.type ?? 'article.extract',
    payload: overrides.payload ?? JSON.stringify({ articleId: 'a1' }),
    scopeType: SCOPE.type,
    scopeId: SCOPE.id,
    runAt: new Date(Date.now() - 1),
    dedupeKey: null,
    maxAttempts: overrides.maxAttempts ?? 3,
  });
  return store.all()[store.all().length - 1].id;
}

describe('JobDispatcher', () => {
  it('runs a due job and marks it succeeded', async () => {
    const seen: Record<string, unknown>[] = [];
    const { store, dispatcher } = build([
      handler((payload) => {
        seen.push(payload);
        return Promise.resolve();
      }),
    ]);
    const id = await seed(store);

    await tick(dispatcher);

    expect(seen).toEqual([{ articleId: 'a1' }]);
    expect(store.get(id)?.status).toBe('SUCCEEDED');
    expect(store.get(id)?.leaseOwner).toBeNull();
  });

  it('claims pending and expired jobs without resetting live leases', async () => {
    const { store, dispatcher } = build([
      handler(() => Promise.resolve(), { concurrency: 5 }),
    ]);
    const due = await seed(store);
    const expired = await seed(store);
    const live = await seed(store);

    Object.assign(store.get(expired)!, {
      status: 'RUNNING',
      leaseOwner: 'dead-worker',
      leaseExpiresAt: new Date(Date.now() - 1000),
    });
    Object.assign(store.get(live)!, {
      status: 'RUNNING',
      leaseOwner: 'healthy-worker',
      leaseExpiresAt: new Date(Date.now() + 60_000),
    });

    await tick(dispatcher);

    expect(store.get(due)?.status).toBe('SUCCEEDED');
    expect(store.get(expired)?.status).toBe('SUCCEEDED');
    expect(store.get(expired)?.attempts).toBe(1);
    expect(store.get(live)?.status).toBe('RUNNING');
    expect(store.get(live)?.leaseOwner).toBe('healthy-worker');
  });

  it('notifies observers of a terminal transition for an expired, exhausted lease', async () => {
    const { transitions, observer } = recorder();
    const { store, dispatcher } = build(
      [handler(() => Promise.resolve())],
      {},
      [observer],
    );
    const id = await seed(store, { maxAttempts: 2 });
    const job = store.get(id)!;
    job.status = 'RUNNING';
    job.attempts = 1;
    job.leaseOwner = 'dead-worker';
    job.leaseExpiresAt = new Date(Date.now() - 1000);

    await tick(dispatcher);

    expect(store.get(id)?.status).toBe('FAILED');
    expect(store.get(id)?.attempts).toBe(2);
    expect(transitions).toHaveLength(1);
    expect(transitions[0]).toMatchObject({
      outcome: 'failed-terminal',
      error: 'The worker lease expired before the job completed',
    });
  });

  it('fails terminal errors immediately without consuming further attempts', async () => {
    const { transitions, observer, outcomes } = recorder();
    const { store, dispatcher } = build(
      [handler(() => Promise.reject(new TerminalJobError('unsupported')))],
      {},
      [observer],
    );
    const id = await seed(store);

    await tick(dispatcher);

    expect(store.get(id)?.status).toBe('FAILED');
    expect(store.get(id)?.attempts).toBe(1);
    expect(store.get(id)?.lastError).toBe('unsupported');
    expect(outcomes()).toEqual(['started', 'failed-terminal']);
    expect(transitions[1].maxAttempts).toBe(3);
  });

  it('schedules a retry for interrupted work', async () => {
    const { outcomes, observer } = recorder();
    const { store, dispatcher } = build(
      [handler(() => Promise.reject(new Error('connection reset')))],
      {},
      [observer],
    );
    const id = await seed(store);

    await tick(dispatcher);

    const job = store.get(id)!;
    expect(job.status).toBe('PENDING');
    expect(job.attempts).toBe(1);
    expect(job.lastError).toBe('connection reset');
    expect(job.runAt.getTime()).toBeGreaterThan(Date.now());
    expect(outcomes()).toEqual(['started', 'retry-scheduled']);
  });

  it('fails terminally once attempts are exhausted', async () => {
    const { outcomes, observer } = recorder();
    const { store, dispatcher } = build(
      [handler(() => Promise.reject(new Error('still broken')))],
      {},
      [observer],
    );
    const id = await seed(store, { maxAttempts: 1 });

    await tick(dispatcher);

    expect(store.get(id)?.status).toBe('FAILED');
    expect(outcomes()).toEqual(['started', 'failed-terminal']);
  });

  it('defers capacity errors without consuming a job attempt', async () => {
    const { store, dispatcher } = build([
      handler(() =>
        Promise.reject(new RetryableJobError('at capacity', undefined, false)),
      ),
    ]);
    const id = await seed(store);

    await tick(dispatcher);

    const job = store.get(id)!;
    expect(job.status).toBe('PENDING');
    expect(job.attempts).toBe(0);
  });

  it('honours retryAfterMs as a floor on the backoff', async () => {
    const { store, dispatcher } = build([
      handler(() => Promise.reject(new RetryableJobError('rate limited', 60_000))),
    ]);
    const id = await seed(store);
    const before = Date.now();

    await tick(dispatcher);

    expect(store.get(id)!.runAt.getTime() - before).toBeGreaterThanOrEqual(
      60_000,
    );
  });

  it('aborts timed-out work and waits for its cooperative cancellation', async () => {
    let observedAbort = false;
    const { store, dispatcher } = build([
      handler(
        (_payload, execution) =>
          new Promise<void>((resolve) => {
            execution.signal.addEventListener('abort', () => {
              observedAbort = true;
              resolve();
            });
          }),
        { timeoutMs: 10 },
      ),
    ]);
    const id = await seed(store);

    await tick(dispatcher);

    expect(observedAbort).toBe(true);
    expect(store.get(id)?.status).toBe('PENDING');
    expect(store.get(id)?.lastError).toContain('timed out');
  });

  it('does not notify success when the completing CAS loses the lease', async () => {
    const { outcomes, observer } = recorder();
    const store = new InMemoryJobStore();
    const cancellations = new JobCancellationRegistry();
    const dispatcher = new JobDispatcher(
      store,
      [
        handler(() => {
          store.all()[0].leaseOwner = 'someone-else';
          return Promise.resolve();
        }),
      ],
      cancellations,
      resolveQueueOptions({ workerId: WORKER }),
      observerRegistry([observer]),
    );
    await seed(store);

    await tick(dispatcher);

    expect(outcomes()).toEqual(['started']);
  });

  it('logs and continues when an observer throws', async () => {
    const exploding: JobLifecycleObserver = {
      onTransition: () => Promise.reject(new Error('observer down')),
    };
    const { store, dispatcher } = build(
      [handler(() => Promise.resolve())],
      {},
      [exploding],
    );
    const id = await seed(store);

    await expect(tick(dispatcher)).resolves.toBeUndefined();
    expect(store.get(id)?.status).toBe('SUCCEEDED');
  });

  it('respects per-handler concurrency', async () => {
    let active = 0;
    let peak = 0;
    const release: (() => void)[] = [];
    const { store, dispatcher } = build([
      handler(
        () =>
          new Promise<void>((resolve) => {
            active += 1;
            peak = Math.max(peak, active);
            release.push(() => {
              active -= 1;
              resolve();
            });
          }),
        { concurrency: 2 },
      ),
    ]);
    await seed(store);
    await seed(store);
    await seed(store);

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();
    expect(peak).toBe(2);
    release.forEach((fn) => fn());
  });

  it('aborts stragglers once the shutdown grace elapses', async () => {
    let observedAbort = false;
    const { store, dispatcher } = build(
      [
        handler(
          (_payload, execution) =>
            new Promise<void>((resolve) => {
              execution.signal.addEventListener('abort', () => {
                observedAbort = true;
                resolve();
              });
            }),
          { timeoutMs: 60_000 },
        ),
      ],
      { shutdownGraceMs: 10 },
    );
    await seed(store);

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();
    await dispatcher.onModuleDestroy();

    expect(observedAbort).toBe(true);
  });

  it('lets in-flight work finish within the shutdown grace', async () => {
    let finished = false;
    let aborted = false;
    const { store, dispatcher } = build(
      [
        handler(
          (_payload, execution) =>
            new Promise<void>((resolve) => {
              execution.signal.addEventListener('abort', () => {
                aborted = true;
              });
              setTimeout(() => {
                finished = true;
                resolve();
              }, 20);
            }),
          { timeoutMs: 60_000 },
        ),
      ],
      { shutdownGraceMs: 2000 },
    );
    const id = await seed(store);

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();
    await dispatcher.onModuleDestroy();

    expect(finished).toBe(true);
    expect(aborted).toBe(false);
    expect(store.get(id)?.status).toBe('SUCCEEDED');
  });

  it('abandons a handler that ignores its abort signal', async () => {
    const { store, dispatcher } = build([
      handler(() => new Promise<void>(() => undefined), {
        timeoutMs: 5,
      }),
    ], { abandonGraceMs: 15 });
    const id = await seed(store);

    await tick(dispatcher);

    const internals = dispatcher as unknown as {
      inFlight: Map<string, number>;
    };
    expect(internals.inFlight.get('article.extract')).toBe(0);
    expect(store.get(id)?.status).toBe('PENDING');
    expect(store.get(id)?.lastError).toContain('timed out');
  });

  it('caps the exponential backoff', async () => {
    const { store, dispatcher } = build(
      [handler(() => Promise.reject(new Error('boom')))],
      { baseBackoffMs: 10_000, maxBackoffMs: 1000 },
    );
    const id = await seed(store);
    const before = Date.now();

    await tick(dispatcher);

    const delay = store.get(id)!.runAt.getTime() - before;
    expect(delay).toBeLessThanOrEqual(1200);
  });

  it('still honours retryAfterMs above the cap', async () => {
    const { store, dispatcher } = build(
      [handler(() => Promise.reject(new RetryableJobError('slow down', 30_000)))],
      { maxBackoffMs: 1000 },
    );
    const id = await seed(store);
    const before = Date.now();

    await tick(dispatcher);

    expect(store.get(id)!.runAt.getTime() - before).toBeGreaterThanOrEqual(
      30_000,
    );
  });

  it('sweeps terminal jobs when retention is configured', async () => {
    const { store, dispatcher } = build([handler(() => Promise.resolve())], {
      retention: { olderThanMs: 1000, sweepIntervalMs: 0 },
    });
    const stale = await seed(store);
    Object.assign(store.get(stale)!, {
      status: 'SUCCEEDED',
      updatedAt: new Date(Date.now() - 10_000),
    });
    const fresh = await seed(store);

    await tick(dispatcher);

    expect(store.get(stale)).toBeUndefined();
    expect(store.get(fresh)).toBeDefined();
  });

  it('leaves terminal jobs alone when retention is not configured', async () => {
    const { store, dispatcher } = build([handler(() => Promise.resolve())]);
    const stale = await seed(store);
    Object.assign(store.get(stale)!, {
      status: 'SUCCEEDED',
      updatedAt: new Date(Date.now() - 10_000),
    });

    await tick(dispatcher);

    expect(store.get(stale)).toBeDefined();
  });

  it('treats an unparseable payload as terminal', async () => {
    const { store, dispatcher } = build([handler(() => Promise.resolve())]);
    const id = await seed(store, { payload: 'not-json' });

    await tick(dispatcher);

    expect(store.get(id)?.status).toBe('FAILED');
    expect(store.get(id)?.lastError).toContain('invalid JSON');
  });
});

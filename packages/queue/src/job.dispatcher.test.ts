import { InMemoryJobStore } from './in-memory-job-store';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import { JobDispatcher } from './job.dispatcher';
import {
  RetryableJobError,
  TerminalJobError,
  resolveQueueOptions,
  type GolemQueueOptions,
  type JobEvent,
  type JobHandler,
  type JobLifecycleObserver,
  type JobLifecycleTransition,
} from './job.types';

const WORKER = 'worker-under-test';
const SCOPE = { type: 'Article', id: 'a1' } as const;

type Handle = (job: JobEvent) => Promise<void>;

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
  it.each([
    [{ type: '   ' }, /handler type.*must not be empty/],
    [{ concurrency: 0 }, /concurrency=0.*integer of at least 1/],
    [{ concurrency: 1.5 }, /concurrency=1.5.*integer of at least 1/],
    [{ timeoutMs: 0 }, /timeoutMs=0.*greater than 0/],
  ] as const)('rejects an invalid handler configuration', (overrides, message) => {
    expect(() => build([handler(() => Promise.resolve(), overrides)])).toThrow(message);
  });

  it('rejects duplicate handler types and identifies both providers', () => {
    class FirstHandler implements JobHandler {
      readonly type = 'duplicate.job';
      readonly concurrency = 1;
      readonly timeoutMs = 1000;
      handle() { return Promise.resolve(); }
    }
    class SecondHandler extends FirstHandler {}

    expect(() => build([new FirstHandler(), new SecondHandler()])).toThrow(
      /duplicate\.job.*FirstHandler.*SecondHandler/,
    );
  });

  it('runs a due job and marks it succeeded', async () => {
    const seen: Record<string, unknown>[] = [];
    const { store, dispatcher } = build([
      handler(({ payload }) => {
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
        (event) =>
          new Promise<void>((resolve) => {
            event.signal.addEventListener('abort', () => {
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
          (event) =>
            new Promise<void>((resolve) => {
              event.signal.addEventListener('abort', () => {
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
          (event) =>
            new Promise<void>((resolve) => {
              event.signal.addEventListener('abort', () => {
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
      { baseBackoffMs: 1000, maxBackoffMs: 1000 },
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
      { baseBackoffMs: 1000, maxBackoffMs: 1000 },
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
      retention: { olderThanMs: 1000, sweepIntervalMs: 1 },
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

describe('lease renewal', () => {
  const RENEWING = { leaseDurationMs: 60, leaseRenewIntervalMs: 20 };

  function leaseOf(store: InMemoryJobStore, id: string): Date | null {
    const jobs = (store as unknown as { jobs: Map<string, { leaseExpiresAt: Date | null }> }).jobs;
    return jobs.get(id)?.leaseExpiresAt ?? null;
  }

  it('keeps a job past its lease duration while it heartbeats', async () => {
    let release: (() => void) | undefined;
    const { store, dispatcher } = build(
      [handler(() => new Promise<void>((resolve) => { release = resolve; }), { timeoutMs: 5_000 })],
      RENEWING,
    );
    const id = await seed(store);

    const running = tick(dispatcher);
    await new Promise((resolve) => setTimeout(resolve, 90));
    const renewed = leaseOf(store, id);
    expect(renewed).not.toBeNull();
    expect(renewed!.getTime()).toBeGreaterThan(Date.now());

    release?.();
    await running;
    expect((await store.findJobs({ limit: 10 }))[0]?.status).toBe('SUCCEEDED');
  });

  it('aborts the handler when the lease is lost', async () => {
    let observedAbort = false;
    const { store, dispatcher } = build(
      [
        handler(
          (event) =>
            new Promise<void>((resolve) => {
              event.signal.addEventListener('abort', () => {
                observedAbort = true;
                resolve();
              });
            }),
          { timeoutMs: 5_000 },
        ),
      ],
      RENEWING,
    );
    const id = await seed(store);

    const running = tick(dispatcher);
    await new Promise((resolve) => setTimeout(resolve, 25));
    const jobs = (store as unknown as { jobs: Map<string, { leaseOwner: string | null }> }).jobs;
    jobs.get(id)!.leaseOwner = 'another-worker';

    await running;
    expect(observedAbort).toBe(true);
  });

  it('refuses to renew a lease that has already expired', async () => {
    const store = new InMemoryJobStore();
    const id = await seed(store);
    await store.claim({
      id,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: WORKER,
      leaseExpiresAt: new Date(Date.now() - 1),
    });

    const renewed = await store.renewLease({
      id,
      leaseOwner: WORKER,
      leaseExpiresAt: new Date(Date.now() + 60_000),
      now: new Date(),
    });
    expect(renewed).toBe(false);
  });

  it('refuses to renew a lease owned by another worker', async () => {
    const store = new InMemoryJobStore();
    const id = await seed(store);
    await store.claim({
      id,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'another-worker',
      leaseExpiresAt: new Date(Date.now() + 60_000),
    });

    const renewed = await store.renewLease({
      id,
      leaseOwner: WORKER,
      leaseExpiresAt: new Date(Date.now() + 60_000),
      now: new Date(),
    });
    expect(renewed).toBe(false);
  });

  it('leaves lease behaviour unchanged when renewal is not configured', async () => {
    let release: (() => void) | undefined;
    const { store, dispatcher } = build(
      [handler(() => new Promise<void>((resolve) => { release = resolve; }), { timeoutMs: 5_000 })],
    );
    const id = await seed(store);
    const running = tick(dispatcher);
    await new Promise((resolve) => setTimeout(resolve, 10));
    const first = leaseOf(store, id);
    await new Promise((resolve) => setTimeout(resolve, 60));

    expect(first).not.toBeNull();
    expect(leaseOf(store, id)?.getTime()).toBe(first?.getTime());

    release?.();
    await running;
  });
});

describe('waitsFor — drain a backlog before running', () => {
  function hydrate(overrides: Partial<JobHandler> = {}) {
    return handler(() => Promise.resolve(), {
      type: 'track-hydrate',
      waitsFor: ['history-import'],
      ...overrides,
    });
  }

  const statusOf = async (store: InMemoryJobStore, id: string) =>
    (await store.findJobs({ limit: 20 })).find((row) => row.id === id)?.status;

  it('does not claim while the awaited type has a due pending job', async () => {
    const { store, dispatcher } = build([hydrate()]);
    await seed(store, { type: 'history-import' });
    const target = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('PENDING');
  });

  it('does not claim while the awaited type is running, expired lease included', async () => {
    const { store, dispatcher } = build([hydrate()]);
    const blocker = await seed(store, { type: 'history-import' });
    await store.claim({
      id: blocker,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'other-worker',
      leaseExpiresAt: new Date(Date.now() - 1),
    });
    const target = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('PENDING');
  });

  it('ignores an awaited job that is not due yet', async () => {
    const { store, dispatcher } = build([hydrate()]);
    await store.create({
      type: 'history-import',
      payload: '{}',
      scopeType: null,
      scopeId: null,
      runAt: new Date(Date.now() + 3_600_000),
      maxAttempts: 3,
    });
    const target = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('SUCCEEDED');
  });

  it('claims once the backlog drains', async () => {
    const { store, dispatcher } = build([hydrate()]);
    const blocker = await seed(store, { type: 'history-import' });
    const target = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);
    expect(await statusOf(store, target)).toBe('PENDING');

    await store.deleteByIds([blocker]);
    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('SUCCEEDED');
  });

  it('refuses a claim when an awaited job appears after candidates are read', async () => {
    const { store, dispatcher } = build([hydrate()]);
    const target = await seed(store, { type: 'track-hydrate' });

    const original = store.findClaimCandidates.bind(store);
    let raced = false;
    store.findClaimCandidates = async (input) => {
      const candidates = await original(input);
      if (!raced) {
        raced = true;
        await seed(store, { type: 'history-import' });
      }
      return candidates;
    };

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('PENDING');
  });

  it('enforces against a type this process has no handler for', async () => {
    const { store, dispatcher } = build([hydrate()]);
    await seed(store, { type: 'history-import' });
    const target = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('PENDING');
  });

  it('still recovers its own dead leases while blocked', async () => {
    const { store, dispatcher } = build([hydrate({ maxAttempts: 1 } as never)]);
    await seed(store, { type: 'history-import' });
    const dead = await seed(store, { type: 'track-hydrate', maxAttempts: 1 });
    await store.claim({
      id: dead,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'dead-worker',
      leaseExpiresAt: new Date(Date.now() - 1),
    });

    await tick(dispatcher);

    expect(await statusOf(store, dead)).toBe('FAILED');
  });

  it('refuses to start when two handlers wait for each other', () => {
    const { dispatcher } = build([
      handler(() => Promise.resolve(), { type: 'a', waitsFor: ['b'] }),
      handler(() => Promise.resolve(), { type: 'b', waitsFor: ['a'] }),
    ]);

    expect(() => dispatcher.start()).toThrow(/wait for each other in a cycle/);
  });
});

describe('notWhileRunning — never overlap', () => {
  const statusOf = async (store: InMemoryJobStore, id: string) =>
    (await store.findJobs({ limit: 20 })).find((row) => row.id === id)?.status;

  function pair() {
    const releases: Array<() => void> = [];
    const hold = () => new Promise<void>((resolve) => releases.push(resolve));
    return {
      releases,
      handlers: [
        handler(hold, { type: 'a', notWhileRunning: ['b'], timeoutMs: 5_000 }),
        handler(hold, { type: 'b', notWhileRunning: ['a'], timeoutMs: 5_000 }),
      ],
    };
  }

  it('permits a mutual declaration rather than calling it a cycle', () => {
    const { handlers } = pair();
    const { dispatcher } = build(handlers);

    expect(() => dispatcher.start()).not.toThrow();
    void dispatcher.onModuleDestroy();
  });

  it('lets only one side run when both have work', async () => {
    const { releases, handlers } = pair();
    const { store, dispatcher } = build(handlers);
    await seed(store, { type: 'a' });
    await seed(store, { type: 'b' });

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();

    const rows = await store.findJobs({ limit: 20 });
    expect(rows.filter((row) => row.status === 'RUNNING')).toHaveLength(1);
    for (const release of releases) release();
  });

  it('does not block on a lease that has expired', async () => {
    const { store, dispatcher } = build([
      handler(() => Promise.resolve(), { type: 'a', notWhileRunning: ['b'] }),
    ]);
    const stale = await seed(store, { type: 'b' });
    await store.claim({
      id: stale,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'dead-worker',
      leaseExpiresAt: new Date(Date.now() - 1),
    });
    const target = await seed(store, { type: 'a' });

    await tick(dispatcher);

    expect(await statusOf(store, target)).toBe('SUCCEEDED');
  });
});

describe('resource pools', () => {
  const POOL = {
    resources: {
      spotify: {
        concurrency: 2,
        types: ['track-hydrate', 'artist-enrich'],
      },
    },
  };

  function pooled(type: string, hold?: () => Promise<void>) {
    return handler(hold ?? (() => Promise.resolve()), { type });
  }

  it('bounds two handlers sharing a pool in aggregate', async () => {
    const releases: Array<() => void> = [];
    const hold = () => new Promise<void>((resolve) => releases.push(resolve));
    const { store, dispatcher } = build(
      [
        pooled('track-hydrate', hold),
        pooled('artist-enrich', hold),
      ],
      { ...POOL, resources: { spotify: { concurrency: 2, types: ['track-hydrate', 'artist-enrich'] } } },
    );
    for (let i = 0; i < 3; i += 1) await seed(store, { type: 'track-hydrate' });
    for (let i = 0; i < 3; i += 1) await seed(store, { type: 'artist-enrich' });

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();

    const running = (await store.findJobs({ limit: 20 })).filter(
      (row) => row.status === 'RUNNING',
    );
    expect(running).toHaveLength(2);

    for (const release of releases) release();
  });

  it('respects a weighted cost', async () => {
    const releases: Array<() => void> = [];
    const hold = () => new Promise<void>((resolve) => releases.push(resolve));
    const { store, dispatcher } = build(
      [handler(hold, { type: 'track-hydrate', concurrency: 10 })],
      {
        resources: {
          spotify: {
            concurrency: 4,
            types: ['track-hydrate'],
            costs: { 'track-hydrate': 2 },
          },
        },
      },
    );
    for (let i = 0; i < 4; i += 1) await seed(store, { type: 'track-hydrate' });

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();

    expect(
      (await store.findJobs({ limit: 20 })).filter((row) => row.status === 'RUNNING'),
    ).toHaveLength(2);

    for (const release of releases) release();
  });

  it('leaves a blocked job PENDING and unleased rather than running and idle', async () => {
    const releases: Array<() => void> = [];
    const hold = () => new Promise<void>((resolve) => releases.push(resolve));
    const { store, dispatcher } = build([pooled('track-hydrate', hold)], {
      resources: { spotify: { concurrency: 1, types: ['track-hydrate'] } },
    });
    await seed(store, { type: 'track-hydrate' });
    const blocked = await seed(store, { type: 'track-hydrate' });

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();

    const job = (await store.findJobs({ limit: 20 })).find((row) => row.id === blocked);
    expect(job?.status).toBe('PENDING');
    expect(job?.leaseOwner ?? null).toBeNull();

    for (const release of releases) release();
  });

  it('does not let a candidate block itself when reclaiming its own expired lease', async () => {
    const { store, dispatcher } = build([pooled('track-hydrate')], {
      resources: { spotify: { concurrency: 1, types: ['track-hydrate'] } },
    });
    const id = await seed(store, { type: 'track-hydrate' });
    await store.claim({
      id,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'dead-worker',
      leaseExpiresAt: new Date(Date.now() - 1),
    });

    await tick(dispatcher);

    expect(
      (await store.findJobs({ limit: 20 })).find((row) => row.id === id)?.status,
    ).toBe('SUCCEEDED');
  });

  it('frees pool capacity when a lease expires, with no reconciliation', async () => {
    const { store, dispatcher } = build([pooled('track-hydrate')], {
      resources: { spotify: { concurrency: 1, types: ['track-hydrate'] } },
    });
    const dead = await seed(store, { type: 'track-hydrate' });
    await store.claim({
      id: dead,
      fromStatus: 'PENDING',
      now: new Date(),
      leaseOwner: 'dead-worker',
      leaseExpiresAt: new Date(Date.now() - 1),
    });
    const waiting = await seed(store, { type: 'track-hydrate' });

    await tick(dispatcher);
    await tick(dispatcher);

    const rows = await store.findJobs({ limit: 20 });
    expect(rows.find((row) => row.id === dead)?.status).toBe('SUCCEEDED');
    expect(rows.find((row) => row.id === waiting)?.status).toBe('SUCCEEDED');
  });

  it('refuses a claim when a pool mate is taken after candidates are read', async () => {
    const { store, dispatcher } = build([pooled('track-hydrate')], {
      resources: { spotify: { concurrency: 1, types: ['track-hydrate', 'artist-enrich'] } },
    });
    const target = await seed(store, { type: 'track-hydrate' });

    const original = store.findClaimCandidates.bind(store);
    let raced = false;
    store.findClaimCandidates = async (input) => {
      const candidates = await original(input);
      if (!raced) {
        raced = true;
        const mate = await seed(store, { type: 'artist-enrich' });
        await store.claim({
          id: mate,
          fromStatus: 'PENDING',
          now: new Date(),
          leaseOwner: 'other-worker',
          leaseExpiresAt: new Date(Date.now() + 60_000),
        });
      }
      return candidates;
    };

    await tick(dispatcher);

    expect(
      (await store.findJobs({ limit: 20 })).find((row) => row.id === target)?.status,
    ).toBe('PENDING');
  });

  it('leaves handlers outside any pool unaffected', async () => {
    const { store, dispatcher } = build([handler(() => Promise.resolve())], {
      resources: { spotify: { concurrency: 1, types: ['track-hydrate'] } },
    });
    const id = await seed(store);

    await tick(dispatcher);

    expect(
      (await store.findJobs({ limit: 20 })).find((row) => row.id === id)?.status,
    ).toBe('SUCCEEDED');
  });

  it('does not let a greedy pool mate starve another across ticks', async () => {
    const releases: Array<() => void> = [];
    const hold = () => new Promise<void>((resolve) => releases.push(resolve));
    const { store, dispatcher } = build(
      [
        handler(hold, { type: 'track-hydrate', concurrency: 10 }),
        handler(hold, { type: 'artist-enrich', concurrency: 1 }),
      ],
      { resources: { spotify: { concurrency: 1, types: ['track-hydrate', 'artist-enrich'] } } },
    );
    for (let i = 0; i < 5; i += 1) await seed(store, { type: 'track-hydrate' });
    const starved = await seed(store, { type: 'artist-enrich' });

    const internals = dispatcher as unknown as { tick(): Promise<void> };
    await internals.tick();
    for (const release of releases.splice(0)) release();
    await new Promise((resolve) => setImmediate(resolve));
    await internals.tick();
    for (const release of releases.splice(0)) release();
    await new Promise((resolve) => setImmediate(resolve));

    const job = (await store.findJobs({ limit: 20 })).find((row) => row.id === starved);
    expect(job?.status).not.toBe('PENDING');
  });
});

describe('store capability', () => {
  it('refuses to start a guarded handler against a store that ignores guards', () => {
    const store = new InMemoryJobStore() as InMemoryJobStore & {
      enforcesClaimGuards?: boolean;
    };
    Object.defineProperty(store, 'enforcesClaimGuards', { value: undefined });
    const dispatcher = new JobDispatcher(
      store,
      [handler(() => Promise.resolve(), { type: 'a', notWhileRunning: ['b'] })],
      new JobCancellationRegistry(),
      resolveQueueOptions({ workerId: WORKER }),
      observerRegistry([]),
    );

    expect(() => dispatcher.start()).toThrow(/does not declare enforcesClaimGuards/);
  });

  it('starts an unguarded handler against any store', () => {
    const store = new InMemoryJobStore();
    Object.defineProperty(store, 'enforcesClaimGuards', { value: undefined });
    const dispatcher = new JobDispatcher(
      store,
      [handler(() => Promise.resolve())],
      new JobCancellationRegistry(),
      resolveQueueOptions({ workerId: WORKER }),
      observerRegistry([]),
    );

    expect(() => dispatcher.start()).not.toThrow();
    void dispatcher.onModuleDestroy();
  });
});

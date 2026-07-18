import { InMemoryJobStore } from './in-memory-job-store';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import { JobQueue } from './job.queue';
import {
  QueuePayloadError,
  resolveQueueOptions,
  type JobLifecycleObserver,
  type JobLifecycleTransition,
} from './job.types';

const SCOPE = { type: 'Article', id: 'a1' } as const;

function build(observers: JobLifecycleObserver[] = []) {
  const store = new InMemoryJobStore();
  const cancellations = new JobCancellationRegistry();
  const registry = new JobObserverRegistry();
  for (const observer of observers) registry.register(observer);
  const queue = new JobQueue(
    store,
    cancellations,
    resolveQueueOptions(),
    registry,
  );
  return { store, cancellations, queue };
}

function recorder() {
  const transitions: JobLifecycleTransition[] = [];
  const observer: JobLifecycleObserver = {
    onTransition: (transition) => {
      transitions.push(transition);
      return Promise.resolve();
    },
  };
  return { transitions, observer };
}

describe('JobQueue', () => {
  it.each([
    ['', {}, {}, /type.*must not be empty/],
    ['job', {}, { maxAttempts: 0 }, /maxAttempts=0/],
    ['job', {}, { maxAttempts: 1.5 }, /maxAttempts=1.5/],
    ['job', {}, { scope: { type: ' ', id: 'a1' } }, /scope\.type/],
    ['job', {}, { scope: { type: 'Article', id: ' ' } }, /scope\.id/],
    ['job', {}, { dedupeKey: ' ' }, /dedupeKey/],
    ['job', {}, { runAt: new Date(Number.NaN) }, /runAt.*valid Date/],
  ] as const)('rejects invalid enqueue metadata', (type, payload, options, message) => {
    const { queue } = build();
    expect(() => queue.add(type, payload, options)).toThrow(message);
  });

  it.each([
    [{ value: 1n }, 'BigInt'],
    [{ value: undefined }, 'unsupported non-JSON values'],
    [{ value: Number.POSITIVE_INFINITY }, 'unsupported non-JSON values'],
    [{ value: () => 'secret' }, 'unsupported non-JSON values'],
    [{ value: new Map([['secret', 'do-not-print']]) }, 'unsupported non-JSON values'],
  ] as const)('rejects unsafe payloads without exposing their values', (payload, reason) => {
    const { queue } = build();
    expect(() => queue.add('sensitive.job', payload as Record<string, unknown>)).toThrow(
      QueuePayloadError,
    );
    try {
      queue.add('sensitive.job', payload as Record<string, unknown>);
    } catch (error) {
      expect((error as Error).message).toContain('sensitive.job');
      expect((error as Error).message).toContain(reason);
      expect((error as Error).message).not.toContain('do-not-print');
    }
  });

  it('identifies circular payloads without printing payload contents', () => {
    const { queue } = build();
    const payload: Record<string, unknown> = { secret: 'do-not-print' };
    payload.self = payload;

    expect(() => queue.add('circular.job', payload)).toThrow(/circular references/);
    try {
      queue.add('circular.job', payload);
    } catch (error) {
      expect((error as Error).message).not.toContain('do-not-print');
    }
  });

  it('accepts JSON-safe dates and repeated non-circular references', async () => {
    const { store, queue } = build();
    const shared = { value: 'safe' };
    await expect(queue.add('safe.job', { at: new Date(0), first: shared, second: shared })).resolves.toBe(true);
    expect(JSON.parse(store.all()[0].payload)).toEqual({
      at: '1970-01-01T00:00:00.000Z',
      first: shared,
      second: shared,
    });
  });

  it('inserts a job carrying its scope', async () => {
    const { store, queue } = build();

    await expect(
      queue.add('article.extract', { articleId: 'a1' }, { scope: SCOPE }),
    ).resolves.toBe(true);

    const [job] = store.all();
    expect(job.type).toBe('article.extract');
    expect(job.scopeType).toBe('Article');
    expect(job.scopeId).toBe('a1');
    expect(job.status).toBe('PENDING');
    expect(JSON.parse(job.payload)).toEqual({ articleId: 'a1' });
  });

  it('defaults maxAttempts from the resolved options', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {});
    expect(store.all()[0].maxAttempts).toBe(3);
  });

  it('returns false when a duplicate dedupe key rejects the insert', async () => {
    const { store, queue } = build();
    const options = { dedupeKey: 'article.extract:a1' };

    await expect(queue.add('article.extract', {}, options)).resolves.toBe(true);
    await expect(queue.add('article.extract', {}, options)).resolves.toBe(false);
    expect(store.all()).toHaveLength(1);
  });

  it('aborts active work and removes durable jobs for a scope', async () => {
    const { store, cancellations, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    await queue.add('article.extract', {}, { scope: { type: 'Article', id: 'other' } });

    const running = store.all()[0];
    running.status = 'RUNNING';
    const controller = new AbortController();
    cancellations.register(running.id, SCOPE, controller);
    setTimeout(() => cancellations.unregister(running.id), 0);

    await expect(queue.cancelForScope(SCOPE, 'article deleted')).resolves.toBe(
      2,
    );
    expect(controller.signal.aborted).toBe(true);
    expect(store.all()).toHaveLength(1);
    expect(store.all()[0].scopeId).toBe('other');
  });

  it('leaves running work alone when only cancelling pending jobs', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    store.all()[0].status = 'RUNNING';

    await expect(
      queue.cancelPendingForScope(SCOPE, 'superseded'),
    ).resolves.toBe(1);

    const remaining = store.all();
    expect(remaining).toHaveLength(1);
    expect(remaining[0].status).toBe('RUNNING');
  });

  it('aborts and removes durable jobs matching the given dedupe keys', async () => {
    const { store, cancellations, queue } = build();
    await queue.add('article.audio', {}, { dedupeKey: 'audio:a1' });
    await queue.add('article.audio', {}, { dedupeKey: 'audio:a2' });

    const [first] = store.all();
    const controller = new AbortController();
    cancellations.register(first.id, null, controller);
    setTimeout(() => cancellations.unregister(first.id), 0);

    await expect(
      queue.cancelByDedupeKeys('article.audio', ['audio:a1'], 'voice changed'),
    ).resolves.toBe(1);

    expect(controller.signal.aborted).toBe(true);
    expect(store.all()).toHaveLength(1);
    expect(store.all()[0].dedupeKey).toBe('audio:a2');
  });

  it('skips all work when no dedupe keys are given', async () => {
    const { store, queue } = build();
    await queue.add('article.audio', {}, { dedupeKey: 'audio:a1' });

    await expect(
      queue.cancelByDedupeKeys('article.audio', [], 'noop'),
    ).resolves.toBe(0);
    expect(store.all()).toHaveLength(1);
  });

  it('enqueues against a caller-supplied store', async () => {
    const { store, queue } = build();
    const transactional = new InMemoryJobStore();

    await expect(
      queue.add('article.extract', { articleId: 'a1' }, {
        scope: SCOPE,
        store: transactional,
      }),
    ).resolves.toBe(true);

    expect(store.all()).toHaveLength(0);
    expect(transactional.all()).toHaveLength(1);
    expect(transactional.all()[0]).toMatchObject({
      type: 'article.extract',
      scopeType: 'Article',
      scopeId: 'a1',
      maxAttempts: 3,
    });
  });

  it('still reports dedupe conflicts through a supplied store', async () => {
    const { queue } = build();
    const transactional = new InMemoryJobStore();
    const options = { dedupeKey: 'k', store: transactional };

    await expect(queue.add('t', {}, options)).resolves.toBe(true);
    await expect(queue.add('t', {}, options)).resolves.toBe(false);
  });

  it('finds jobs for a scope', async () => {
    const { queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    await queue.add('article.extract', {}, {
      scope: { type: 'Article', id: 'other' },
    });

    const found = await queue.findForScope(SCOPE);

    expect(found).toHaveLength(2);
    expect(found.map(({ type }) => type).sort()).toEqual([
      'article.audio',
      'article.extract',
    ]);
    expect(found[0]).toMatchObject({ status: 'PENDING', attempts: 0 });
  });

  it('filters by type and status', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    store.all()[1].status = 'FAILED';

    await expect(
      queue.find({ types: ['article.audio'] }),
    ).resolves.toHaveLength(1);
    await expect(queue.find({ statuses: ['FAILED'] })).resolves.toHaveLength(1);
    await expect(queue.find({ statuses: ['PENDING'] })).resolves.toHaveLength(
      1,
    );
  });

  it('counts jobs by status', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    await queue.add('article.embed', {}, { scope: SCOPE });
    store.all()[1].status = 'FAILED';
    store.all()[2].status = 'SUCCEEDED';

    await expect(queue.countByStatus()).resolves.toEqual({
      PENDING: 1,
      RUNNING: 0,
      SUCCEEDED: 1,
      FAILED: 1,
    });
  });

  it('requeues failed jobs with a fresh attempt budget', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.audio', {}, { scope: SCOPE });
    const [failed, healthy] = store.all();
    Object.assign(failed, {
      status: 'FAILED',
      attempts: 3,
      lastError: 'boom',
    });

    await expect(queue.retryFailed()).resolves.toBe(1);

    expect(store.get(failed.id)).toMatchObject({
      status: 'PENDING',
      attempts: 0,
      lastError: null,
    });
    expect(store.get(healthy.id)?.status).toBe('PENDING');
  });

  it('only requeues failed jobs inside the requested scope', async () => {
    const { store, queue } = build();
    await queue.add('article.extract', {}, { scope: SCOPE });
    await queue.add('article.extract', {}, {
      scope: { type: 'Article', id: 'other' },
    });
    store.all().forEach((job) => (job.status = 'FAILED'));

    await expect(
      queue.retryFailed({ scopeType: SCOPE.type, scopeId: SCOPE.id }),
    ).resolves.toBe(1);
  });

  it('requeues every matching failed job beyond the listing default', async () => {
    const { store, queue } = build();
    for (let index = 0; index < 125; index += 1) {
      await queue.add('bulk.retry', { index });
    }
    store.all().forEach((job) => {
      job.status = 'FAILED';
      job.attempts = 3;
      job.lastError = 'boom';
    });

    await expect(queue.retryFailed({ types: ['bulk.retry'] })).resolves.toBe(125);
    expect(store.all().every((job) => job.status === 'PENDING')).toBe(true);
  });

  it('honors an explicit administrative retry page', async () => {
    const { store, queue } = build();
    for (let index = 0; index < 20; index += 1) {
      await queue.add('bulk.retry', { index });
    }
    store.all().forEach((job) => (job.status = 'FAILED'));

    await expect(queue.retryFailed({ limit: 7 })).resolves.toBe(7);
    expect(store.all().filter((job) => job.status === 'FAILED')).toHaveLength(13);
  });

  it('prunes only terminal jobs older than the cutoff', async () => {
    const { store, queue } = build();
    await queue.add('a', {});
    await queue.add('b', {});
    await queue.add('c', {});
    const [old, recent, pending] = store.all();
    Object.assign(old, {
      status: 'SUCCEEDED',
      updatedAt: new Date(Date.now() - 10_000),
    });
    Object.assign(recent, { status: 'SUCCEEDED', updatedAt: new Date() });

    await expect(
      queue.prune({ olderThan: new Date(Date.now() - 5000) }),
    ).resolves.toBe(1);

    expect(store.get(old.id)).toBeUndefined();
    expect(store.get(recent.id)).toBeDefined();
    expect(store.get(pending.id)).toBeDefined();
  });

  it('reports a cancelled transition for every discarded job', async () => {
    const { transitions, observer } = recorder();
    const { queue } = build([observer]);
    await queue.add('article.extract', {}, { scope: SCOPE });

    await queue.cancelForScope(SCOPE, 'article deleted');

    expect(transitions).toHaveLength(1);
    expect(transitions[0]).toMatchObject({
      type: 'article.extract',
      outcome: 'cancelled',
      error: 'article deleted',
      scope: { type: 'Article', id: 'a1' },
    });
  });
});

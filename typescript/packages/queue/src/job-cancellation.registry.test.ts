import { JobCancellationRegistry } from './job-cancellation.registry';

describe('JobCancellationRegistry', () => {
  it('waits until an aborted worker fully unregisters', async () => {
    const registry = new JobCancellationRegistry();
    const controller = new AbortController();
    registry.register('job-1', { type: 'Article', id: 'a1' }, controller);

    let settled = false;
    const waiting = registry.waitForJobs(['job-1']).then(() => {
      settled = true;
    });

    registry.abortJobs(['job-1'], 'cancelled');
    await Promise.resolve();
    expect(settled).toBe(false);
    expect(controller.signal.aborted).toBe(true);

    registry.unregister('job-1');
    await waiting;
    expect(settled).toBe(true);
  });

  it('does not wait for jobs which are no longer active', async () => {
    const registry = new JobCancellationRegistry();
    await expect(registry.waitForJobs(['missing'])).resolves.toBeUndefined();
  });

  it('aborts only the jobs sharing a scope', () => {
    const registry = new JobCancellationRegistry();
    const target = new AbortController();
    const other = new AbortController();
    const unscoped = new AbortController();
    registry.register('a', { type: 'Article', id: 'a1' }, target);
    registry.register('b', { type: 'Article', id: 'a2' }, other);
    registry.register('c', null, unscoped);

    registry.abortForScope({ type: 'Article', id: 'a1' }, 'article deleted');

    expect(target.signal.aborted).toBe(true);
    expect(other.signal.aborted).toBe(false);
    expect(unscoped.signal.aborted).toBe(false);
  });

  it('aborts jobs missing from the active set', () => {
    const registry = new JobCancellationRegistry();
    const stillRunning = new AbortController();
    const vanished = new AbortController();
    registry.register('keep', null, stillRunning);
    registry.register('gone', null, vanished);

    registry.abortJobsMissingFrom(new Set(['keep']));

    expect(stillRunning.signal.aborted).toBe(false);
    expect(vanished.signal.aborted).toBe(true);
  });
});

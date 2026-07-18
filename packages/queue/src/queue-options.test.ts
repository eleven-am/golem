import { resolveQueueOptions, type GolemQueueOptions } from './job.types';

describe('queue option validation', () => {
  it.each([
    [{ pollIntervalMs: 0 }, 'pollIntervalMs=0'],
    [{ baseBackoffMs: -1 }, 'baseBackoffMs=-1'],
    [{ maxBackoffMs: -1 }, 'maxBackoffMs=-1'],
    [{ baseBackoffMs: 10, maxBackoffMs: 9 }, 'maxBackoffMs=9'],
    [{ leaseGraceMs: -1 }, 'leaseGraceMs=-1'],
    [{ abandonGraceMs: -1 }, 'abandonGraceMs=-1'],
    [{ shutdownGraceMs: -1 }, 'shutdownGraceMs=-1'],
    [{ defaultMaxAttempts: 0 }, 'defaultMaxAttempts=0'],
    [{ defaultMaxAttempts: 1.5 }, 'defaultMaxAttempts=1.5'],
    [{ workerId: '   ' }, 'workerId'],
    [{ retention: { olderThanMs: 0 } }, 'retention.olderThanMs=0'],
    [{ retention: { olderThanMs: 1000, sweepIntervalMs: 0 } }, 'retention.sweepIntervalMs=0'],
    [{ retention: { olderThanMs: 1000, statuses: [] } }, 'retention.statuses=[]'],
  ] satisfies [GolemQueueOptions, string][])('rejects %j', (options, message) => {
    expect(() => resolveQueueOptions(options)).toThrow(message);
  });

  it('accepts zero-valued grace and backoff durations where zero is meaningful', () => {
    expect(() => resolveQueueOptions({
      baseBackoffMs: 0,
      maxBackoffMs: 0,
      leaseGraceMs: 0,
      abandonGraceMs: 0,
      shutdownGraceMs: 0,
    })).not.toThrow();
  });
});

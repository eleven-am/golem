import { Test } from '@nestjs/testing';
import {
  GOLEM_QUEUE_OPTIONS,
  JOB_STORE,
  resolveQueueOptions,
  type GolemQueueOptions,
  type ResolvedGolemQueueOptions,
} from './job.types';
import { GolemQueueModule } from './queue.module';
import { InMemoryJobStore } from './in-memory-job-store';

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

describe('async factory options', () => {
  it('takes options from the factory result', async () => {
    const store = new InMemoryJobStore();
    const moduleRef = await Test.createTestingModule({
      imports: [
        GolemQueueModule.forRootAsync({
          useFactory: () => ({ store, pollIntervalMs: 250, defaultMaxAttempts: 7 }),
        }),
      ],
    }).compile();

    const resolved = moduleRef.get<ResolvedGolemQueueOptions>(GOLEM_QUEUE_OPTIONS);
    expect(resolved.pollIntervalMs).toBe(250);
    expect(resolved.defaultMaxAttempts).toBe(7);
    expect(moduleRef.get(JOB_STORE)).toBe(store);
    await moduleRef.close();
  });

  it('still accepts a bare store', async () => {
    const store = new InMemoryJobStore();
    const moduleRef = await Test.createTestingModule({
      imports: [
        GolemQueueModule.forRootAsync({
          pollIntervalMs: 500,
          useFactory: () => store,
        }),
      ],
    }).compile();

    expect(moduleRef.get<ResolvedGolemQueueOptions>(GOLEM_QUEUE_OPTIONS).pollIntervalMs).toBe(500);
    expect(moduleRef.get(JOB_STORE)).toBe(store);
    await moduleRef.close();
  });

  it('lets the factory override statically supplied options', async () => {
    const store = new InMemoryJobStore();
    const moduleRef = await Test.createTestingModule({
      imports: [
        GolemQueueModule.forRootAsync({
          pollIntervalMs: 500,
          useFactory: () => ({ store, pollIntervalMs: 125 }),
        }),
      ],
    }).compile();

    expect(moduleRef.get<ResolvedGolemQueueOptions>(GOLEM_QUEUE_OPTIONS).pollIntervalMs).toBe(125);
    await moduleRef.close();
  });
});

describe('resource pool configuration', () => {
  it('rejects a pool that bounds nothing', () => {
    expect(() =>
      resolveQueueOptions({ resources: { spotify: { concurrency: 0, types: ['a'] } } }),
    ).toThrow(/concurrency.*at least 1/);
    expect(() =>
      resolveQueueOptions({ resources: { spotify: { concurrency: 2, types: [] } } }),
    ).toThrow(/must name at least one job type/);
  });

  it('rejects a cost that is not a positive integer or names an outsider', () => {
    expect(() =>
      resolveQueueOptions({
        resources: { spotify: { concurrency: 2, types: ['a'], costs: { a: 0 } } },
      }),
    ).toThrow(/costs\.a.*at least 1/);
    expect(() =>
      resolveQueueOptions({
        resources: { spotify: { concurrency: 2, types: ['a'], costs: { b: 1 } } },
      }),
    ).toThrow(/costs\.b.*listed in resources\.spotify\.types/);
  });

  it('rejects a job type drawing on two resources', () => {
    expect(() =>
      resolveQueueOptions({
        resources: {
          spotify: { concurrency: 2, types: ['shared'] },
          openai: { concurrency: 2, types: ['shared'] },
        },
      }),
    ).toThrow(/already claimed by resources\.spotify/);
  });

  it('accepts a member type this process has no handler for', () => {
    expect(() =>
      resolveQueueOptions({
        resources: { spotify: { concurrency: 2, types: ['remote-only'] } },
      }),
    ).not.toThrow();
  });
});

describe('rate budget configuration', () => {
  it('rejects a resource that bounds nothing', () => {
    expect(() =>
      resolveQueueOptions({ resources: { api: { types: ['a'] } } as never }),
    ).toThrow(/must declare concurrency, ratePerMinute, or both/);
  });

  it('rejects a rate that is not a positive integer', () => {
    expect(() =>
      resolveQueueOptions({
        resources: { api: { ratePerMinute: 0, types: ['a'] } },
      }),
    ).toThrow(/ratePerMinute.*at least 1/);
  });

  it('rejects weights against a budget that was never declared', () => {
    expect(() =>
      resolveQueueOptions({
        resources: { api: { concurrency: 2, types: ['a'], rateCosts: { a: 2 } } },
      }),
    ).toThrow(/weighs a budget that resources\.api does not declare/);
  });

  it('rejects a rate cost larger than the budget', () => {
    expect(() =>
      resolveQueueOptions({
        resources: {
          api: { ratePerMinute: 5, types: ['a'], rateCosts: { a: 6 } },
        },
      }),
    ).toThrow(/exceeds resources\.api\.ratePerMinute/);
  });

  it('accepts a rate-only resource', () => {
    expect(() =>
      resolveQueueOptions({
        resources: { api: { ratePerMinute: 180, types: ['a'] } },
      }),
    ).not.toThrow();
  });
});

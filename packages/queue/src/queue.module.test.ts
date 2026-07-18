import { Injectable } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { GolemQueueModule } from './queue.module';
import { InMemoryJobStore } from './in-memory-job-store';
import { JobDispatcher } from './job.dispatcher';
import { JobObserverRegistry } from './job-observer.registry';
import { JobQueue } from './job.queue';
import { QueueHandler, QueueObserver } from './queue.decorators';
import type { JobExecution, JobLifecycleTransition, JobWork } from './job.types';

const handled: string[] = [];
const seen: JobLifecycleTransition[] = [];

@QueueHandler({ type: 'discovered.job', concurrency: 4, timeoutMs: 5_000 })
@Injectable()
class DiscoveredHandler implements JobWork {
  handle(payload: Record<string, unknown>, _execution: JobExecution) {
    handled.push(payload.tag as string);
    return Promise.resolve();
  }
}

@QueueObserver()
@Injectable()
class DiscoveredObserver {
  onTransition(transition: JobLifecycleTransition) {
    seen.push(transition);
    return Promise.resolve();
  }
}

@Injectable()
class DeclaredHandler {
  readonly type = 'declared.job';
  readonly concurrency = 1;
  readonly timeoutMs = 1_000;
  handle() {
    return Promise.resolve();
  }
}

async function boot() {
  const store = new InMemoryJobStore();
  const moduleRef = await Test.createTestingModule({
    imports: [
      GolemQueueModule.forRoot({
        store,
        handlers: [DeclaredHandler],
        pollIntervalMs: 5,
      }),
    ],
    providers: [DiscoveredHandler, DiscoveredObserver],
  }).compile();
  await moduleRef.init();
  return { moduleRef, store };
}

describe('GolemQueueModule', () => {
  beforeEach(() => {
    handled.length = 0;
    seen.length = 0;
  });

  it('registers decorated handlers alongside explicitly declared ones', async () => {
    const { moduleRef } = await boot();
    const dispatcher = moduleRef.get(JobDispatcher);

    expect(dispatcher.registeredTypes().sort()).toEqual([
      'declared.job',
      'discovered.job',
    ]);

    await moduleRef.close();
  });

  it('reads concurrency and timeout from the decorator', async () => {
    const { moduleRef } = await boot();
    const handler = moduleRef.get(DiscoveredHandler) as unknown as {
      type: string;
      concurrency: number;
      timeoutMs: number;
    };

    expect(handler.type).toBe('discovered.job');
    expect(handler.concurrency).toBe(4);
    expect(handler.timeoutMs).toBe(5_000);

    await moduleRef.close();
  });

  it('runs a discovered handler and reports it to a discovered observer', async () => {
    const { moduleRef } = await boot();
    const queue = moduleRef.get(JobQueue);

    await queue.add('discovered.job', { tag: 'from-discovery' });
    await new Promise((resolve) => setTimeout(resolve, 60));

    expect(handled).toEqual(['from-discovery']);
    expect(seen.map(({ outcome }) => outcome)).toEqual(['started', 'succeeded']);

    await moduleRef.close();
  });

  it('registers discovered observers in the shared registry', async () => {
    const { moduleRef } = await boot();
    expect(moduleRef.get(JobObserverRegistry).count()).toBe(1);
    await moduleRef.close();
  });
});

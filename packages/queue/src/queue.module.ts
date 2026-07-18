import {
  DynamicModule,
  Module,
  type ModuleMetadata,
  type Provider,
  type Type,
} from '@nestjs/common';
import { DiscoveryModule } from '@nestjs/core';
import { JobQueue } from './job.queue';
import { JobDispatcher } from './job.dispatcher';
import { JobCancellationRegistry } from './job-cancellation.registry';
import { JobObserverRegistry } from './job-observer.registry';
import { QueueExplorer } from './queue.explorer';
import type { JobStore } from './job-store';
import {
  GOLEM_QUEUE_OPTIONS,
  JOB_HANDLER,
  JOB_LIFECYCLE_OBSERVER,
  JOB_STORE,
  resolveQueueOptions,
  type GolemQueueOptions,
  type JobHandler,
  type JobLifecycleObserver,
} from './job.types';

interface RegistrationOptions extends GolemQueueOptions {
  handlers?: Type<JobHandler>[];
  observers?: Type<JobLifecycleObserver>[];
  imports?: ModuleMetadata['imports'];
}

export interface GolemQueueRootOptions extends RegistrationOptions {
  store: JobStore;
}

export interface GolemQueueRootAsyncOptions extends RegistrationOptions {
  useFactory: (...args: never[]) => JobStore | Promise<JobStore>;
  inject?: unknown[];
}

const CORE_PROVIDERS = [
  JobCancellationRegistry,
  JobObserverRegistry,
  JobQueue,
  JobDispatcher,
  QueueExplorer,
];

const CORE_EXPORTS = [JobQueue, JobCancellationRegistry, JobObserverRegistry];

function registrationProviders(options: RegistrationOptions): Provider[] {
  const handlers = options.handlers ?? [];
  const observers = options.observers ?? [];
  return [
    ...handlers,
    ...observers,
    {
      provide: GOLEM_QUEUE_OPTIONS,
      useValue: resolveQueueOptions(options),
    },
    {
      provide: JOB_HANDLER,
      useFactory: (...resolved: JobHandler[]): JobHandler[] => resolved,
      inject: handlers,
    },
    {
      provide: JOB_LIFECYCLE_OBSERVER,
      useFactory: (
        ...resolved: JobLifecycleObserver[]
      ): JobLifecycleObserver[] => resolved,
      inject: observers,
    },
  ];
}

@Module({})
export class GolemQueueModule {
  static forRoot(options: GolemQueueRootOptions): DynamicModule {
    return {
      module: GolemQueueModule,
      global: true,
      imports: [DiscoveryModule, ...(options.imports ?? [])],
      providers: [
        { provide: JOB_STORE, useValue: options.store },
        ...registrationProviders(options),
        ...CORE_PROVIDERS,
      ],
      exports: CORE_EXPORTS,
    };
  }

  static forRootAsync(options: GolemQueueRootAsyncOptions): DynamicModule {
    return {
      module: GolemQueueModule,
      global: true,
      imports: [DiscoveryModule, ...(options.imports ?? [])],
      providers: [
        {
          provide: JOB_STORE,
          useFactory: options.useFactory,
          inject: options.inject as never[],
        },
        ...registrationProviders(options),
        ...CORE_PROVIDERS,
      ],
      exports: CORE_EXPORTS,
    };
  }
}

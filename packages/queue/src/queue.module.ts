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

export type GolemQueueAsyncFactoryResult =
  | JobStore
  | ({ store: JobStore } & GolemQueueOptions);

export interface GolemQueueRootAsyncOptions extends RegistrationOptions {
  useFactory: (
    ...args: never[]
  ) => GolemQueueAsyncFactoryResult | Promise<GolemQueueAsyncFactoryResult>;
  inject?: unknown[];
}

const GOLEM_QUEUE_ASYNC_RESULT = 'GOLEM_QUEUE_ASYNC_RESULT';

function isFactoryResultWithOptions(
  value: GolemQueueAsyncFactoryResult,
): value is { store: JobStore } & GolemQueueOptions {
  return typeof value === 'object' && value !== null && 'store' in value;
}

function resolvedStore(value: GolemQueueAsyncFactoryResult): JobStore {
  return isFactoryResultWithOptions(value) ? value.store : value;
}

const CORE_PROVIDERS = [
  JobCancellationRegistry,
  JobObserverRegistry,
  JobQueue,
  JobDispatcher,
  QueueExplorer,
];

const CORE_EXPORTS = [JobQueue, JobCancellationRegistry, JobObserverRegistry];

function registrationProviders(
  options: RegistrationOptions,
  optionsProvider?: Provider,
): Provider[] {
  const handlers = options.handlers ?? [];
  const observers = options.observers ?? [];
  return [
    ...handlers,
    ...observers,
    optionsProvider ?? {
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
          provide: GOLEM_QUEUE_ASYNC_RESULT,
          useFactory: options.useFactory,
          inject: options.inject as never[],
        },
        {
          provide: JOB_STORE,
          useFactory: resolvedStore,
          inject: [GOLEM_QUEUE_ASYNC_RESULT],
        },
        ...registrationProviders(options, {
          provide: GOLEM_QUEUE_OPTIONS,
          useFactory: (resolved: GolemQueueAsyncFactoryResult) =>
            resolveQueueOptions(
              isFactoryResultWithOptions(resolved)
                ? { ...options, ...resolved }
                : options,
            ),
          inject: [GOLEM_QUEUE_ASYNC_RESULT],
        }),
        ...CORE_PROVIDERS,
      ],
      exports: CORE_EXPORTS,
    };
  }
}

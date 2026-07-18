import { Inject, Injectable, OnModuleInit, Optional } from '@nestjs/common';
import { DiscoveryService, Reflector } from '@nestjs/core';
import { JobDispatcher } from './job.dispatcher';
import { JobObserverRegistry } from './job-observer.registry';
import {
  QUEUE_HANDLER_METADATA,
  QUEUE_OBSERVER_METADATA,
  type ResolvedQueueHandlerConfig,
} from './queue.decorators';
import {
  JOB_LIFECYCLE_OBSERVER,
  type JobHandler,
  type JobLifecycleObserver,
} from './job.types';

@Injectable()
export class QueueExplorer implements OnModuleInit {
  constructor(
    private readonly discovery: DiscoveryService,
    private readonly reflector: Reflector,
    private readonly dispatcher: JobDispatcher,
    private readonly observers: JobObserverRegistry,
    @Optional()
    @Inject(JOB_LIFECYCLE_OBSERVER)
    private readonly declared: JobLifecycleObserver[] = [],
  ) {}

  onModuleInit(): void {
    for (const observer of this.declared) {
      this.observers.register(observer);
    }
    for (const wrapper of this.discovery.getProviders()) {
      const { instance, metatype } = wrapper;
      if (!instance || !metatype) {
        continue;
      }
      const config = this.reflector.get<ResolvedQueueHandlerConfig | undefined>(
        QUEUE_HANDLER_METADATA,
        metatype,
      );
      if (config) {
        this.dispatcher.register(instance as JobHandler);
      }
      if (this.reflector.get<boolean | undefined>(QUEUE_OBSERVER_METADATA, metatype)) {
        this.observers.register(instance as JobLifecycleObserver);
      }
    }
    this.dispatcher.start();
  }
}

import { SetMetadata } from '@nestjs/common';
import type { JobType } from './register';

export const QUEUE_HANDLER_METADATA = 'GOLEM_QUEUE_HANDLER_METADATA';

export const QUEUE_OBSERVER_METADATA = 'GOLEM_QUEUE_OBSERVER_METADATA';

export interface QueueHandlerConfig<TType extends JobType = JobType> {
  readonly type: TType;
  readonly concurrency?: number;
  readonly timeoutMs?: number;
  readonly serializeByScope?: boolean;
  readonly excludes?: readonly TType[];
}

export interface ResolvedQueueHandlerConfig {
  readonly type: string;
  readonly concurrency: number;
  readonly timeoutMs: number;
  readonly serializeByScope: boolean;
  readonly excludes: readonly string[];
}

const DEFAULT_CONCURRENCY = 1;
const DEFAULT_TIMEOUT_MS = 30_000;

export function QueueHandler<TType extends JobType>(
  config: QueueHandlerConfig<TType>,
): ClassDecorator {
  const resolved: ResolvedQueueHandlerConfig = {
    type: config.type,
    concurrency: config.concurrency ?? DEFAULT_CONCURRENCY,
    timeoutMs: config.timeoutMs ?? DEFAULT_TIMEOUT_MS,
    serializeByScope: config.serializeByScope ?? false,
    excludes: [...new Set(config.excludes ?? [])],
  };
  return (target) => {
    SetMetadata(QUEUE_HANDLER_METADATA, resolved)(target);
    const prototype = (target as unknown as { prototype: object }).prototype;
    for (const [key, value] of Object.entries(resolved)) {
      Object.defineProperty(prototype, key, {
        value,
        enumerable: false,
        writable: false,
        configurable: true,
      });
    }
  };
}

export function QueueObserver(): ClassDecorator {
  return (target) => {
    SetMetadata(QUEUE_OBSERVER_METADATA, true)(target);
  };
}

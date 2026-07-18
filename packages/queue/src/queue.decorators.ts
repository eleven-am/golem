import { SetMetadata } from '@nestjs/common';

export const QUEUE_HANDLER_METADATA = 'GOLEM_QUEUE_HANDLER_METADATA';

export const QUEUE_OBSERVER_METADATA = 'GOLEM_QUEUE_OBSERVER_METADATA';

export interface QueueHandlerConfig {
  readonly type: string;
  readonly concurrency?: number;
  readonly timeoutMs?: number;
}

export interface ResolvedQueueHandlerConfig {
  readonly type: string;
  readonly concurrency: number;
  readonly timeoutMs: number;
}

const DEFAULT_CONCURRENCY = 1;
const DEFAULT_TIMEOUT_MS = 30_000;

export function QueueHandler(config: QueueHandlerConfig): ClassDecorator {
  const resolved: ResolvedQueueHandlerConfig = {
    type: config.type,
    concurrency: config.concurrency ?? DEFAULT_CONCURRENCY,
    timeoutMs: config.timeoutMs ?? DEFAULT_TIMEOUT_MS,
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

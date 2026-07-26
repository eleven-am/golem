import { SetMetadata } from '@nestjs/common';
import type { JobType } from './register';

export const QUEUE_HANDLER_METADATA = 'GOLEM_QUEUE_HANDLER_METADATA';

export const QUEUE_OBSERVER_METADATA = 'GOLEM_QUEUE_OBSERVER_METADATA';

export interface QueueHandlerConfig<TType extends JobType = JobType> {
  readonly type: TType;
  readonly concurrency?: number;
  readonly timeoutMs?: number;
  readonly serializeByScope?: boolean;
  /**
   * Do not claim while any of these types has work outstanding — a due PENDING
   * job or a RUNNING one. Drains their backlog before this type runs, so it is
   * one-way by nature: two types that wait for each other could never start.
   */
  readonly waitsFor?: readonly TType[];
  /**
   * Do not claim while any of these types holds a live lease. Prevents overlap
   * rather than ordering, so it is safe to declare on both sides and that is
   * usually what you want.
   */
  readonly notWhileRunning?: readonly TType[];
}

export interface ResolvedQueueHandlerConfig {
  readonly type: string;
  readonly concurrency: number;
  readonly timeoutMs: number;
  readonly serializeByScope: boolean;
  readonly waitsFor: readonly string[];
  readonly notWhileRunning: readonly string[];
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
    waitsFor: [...new Set(config.waitsFor ?? [])],
    notWhileRunning: [...new Set(config.notWhileRunning ?? [])],
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

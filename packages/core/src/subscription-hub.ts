import { GraphQLError } from 'graphql';
import { GolemEventPayload } from './events';

export const DEFAULT_SUBSCRIPTION_QUEUE_CAPACITY = 64;

export type GolemSubscriptionSuppressionReason =
  | 'authorization'
  | 'deletion-filter'
  | 'deletion-unverifiable'
  | 'filter'
  | 'invalid-identity';

export interface GolemSubscriptionObserver {
  activeSubscriptions?(model: string, count: number): void;
  eventReceived?(model: string): void;
  evaluationPerformed?(model: string, latencyMs: number): void;
  eventDelivered?(model: string): void;
  eventSuppressed?(model: string, reason: GolemSubscriptionSuppressionReason): void;
  queueDepth?(model: string, depth: number, capacity: number): void;
  overflowDisconnected?(model: string): void;
}

export interface GolemSubscriptionOptions {
  queueCapacity?: number;
  observer?: GolemSubscriptionObserver;
}

export type SubscriptionEvaluation<T> =
  | { deliver: true; value: T }
  | { deliver: false; reason: GolemSubscriptionSuppressionReason };

export interface SubscriptionRegistration<T> {
  /** Used by Map directly so policy work is shared only for the exact same context identity. */
  contextIdentity: unknown;
  /** Canonical normalized where plus normalized entity selection. */
  evaluationKey: string;
  evaluate(event: GolemEventPayload): Promise<SubscriptionEvaluation<T>>;
}

interface Subscriber<T> extends SubscriptionRegistration<T> {
  iterator: HubIterator<T>;
}

function observe(callback: (() => void) | undefined): void {
  try {
    callback?.();
  } catch {
    // Observability must never affect subscription correctness or availability.
  }
}

class HubIterator<T> implements AsyncIterableIterator<T> {
  private readonly values: T[] = [];
  private readonly waiters: Array<{
    resolve: (result: IteratorResult<T>) => void;
    reject: (error: unknown) => void;
  }> = [];
  private ended = false;
  private failure: unknown;

  constructor(
    private readonly capacity: number,
    private readonly depth: (value: number) => void,
    private readonly detach: (error?: unknown) => void,
  ) {}

  push(value: T): 'delivered' | 'overflow' | 'closed' {
    if (this.ended) return 'closed';
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter.resolve({ value, done: false });
      return 'delivered';
    }
    if (this.values.length >= this.capacity) {
      const error = new GraphQLError(
        `Golem subscription queue exceeded its capacity of ${this.capacity}`,
        { extensions: { code: 'GOLEM_SUBSCRIPTION_OVERFLOW', capacity: this.capacity } },
      );
      this.detach(error);
      return 'overflow';
    }
    this.values.push(value);
    this.depth(this.values.length);
    return 'delivered';
  }

  finish(error?: unknown): void {
    if (this.ended) return;
    this.ended = true;
    this.failure = error;
    this.values.length = 0;
    this.depth(0);
    for (const waiter of this.waiters.splice(0)) {
      if (error) waiter.reject(error);
      else waiter.resolve({ value: undefined as T, done: true });
    }
  }

  next(): Promise<IteratorResult<T>> {
    if (this.failure) return Promise.reject(this.failure);
    if (this.values.length > 0) {
      const value = this.values.shift()!;
      this.depth(this.values.length);
      return Promise.resolve({ value, done: false });
    }
    if (this.ended) return Promise.resolve({ value: undefined as T, done: true });
    return new Promise((resolve, reject) => this.waiters.push({ resolve, reject }));
  }

  return(value?: unknown): Promise<IteratorResult<T>> {
    this.detach();
    return Promise.resolve({ value: value as T, done: true });
  }

  throw(error?: unknown): Promise<IteratorResult<T>> {
    this.detach(error);
    return Promise.reject(error);
  }

  [Symbol.asyncIterator](): AsyncIterableIterator<T> {
    return this;
  }
}

/** One instance is owned by one model in one generated GraphQL schema. */
export class ModelSubscriptionHub<T> {
  private readonly subscribers = new Set<Subscriber<T>>();
  private readonly capacity: number;
  private readonly observer?: GolemSubscriptionObserver;
  private source?: AsyncIterableIterator<GolemEventPayload>;
  private runIdentity?: object;
  private stopReading?: () => void;

  constructor(
    private readonly model: string,
    private readonly open: () => AsyncIterableIterator<GolemEventPayload>,
    options: GolemSubscriptionOptions = {},
  ) {
    this.capacity = options.queueCapacity ?? DEFAULT_SUBSCRIPTION_QUEUE_CAPACITY;
    if (!Number.isSafeInteger(this.capacity) || this.capacity < 1) {
      throw new Error('subscription.queueCapacity must be a positive safe integer');
    }
    this.observer = options.observer;
  }

  subscribe(registration: SubscriptionRegistration<T>): AsyncIterableIterator<T> {
    let subscriber!: Subscriber<T>;
    const iterator = new HubIterator<T>(
      this.capacity,
      (depth) => observe(() => this.observer?.queueDepth?.(this.model, depth, this.capacity)),
      (error) => this.remove(subscriber, error),
    );
    subscriber = { ...registration, iterator };
    this.subscribers.add(subscriber);
    observe(() => this.observer?.activeSubscriptions?.(this.model, this.subscribers.size));
    if (this.subscribers.size === 1) this.start();
    return iterator;
  }

  private start(): void {
    const identity = {};
    this.runIdentity = identity;
    let stop!: () => void;
    const stopped = new Promise<void>((resolve) => {
      stop = resolve;
    });
    this.stopReading = stop;
    try {
      this.source = this.open();
    } catch (error) {
      this.failAll(error);
      return;
    }
    void this.read(this.source, stopped, identity);
  }

  private async read(
    source: AsyncIterableIterator<GolemEventPayload>,
    stopped: Promise<void>,
    identity: object,
  ): Promise<void> {
    try {
      while (this.runIdentity === identity && this.subscribers.size > 0) {
        const step = await Promise.race([
          source.next().then((value) => ({ kind: 'event' as const, value })),
          stopped.then(() => ({ kind: 'stopped' as const })),
        ]);
        if (step.kind === 'stopped' || step.value.done) break;
        observe(() => this.observer?.eventReceived?.(this.model));
        await this.dispatch(step.value.value);
      }
      if (this.runIdentity === identity && this.subscribers.size > 0) {
        this.failAll(new GraphQLError('Golem subscription event source closed unexpectedly', {
          extensions: { code: 'GOLEM_SUBSCRIPTION_SOURCE_CLOSED' },
        }));
      }
    } catch (error) {
      if (this.runIdentity === identity) this.failAll(error);
    }
  }

  private async dispatch(event: GolemEventPayload): Promise<void> {
    const byContext = new Map<unknown, Map<string, Subscriber<T>[]>>();
    for (const subscriber of this.subscribers) {
      let byEvaluation = byContext.get(subscriber.contextIdentity);
      if (!byEvaluation) {
        byEvaluation = new Map();
        byContext.set(subscriber.contextIdentity, byEvaluation);
      }
      const group = byEvaluation.get(subscriber.evaluationKey) ?? [];
      group.push(subscriber);
      byEvaluation.set(subscriber.evaluationKey, group);
    }
    const groups = [...byContext.values()].flatMap((byEvaluation) => [...byEvaluation.values()]);
    await Promise.all(groups.map(async (group) => {
      const started = performance.now();
      let evaluation: SubscriptionEvaluation<T>;
      try {
        evaluation = await group[0].evaluate(event);
      } catch (error) {
        observe(() => this.observer?.evaluationPerformed?.(
          this.model,
          performance.now() - started,
        ));
        for (const subscriber of group) this.remove(subscriber, error);
        return;
      }
      observe(() => this.observer?.evaluationPerformed?.(
        this.model,
        performance.now() - started,
      ));
      for (const subscriber of group) {
        if (!this.subscribers.has(subscriber)) continue;
        if (!evaluation.deliver) {
          observe(() => this.observer?.eventSuppressed?.(this.model, evaluation.reason));
          continue;
        }
        const outcome = subscriber.iterator.push(evaluation.value);
        if (outcome === 'delivered') {
          observe(() => this.observer?.eventDelivered?.(this.model));
        } else if (outcome === 'overflow') {
          observe(() => this.observer?.overflowDisconnected?.(this.model));
        }
      }
    }));
  }

  private remove(subscriber: Subscriber<T>, error?: unknown): void {
    if (!this.subscribers.delete(subscriber)) return;
    subscriber.iterator.finish(error);
    observe(() => this.observer?.activeSubscriptions?.(this.model, this.subscribers.size));
    if (this.subscribers.size === 0) this.stop();
  }

  private failAll(error: unknown): void {
    for (const subscriber of [...this.subscribers]) this.remove(subscriber, error);
  }

  private stop(): void {
    this.runIdentity = undefined;
    this.stopReading?.();
    this.stopReading = undefined;
    const source = this.source;
    this.source = undefined;
    void Promise.resolve(source?.return?.()).catch(() => undefined);
  }
}

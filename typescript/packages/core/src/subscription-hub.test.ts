import { GolemEventPayload } from './events';
import { ModelSubscriptionHub } from './subscription-hub';

class Source {
  opened = 0;
  closed = 0;
  private values: GolemEventPayload[] = [];
  private notify?: () => void;

  push(value: GolemEventPayload): void {
    this.values.push(value);
    this.notify?.();
    this.notify = undefined;
  }

  open = (): AsyncIterableIterator<GolemEventPayload> => {
    this.opened += 1;
    const owner = this;
    const inner = (async function* () {
      while (true) {
        while (owner.values.length > 0) yield owner.values.shift()!;
        await new Promise<void>((resolve) => {
          owner.notify = resolve;
        });
      }
    })();
    return {
      next: () => inner.next(),
      return: (value?: never) => {
        owner.closed += 1;
        return inner.return(value);
      },
      throw: (error?: unknown) => inner.throw(error),
      [Symbol.asyncIterator]() { return this; },
    };
  };
}

const event: GolemEventPayload = { type: 'UPDATED', model: 'User', id: 'u1' };

describe('ModelSubscriptionHub observability', () => {
  it('reports shared evaluations, per-subscriber delivery, queue depth, and lifecycle', async () => {
    const source = new Source();
    const observer = {
      activeSubscriptions: jest.fn(),
      eventReceived: jest.fn(),
      evaluationPerformed: jest.fn(),
      eventDelivered: jest.fn(),
      eventSuppressed: jest.fn(),
      queueDepth: jest.fn(),
      overflowDisconnected: jest.fn(),
    };
    const hub = new ModelSubscriptionHub<{ id: unknown }>('User', source.open, { observer });
    const context = {};
    const evaluate = jest.fn(async (payload: GolemEventPayload) => ({
      deliver: true as const,
      value: { id: payload.id },
    }));
    const first = hub.subscribe({ contextIdentity: context, evaluationKey: 'same', evaluate });
    const second = hub.subscribe({ contextIdentity: context, evaluationKey: 'same', evaluate });

    source.push(event);
    expect((await first.next()).value).toEqual({ id: 'u1' });
    expect((await second.next()).value).toEqual({ id: 'u1' });

    expect(source.opened).toBe(1);
    expect(evaluate).toHaveBeenCalledTimes(1);
    expect(observer.eventReceived).toHaveBeenCalledWith('User');
    expect(observer.evaluationPerformed).toHaveBeenCalledWith('User', expect.any(Number));
    expect(observer.eventDelivered).toHaveBeenCalledTimes(2);
    expect(observer.queueDepth).toHaveBeenCalledWith('User', 1, 64);
    await first.return?.();
    await second.return?.();
    expect(observer.activeSubscriptions.mock.calls).toEqual([
      ['User', 1],
      ['User', 2],
      ['User', 1],
      ['User', 0],
    ]);
    expect(source.closed).toBe(1);
  });

  it('reports each suppression reason without enqueuing an event', async () => {
    const source = new Source();
    const eventSuppressed = jest.fn();
    const hub = new ModelSubscriptionHub('User', source.open, {
      observer: { eventSuppressed },
    });
    const iterator = hub.subscribe({
      contextIdentity: {},
      evaluationKey: 'denied',
      evaluate: async () => ({ deliver: false, reason: 'authorization' }),
    });

    source.push(event);
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(eventSuppressed).toHaveBeenCalledWith('User', 'authorization');
    await iterator.return?.();
  });

  it('swallows observer failures so metrics cannot break event delivery', async () => {
    const source = new Source();
    const hub = new ModelSubscriptionHub<{ id: unknown }>('User', source.open, {
      observer: { eventReceived: () => { throw new Error('metrics unavailable'); } },
    });
    const iterator = hub.subscribe({
      contextIdentity: {},
      evaluationKey: 'one',
      evaluate: async (payload) => ({ deliver: true, value: { id: payload.id } }),
    });

    source.push(event);
    expect((await iterator.next()).value).toEqual({ id: 'u1' });
    await iterator.return?.();
  });

  it('validates queue capacity eagerly', () => {
    expect(() => new ModelSubscriptionHub('User', new Source().open, { queueCapacity: 0 }))
      .toThrow('subscription.queueCapacity must be a positive safe integer');
  });
});

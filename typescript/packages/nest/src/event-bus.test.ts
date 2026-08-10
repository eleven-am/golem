import {
  decodeGolemEventMessage,
  encodeGolemEventMessage,
  GolemEventPayload,
  GolemEventWireEnvelope,
} from '@eleven-am/golem-core';
import { PubSubEngine } from 'graphql-subscriptions';
import { PubSubEventBus } from './event-bus';

function sourceOf(values: readonly GolemEventWireEnvelope[]): AsyncIterableIterator<GolemEventWireEnvelope> {
  return (async function* () {
    for (const value of values) yield value;
  })();
}

describe('PubSubEventBus wire boundaries', () => {
  it('publishes only versioned JSON-safe envelopes', async () => {
    const publish = jest.fn(async () => undefined);
    const engine = { publish } as unknown as PubSubEngine;
    const bus = new PubSubEventBus(engine);
    const payload: GolemEventPayload = { type: 'UPDATED', model: 'Metric', id: 7n };

    await bus.publish('golem.Metric', payload);

    const envelope = publish.mock.calls[0][1];
    expect(() => JSON.stringify(envelope)).not.toThrow();
    expect(decodeGolemEventMessage(JSON.parse(JSON.stringify(envelope)))).toEqual(payload);
  });

  it('decodes and expands a transport batch in order', async () => {
    const events: GolemEventPayload[] = [
      { type: 'UPDATED', model: 'User', id: 'u1' },
      { type: 'UPDATED', model: 'User', id: 'u2' },
    ];
    const engine = {
      asyncIterableIterator: jest.fn(() =>
        sourceOf([encodeGolemEventMessage({ kind: 'batch', events })]),
      ),
    } as unknown as PubSubEngine;
    const iterator = new PubSubEventBus(engine).iterate('golem.User');

    expect((await iterator.next()).value).toEqual(events[0]);
    expect((await iterator.next()).value).toEqual(events[1]);
    expect((await iterator.next()).done).toBe(true);
  });

  it('publishes a bounded batch as one versioned transport envelope', async () => {
    const publish = jest.fn(async () => undefined);
    const engine = { publish } as unknown as PubSubEngine;
    const bus = new PubSubEventBus(engine);
    const events: GolemEventPayload[] = [
      { type: 'UPDATED', model: 'User', id: 'u1' },
      { type: 'UPDATED', model: 'User', id: 'u2' },
    ];

    await bus.publishMany('golem.User', events);

    expect(publish).toHaveBeenCalledTimes(1);
    expect(decodeGolemEventMessage(publish.mock.calls[0][1])).toEqual({ kind: 'batch', events });
  });

  it('cancels immediately while the underlying transport is waiting', async () => {
    const source = {
      next: jest.fn(() => new Promise<IteratorResult<GolemEventWireEnvelope>>(() => undefined)),
      return: jest.fn(() => new Promise<IteratorResult<GolemEventWireEnvelope>>(() => undefined)),
      [Symbol.asyncIterator]() { return this; },
    };
    const engine = {
      asyncIterableIterator: jest.fn(() => source),
    } as unknown as PubSubEngine;
    const iterator = new PubSubEventBus(engine).iterate('golem.User');
    const pending = iterator.next();

    await expect(iterator.return!()).resolves.toMatchObject({ done: true });
    await expect(pending).resolves.toMatchObject({ done: true });
    expect(source.return).toHaveBeenCalledTimes(1);
  });
});

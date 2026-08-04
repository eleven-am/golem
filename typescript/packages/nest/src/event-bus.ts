import {
  decodeGolemEventMessage,
  encodeGolemEventMessage,
  GolemEventBus,
  GolemEventPayload,
  GolemEventWireEnvelope,
  isGolemEventBatch,
} from '@eleven-am/golem-core';
import { PubSubEngine } from 'graphql-subscriptions';

export class PubSubEventBus implements GolemEventBus {
  constructor(private readonly engine: PubSubEngine) {}

  async publish(topic: string, payload: GolemEventPayload): Promise<void> {
    await this.engine.publish(topic, encodeGolemEventMessage(payload));
  }

  async publishMany(topic: string, payloads: readonly GolemEventPayload[]): Promise<void> {
    if (payloads.length === 0) return;
    await this.engine.publish(topic, encodeGolemEventMessage({ kind: 'batch', events: payloads }));
  }

  iterate(topic: string): AsyncIterableIterator<GolemEventPayload> {
    const source = this.engine.asyncIterableIterator<GolemEventWireEnvelope>(topic);
    const abandonedMarker = Symbol('golem.pubsub.abandoned');
    let abandon!: () => void;
    const abandoned = new Promise<typeof abandonedMarker>((resolve) => {
      abandon = () => resolve(abandonedMarker);
    });
    const events = (async function* (): AsyncIterableIterator<GolemEventPayload> {
      try {
        while (true) {
          const step = await Promise.race([source.next(), abandoned]);
          if (step === abandonedMarker || step.done) {
            return;
          }
          const message = decodeGolemEventMessage(step.value);
          if (isGolemEventBatch(message)) {
            for (const event of message.events) {
              yield event;
            }
          } else {
            yield message;
          }
        }
      } finally {
        void Promise.resolve(source.return?.()).catch(() => undefined);
      }
    })();
    return {
      next: () => events.next(),
      return: (value?: unknown) => {
        abandon();
        return events.return!(value as never);
      },
      throw: (error?: unknown) => events.throw!(error),
      [Symbol.asyncIterator]() {
        return this;
      },
    };
  }
}

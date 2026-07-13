import { GolemEventBus, GolemEventPayload } from '@eleven-am/golem-core';
import { PubSubEngine } from 'graphql-subscriptions';

export class PubSubEventBus implements GolemEventBus {
  constructor(private readonly engine: PubSubEngine) {}

  async publish(topic: string, payload: GolemEventPayload): Promise<void> {
    await this.engine.publish(topic, payload);
  }

  iterate(topic: string): AsyncIterableIterator<GolemEventPayload> {
    return this.engine.asyncIterableIterator<GolemEventPayload>(topic);
  }
}

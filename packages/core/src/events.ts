export type GolemEventType = 'CREATED' | 'UPDATED' | 'DELETED';

export interface GolemEventPayload {
  type: GolemEventType;
  model: string;
  id: string | number;
  /** Pre-delete snapshot used only to authorize deletion delivery; never exposed as event.entity. */
  entity?: Record<string, unknown>;
}

export interface GolemEventBus {
  publish(topic: string, payload: GolemEventPayload): Promise<void>;
  iterate(topic: string): AsyncIterableIterator<GolemEventPayload>;
}

export function eventTopic(model: string): string {
  return `golem.${model}`;
}

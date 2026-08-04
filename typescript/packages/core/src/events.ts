import type { DecimalLike } from '@eleven-am/golem-policy';

export type GolemEventType = 'CREATED' | 'UPDATED' | 'DELETED';

export type GolemEventIdentityScalar = string | number | bigint | Date | Uint8Array | DecimalLike;

export type GolemEventIdentity =
  | GolemEventIdentityScalar
  | Readonly<Record<string, GolemEventIdentityScalar>>;

export interface GolemEventPayload {
  type: GolemEventType;
  model: string;
  id: GolemEventIdentity;
  /** Pre-delete snapshot used only to authorize deletion delivery; never exposed as event.entity. */
  entity?: Record<string, unknown>;
}

export interface GolemEventBatch {
  kind: 'batch';
  events: readonly GolemEventPayload[];
}

export type GolemEventMessage = GolemEventPayload | GolemEventBatch;

export interface GolemEventBus {
  publish(topic: string, payload: GolemEventPayload): Promise<void>;
  publishMany?(topic: string, payloads: readonly GolemEventPayload[]): Promise<void>;
  iterate(topic: string): AsyncIterableIterator<GolemEventPayload>;
}

export function eventTopic(model: string): string {
  return `golem.${model}`;
}

export function isGolemEventBatch(message: GolemEventMessage): message is GolemEventBatch {
  return 'kind' in message && message.kind === 'batch';
}

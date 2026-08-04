import { isDecimalLike, isPlainObject } from '@eleven-am/golem-policy';
import { prismaDecimal } from './compiled-read-decode';
import {
  GolemEventBatch,
  GolemEventIdentity,
  GolemEventMessage,
  GolemEventPayload,
  GolemEventType,
} from './events';

export const GOLEM_EVENT_PROTOCOL = 'golem.event';
export const GOLEM_EVENT_PROTOCOL_VERSION = 1 as const;

type WireNode =
  | { tag: 'null' }
  | { tag: 'undefined' }
  | { tag: 'boolean'; value: boolean }
  | { tag: 'number'; value: string }
  | { tag: 'bigint'; value: string }
  | { tag: 'string'; value: string }
  | { tag: 'date'; value: string | null }
  | { tag: 'decimal'; value: string }
  | { tag: 'bytes'; value: string }
  | { tag: 'hole' }
  | { tag: 'array'; value: WireNode[] }
  | { tag: 'object'; value: Array<[string, WireNode]> };

export interface GolemEventWireEnvelopeV1 {
  protocol: typeof GOLEM_EVENT_PROTOCOL;
  version: typeof GOLEM_EVENT_PROTOCOL_VERSION;
  body: WireNode;
}

export type GolemEventWireEnvelope = GolemEventWireEnvelopeV1;

export class GolemEventCodecError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'GolemEventCodecError';
  }
}

class DecodedDecimal {
  constructor(private readonly text: string) {}

  toString(): string {
    return this.text;
  }

  toFixed(): string {
    return this.text;
  }

  toJSON(): string {
    return this.text;
  }
}

function bytes(value: Uint8Array): string {
  return Buffer.from(value.buffer, value.byteOffset, value.byteLength).toString('base64');
}

function pathFor(path: string, key: string): string {
  return path === '$' ? `$.${key}` : `${path}.${key}`;
}

function encodeNode(value: unknown, path: string, open: readonly object[]): WireNode {
  if (value === null) return { tag: 'null' };
  switch (typeof value) {
    case 'undefined':
      return { tag: 'undefined' };
    case 'boolean':
      return { tag: 'boolean', value };
    case 'number':
      return {
        tag: 'number',
        value: Number.isNaN(value)
          ? 'NaN'
          : value === Infinity
            ? 'Infinity'
            : value === -Infinity
              ? '-Infinity'
              : Object.is(value, -0)
                ? '-0'
                : String(value),
      };
    case 'bigint':
      return { tag: 'bigint', value: value.toString() };
    case 'string':
      return { tag: 'string', value };
    case 'function':
    case 'symbol':
      throw new GolemEventCodecError(`${path} contains an unsupported ${typeof value}`);
  }

  const object = value as object;
  if (open.includes(object)) {
    throw new GolemEventCodecError(`${path} contains a cyclic reference`);
  }
  if (object instanceof Date) {
    return { tag: 'date', value: Number.isNaN(object.getTime()) ? null : object.toISOString() };
  }
  if (object instanceof Uint8Array) {
    return { tag: 'bytes', value: bytes(object) };
  }
  if (isDecimalLike(object)) {
    return { tag: 'decimal', value: object.toString() };
  }

  const nested = [...open, object];
  if (Array.isArray(object)) {
    const encoded: WireNode[] = [];
    for (let index = 0; index < object.length; index += 1) {
      encoded.push(
        index in object ? encodeNode(object[index], `${path}[${index}]`, nested) : { tag: 'hole' },
      );
    }
    return { tag: 'array', value: encoded };
  }
  if (!isPlainObject(object)) {
    const name = (object as { constructor?: { name?: unknown } }).constructor?.name;
    throw new GolemEventCodecError(
      `${path} contains an unsupported ${typeof name === 'string' ? name : 'object'} instance`,
    );
  }
  if (Object.getOwnPropertySymbols(object).length > 0) {
    throw new GolemEventCodecError(`${path} contains symbol-keyed state`);
  }

  const encoded: Array<[string, WireNode]> = [];
  for (const key of Object.getOwnPropertyNames(object).sort()) {
    const descriptor = Object.getOwnPropertyDescriptor(object, key)!;
    if (!('value' in descriptor)) {
      throw new GolemEventCodecError(`${pathFor(path, key)} is an accessor property`);
    }
    encoded.push([key, encodeNode(descriptor.value, pathFor(path, key), nested)]);
  }
  return { tag: 'object', value: encoded };
}

function node(value: unknown, path: string): WireNode {
  if (!isPlainObject(value) || typeof value.tag !== 'string') {
    throw new GolemEventCodecError(`${path} is not a wire value`);
  }
  return value as WireNode;
}

function stringValue(value: unknown, path: string): string {
  if (typeof value !== 'string') {
    throw new GolemEventCodecError(`${path} must be a string`);
  }
  return value;
}

function decodeNumber(value: unknown, path: string): number {
  const text = stringValue(value, path);
  switch (text) {
    case 'NaN': return Number.NaN;
    case 'Infinity': return Infinity;
    case '-Infinity': return -Infinity;
    case '-0': return -0;
    default: {
      const parsed = Number(text);
      if (!Number.isFinite(parsed)) {
        throw new GolemEventCodecError(`${path} is not a finite encoded number`);
      }
      return parsed;
    }
  }
}

function decodeNode(input: unknown, path: string): unknown {
  const encoded = node(input, path);
  switch (encoded.tag) {
    case 'null': return null;
    case 'undefined': return undefined;
    case 'boolean':
      if (typeof encoded.value !== 'boolean') {
        throw new GolemEventCodecError(`${path}.value must be a boolean`);
      }
      return encoded.value;
    case 'number': return decodeNumber(encoded.value, `${path}.value`);
    case 'bigint': return BigInt(stringValue(encoded.value, `${path}.value`));
    case 'string': return stringValue(encoded.value, `${path}.value`);
    case 'date':
      if (encoded.value !== null && typeof encoded.value !== 'string') {
        throw new GolemEventCodecError(`${path}.value must be a timestamp or null`);
      }
      return encoded.value === null ? new Date(Number.NaN) : new Date(encoded.value);
    case 'decimal': {
      const text = stringValue(encoded.value, `${path}.value`);
      const Decimal = prismaDecimal();
      return Decimal === null ? new DecodedDecimal(text) : new Decimal(text);
    }
    case 'bytes':
      return Uint8Array.from(Buffer.from(stringValue(encoded.value, `${path}.value`), 'base64'));
    case 'hole':
      return undefined;
    case 'array': {
      if (!Array.isArray(encoded.value)) {
        throw new GolemEventCodecError(`${path}.value must be an array`);
      }
      const result: unknown[] = new Array(encoded.value.length);
      for (let index = 0; index < encoded.value.length; index += 1) {
        const item = node(encoded.value[index], `${path}.value[${index}]`);
        if (item.tag !== 'hole') {
          result[index] = decodeNode(item, `${path}.value[${index}]`);
        }
      }
      return result;
    }
    case 'object': {
      if (!Array.isArray(encoded.value)) {
        throw new GolemEventCodecError(`${path}.value must be an entry array`);
      }
      const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
      for (let index = 0; index < encoded.value.length; index += 1) {
        const entry = encoded.value[index];
        if (!Array.isArray(entry) || entry.length !== 2 || typeof entry[0] !== 'string') {
          throw new GolemEventCodecError(`${path}.value[${index}] must be a string/value pair`);
        }
        if (Object.prototype.hasOwnProperty.call(result, entry[0])) {
          throw new GolemEventCodecError(`${path}.value contains duplicate key ${JSON.stringify(entry[0])}`);
        }
        result[entry[0]] = decodeNode(entry[1], `${path}.value[${index}][1]`);
      }
      return result;
    }
    default:
      throw new GolemEventCodecError(
        `${path} carries unknown tag ${JSON.stringify((input as { tag?: unknown }).tag)}`,
      );
  }
}

const EVENT_TYPES = new Set<GolemEventType>(['CREATED', 'UPDATED', 'DELETED']);

function eventPayload(value: unknown, path: string): GolemEventPayload {
  if (!isPlainObject(value)) {
    throw new GolemEventCodecError(`${path} is not an event payload`);
  }
  if (!EVENT_TYPES.has(value.type as GolemEventType)) {
    throw new GolemEventCodecError(`${path}.type is not a Golem event type`);
  }
  if (typeof value.model !== 'string' || value.model.length === 0) {
    throw new GolemEventCodecError(`${path}.model must be a non-empty string`);
  }
  if (!('id' in value)) {
    throw new GolemEventCodecError(`${path}.id is required`);
  }
  if ('entity' in value && value.entity !== undefined && !isPlainObject(value.entity)) {
    throw new GolemEventCodecError(`${path}.entity must be an object when present`);
  }
  return value as unknown as GolemEventPayload;
}

function eventMessage(value: unknown): GolemEventMessage {
  if (isPlainObject(value) && value.kind === 'batch') {
    if (!Array.isArray(value.events)) {
      throw new GolemEventCodecError('$.body.events must be an array');
    }
    return {
      kind: 'batch',
      events: value.events.map((event, index) => eventPayload(event, `$.body.events[${index}]`)),
    } satisfies GolemEventBatch;
  }
  return eventPayload(value, '$.body');
}

export function encodeGolemEventMessage(message: GolemEventMessage): GolemEventWireEnvelope {
  return {
    protocol: GOLEM_EVENT_PROTOCOL,
    version: GOLEM_EVENT_PROTOCOL_VERSION,
    body: encodeNode(message, '$.body', []),
  };
}

export function decodeGolemEventMessage(envelope: unknown): GolemEventMessage {
  if (!isPlainObject(envelope) || envelope.protocol !== GOLEM_EVENT_PROTOCOL) {
    throw new GolemEventCodecError('message is not a Golem event envelope');
  }
  if (envelope.version !== GOLEM_EVENT_PROTOCOL_VERSION) {
    throw new GolemEventCodecError(`unsupported Golem event protocol version ${String(envelope.version)}`);
  }
  return eventMessage(decodeNode(envelope.body, '$.body'));
}

export function encodedGolemEventBytes(message: GolemEventMessage): number {
  return Buffer.byteLength(JSON.stringify(encodeGolemEventMessage(message)), 'utf8');
}

export function identityRecord(identity: GolemEventIdentity): Readonly<Record<string, unknown>> | null {
  return isPlainObject(identity) ? identity : null;
}

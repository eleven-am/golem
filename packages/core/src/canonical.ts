import { isDecimalLike } from '@eleven-am/golem-policy';

export class CanonicalValueError extends Error {
  constructor(
    readonly path: string,
    readonly reason: string,
  ) {
    super(`${path === '' ? 'value' : path}: ${reason}`);
    this.name = 'CanonicalValueError';
  }
}

function instanceName(value: object): string {
  const constructor = (value as { constructor?: { name?: unknown } }).constructor;
  const name = constructor === undefined ? undefined : constructor.name;
  return typeof name === 'string' && name.length > 0 ? name : 'object';
}

function childPath(path: string, key: string): string {
  return path === '' ? key : `${path}.${key}`;
}

function bytesToken(value: Uint8Array): string {
  return Buffer.from(value.buffer, value.byteOffset, value.byteLength).toString('base64');
}

function token(value: unknown, path: string, open: readonly object[]): string {
  if (value === null) {
    return 'null';
  }
  switch (typeof value) {
    case 'undefined':
      return 'undefined';
    case 'string':
      return `string:${JSON.stringify(value)}`;
    case 'boolean':
      return `boolean:${String(value)}`;
    case 'number':
      if (Number.isNaN(value)) return 'number:NaN';
      if (value === Infinity) return 'number:Infinity';
      if (value === -Infinity) return 'number:-Infinity';
      if (Object.is(value, -0)) return 'number:-0';
      return `number:${String(value)}`;
    case 'bigint':
      return `bigint:${value.toString()}`;
    case 'function':
      throw new CanonicalValueError(path, 'a function cannot be serialized exactly');
    case 'symbol':
      throw new CanonicalValueError(path, 'a symbol cannot be serialized exactly');
  }

  const object = value as object;
  if (open.includes(object)) {
    throw new CanonicalValueError(path, 'it holds a reference back to itself');
  }
  if (object instanceof Date) {
    const time = object.getTime();
    return Number.isNaN(time) ? 'date:invalid' : `date:${object.toISOString()}`;
  }
  if (object instanceof Uint8Array) {
    return `bytes:${bytesToken(object)}`;
  }
  if (isDecimalLike(object)) {
    return `decimal:${JSON.stringify(object.toString())}`;
  }

  const nested = [...open, object];
  if (Array.isArray(object)) {
    const values: string[] = [];
    for (let index = 0; index < object.length; index += 1) {
      values.push(
        index in object ? token(object[index], `${path}[${index}]`, nested) : 'hole',
      );
    }
    return `array:[${values.join(',')}]`;
  }

  const prototype = Object.getPrototypeOf(object) as unknown;
  if (prototype !== Object.prototype && prototype !== null) {
    throw new CanonicalValueError(
      path,
      `a ${instanceName(object)} instance cannot be serialized exactly`,
    );
  }
  if (Object.getOwnPropertySymbols(object).length > 0) {
    throw new CanonicalValueError(path, 'it carries symbol keys, which cannot be serialized exactly');
  }

  const record = object as Record<string, unknown>;
  const entries = Object.getOwnPropertyNames(record)
    .sort()
    .map((key) => {
      const descriptor = Object.getOwnPropertyDescriptor(record, key)!;
      if (!('value' in descriptor)) {
        throw new CanonicalValueError(
          childPath(path, key),
          'an accessor property cannot be serialized without executing user code',
        );
      }
      return `${JSON.stringify(key)}:${token(descriptor.value, childPath(path, key), nested)}`;
    });
  return `object:{${entries.join(',')}}`;
}

/**
 * Produces an exact, type-tagged and key-order-independent token for supported data values.
 * The token is intended for identity and cache keys; it is not a public wire format.
 */
export function canonicalToken(value: unknown): string {
  return token(value, '', []);
}

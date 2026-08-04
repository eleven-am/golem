export interface DecimalLike {
  toFixed(digits?: number): string;
  toString(): string;
}

export type ScalarKind = 'null' | 'numeric' | 'string' | 'boolean' | 'date';

export type ValueKind = ScalarKind | 'unsupported';

interface DecimalValue {
  readonly sign: number;
  readonly int: string;
  readonly frac: string;
}

const DECIMAL_PATTERN = /^([+-]?)(\d*)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/;

export function isDecimalLike(value: unknown): value is DecimalLike {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as DecimalLike).toFixed === 'function' &&
    typeof (value as DecimalLike).toString === 'function'
  );
}

export function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false;
  }
  if (value instanceof Date || isDecimalLike(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value) as unknown;
  return prototype === Object.prototype || prototype === null;
}

export function valueKind(value: unknown): ValueKind {
  if (value === null || value === undefined) {
    return 'null';
  }
  if (typeof value === 'number' || typeof value === 'bigint') {
    return 'numeric';
  }
  if (typeof value === 'string') {
    return 'string';
  }
  if (typeof value === 'boolean') {
    return 'boolean';
  }
  if (value instanceof Date) {
    return 'date';
  }
  if (isDecimalLike(value)) {
    return 'numeric';
  }
  return 'unsupported';
}

export function describeValue(value: unknown): string {
  if (value === null) {
    return 'null';
  }
  if (value === undefined) {
    return 'undefined';
  }
  if (Array.isArray(value)) {
    return 'array';
  }
  if (typeof value === 'object') {
    if (isDecimalLike(value)) {
      return 'decimal';
    }
    return (value as { constructor?: { name?: string } }).constructor?.name ?? 'object';
  }
  return typeof value;
}

function parseDecimal(text: string): DecimalValue | null {
  const match = DECIMAL_PATTERN.exec(text.trim());
  if (match === null) {
    return null;
  }
  const signText = match[1] ?? '';
  const intText = match[2] ?? '';
  const fracText = match[3] ?? '';
  const expText = match[4];
  if (intText.length === 0 && fracText.length === 0) {
    return null;
  }
  const exponent = expText === undefined ? 0 : Number(expText);
  if (!Number.isFinite(exponent)) {
    return null;
  }
  const digits = intText + fracText;
  const pointIndex = intText.length + exponent;
  let int: string;
  let frac: string;
  if (pointIndex <= 0) {
    int = '';
    frac = '0'.repeat(-pointIndex) + digits;
  } else if (pointIndex >= digits.length) {
    int = digits + '0'.repeat(pointIndex - digits.length);
    frac = '';
  } else {
    int = digits.slice(0, pointIndex);
    frac = digits.slice(pointIndex);
  }
  int = int.replace(/^0+/, '');
  frac = frac.replace(/0+$/, '');
  const sign = int.length === 0 && frac.length === 0 ? 0 : signText === '-' ? -1 : 1;
  return { sign, int, frac };
}

function toDecimal(value: unknown): DecimalValue | null {
  if (typeof value === 'bigint') {
    return parseDecimal(value.toString());
  }
  if (typeof value === 'number') {
    return Number.isFinite(value) ? parseDecimal(value.toString()) : null;
  }
  if (isDecimalLike(value)) {
    return parseDecimal(String(value));
  }
  return null;
}

function compareMagnitude(left: DecimalValue, right: DecimalValue): number {
  if (left.int.length !== right.int.length) {
    return left.int.length < right.int.length ? -1 : 1;
  }
  if (left.int !== right.int) {
    return left.int < right.int ? -1 : 1;
  }
  const width = Math.max(left.frac.length, right.frac.length);
  const leftFrac = left.frac.padEnd(width, '0');
  const rightFrac = right.frac.padEnd(width, '0');
  if (leftFrac === rightFrac) {
    return 0;
  }
  return leftFrac < rightFrac ? -1 : 1;
}

function compareDecimal(left: DecimalValue, right: DecimalValue): number {
  if (left.sign !== right.sign) {
    return left.sign < right.sign ? -1 : 1;
  }
  if (left.sign === 0) {
    return 0;
  }
  return compareMagnitude(left, right) * left.sign;
}

function toTime(value: unknown): number | null {
  if (value instanceof Date) {
    const time = value.getTime();
    return Number.isNaN(time) ? null : time;
  }
  if (typeof value === 'string') {
    const time = Date.parse(value);
    return Number.isNaN(time) ? null : time;
  }
  return null;
}

export function compareValues(left: unknown, right: unknown): number | null {
  const leftKind = valueKind(left);
  const rightKind = valueKind(right);
  if (leftKind === 'null' || rightKind === 'null' || leftKind === 'unsupported' || rightKind === 'unsupported') {
    return null;
  }
  if (leftKind === 'numeric' && rightKind === 'numeric') {
    const leftDecimal = toDecimal(left);
    const rightDecimal = toDecimal(right);
    if (leftDecimal === null || rightDecimal === null) {
      return null;
    }
    return compareDecimal(leftDecimal, rightDecimal);
  }
  if (leftKind === 'string' && rightKind === 'string') {
    const leftText = left as string;
    const rightText = right as string;
    if (leftText === rightText) {
      return 0;
    }
    return leftText < rightText ? -1 : 1;
  }
  if (leftKind === 'date' || rightKind === 'date') {
    if (leftKind !== 'date' && leftKind !== 'string') {
      return null;
    }
    if (rightKind !== 'date' && rightKind !== 'string') {
      return null;
    }
    const leftTime = toTime(left);
    const rightTime = toTime(right);
    if (leftTime === null || rightTime === null) {
      return null;
    }
    if (leftTime === rightTime) {
      return 0;
    }
    return leftTime < rightTime ? -1 : 1;
  }
  return null;
}

export function valuesEqual(left: unknown, right: unknown): boolean {
  const leftKind = valueKind(left);
  const rightKind = valueKind(right);
  if (leftKind === 'null' || rightKind === 'null') {
    return leftKind === 'null' && rightKind === 'null';
  }
  if (leftKind === 'boolean' || rightKind === 'boolean') {
    return leftKind === 'boolean' && rightKind === 'boolean' && left === right;
  }
  return compareValues(left, right) === 0;
}

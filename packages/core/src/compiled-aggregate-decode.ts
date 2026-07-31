import { DecimalConstructor, decodeDateTime } from './compiled-read-decode';

export type AggregateDecodeKind =
  | 'safeInteger'
  | 'float'
  | 'bigint'
  | 'decimal'
  | 'datetime'
  | 'string';

export type AggregateGroup = '_count' | '_sum' | '_avg' | '_min' | '_max';

export interface CompiledMeasure {
  readonly alias: string;
  readonly group: AggregateGroup;
  readonly field?: string;
  readonly decode: AggregateDecodeKind;
}

const PRISMA_RUNTIME = '@prisma/client/runtime/client';

const PRISMA_PACKAGE = '@prisma/client/package.json';

const UNKNOWN_VERSION = '0.0.0';

export type UnsafeIntegerErrorFactory = (column: string, value: string) => Error;

let cachedFactory: UnsafeIntegerErrorFactory | null | undefined;

function loadUnsafeIntegerError(): UnsafeIntegerErrorFactory | null {
  try {
    const runtime = require(PRISMA_RUNTIME) as { PrismaClientKnownRequestError?: unknown };
    const constructor = runtime.PrismaClientKnownRequestError;
    if (typeof constructor !== 'function') {
      return null;
    }
    let version = UNKNOWN_VERSION;
    try {
      version = (require(PRISMA_PACKAGE) as { version?: string }).version ?? UNKNOWN_VERSION;
    } catch {
      version = UNKNOWN_VERSION;
    }
    const known = constructor as new (
      message: string,
      options: { code: string; clientVersion: string },
    ) => Error;
    return (column, value) =>
      new known(
        `Integer value in column '${column}' is too large to represent as a JavaScript number without loss of precision, got: ${value}. Consider using BigInt type.`,
        { code: 'P2023', clientVersion: version },
      );
  } catch {
    return null;
  }
}

export function unsafeIntegerError(): UnsafeIntegerErrorFactory | null {
  if (cachedFactory === undefined) {
    cachedFactory = loadUnsafeIntegerError();
  }
  return cachedFactory;
}

const MAX_SAFE = BigInt(Number.MAX_SAFE_INTEGER);

function decodeSafeInteger(value: unknown, alias: string): number {
  if (typeof value === 'bigint') {
    if (value > MAX_SAFE || value < -MAX_SAFE) {
      throw (unsafeIntegerError() ?? fallbackUnsafeIntegerError)(alias, value.toString());
    }
    return Number(value);
  }
  const text = String(value);
  const parsed = Number(text);
  if (!Number.isSafeInteger(parsed)) {
    throw (unsafeIntegerError() ?? fallbackUnsafeIntegerError)(alias, text);
  }
  return parsed;
}

const fallbackUnsafeIntegerError: UnsafeIntegerErrorFactory = (column, value) =>
  new Error(
    `Integer value in column '${column}' is too large to represent as a JavaScript number without loss of precision, got: ${value}. Consider using BigInt type.`,
  );

function decodeMeasure(
  value: unknown,
  measure: CompiledMeasure,
  decimal: DecimalConstructor | null,
): unknown {
  if (value === null || value === undefined) {
    return null;
  }
  switch (measure.decode) {
    case 'safeInteger':
      return decodeSafeInteger(value, measure.alias);
    case 'float':
      return typeof value === 'number' ? value : Number(String(value));
    case 'bigint':
      return typeof value === 'bigint' ? value : BigInt(String(value));
    case 'decimal':
      return new decimal!(String(value));
    case 'datetime':
      return decodeDateTime(value);
    case 'string':
      return value;
  }
}

export function decodeAggregateRow(
  row: Record<string, unknown>,
  measures: readonly CompiledMeasure[],
  keys: readonly string[],
  decimal: DecimalConstructor | null,
): Record<string, unknown> {
  const decoded: Record<string, unknown> = {};
  for (const key of keys) {
    decoded[key] = row[key];
  }
  for (const measure of measures) {
    const value = decodeMeasure(row[measure.alias], measure, decimal);
    if (measure.field === undefined) {
      decoded[measure.group] = value;
      continue;
    }
    const nested = (decoded[measure.group] ?? {}) as Record<string, unknown>;
    nested[measure.field] = value;
    decoded[measure.group] = nested;
  }
  return decoded;
}

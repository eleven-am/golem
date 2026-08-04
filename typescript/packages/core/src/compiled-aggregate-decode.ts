import {
  DecimalConstructor,
  decodeDateTime,
  decodeSafeInteger,
} from './compiled-read-decode';

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

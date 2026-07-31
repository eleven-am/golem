export type CompiledReadScalarKind = 'plain' | 'bigint' | 'decimal' | 'datetime' | 'boolean';

export interface CompiledReadNestedField {
  readonly name: string;
  readonly kind: CompiledReadScalarKind;
}

export interface CompiledReadNestedProjection {
  readonly name: string;
  readonly list: boolean;
  readonly reversed?: boolean;
  readonly fields: readonly CompiledReadNestedField[];
  readonly relations: readonly CompiledReadNestedProjection[];
}

export type DecimalConstructor = new (value: string) => object;

const PRISMA_RUNTIME = '@prisma/client/runtime/client';

let cached: DecimalConstructor | null | undefined;

function loadDecimal(): DecimalConstructor | null {
  try {
    const runtime = require(PRISMA_RUNTIME) as { Decimal?: unknown };
    return typeof runtime.Decimal === 'function'
      ? (runtime.Decimal as DecimalConstructor)
      : null;
  } catch {
    return null;
  }
}

export function prismaDecimal(): DecimalConstructor | null {
  if (cached === undefined) {
    cached = loadDecimal();
  }
  return cached;
}

const ZONED = /(?:Z|z|[+-]\d{2}:?\d{2})$/;

export function decodeDateTime(value: unknown): unknown {
  if (value instanceof Date) {
    return value;
  }
  if (typeof value === 'number') {
    return new Date(value);
  }
  if (typeof value !== 'string') {
    return value;
  }
  const stamp = value.includes('T') ? value : value.replace(' ', 'T');
  return new Date(ZONED.test(stamp) ? stamp : `${stamp}Z`);
}

function decodeScalar(
  value: unknown,
  kind: CompiledReadScalarKind,
  decimal: DecimalConstructor | null,
): unknown {
  if (value === null || value === undefined) {
    return null;
  }
  switch (kind) {
    case 'plain':
      return value;
    case 'bigint':
      return typeof value === 'bigint' ? value : BigInt(String(value));
    case 'decimal':
      return new decimal!(String(value));
    case 'datetime':
      return decodeDateTime(value);
    case 'boolean':
      return typeof value === 'boolean' ? value : value === 1;
  }
}

function decodeEntry(
  entry: unknown,
  projection: CompiledReadNestedProjection,
  decimal: DecimalConstructor | null,
): unknown {
  if (!entry || typeof entry !== 'object') {
    return entry;
  }
  const row = entry as Record<string, unknown>;
  for (const field of projection.fields) {
    row[field.name] = decodeScalar(row[field.name], field.kind, decimal);
  }
  for (const relation of projection.relations) {
    row[relation.name] = decodeRelation(row[relation.name], relation, decimal);
  }
  return row;
}

function decodeRelation(
  value: unknown,
  projection: CompiledReadNestedProjection,
  decimal: DecimalConstructor | null,
): unknown {
  const parsed =
    value === null || value === undefined
      ? null
      : typeof value === 'string'
        ? (JSON.parse(value) as unknown)
        : value;
  if (projection.list) {
    if (!Array.isArray(parsed)) {
      return [];
    }
    const entries = parsed.map((entry) => decodeEntry(entry, projection, decimal));
    return projection.reversed === true ? entries.reverse() : entries;
  }
  return parsed === null ? null : decodeEntry(parsed, projection, decimal);
}

export function decodeCompiledRelations(
  rows: readonly Record<string, unknown>[],
  projections: readonly CompiledReadNestedProjection[],
  decimal: DecimalConstructor | null,
): readonly Record<string, unknown>[] {
  if (projections.length === 0) {
    return rows;
  }
  for (const row of rows) {
    for (const projection of projections) {
      row[projection.name] = decodeRelation(row[projection.name], projection, decimal);
    }
  }
  return rows;
}

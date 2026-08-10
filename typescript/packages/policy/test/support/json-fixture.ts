import {
  PolicyDatamodel,
  SqlDialect,
  SqlNode,
  SqlParameter,
  SqlScope,
  compileJsonFilter,
  evaluateConditions,
  renderConstraintSql,
  renderJsonFilter,
  renderSql,
  sqlIdentifier,
} from '../../src/index';

export const JSON_TABLE = 'JsonDoc';

export const JSON_ALIAS = 't0';

export interface JsonSeed {
  readonly id: number;
  readonly document: string | null;
}

export const JSON_ROWS: readonly JsonSeed[] = [
  { id: 1, document: '{"a":{"b":"hello"}}' },
  { id: 2, document: '{"a":{"b":null}}' },
  { id: 3, document: '{"a":[1,2,3]}' },
  { id: 4, document: '{"z":1}' },
  { id: 5, document: '[1,2,3]' },
  { id: 6, document: '{"a":10}' },
  { id: 7, document: '{"a":"x%y_z\\\\w"}' },
  { id: 8, document: 'null' },
  { id: 9, document: null },
  { id: 10, document: '{"a":9007199254740991}' },
  { id: 11, document: '{"a":0.5}' },
  { id: 12, document: '{"a":{"c":2,"b":1}}' },
  { id: 13, document: '{"a":[]}' },
  { id: 14, document: '{"a":[null,1]}' },
  { id: 15, document: '{"a":true}' },
  { id: 16, document: '{"a":""}' },
  { id: 17, document: '{"a":"ABC"}' },
  { id: 18, document: '{"a":[[1,2]]}' },
  { id: 19, document: '{"a":"hello world"}' },
  { id: 20, document: '{"a":{"b":{"c":1}}}' },
  { id: 21, document: '{"a":[{"k":1,"j":2}]}' },
  { id: 22, document: '{"a":"100%"}' },
  { id: 23, document: '{"a":"ÉCOLE"}' },
  { id: 24, document: '{"a":"tail𝄞"}' },
];

export const JSON_OWNER_TABLE = 'JsonOwner';

export const JSON_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'JsonDoc',
      dbName: JSON_TABLE,
      fields: [
        { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
        { name: 'payload', dbName: 'payload', kind: 'scalar', type: 'Json', isList: false },
        { name: 'ownerId', dbName: 'ownerId', kind: 'scalar', type: 'Int', isList: false },
        {
          name: 'owner',
          kind: 'object',
          type: 'JsonOwner',
          isList: false,
          relationName: 'OwnerToDoc',
          relationFromFields: ['ownerId'],
          relationToFields: ['id'],
        },
      ],
    },
    {
      name: 'JsonOwner',
      dbName: JSON_OWNER_TABLE,
      fields: [
        { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
        { name: 'label', dbName: 'label', kind: 'scalar', type: 'String', isList: false },
        { name: 'docs', kind: 'object', type: 'JsonDoc', isList: true, relationName: 'OwnerToDoc' },
      ],
    },
  ],
};

export interface JsonOwnerSeed {
  readonly id: number;
  readonly label: string;
}

export const JSON_OWNERS: readonly JsonOwnerSeed[] = [
  { id: 1, label: 'first' },
  { id: 2, label: 'second' },
  { id: 3, label: 'empty' },
];

export function jsonOwnerOf(documentId: number): number {
  return documentId <= 12 ? 1 : 2;
}

export interface JsonRow {
  readonly id: number;
  readonly value: unknown;
}

export function jsonRows(seeds: readonly JsonSeed[] = JSON_ROWS): readonly JsonRow[] {
  return seeds.map((seed) => ({
    id: seed.id,
    value: seed.document === null ? undefined : (JSON.parse(seed.document) as unknown),
  }));
}

export type JsonSegment = string | number;

export interface JsonCase {
  readonly id: string;
  readonly segments?: readonly JsonSegment[];
  readonly filter: Record<string, unknown>;
}

export function postgresPath(segments: readonly JsonSegment[]): readonly string[] {
  return segments.map((segment) => String(segment));
}

export function jsonPath(segments: readonly JsonSegment[]): string {
  return `$${segments
    .map((segment) => (typeof segment === 'number' ? `[${segment}]` : `."${segment}"`))
    .join('')}`;
}

export function filterFor(entry: JsonCase, style: 'array' | 'jsonpath'): Record<string, unknown> {
  if (entry.segments === undefined) {
    return entry.filter;
  }
  const path = style === 'array' ? postgresPath(entry.segments) : jsonPath(entry.segments);
  return { path, ...entry.filter };
}

const SCOPE: SqlScope = {
  column: (field) => sqlIdentifier([JSON_ALIAS, field]),
  relation: () => undefined,
};

export interface JsonStatement {
  readonly text: string;
  readonly parameters: readonly SqlParameter[];
}

export function renderJsonStatement(filter: unknown, dialect: SqlDialect): JsonStatement {
  const node: SqlNode = renderJsonFilter(filter, {
    field: 'payload',
    scope: SCOPE,
    path: ['payload'],
  });
  const fragment = renderSql(node, dialect);
  const quote = (identifier: string): string => dialect.quoteIdentifier(identifier);
  return {
    text:
      `SELECT ${quote(JSON_ALIAS)}.${quote('id')} AS ${quote('id')} ` +
      `FROM ${quote(JSON_TABLE)} AS ${quote(JSON_ALIAS)} WHERE ${fragment.text}`,
    parameters: fragment.parameters,
  };
}

export function evaluateJson(filter: unknown, rows: readonly JsonRow[]): readonly number[] {
  const predicate = compileJsonFilter(filter);
  return rows.filter((row) => predicate(row.value)).map((row) => row.id);
}

export function renderConditionStatement(filter: unknown, dialect: SqlDialect): JsonStatement {
  const constraint = renderConstraintSql(
    { payload: filter },
    { datamodel: JSON_DATAMODEL, model: 'JsonDoc', dialect, absent: 'deny-all', alias: JSON_ALIAS },
  );
  const quote = (identifier: string): string => dialect.quoteIdentifier(identifier);
  return {
    text:
      `SELECT ${quote(JSON_ALIAS)}.${quote('id')} AS ${quote('id')} ` +
      `FROM ${constraint.from} WHERE ${constraint.text}`,
    parameters: constraint.parameters,
  };
}

export function evaluateCondition(filter: unknown, rows: readonly JsonRow[]): readonly number[] {
  return rows
    .filter((row) =>
      evaluateConditions({ payload: filter }, { id: row.id, payload: row.value }, {
        datamodel: JSON_DATAMODEL,
        model: 'JsonDoc',
      }),
    )
    .map((row) => row.id);
}

export interface JsonOwnerRow {
  readonly id: number;
  readonly label: string;
  readonly docs: readonly { id: number; ownerId: number; payload: unknown }[];
}

export function jsonOwnerRows(seeds: readonly JsonSeed[] = JSON_ROWS): readonly JsonOwnerRow[] {
  return JSON_OWNERS.map((owner) => ({
    id: owner.id,
    label: owner.label,
    docs: jsonRows(seeds)
      .filter((row) => jsonOwnerOf(row.id) === owner.id)
      .map((row) => ({ id: row.id, ownerId: owner.id, payload: row.value })),
  }));
}

export function renderOwnerStatement(where: unknown, dialect: SqlDialect): JsonStatement {
  const constraint = renderConstraintSql(where, {
    datamodel: JSON_DATAMODEL,
    model: 'JsonOwner',
    dialect,
    absent: 'deny-all',
    alias: JSON_ALIAS,
  });
  const quote = (identifier: string): string => dialect.quoteIdentifier(identifier);
  return {
    text:
      `SELECT ${quote(JSON_ALIAS)}.${quote('id')} AS ${quote('id')} ` +
      `FROM ${constraint.from} WHERE ${constraint.text}`,
    parameters: constraint.parameters,
  };
}

export function evaluateOwners(where: unknown, rows: readonly JsonOwnerRow[]): readonly number[] {
  return rows
    .filter((row) => evaluateConditions(where, row, { datamodel: JSON_DATAMODEL, model: 'JsonOwner' }))
    .map((row) => row.id);
}

export interface JsonOwnerCase {
  readonly id: string;
  readonly segments?: readonly JsonSegment[];
  readonly quantifier: 'some' | 'every' | 'none';
  readonly filter: Record<string, unknown>;
  readonly also?: Record<string, unknown>;
}

export function ownerWhereFor(entry: JsonOwnerCase, style: 'array' | 'jsonpath'): Record<string, unknown> {
  const payload =
    entry.segments === undefined
      ? entry.filter
      : {
          path: style === 'array' ? postgresPath(entry.segments) : jsonPath(entry.segments),
          ...entry.filter,
        };
  const quantified = { docs: { [entry.quantifier]: { payload } } };
  return entry.also === undefined ? quantified : { AND: [entry.also, quantified] };
}

export const JSON_OWNER_CASES: readonly JsonOwnerCase[] = [
  { id: 'owner/some-number', segments: ['a'], quantifier: 'some', filter: { equals: 10 } },
  { id: 'owner/some-string-contains', segments: ['a'], quantifier: 'some', filter: { string_contains: 'ell' } },
  { id: 'owner/some-nested-string', segments: ['a', 'b'], quantifier: 'some', filter: { equals: 'hello' } },
  {
    id: 'owner/some-jsonnull-at-path',
    segments: ['a', 'b'],
    quantifier: 'some',
    filter: { equals: { $type: 'JsonNull' } },
  },
  {
    id: 'owner/none-jsonnull-at-path',
    segments: ['a', 'b'],
    quantifier: 'none',
    filter: { equals: { $type: 'JsonNull' } },
  },
  { id: 'owner/none-missing-path', segments: ['nope'], quantifier: 'none', filter: { equals: 1 } },
  { id: 'owner/every-missing-path', segments: ['nope'], quantifier: 'every', filter: { equals: 1 } },
  { id: 'owner/every-dbnull-at-path', segments: ['zz'], quantifier: 'every', filter: { equals: { $type: 'DbNull' } } },
  {
    id: 'owner/some-array-index',
    segments: ['a', 0],
    quantifier: 'some',
    filter: { equals: 1 },
  },
  {
    id: 'owner/label-and-some-wildcard',
    segments: ['a'],
    quantifier: 'some',
    filter: { string_contains: '%' },
    also: { label: 'first' },
  },
  {
    id: 'owner/label-and-some-astral',
    segments: ['a'],
    quantifier: 'some',
    filter: { string_ends_with: '\u{1d11e}' },
    also: { label: 'second' },
  },
];

export function renderIds(ids: readonly number[]): string {
  return ids.length === 0 ? '<none>' : [...ids].sort((left, right) => left - right).join(',');
}

export const JSON_CASES: readonly JsonCase[] = [
  { id: 'equals-nested-string', segments: ['a', 'b'], filter: { equals: 'hello' } },
  { id: 'equals-nested-object', segments: ['a'], filter: { equals: { b: 'hello' } } },
  { id: 'equals-nested-array', segments: ['a'], filter: { equals: [1, 2, 3] } },
  { id: 'equals-empty-array', segments: ['a'], filter: { equals: [] } },
  { id: 'equals-number', segments: ['a'], filter: { equals: 10 } },
  { id: 'equals-float', segments: ['a'], filter: { equals: 0.5 } },
  { id: 'equals-safe-integer', segments: ['a'], filter: { equals: 9007199254740991 } },
  { id: 'equals-boolean', segments: ['a'], filter: { equals: true } },
  { id: 'equals-empty-string', segments: ['a'], filter: { equals: '' } },
  { id: 'equals-wildcards', segments: ['a'], filter: { equals: 'x%y_z\\w' } },
  { id: 'equals-whole-document', filter: { equals: [1, 2, 3] } },
  { id: 'equals-deep-object', segments: ['a', 'b'], filter: { equals: { c: 1 } } },
  { id: 'equals-array-index', segments: ['a', 0], filter: { equals: 1 } },
  { id: 'equals-array-index-null', segments: ['a', 0], filter: { equals: { $type: 'JsonNull' } } },
  { id: 'equals-missing-path', segments: ['nope', 'nada'], filter: { equals: 1 } },

  { id: 'equals-dbnull-at-path', segments: ['a', 'b'], filter: { equals: { $type: 'DbNull' } } },
  { id: 'equals-jsonnull-at-path', segments: ['a', 'b'], filter: { equals: { $type: 'JsonNull' } } },
  { id: 'equals-anynull-at-path', segments: ['a', 'b'], filter: { equals: { $type: 'AnyNull' } } },
  { id: 'equals-dbnull-whole', filter: { equals: { $type: 'DbNull' } } },
  { id: 'equals-jsonnull-whole', filter: { equals: { $type: 'JsonNull' } } },
  { id: 'equals-anynull-whole', filter: { equals: { $type: 'AnyNull' } } },
  { id: 'equals-plain-null-at-path', segments: ['a', 'b'], filter: { equals: null } },

  { id: 'not-nested-string', segments: ['a', 'b'], filter: { not: 'hello' } },
  { id: 'not-number', segments: ['a'], filter: { not: 10 } },
  { id: 'not-dbnull-at-path', segments: ['a', 'b'], filter: { not: { $type: 'DbNull' } } },
  { id: 'not-jsonnull-at-path', segments: ['a', 'b'], filter: { not: { $type: 'JsonNull' } } },
  { id: 'not-anynull-at-path', segments: ['a', 'b'], filter: { not: { $type: 'AnyNull' } } },
  { id: 'not-jsonnull-whole', filter: { not: { $type: 'JsonNull' } } },

  { id: 'string-contains-plain', segments: ['a'], filter: { string_contains: 'ell' } },
  { id: 'string-contains-percent', segments: ['a'], filter: { string_contains: '%' } },
  { id: 'string-contains-underscore', segments: ['a'], filter: { string_contains: '_' } },
  { id: 'string-contains-backslash', segments: ['a'], filter: { string_contains: '\\' } },
  { id: 'string-contains-percent-tail', segments: ['a'], filter: { string_contains: '0%' } },
  { id: 'string-contains-empty', segments: ['a'], filter: { string_contains: '' } },
  { id: 'string-contains-space', segments: ['a'], filter: { string_contains: 'o w' } },
  { id: 'string-contains-case', segments: ['a'], filter: { string_contains: 'abc' } },
  { id: 'string-contains-case-exact', segments: ['a'], filter: { string_contains: 'ABC' } },
  { id: 'string-starts-with-wildcard', segments: ['a'], filter: { string_starts_with: 'x%' } },
  { id: 'string-starts-with-plain', segments: ['a'], filter: { string_starts_with: 'hello' } },
  { id: 'string-ends-with-backslash', segments: ['a'], filter: { string_ends_with: '\\w' } },
  { id: 'string-ends-with-percent', segments: ['a'], filter: { string_ends_with: '%' } },
  { id: 'string-ends-with-empty', segments: ['a'], filter: { string_ends_with: '' } },
  { id: 'string-ends-with-astral', segments: ['a'], filter: { string_ends_with: '\u{1d11e}' } },
  { id: 'string-contains-astral', segments: ['a'], filter: { string_contains: '\u{1d11e}' } },
  { id: 'string-contains-nested', segments: ['a', 'b'], filter: { string_contains: 'ell' } },
  { id: 'string-contains-whole', filter: { string_contains: 'ell' } },

  { id: 'array-starts-with-number', segments: ['a'], filter: { array_starts_with: 1 } },
  { id: 'array-starts-with-null', segments: ['a'], filter: { array_starts_with: null } },
  { id: 'array-starts-with-array', segments: ['a'], filter: { array_starts_with: [1, 2] } },
  { id: 'array-ends-with-number', segments: ['a'], filter: { array_ends_with: 3 } },
  { id: 'array-ends-with-one', segments: ['a'], filter: { array_ends_with: 1 } },
  { id: 'array-ends-with-whole', filter: { array_ends_with: 3 } },
];

export const POSTGRES_ONLY_JSON_CASES: readonly JsonCase[] = [
  { id: 'equals-reordered-object', segments: ['a'], filter: { equals: { b: 1, c: 2 } } },
  { id: 'array-contains-number', segments: ['a'], filter: { array_contains: 2 } },
  { id: 'array-contains-array', segments: ['a'], filter: { array_contains: [1, 2] } },
  { id: 'array-contains-nested-array', segments: ['a'], filter: { array_contains: [[1]] } },
  { id: 'array-contains-flat-in-nested', segments: ['a'], filter: { array_contains: [1] } },
  { id: 'array-contains-object-subset', segments: ['a'], filter: { array_contains: [{ k: 1 }] } },
  { id: 'array-contains-empty', segments: ['a'], filter: { array_contains: [] } },
  { id: 'array-contains-whole', filter: { array_contains: 2 } },
  { id: 'lt-number', segments: ['a'], filter: { lt: 20 } },
  { id: 'lte-number', segments: ['a'], filter: { lte: 10 } },
  { id: 'gt-number', segments: ['a'], filter: { gt: 0.5 } },
  { id: 'gte-number', segments: ['a'], filter: { gte: 10 } },
  { id: 'lt-string', segments: ['a'], filter: { lt: 'y' } },
  { id: 'gt-string-upper', segments: ['a'], filter: { gt: 'B' } },
  { id: 'gte-string', segments: ['a'], filter: { gte: 'ABC' } },
  { id: 'lt-and-gt', segments: ['a'], filter: { gt: 0, lt: 100 } },
  {
    id: 'insensitive-ascii-fold',
    segments: ['a'],
    filter: { string_contains: 'cole', mode: 'insensitive' },
  },
  {
    id: 'insensitive-leaves-non-ascii-alone',
    segments: ['a'],
    filter: { string_contains: 'école', mode: 'insensitive' },
  },
  {
    id: 'insensitive-starts-with',
    segments: ['a'],
    filter: { string_starts_with: 'HELLO', mode: 'insensitive' },
  },
];

export const PRECISION_ROWS: readonly JsonSeed[] = [
  { id: 1, document: '{"a":9007199254740992}' },
  { id: 2, document: '{"a":9007199254740993}' },
  { id: 3, document: '{"a":9007199254740991}' },
];

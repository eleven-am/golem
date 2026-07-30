export type SqlParameter = string | number | bigint | boolean | Date | null;

export type SqlNode =
  | { readonly kind: 'text'; readonly text: string }
  | { readonly kind: 'identifier'; readonly path: readonly string[] }
  | { readonly kind: 'parameter'; readonly value: SqlParameter }
  | { readonly kind: 'sequence'; readonly nodes: readonly SqlNode[] }
  | { readonly kind: 'dialectal'; readonly select: (dialect: SqlDialect) => SqlNode };

export interface SqlFragment {
  readonly text: string;
  readonly parameters: readonly SqlParameter[];
}

export type NullSafeEqualityStyle = 'is-not-distinct-from' | 'null-safe-equals' | 'is';

export interface SqlLikeSupport {
  readonly escape: string;
  readonly insensitive: string | null;
}

export type SqlJsonPathStyle = 'array' | 'jsonpath';

export type SqlJsonPathSource = readonly string[] | string | null;

export type SqlJsonType = 'null' | 'boolean' | 'number' | 'string' | 'array' | 'object';

export type SqlJsonPosition = 'first' | 'last';

export interface SqlJsonDialect {
  readonly pathStyle: SqlJsonPathStyle;
  readonly normalisesDocuments: boolean;
  readonly supportsContainment: boolean;
  readonly supportsOrdering: boolean;
  readonly typeNames: Readonly<Record<SqlJsonType, readonly string[]>>;
  at(column: SqlNode, path: SqlJsonPathSource): SqlNode;
  child(value: SqlNode, position: SqlJsonPosition): SqlNode;
  typeOf(value: SqlNode): SqlNode;
  text(value: SqlNode): SqlNode;
  document(json: string): SqlNode;
  containsDocument?(value: SqlNode, candidate: SqlNode): SqlNode;
}

export interface SqlDialect {
  readonly name: string;
  readonly nullSafeEquality: NullSafeEqualityStyle;
  readonly binaryCollation: string;
  readonly textType: string;
  readonly like: SqlLikeSupport | null;
  readonly json?: SqlJsonDialect | null;
  placeholder(position: number): string;
  quoteIdentifier(identifier: string): string;
}

export interface SqlRelationHop {
  readonly scope: SqlScope;
  readonly toMany: boolean;
  wrap(predicate: SqlNode): SqlNode;
}

export interface SqlScope {
  column(field: string): SqlNode;
  relation(field: string): SqlRelationHop | undefined;
  listColumn?(field: string): SqlNode;
}

export function sqlText(text: string): SqlNode {
  return { kind: 'text', text };
}

export function sqlIdentifier(path: readonly string[]): SqlNode {
  return { kind: 'identifier', path };
}

export function sqlParameter(value: SqlParameter): SqlNode {
  return { kind: 'parameter', value };
}

export function sqlSequence(nodes: readonly SqlNode[]): SqlNode {
  return { kind: 'sequence', nodes };
}

export function sqlDialectal(select: (dialect: SqlDialect) => SqlNode): SqlNode {
  return { kind: 'dialectal', select };
}

export function sqlJoin(nodes: readonly SqlNode[], separator: string): SqlNode {
  const joined: SqlNode[] = [];
  nodes.forEach((node, index) => {
    if (index > 0) {
      joined.push(sqlText(separator));
    }
    joined.push(node);
  });
  return sqlSequence(joined);
}

export function sqlGroup(node: SqlNode): SqlNode {
  return sqlSequence([sqlText('('), node, sqlText(')')]);
}

export const SQL_TRUE: SqlNode = sqlText('(1 = 1)');

export const SQL_FALSE: SqlNode = sqlText('(1 = 0)');

export function sqlNullSafeEquals(column: SqlNode, value: SqlNode): SqlNode {
  return sqlDialectal((dialect) => {
    if (dialect.nullSafeEquality === 'null-safe-equals') {
      return sqlSequence([column, sqlText(' <=> '), value]);
    }
    if (dialect.nullSafeEquality === 'is') {
      return sqlSequence([column, sqlText(' IS '), value]);
    }
    return sqlSequence([column, sqlText(' IS NOT DISTINCT FROM '), value]);
  });
}

export function sqlIsNotTrue(node: SqlNode): SqlNode {
  return sqlGroup(sqlSequence([sqlGroup(node), sqlText(' IS NOT TRUE')]));
}

export function sqlCastToText(node: SqlNode): SqlNode {
  return sqlDialectal((dialect) =>
    sqlSequence([sqlText('CAST('), node, sqlText(` AS ${dialect.textType})`)]),
  );
}

export function sqlBinaryCollate(node: SqlNode): SqlNode {
  return sqlDialectal((dialect) => sqlSequence([node, sqlText(` COLLATE ${dialect.binaryCollation}`)]));
}

export function sqlCall(name: string, args: readonly SqlNode[]): SqlNode {
  return sqlSequence([sqlText(`${name}(`), sqlJoin(args, ', '), sqlText(')')]);
}

export function sqlArrayCardinality(column: SqlNode): SqlNode {
  return sqlCall('cardinality', [column]);
}

export function sqlArrayElement(column: SqlNode, position: number): SqlNode {
  if (!Number.isInteger(position) || position < 1) {
    throw new RangeError(`array positions are 1-based integers, received ${String(position)}`);
  }
  return sqlSequence([column, sqlText(`[${position}]`)]);
}

export function sqlArrayContainsValue(column: SqlNode, value: SqlNode): SqlNode {
  return sqlSequence([sqlText('COALESCE('), value, sqlText(' = ANY('), column, sqlText('), FALSE)')]);
}

export function renderSql(node: SqlNode, dialect: SqlDialect): SqlFragment {
  const chunks: string[] = [];
  const parameters: SqlParameter[] = [];
  const walk = (current: SqlNode): void => {
    switch (current.kind) {
      case 'text':
        chunks.push(current.text);
        return;
      case 'identifier':
        chunks.push(current.path.map((part) => dialect.quoteIdentifier(part)).join('.'));
        return;
      case 'parameter':
        parameters.push(current.value);
        chunks.push(dialect.placeholder(parameters.length));
        return;
      case 'sequence':
        current.nodes.forEach(walk);
        return;
      case 'dialectal':
        walk(current.select(dialect));
        return;
    }
  };
  walk(node);
  return { text: chunks.join(''), parameters };
}

function postgresJsonPath(path: SqlJsonPathSource): SqlNode {
  const segments = path === null ? [] : (path as readonly string[]);
  return sqlSequence([
    sqlText('ARRAY['),
    sqlJoin(
      segments.map((segment) => sqlParameter(segment)),
      ', ',
    ),
    sqlText(']::text[]'),
  ]);
}

function jsonPathExpression(path: SqlJsonPathSource): SqlNode {
  return sqlParameter(path === null ? '$' : (path as string));
}

const postgresJsonDialect: SqlJsonDialect = {
  pathStyle: 'array',
  normalisesDocuments: true,
  supportsContainment: true,
  supportsOrdering: true,
  typeNames: {
    null: ['null'],
    boolean: ['boolean'],
    number: ['number'],
    string: ['string'],
    array: ['array'],
    object: ['object'],
  },
  at: (column, path) =>
    path === null || path.length === 0
      ? column
      : sqlGroup(sqlSequence([column, sqlText(' #> '), postgresJsonPath(path)])),
  child: (value, position) =>
    sqlGroup(sqlSequence([value, sqlText(position === 'first' ? ' -> 0' : ' -> -1')])),
  typeOf: (value) => sqlCall('jsonb_typeof', [value]),
  text: (value) => sqlGroup(sqlSequence([value, sqlText(' #>> ARRAY[]::text[]')])),
  document: (json) => sqlSequence([sqlParameter(json), sqlText('::jsonb')]),
  containsDocument: (value, candidate) => sqlGroup(sqlSequence([value, sqlText(' @> '), candidate])),
};

const mysqlJsonDialect: SqlJsonDialect = {
  pathStyle: 'jsonpath',
  normalisesDocuments: true,
  supportsContainment: true,
  supportsOrdering: true,
  typeNames: {
    null: ['NULL'],
    boolean: ['BOOLEAN'],
    number: ['INTEGER', 'DOUBLE', 'DECIMAL'],
    string: ['STRING'],
    array: ['ARRAY'],
    object: ['OBJECT'],
  },
  at: (column, path) => sqlCall('JSON_EXTRACT', [column, jsonPathExpression(path)]),
  child: (value, position) =>
    sqlCall('JSON_EXTRACT', [value, sqlParameter(position === 'first' ? '$[0]' : '$[last]')]),
  typeOf: (value) => sqlCall('JSON_TYPE', [value]),
  text: (value) => sqlCall('JSON_UNQUOTE', [value]),
  document: (json) => sqlSequence([sqlText('CAST('), sqlParameter(json), sqlText(' AS JSON)')]),
  containsDocument: (value, candidate) =>
    sqlGroup(sqlSequence([sqlCall('JSON_CONTAINS', [value, candidate]), sqlText(' = 1')])),
};

const sqliteJsonDialect: SqlJsonDialect = {
  pathStyle: 'jsonpath',
  normalisesDocuments: false,
  supportsContainment: false,
  supportsOrdering: false,
  typeNames: {
    null: ['null'],
    boolean: ['true', 'false'],
    number: ['integer', 'real'],
    string: ['text'],
    array: ['array'],
    object: ['object'],
  },
  at: (column, path) => sqlGroup(sqlSequence([column, sqlText(' -> '), jsonPathExpression(path)])),
  child: (value, position) =>
    sqlGroup(sqlSequence([value, sqlText(position === 'first' ? " -> '$[0]'" : " -> '$[#-1]'")])),
  typeOf: (value) => sqlCall('json_type', [value]),
  text: (value) => sqlGroup(sqlSequence([value, sqlText(" ->> '$'")])),
  document: (json) => sqlCall('json', [sqlParameter(json)]),
};

export const postgresDialect: SqlDialect = {
  name: 'postgres',
  nullSafeEquality: 'is-not-distinct-from',
  binaryCollation: '"C"',
  textType: 'TEXT',
  like: { escape: "'\\'", insensitive: 'ILIKE' },
  json: postgresJsonDialect,
  placeholder: (position) => `$${position}`,
  quoteIdentifier: (identifier) => `"${identifier.replace(/"/g, '""')}"`,
};

export const mysqlDialect: SqlDialect = {
  name: 'mysql',
  nullSafeEquality: 'null-safe-equals',
  binaryCollation: 'utf8mb4_bin',
  textType: 'CHAR',
  like: { escape: "'\\\\'", insensitive: null },
  json: mysqlJsonDialect,
  placeholder: () => '?',
  quoteIdentifier: (identifier) => `\`${identifier.replace(/`/g, '``')}\``,
};

export const sqliteDialect: SqlDialect = {
  name: 'sqlite',
  nullSafeEquality: 'is',
  binaryCollation: 'BINARY',
  textType: 'TEXT',
  like: null,
  json: sqliteJsonDialect,
  placeholder: () => '?',
  quoteIdentifier: (identifier) => `"${identifier.replace(/"/g, '""')}"`,
};

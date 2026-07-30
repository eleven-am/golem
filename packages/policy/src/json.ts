import { ConditionIssue, SqlRenderError } from './errors';
import { StringOperatorName, SqlFieldTarget, foldAscii, likeMatch, textMatches } from './operators';
import {
  SQL_TRUE,
  SqlDialect,
  SqlJsonDialect,
  SqlJsonPathSource,
  SqlJsonPosition,
  SqlJsonType,
  SqlNode,
  sqlBinaryCollate,
  sqlDialectal,
  sqlGroup,
  sqlJoin,
  sqlParameter,
  sqlSequence,
  sqlText,
} from './sql';
import { FieldPredicate } from './types';
import { describeValue, isPlainObject } from './values';

export type JsonOperatorName =
  | 'equals'
  | 'not'
  | 'lt'
  | 'lte'
  | 'gt'
  | 'gte'
  | 'string_contains'
  | 'string_starts_with'
  | 'string_ends_with'
  | 'array_contains'
  | 'array_starts_with'
  | 'array_ends_with';

export const JSON_PATH_KEY = 'path';

export const JSON_MODE_KEY = 'mode';

const JSON_OPERATOR_NAMES: readonly JsonOperatorName[] = [
  'equals',
  'not',
  'lt',
  'lte',
  'gt',
  'gte',
  'string_contains',
  'string_starts_with',
  'string_ends_with',
  'array_contains',
  'array_starts_with',
  'array_ends_with',
];

const JSON_STRING_OPERATORS: Readonly<Record<string, StringOperatorName>> = {
  string_contains: 'contains',
  string_starts_with: 'startsWith',
  string_ends_with: 'endsWith',
};

const JSON_ORDERING_SYMBOLS: Readonly<Record<string, string>> = {
  lt: '<',
  lte: '<=',
  gt: '>',
  gte: '>=',
};

export type JsonNullSentinel = 'DbNull' | 'JsonNull' | 'AnyNull';

const JSON_NULL_SENTINELS: readonly JsonNullSentinel[] = ['DbNull', 'JsonNull', 'AnyNull'];

export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };

export const JSON_ABSENT: unique symbol = Symbol('golem.json.absent');

export type JsonSlot = JsonValue | typeof JSON_ABSENT;

export interface JsonPathSegment {
  readonly text: string;
  readonly index: number | null;
}

interface JsonPath {
  readonly style: 'array' | 'jsonpath';
  readonly segments: readonly JsonPathSegment[];
  readonly source: SqlJsonPathSource;
}

const WHOLE_DOCUMENT: JsonPath = { style: 'array', segments: [], source: null };

function issue(
  operator: string,
  path: readonly string[],
  message: string,
  reason: ConditionIssue['reason'],
): ConditionIssue {
  return { reason, operator, path, message };
}

export function isJsonOperatorName(key: string): key is JsonOperatorName {
  return (JSON_OPERATOR_NAMES as readonly string[]).includes(key);
}

export function isJsonFilterKey(key: string): boolean {
  return key === JSON_PATH_KEY || key === JSON_MODE_KEY || isJsonOperatorName(key);
}

export function readNullSentinel(operand: unknown): JsonNullSentinel | null {
  if (typeof operand === 'object' && operand !== null && !Array.isArray(operand)) {
    const named = (operand as { $type?: unknown }).$type;
    if (typeof named === 'string' && (JSON_NULL_SENTINELS as readonly string[]).includes(named)) {
      return named as JsonNullSentinel;
    }
    const constructorName = (operand as { constructor?: { name?: string } }).constructor?.name;
    if (
      constructorName !== undefined &&
      (JSON_NULL_SENTINELS as readonly string[]).includes(constructorName) &&
      String(operand) === `Prisma.${constructorName}`
    ) {
      return constructorName as JsonNullSentinel;
    }
  }
  return null;
}

function jsonTypeOf(value: JsonValue): SqlJsonType {
  if (value === null) {
    return 'null';
  }
  if (Array.isArray(value)) {
    return 'array';
  }
  switch (typeof value) {
    case 'boolean':
      return 'boolean';
    case 'number':
      return 'number';
    case 'string':
      return 'string';
    default:
      return 'object';
  }
}

function isJsonScalar(value: JsonValue): boolean {
  const type = jsonTypeOf(value);
  return type !== 'array' && type !== 'object';
}

function describeJsonProblem(value: unknown): string | null {
  if (value === null) {
    return null;
  }
  if (typeof value === 'boolean' || typeof value === 'string') {
    return null;
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      return `the number ${String(value)} has no JSON representation`;
    }
    if (Math.abs(value) > Number.MAX_SAFE_INTEGER) {
      return `the number ${String(value)} is beyond ${Number.MAX_SAFE_INTEGER}, where a JSON document and a JavaScript number stop agreeing: the database compares the digits it stored, and the evaluator compares the nearest double, so 9007199254740992 and 9007199254740993 are two documents on the server and one value in memory. Compare against a value inside the safe integer range, or store the number as a JSON string`;
    }
    return null;
  }
  if (Array.isArray(value)) {
    for (const element of value) {
      const problem = describeJsonProblem(element);
      if (problem !== null) {
        return problem;
      }
    }
    return null;
  }
  if (isPlainObject(value)) {
    for (const entry of Object.values(value)) {
      const problem = describeJsonProblem(entry);
      if (problem !== null) {
        return problem;
      }
    }
    return null;
  }
  return `a value of type ${describeValue(value)} has no JSON representation`;
}

function containsStructure(value: JsonValue): boolean {
  if (Array.isArray(value)) {
    return true;
  }
  return jsonTypeOf(value) === 'object';
}

export function jsonEquals(left: JsonValue, right: JsonValue): boolean {
  const type = jsonTypeOf(left);
  if (type !== jsonTypeOf(right)) {
    return false;
  }
  if (type === 'array') {
    const leftList = left as readonly JsonValue[];
    const rightList = right as readonly JsonValue[];
    return (
      leftList.length === rightList.length &&
      leftList.every((element, index) => jsonEquals(element, rightList[index]!))
    );
  }
  if (type === 'object') {
    const leftObject = left as Record<string, JsonValue>;
    const rightObject = right as Record<string, JsonValue>;
    const leftKeys = Object.keys(leftObject);
    return (
      leftKeys.length === Object.keys(rightObject).length &&
      leftKeys.every(
        (key) =>
          Object.prototype.hasOwnProperty.call(rightObject, key) &&
          jsonEquals(leftObject[key]!, rightObject[key]!),
      )
    );
  }
  return left === right;
}

function containsDeep(target: JsonValue, candidate: JsonValue): boolean {
  if (Array.isArray(target) && Array.isArray(candidate)) {
    return candidate.every((element) => target.some((entry) => containsDeep(entry, element)));
  }
  if (jsonTypeOf(target) === 'object' && jsonTypeOf(candidate) === 'object') {
    const targetObject = target as Record<string, JsonValue>;
    const candidateObject = candidate as Record<string, JsonValue>;
    return Object.keys(candidateObject).every(
      (key) =>
        Object.prototype.hasOwnProperty.call(targetObject, key) &&
        containsDeep(targetObject[key]!, candidateObject[key]!),
    );
  }
  return jsonEquals(target, candidate);
}

export function jsonContains(target: JsonValue, candidate: JsonValue): boolean {
  if (Array.isArray(target) && !Array.isArray(candidate) && isJsonScalar(candidate)) {
    return target.some((element) => containsDeep(element, candidate));
  }
  return containsDeep(target, candidate);
}

function parseJsonPathText(text: string): readonly JsonPathSegment[] | null {
  if (!text.startsWith('$')) {
    return null;
  }
  const segments: JsonPathSegment[] = [];
  let cursor = 1;
  while (cursor < text.length) {
    const character = text[cursor];
    if (character === '.') {
      if (text[cursor + 1] === '"') {
        const close = text.indexOf('"', cursor + 2);
        if (close === -1) {
          return null;
        }
        segments.push({ text: text.slice(cursor + 2, close), index: null });
        cursor = close + 1;
        continue;
      }
      const start = cursor + 1;
      let end = start;
      while (end < text.length && text[end] !== '.' && text[end] !== '[') {
        end += 1;
      }
      if (end === start) {
        return null;
      }
      segments.push({ text: text.slice(start, end), index: null });
      cursor = end;
      continue;
    }
    if (character !== '[') {
      return null;
    }
    const close = text.indexOf(']', cursor);
    if (close === -1) {
      return null;
    }
    const inner = text.slice(cursor + 1, close);
    if (!/^\d+$/.test(inner)) {
      return null;
    }
    segments.push({ text: inner, index: Number(inner) });
    cursor = close + 1;
  }
  return segments;
}

function segmentsFromList(list: readonly string[]): readonly JsonPathSegment[] {
  return list.map((entry) => ({ text: entry, index: /^\d+$/.test(entry) ? Number(entry) : null }));
}

function readPath(filter: Record<string, unknown>): JsonPath | string {
  if (!Object.prototype.hasOwnProperty.call(filter, JSON_PATH_KEY)) {
    return WHOLE_DOCUMENT;
  }
  const source = filter[JSON_PATH_KEY];
  if (Array.isArray(source)) {
    if (!source.every((entry) => typeof entry === 'string')) {
      return `"path" given as an array addresses a document with string segments, and this one carries ${describeValue(source.find((entry) => typeof entry !== 'string'))}; Prisma's Postgres client types "path" as string[]`;
    }
    return { style: 'array', segments: segmentsFromList(source as string[]), source: source as string[] };
  }
  if (typeof source === 'string') {
    const segments = parseJsonPathText(source);
    if (segments === null) {
      return `"path" given as a string is a JSONPath rooted at "$", built from ".name", '."quoted name"' and "[0]" steps, and golem will not guess at ${JSON.stringify(source)}`;
    }
    return { style: 'jsonpath', segments, source };
  }
  return `"path" is an array of segments on Postgres and a JSONPath string on MySQL and SQLite, received ${describeValue(source)}`;
}

export function navigateJson(value: unknown, segments: readonly JsonPathSegment[]): JsonSlot {
  let current: JsonSlot = value === undefined ? JSON_ABSENT : (value as JsonValue);
  for (const segment of segments) {
    if (current === JSON_ABSENT || current === null) {
      return JSON_ABSENT;
    }
    if (Array.isArray(current)) {
      if (segment.index === null || segment.index >= current.length) {
        return JSON_ABSENT;
      }
      current = current[segment.index]!;
      continue;
    }
    if (jsonTypeOf(current) !== 'object') {
      return JSON_ABSENT;
    }
    const record = current as Record<string, JsonValue>;
    if (!Object.prototype.hasOwnProperty.call(record, segment.text)) {
      return JSON_ABSENT;
    }
    current = record[segment.text]!;
  }
  return current;
}

function elementOf(value: JsonSlot, position: SqlJsonPosition): JsonSlot {
  if (value === JSON_ABSENT || !Array.isArray(value) || value.length === 0) {
    return JSON_ABSENT;
  }
  return position === 'first' ? value[0]! : value[value.length - 1]!;
}

function refuse(message: string, path: readonly string[]): never {
  throw new SqlRenderError(message, path);
}

function jsonDialect(dialect: SqlDialect, operator: string, path: readonly string[]): SqlJsonDialect {
  if (dialect.json == null) {
    refuse(
      `operator "${operator}" filters a Json column, and the ${dialect.name} dialect declares no Json support golem can render`,
      path,
    );
  }
  return dialect.json;
}

function checkPathStyle(
  json: SqlJsonDialect,
  dialect: SqlDialect,
  jsonPath: JsonPath,
  operator: string,
  path: readonly string[],
): void {
  if (jsonPath.source === null || json.pathStyle === jsonPath.style) {
    return;
  }
  const wanted = json.pathStyle === 'array' ? 'an array of segments' : 'a JSONPath string such as "$.a.b"';
  const given = jsonPath.style === 'array' ? 'an array of segments' : 'a JSONPath string';
  refuse(
    `operator "${operator}" was given "path" as ${given}, and ${dialect.name} takes ${wanted}; Prisma's ${dialect.name} client rejects the other form too, so golem will not translate between them`,
    path,
  );
}

function sqlPresent(value: SqlNode): SqlNode {
  return sqlSequence([value, sqlText(' IS NOT NULL')]);
}

function sqlAbsent(value: SqlNode): SqlNode {
  return sqlGroup(sqlSequence([value, sqlText(' IS NULL')]));
}

function sqlTypeIs(json: SqlJsonDialect, value: SqlNode, type: SqlJsonType): SqlNode {
  const names = json.typeNames[type];
  const observed = json.typeOf(value);
  return sqlGroup(
    sqlSequence([
      sqlPresent(observed),
      sqlText(' AND '),
      observed,
      sqlText(' IN ('),
      sqlJoin(
        names.map((name) => sqlParameter(name)),
        ', ',
      ),
      sqlText(')'),
    ]),
  );
}

function scalarLiteral(value: JsonValue): SqlNode {
  if (typeof value === 'boolean') {
    return sqlParameter(value ? 1 : 0);
  }
  if (typeof value === 'string' || typeof value === 'number') {
    return sqlParameter(value);
  }
  return sqlParameter(null);
}

function sqlScalarEquals(json: SqlJsonDialect, value: SqlNode, operand: JsonValue): SqlNode {
  const type = jsonTypeOf(operand);
  if (type === 'null') {
    return sqlTypeIs(json, value, 'null');
  }
  const subject = type === 'string' ? sqlBinaryCollate(json.text(value)) : json.text(value);
  return sqlGroup(
    sqlSequence([
      sqlTypeIs(json, value, type),
      sqlText(' AND '),
      subject,
      sqlText(' = '),
      scalarLiteral(operand),
    ]),
  );
}

function sqlDocumentEquals(
  json: SqlJsonDialect,
  dialect: SqlDialect,
  value: SqlNode,
  operand: JsonValue,
  operator: string,
  path: readonly string[],
): SqlNode {
  if (json.normalisesDocuments) {
    return sqlGroup(
      sqlSequence([sqlPresent(value), sqlText(' AND '), value, sqlText(' = '), json.document(JSON.stringify(operand))]),
    );
  }
  if (containsStructure(operand)) {
    refuse(
      `operator "${operator}" was given a JSON ${jsonTypeOf(operand)} to compare, and ${dialect.name} compares JSON documents as text: object key order and number formatting would decide the answer, so a row storing {"b":1,"a":2} would not equal the operand {"a":2,"b":1} that the evaluator calls equal. Compare a scalar at a "path" instead`,
      path,
    );
  }
  return sqlScalarEquals(json, value, operand);
}

interface JsonRenderContext {
  readonly json: SqlJsonDialect;
  readonly dialect: SqlDialect;
  readonly value: SqlNode;
  readonly path: readonly string[];
}

function renderEquals(context: JsonRenderContext, operand: unknown, negated: boolean): SqlNode {
  const operator = negated ? 'not' : 'equals';
  const sentinel = readNullSentinel(operand);
  if (sentinel !== null) {
    const absent = sqlAbsent(context.value);
    const jsonNull = sqlTypeIs(context.json, context.value, 'null');
    if (sentinel === 'DbNull') {
      return negated ? sqlGroup(sqlPresent(context.value)) : absent;
    }
    const present = sqlPresent(context.value);
    if (sentinel === 'JsonNull') {
      return negated
        ? sqlGroup(sqlSequence([present, sqlText(' AND NOT '), jsonNull]))
        : sqlGroup(sqlSequence([present, sqlText(' AND '), jsonNull]));
    }
    return negated
      ? sqlGroup(sqlSequence([present, sqlText(' AND NOT '), jsonNull]))
      : sqlGroup(sqlSequence([absent, sqlText(' OR '), jsonNull]));
  }
  const matched = sqlDocumentEquals(
    context.json,
    context.dialect,
    context.value,
    operand as JsonValue,
    operator,
    context.path,
  );
  if (!negated) {
    return matched;
  }
  return sqlGroup(sqlSequence([sqlPresent(context.value), sqlText(' AND NOT '), matched]));
}

function renderOrdering(context: JsonRenderContext, operator: string, operand: JsonValue): SqlNode {
  if (!context.json.supportsOrdering) {
    refuse(
      `operator "${operator}" is not available on ${context.dialect.name}: Prisma's ${context.dialect.name} Json filter offers no ordering comparison, and golem will not invent one`,
      context.path,
    );
  }
  const symbol = JSON_ORDERING_SYMBOLS[operator]!;
  const type = jsonTypeOf(operand);
  const guard = sqlTypeIs(context.json, context.value, type);
  if (type === 'string') {
    return sqlGroup(
      sqlSequence([
        guard,
        sqlText(' AND '),
        sqlBinaryCollate(context.json.text(context.value)),
        sqlText(` ${symbol} `),
        sqlParameter(operand as string),
      ]),
    );
  }
  return sqlGroup(
    sqlSequence([
      guard,
      sqlText(' AND '),
      context.value,
      sqlText(` ${symbol} `),
      context.json.document(JSON.stringify(operand)),
    ]),
  );
}

function renderStringMatch(
  context: JsonRenderContext,
  operator: string,
  operand: string,
  insensitive: boolean,
): SqlNode {
  if (insensitive && context.dialect.like?.insensitive == null) {
    refuse(
      `operator "${operator}" was given mode "insensitive", and ${context.dialect.name} offers no case-insensitive match golem can force onto byte order; the evaluator folds ASCII case only, and pairing it with a SQL fold that disagrees outside ASCII would answer one way in memory and another in the database`,
      context.path,
    );
  }
  const guard = sqlTypeIs(context.json, context.value, 'string');
  if (operand.length === 0) {
    return guard;
  }
  const subject = sqlBinaryCollate(context.json.text(context.value));
  return sqlGroup(
    sqlSequence([
      guard,
      sqlText(' AND '),
      likeMatch(
        JSON_STRING_OPERATORS[operator]!,
        context.dialect,
        subject,
        operand,
        insensitive,
        context.path,
      ),
    ]),
  );
}

function renderArrayContains(context: JsonRenderContext, operand: JsonValue): SqlNode {
  if (!context.json.supportsContainment || context.json.containsDocument === undefined) {
    refuse(
      `operator "array_contains" is not available on ${context.dialect.name}: Prisma's ${context.dialect.name} Json filter offers no containment test, and golem will not emulate one row by row`,
      context.path,
    );
  }
  return sqlGroup(
    sqlSequence([
      sqlTypeIs(context.json, context.value, 'array'),
      sqlText(' AND '),
      context.json.containsDocument(context.value, context.json.document(JSON.stringify(operand))),
    ]),
  );
}

function renderArrayEdge(
  context: JsonRenderContext,
  operator: string,
  operand: JsonValue,
  position: SqlJsonPosition,
): SqlNode {
  const element = context.json.child(context.value, position);
  return sqlGroup(
    sqlSequence([
      sqlTypeIs(context.json, context.value, 'array'),
      sqlText(' AND '),
      sqlDocumentEquals(context.json, context.dialect, element, operand, operator, context.path),
    ]),
  );
}

function renderOperator(
  context: JsonRenderContext,
  operator: string,
  operand: unknown,
  insensitive: boolean,
): SqlNode {
  if (operator === 'equals' || operator === 'not') {
    return renderEquals(context, operand, operator === 'not');
  }
  if (Object.prototype.hasOwnProperty.call(JSON_ORDERING_SYMBOLS, operator)) {
    return renderOrdering(context, operator, operand as JsonValue);
  }
  if (Object.prototype.hasOwnProperty.call(JSON_STRING_OPERATORS, operator)) {
    return renderStringMatch(context, operator, operand as string, insensitive);
  }
  if (operator === 'array_contains') {
    return renderArrayContains(context, operand as JsonValue);
  }
  return renderArrayEdge(
    context,
    operator,
    operand as JsonValue,
    operator === 'array_starts_with' ? 'first' : 'last',
  );
}

function compileEquals(operand: unknown, negated: boolean): (slot: JsonSlot) => boolean {
  const sentinel = readNullSentinel(operand);
  if (sentinel !== null) {
    if (sentinel === 'DbNull') {
      return (slot) => (negated ? slot !== JSON_ABSENT : slot === JSON_ABSENT);
    }
    if (sentinel === 'JsonNull') {
      return (slot) => (negated ? slot !== JSON_ABSENT && slot !== null : slot === null);
    }
    return (slot) =>
      negated ? slot !== JSON_ABSENT && slot !== null : slot === JSON_ABSENT || slot === null;
  }
  const expected = operand as JsonValue;
  return (slot) => {
    if (slot === JSON_ABSENT) {
      return false;
    }
    const matched = jsonEquals(slot, expected);
    return negated ? !matched : matched;
  };
}

function compileOrdering(operator: string, operand: JsonValue): (slot: JsonSlot) => boolean {
  const type = jsonTypeOf(operand);
  const accept = (comparison: number): boolean => {
    switch (operator) {
      case 'lt':
        return comparison < 0;
      case 'lte':
        return comparison <= 0;
      case 'gt':
        return comparison > 0;
      default:
        return comparison >= 0;
    }
  };
  return (slot) => {
    if (slot === JSON_ABSENT || jsonTypeOf(slot) !== type) {
      return false;
    }
    if (type === 'number') {
      const left = slot as number;
      const right = operand as number;
      return accept(left < right ? -1 : left > right ? 1 : 0);
    }
    const left = slot as string;
    const right = operand as string;
    return accept(left < right ? -1 : left > right ? 1 : 0);
  };
}

function compileStringMatch(
  operator: string,
  operand: string,
  insensitive: boolean,
): (slot: JsonSlot) => boolean {
  const name = JSON_STRING_OPERATORS[operator]!;
  const needle = insensitive ? foldAscii(operand) : operand;
  return (slot) =>
    typeof slot === 'string' && textMatches(name, insensitive ? foldAscii(slot) : slot, needle);
}

function compileOperator(
  operator: string,
  operand: unknown,
  insensitive: boolean,
): (slot: JsonSlot) => boolean {
  if (operator === 'equals' || operator === 'not') {
    return compileEquals(operand, operator === 'not');
  }
  if (Object.prototype.hasOwnProperty.call(JSON_ORDERING_SYMBOLS, operator)) {
    return compileOrdering(operator, operand as JsonValue);
  }
  if (Object.prototype.hasOwnProperty.call(JSON_STRING_OPERATORS, operator)) {
    return compileStringMatch(operator, operand as string, insensitive);
  }
  if (operator === 'array_contains') {
    const candidate = operand as JsonValue;
    return (slot) => slot !== JSON_ABSENT && Array.isArray(slot) && jsonContains(slot, candidate);
  }
  const position: SqlJsonPosition = operator === 'array_starts_with' ? 'first' : 'last';
  const expected = operand as JsonValue;
  return (slot) => {
    if (slot === JSON_ABSENT || !Array.isArray(slot)) {
      return false;
    }
    const element = elementOf(slot, position);
    return element !== JSON_ABSENT && jsonEquals(element, expected);
  };
}

function readInsensitive(filter: Record<string, unknown>): boolean {
  return filter[JSON_MODE_KEY] === 'insensitive';
}

function operatorEntries(filter: Record<string, unknown>): readonly (readonly [string, unknown])[] {
  return Object.entries(filter).filter(([key]) => key !== JSON_PATH_KEY && key !== JSON_MODE_KEY);
}

export function validateJsonFilter(
  field: string,
  filter: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  if (!isPlainObject(filter)) {
    issues.push(
      issue(
        'equals',
        path,
        `the filter on Json field "${field}" is a filter object such as { equals: … } or { path: …, string_contains: … }, received ${describeValue(filter)}; Prisma's JsonFilter has no bare-value shorthand`,
        'unsupported-shape',
      ),
    );
    return;
  }
  const keys = Object.keys(filter);
  const unknownKeys = keys.filter((key) => !isJsonFilterKey(key));
  if (unknownKeys.length > 0) {
    issues.push(
      issue(
        unknownKeys[0]!,
        path,
        `operator "${unknownKeys[0]!}" is not part of Prisma's Json filter; the supported keys are ${JSON_PATH_KEY}, ${JSON_MODE_KEY}, ${JSON_OPERATOR_NAMES.join(', ')}`,
        'unsupported-operator',
      ),
    );
  }
  const parsed = readPath(filter);
  if (typeof parsed === 'string') {
    issues.push(issue(JSON_PATH_KEY, path, parsed, 'unsupported-value'));
  }
  if (Object.prototype.hasOwnProperty.call(filter, JSON_MODE_KEY)) {
    const mode = filter[JSON_MODE_KEY];
    if (mode !== 'default' && mode !== 'insensitive') {
      issues.push(
        issue(
          JSON_MODE_KEY,
          path,
          `"mode" accepts only "default" or "insensitive", received ${describeValue(mode)}`,
          'unsupported-value',
        ),
      );
    } else if (mode === 'insensitive') {
      const unfolded = keys.filter(
        (key) =>
          key !== JSON_MODE_KEY &&
          key !== JSON_PATH_KEY &&
          !Object.prototype.hasOwnProperty.call(JSON_STRING_OPERATORS, key),
      );
      if (unfolded.length > 0) {
        issues.push(
          issue(
            JSON_MODE_KEY,
            path,
            `mode "insensitive" folds case for ${Object.keys(JSON_STRING_OPERATORS).join(', ')} only, and this filter also carries ${unfolded.join(', ')}; golem refuses rather than comparing ${unfolded.join(', ')} case-sensitively under a filter that asks for case-insensitive matching`,
            'unsupported-shape',
          ),
        );
      }
    }
  }
  for (const [key, operand] of operatorEntries(filter)) {
    if (!isJsonOperatorName(key)) {
      continue;
    }
    if (Object.prototype.hasOwnProperty.call(JSON_STRING_OPERATORS, key)) {
      if (typeof operand !== 'string') {
        issues.push(
          issue(
            key,
            path,
            `operator "${key}" compares a JSON string against a string, received ${describeValue(operand)}`,
            'unsupported-value',
          ),
        );
      }
      continue;
    }
    if ((key === 'equals' || key === 'not') && readNullSentinel(operand) !== null) {
      continue;
    }
    const problem = describeJsonProblem(operand);
    if (problem !== null) {
      issues.push(
        issue(
          key,
          path,
          `operator "${key}" compares against a JSON value, and ${problem}`,
          'unsupported-value',
        ),
      );
      continue;
    }
    if (Object.prototype.hasOwnProperty.call(JSON_ORDERING_SYMBOLS, key)) {
      const type = jsonTypeOf(operand as JsonValue);
      if (type !== 'number' && type !== 'string') {
        issues.push(
          issue(
            key,
            path,
            `operator "${key}" orders a JSON number against a number or a JSON string against a string, and golem will not order a JSON ${type}: SQL orders documents by a rule the evaluator cannot reproduce`,
            'unsupported-value',
          ),
        );
      }
    }
  }
}

export function compileJsonFilter(filter: unknown): FieldPredicate {
  if (!isPlainObject(filter)) {
    return () => false;
  }
  const parsed = readPath(filter);
  if (typeof parsed === 'string') {
    return () => false;
  }
  const insensitive = readInsensitive(filter);
  const predicates = operatorEntries(filter)
    .filter(([key]) => isJsonOperatorName(key))
    .map(([key, operand]) => compileOperator(key, operand, insensitive));
  if (predicates.length === 0) {
    return () => true;
  }
  return (value) => {
    const slot = navigateJson(value, parsed.segments);
    return predicates.every((predicate) => predicate(slot));
  };
}

export function renderJsonFilter(filter: unknown, target: SqlFieldTarget): SqlNode {
  if (!isPlainObject(filter)) {
    refuse(`the filter on Json field "${target.field}" is not a filter object`, target.path);
  }
  const parsed = readPath(filter);
  if (typeof parsed === 'string') {
    refuse(parsed, target.path);
  }
  const insensitive = readInsensitive(filter);
  const entries = operatorEntries(filter).filter(([key]) => isJsonOperatorName(key));
  if (entries.length === 0) {
    return SQL_TRUE;
  }
  const column = target.scope.column(target.field);
  return sqlDialectal((dialect) => {
    const json = jsonDialect(dialect, entries[0]![0], target.path);
    checkPathStyle(json, dialect, parsed, entries[0]![0], target.path);
    const context: JsonRenderContext = {
      json,
      dialect,
      value: json.at(column, parsed.source),
      path: target.path,
    };
    const nodes = entries.map(([key, operand]) => renderOperator(context, key, operand, insensitive));
    return nodes.length === 1 ? nodes[0]! : sqlGroup(sqlJoin(nodes, ' AND '));
  });
}

export function isJsonField(type: string): boolean {
  return type === 'Json';
}

export const JSON_FILTER_KEYS: readonly string[] = [JSON_PATH_KEY, JSON_MODE_KEY, ...JSON_OPERATOR_NAMES];

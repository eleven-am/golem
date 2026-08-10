import { ConditionIssue, SqlRenderError } from './errors';
import {
  SQL_FALSE,
  SQL_TRUE,
  SqlDialect,
  SqlNode,
  SqlParameter,
  SqlScope,
  sqlArrayCardinality,
  sqlArrayContainsValue,
  sqlArrayElement,
  sqlBinaryCollate,
  sqlCall,
  sqlDialectal,
  sqlGroup,
  sqlIsNotTrue,
  sqlJoin,
  sqlNullSafeEquals,
  sqlParameter,
  sqlSequence,
  sqlText,
} from './sql';
import { FieldPredicate, ObjectPredicate } from './types';
import {
  ScalarKind,
  compareValues,
  describeValue,
  isDecimalLike,
  isPlainObject,
  valueKind,
  valuesEqual,
} from './values';

export type OperatorName =
  | 'equals'
  | 'not'
  | 'in'
  | 'notIn'
  | 'lt'
  | 'lte'
  | 'gt'
  | 'gte'
  | 'contains'
  | 'startsWith'
  | 'endsWith'
  | 'is'
  | 'isNot'
  | 'some'
  | 'every'
  | 'none';

export type ScalarListOperatorName = 'has' | 'hasEvery' | 'hasSome' | 'isEmpty' | 'equals';

export type AnyOperatorName = OperatorName | ScalarListOperatorName;

export type CombinatorName = 'AND' | 'OR' | 'NOT';

export type OperandShape = 'scalar' | 'scalar-list' | 'conditions';

export type NullValueBehaviour =
  | 'never-matches'
  | 'always-matches'
  | 'matches-when-operand-null'
  | 'matches-when-operand-not-null';

export type NullOperandBehaviour = 'allowed' | 'rejected' | 'never-matches';

export interface OperatorNullSemantics {
  readonly nullValue: NullValueBehaviour;
  readonly nullOperand: NullOperandBehaviour;
  readonly sqlIsTwoValued: boolean;
}

export interface OperatorHooks {
  validateConditions(conditions: unknown, path: readonly string[], issues: ConditionIssue[]): void;
  compileConditions(conditions: unknown): ObjectPredicate;
  renderConditions(conditions: unknown, scope: SqlScope, path: readonly string[]): SqlNode;
}

export interface SqlFieldTarget {
  readonly field: string;
  readonly scope: SqlScope;
  readonly path: readonly string[];
}

export interface OperatorDefinition {
  readonly name: AnyOperatorName;
  readonly operand: OperandShape;
  readonly acceptedValueKinds: readonly ScalarKind[];
  readonly nullSemantics: OperatorNullSemantics;
  validate(operand: unknown, path: readonly string[], issues: ConditionIssue[], hooks: OperatorHooks): void;
  compile(operand: unknown, hooks: OperatorHooks): FieldPredicate;
  renderSql(operand: unknown, target: SqlFieldTarget, hooks: OperatorHooks): SqlNode;
}

export interface CombinatorDefinition {
  readonly name: CombinatorName;
  validate(operand: unknown, path: readonly string[], issues: ConditionIssue[], hooks: OperatorHooks): void;
  compile(operand: unknown, hooks: OperatorHooks): ObjectPredicate;
  renderSql(operand: unknown, scope: SqlScope, path: readonly string[], hooks: OperatorHooks): SqlNode;
}

const SCALAR_KINDS: readonly ScalarKind[] = ['null', 'numeric', 'string', 'boolean', 'date'];

const ORDERED_KINDS: readonly ScalarKind[] = ['null', 'numeric', 'string', 'date'];

const TEXT_KINDS: readonly ScalarKind[] = ['null', 'string'];

const LIST_ELEMENT_KINDS: readonly ScalarKind[] = ['numeric', 'string', 'boolean', 'date'];

function isRelationObject(value: unknown): boolean {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    !(value instanceof Date) &&
    !isDecimalLike(value)
  );
}

function issue(
  operator: string,
  path: readonly string[],
  message: string,
  reason: ConditionIssue['reason'],
): ConditionIssue {
  return { reason, operator, path, message };
}

function validateScalarOperand(
  operator: AnyOperatorName,
  operand: unknown,
  accepted: readonly ScalarKind[],
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  const kind = valueKind(operand);
  if (kind === 'unsupported' || !accepted.includes(kind)) {
    issues.push(
      issue(
        operator,
        path,
        `operator "${operator}" does not accept an operand of type ${describeValue(operand)}; accepted: ${accepted.join(', ')}`,
        'unsupported-value',
      ),
    );
  }
}

function validateListOperand(
  operator: OperatorName,
  operand: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  if (!Array.isArray(operand)) {
    issues.push(
      issue(
        operator,
        path,
        `operator "${operator}" requires an array operand, received ${describeValue(operand)}`,
        'unsupported-value',
      ),
    );
    return;
  }
  for (const element of operand) {
    const kind = valueKind(element);
    if (kind === 'null') {
      issues.push(
        issue(
          operator,
          path,
          `operator "${operator}" does not accept null in its list: a null makes the SQL predicate unknown for every row`,
          'unsupported-value',
        ),
      );
      continue;
    }
    if (!LIST_ELEMENT_KINDS.includes(kind as ScalarKind)) {
      issues.push(
        issue(
          operator,
          path,
          `operator "${operator}" does not accept a list element of type ${describeValue(element)}`,
          'unsupported-value',
        ),
      );
    }
  }
}

function toSqlParameter(value: unknown, path: readonly string[]): SqlParameter {
  if (value === null || value === undefined) {
    return null;
  }
  if (
    typeof value === 'string' ||
    typeof value === 'number' ||
    typeof value === 'bigint' ||
    typeof value === 'boolean' ||
    value instanceof Date
  ) {
    return value;
  }
  if (isDecimalLike(value)) {
    return String(value);
  }
  throw new SqlRenderError(`value of type ${describeValue(value)} cannot be bound as a SQL parameter`, path);
}

function comparisonOperator(
  name: 'lt' | 'lte' | 'gt' | 'gte',
  symbol: string,
  accept: (comparison: number) => boolean,
): OperatorDefinition {
  return {
    name,
    operand: 'scalar',
    acceptedValueKinds: ORDERED_KINDS,
    nullSemantics: {
      nullValue: 'never-matches',
      nullOperand: 'never-matches',
      sqlIsTwoValued: true,
    },
    validate: (operand, path, issues) => {
      if (validateFoldedOperand(name, operand, ORDERED_KINDS, path, issues)) {
        return;
      }
      validateScalarOperand(name, operand, ORDERED_KINDS, path, issues);
    },
    compile: (operand) => {
      const folded = textOperand(operand);
      if (folded !== undefined) {
        const needle = foldAscii(folded);
        return (value) => {
          if (typeof value !== 'string') {
            return false;
          }
          const comparison = compareValues(foldAscii(value), needle);
          return comparison !== null && accept(comparison);
        };
      }
      const value = readMode(operand).operand;
      return (subject) => {
        const comparison = compareValues(subject, value);
        return comparison !== null && accept(comparison);
      };
    },
    renderSql: (operand, target) => {
      const column = target.scope.column(target.field);
      const folded = textOperand(operand);
      if (folded !== undefined) {
        return sqlGroup(
          sqlSequence([
            presence(column),
            sqlText(' AND '),
            foldedColumn(column, target.path),
            sqlText(` ${symbol} `),
            sqlParameter(foldAscii(folded)),
          ]),
        );
      }
      const { operand: value, insensitive } = readMode(operand);
      if (value === null || value === undefined) {
        return foldedGuard(insensitive, target.path, SQL_FALSE);
      }
      return foldedGuard(
        insensitive,
        target.path,
        sqlGroup(
          sqlSequence([
            column,
            sqlText(' IS NOT NULL AND '),
            column,
            sqlText(` ${symbol} `),
            sqlParameter(toSqlParameter(value, target.path)),
          ]),
        ),
      );
    },
  };
}

export type StringOperatorName = 'contains' | 'startsWith' | 'endsWith';

export const STRING_MODE_KEY = 'mode';

const STRING_OPERATOR_NAMES: readonly StringOperatorName[] = ['contains', 'startsWith', 'endsWith'];

const LIKE_ESCAPE = '\\';

class InsensitiveOperand {
  constructor(readonly operand: unknown) {}
}

export function isStringOperatorName(key: string): key is StringOperatorName {
  return key === 'contains' || key === 'startsWith' || key === 'endsWith';
}

export function foldAscii(text: string): string {
  return text.replace(/[A-Z]/g, (letter) => letter.toLowerCase());
}

function escapeLikeOperand(text: string): string {
  return text.replace(/[\\%_]/g, (character) => `${LIKE_ESCAPE}${character}`);
}

function likePattern(name: StringOperatorName, operand: string): string {
  const escaped = escapeLikeOperand(operand);
  if (name === 'contains') {
    return `%${escaped}%`;
  }
  return name === 'startsWith' ? `${escaped}%` : `%${escaped}`;
}

export function textMatches(name: StringOperatorName, value: string, operand: string): boolean {
  if (name === 'contains') {
    return value.includes(operand);
  }
  return name === 'startsWith' ? value.startsWith(operand) : value.endsWith(operand);
}

function readMode(operand: unknown): {
  readonly operand: unknown;
  readonly insensitive: boolean;
} {
  return operand instanceof InsensitiveOperand
    ? { operand: operand.operand, insensitive: true }
    : { operand, insensitive: false };
}

const FOLDED_OPERATOR_NAMES: readonly OperatorName[] = [
  'equals',
  'not',
  'in',
  'notIn',
  'lt',
  'lte',
  'gt',
  'gte',
  'contains',
  'startsWith',
  'endsWith',
];

export function isFoldedOperatorName(key: string): key is OperatorName {
  return (FOLDED_OPERATOR_NAMES as readonly string[]).includes(key);
}

export function scalarFilterEntries(
  filter: Record<string, unknown>,
): readonly (readonly [string, unknown])[] {
  const entries = Object.entries(filter).filter(([key]) => key !== STRING_MODE_KEY);
  if (filter[STRING_MODE_KEY] !== 'insensitive') {
    return entries;
  }
  return entries.map(([key, operand]) =>
    isFoldedOperatorName(key) ? ([key, new InsensitiveOperand(operand)] as const) : ([key, operand] as const),
  );
}

export function validateStringMode(
  filter: Record<string, unknown>,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  if (!Object.prototype.hasOwnProperty.call(filter, STRING_MODE_KEY)) {
    return;
  }
  const mode = filter[STRING_MODE_KEY];
  if (mode !== 'default' && mode !== 'insensitive') {
    issues.push(
      issue(
        STRING_MODE_KEY,
        path,
        `"mode" accepts only "default" or "insensitive", received ${describeValue(mode)}`,
        'unsupported-value',
      ),
    );
    return;
  }
  if (mode === 'default') {
    return;
  }
  const unfolded = Object.keys(filter).filter(
    (key) => isOperatorName(key) && !isFoldedOperatorName(key),
  );
  if (unfolded.length > 0) {
    issues.push(
      issue(
        STRING_MODE_KEY,
        path,
        `"mode" set to "insensitive" folds case for ${FOLDED_OPERATOR_NAMES.join(', ')}, and this filter also carries ${unfolded.join(', ')}, which Prisma's string filter does not generate; golem refuses rather than leaving ${unfolded.join(', ')} case-sensitive under a filter that asks for case-insensitive matching`,
        'unsupported-shape',
      ),
    );
  }
}

function refuseInsensitive(dialect: SqlDialect, path: readonly string[]): never {
  throw new SqlRenderError(
    `mode "insensitive" is not supported on ${dialect.name}, which offers no case-insensitive match golem can force onto byte order; folding case any other way would not agree with the evaluator`,
    path,
  );
}

function sqliteMatch(name: StringOperatorName, column: SqlNode, operand: string): SqlNode {
  if (name === 'endsWith') {
    return sqlSequence([
      sqlCall('substr', [column, sqlParameter(-[...operand].length)]),
      sqlText(' = '),
      sqlParameter(operand),
    ]);
  }
  return sqlSequence([
    sqlCall('instr', [column, sqlParameter(operand)]),
    sqlText(name === 'startsWith' ? ' = 1' : ' > 0'),
  ]);
}

export function assertInsensitiveSupported(
  dialect: SqlDialect,
  insensitive: boolean,
  path: readonly string[],
): void {
  if (insensitive && (dialect.like === null || dialect.like.insensitive === null)) {
    refuseInsensitive(dialect, path);
  }
}

export function likeMatch(
  name: StringOperatorName,
  dialect: SqlDialect,
  column: SqlNode,
  operand: string,
  insensitive: boolean,
  path: readonly string[],
): SqlNode {
  assertInsensitiveSupported(dialect, insensitive, path);
  const support = dialect.like;
  if (support === null) {
    return sqliteMatch(name, column, operand);
  }
  return sqlSequence([
    column,
    sqlText(` ${insensitive ? support.insensitive! : 'LIKE'} `),
    sqlParameter(likePattern(name, operand)),
    sqlText(` ESCAPE ${support.escape}`),
  ]);
}

export function foldedEquals(value: unknown, operand: unknown): boolean {
  if (typeof value !== 'string' || typeof operand !== 'string') {
    return valuesEqual(value, operand);
  }
  return foldAscii(value) === foldAscii(operand);
}

function foldedMatch(column: SqlNode, operand: string, path: readonly string[]): SqlNode {
  return sqlDialectal((dialect) => {
    assertInsensitiveSupported(dialect, true, path);
    return sqlSequence([
      column,
      sqlText(` ${dialect.like!.insensitive!} `),
      sqlParameter(escapeLikeOperand(operand)),
      sqlText(` ESCAPE ${dialect.like!.escape}`),
    ]);
  });
}

function foldedColumn(column: SqlNode, path: readonly string[]): SqlNode {
  return sqlDialectal((dialect) => {
    assertInsensitiveSupported(dialect, true, path);
    return sqlBinaryCollate(sqlCall('lower', [column]));
  });
}

function presence(column: SqlNode): SqlNode {
  return sqlSequence([column, sqlText(' IS NOT NULL')]);
}

function foldedGuard(insensitive: boolean, path: readonly string[], node: SqlNode): SqlNode {
  if (!insensitive) {
    return node;
  }
  return sqlDialectal((dialect) => {
    assertInsensitiveSupported(dialect, true, path);
    return node;
  });
}

function foldedList(column: SqlNode, elements: readonly string[], path: readonly string[]): SqlNode {
  return sqlGroup(
    sqlJoin(
      elements.map((element) => foldedMatch(column, element, path)),
      ' OR ',
    ),
  );
}

function textOperand(operand: unknown): string | undefined {
  const { operand: value, insensitive } = readMode(operand);
  return insensitive && typeof value === 'string' ? value : undefined;
}

function textOperands(operand: unknown): readonly string[] | undefined {
  const { operand: value, insensitive } = readMode(operand);
  if (!insensitive || !Array.isArray(value) || !value.every((element) => typeof element === 'string')) {
    return undefined;
  }
  return value as readonly string[];
}

function validateFoldedOperand(
  operator: OperatorName,
  operand: unknown,
  accepted: readonly ScalarKind[],
  path: readonly string[],
  issues: ConditionIssue[],
): boolean {
  const { operand: value, insensitive } = readMode(operand);
  if (!insensitive) {
    return false;
  }
  validateScalarOperand(operator, value, accepted.includes('null') ? TEXT_KINDS : ['string'], path, issues);
  return true;
}

function stringOperator(name: StringOperatorName): OperatorDefinition {
  return {
    name,
    operand: 'scalar',
    acceptedValueKinds: TEXT_KINDS,
    nullSemantics: {
      nullValue: 'never-matches',
      nullOperand: 'never-matches',
      sqlIsTwoValued: true,
    },
    validate: (operand, path, issues) =>
      validateScalarOperand(name, readMode(operand).operand, TEXT_KINDS, path, issues),
    compile: (operand) => {
      const { operand: text, insensitive } = readMode(operand);
      if (typeof text !== 'string') {
        return () => false;
      }
      const needle = insensitive ? foldAscii(text) : text;
      return (value) =>
        typeof value === 'string' && textMatches(name, insensitive ? foldAscii(value) : value, needle);
    },
    renderSql: (operand, target) => {
      const { operand: text, insensitive } = readMode(operand);
      if (typeof text !== 'string') {
        return foldedGuard(insensitive, target.path, SQL_FALSE);
      }
      const column = target.scope.column(target.field);
      const present = sqlSequence([column, sqlText(' IS NOT NULL')]);
      if (text.length === 0) {
        return sqlGroup(
          sqlDialectal((dialect) => {
            assertInsensitiveSupported(dialect, insensitive, target.path);
            return present;
          }),
        );
      }
      return sqlGroup(
        sqlSequence([
          present,
          sqlText(' AND '),
          sqlDialectal((dialect) => likeMatch(name, dialect, column, text, insensitive, target.path)),
        ]),
      );
    },
  };
}

const equalsOperator: OperatorDefinition = {
  name: 'equals',
  operand: 'scalar',
  acceptedValueKinds: SCALAR_KINDS,
  nullSemantics: {
    nullValue: 'matches-when-operand-null',
    nullOperand: 'allowed',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => {
    if (validateFoldedOperand('equals', operand, SCALAR_KINDS, path, issues)) {
      return;
    }
    validateScalarOperand('equals', operand, SCALAR_KINDS, path, issues);
  },
  compile: (operand) => {
    const folded = textOperand(operand);
    if (folded !== undefined) {
      return (value) => foldedEquals(value, folded);
    }
    return (value) => valuesEqual(value, readMode(operand).operand);
  },
  renderSql: (operand, target) => {
    const column = target.scope.column(target.field);
    const folded = textOperand(operand);
    if (folded !== undefined) {
      return sqlGroup(
        sqlSequence([presence(column), sqlText(' AND '), foldedMatch(column, folded, target.path)]),
      );
    }
    const { operand: value, insensitive } = readMode(operand);
    if (value === null || value === undefined) {
      return foldedGuard(insensitive, target.path, sqlGroup(sqlSequence([column, sqlText(' IS NULL')])));
    }
    return foldedGuard(
      insensitive,
      target.path,
      sqlGroup(sqlNullSafeEquals(column, sqlParameter(toSqlParameter(value, target.path)))),
    );
  },
};

const notOperator: OperatorDefinition = {
  name: 'not',
  operand: 'scalar',
  acceptedValueKinds: SCALAR_KINDS,
  nullSemantics: {
    nullValue: 'matches-when-operand-not-null',
    nullOperand: 'allowed',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => {
    if (validateFoldedOperand('not', operand, SCALAR_KINDS, path, issues)) {
      return;
    }
    validateScalarOperand('not', operand, SCALAR_KINDS, path, issues);
  },
  compile: (operand) => {
    const folded = textOperand(operand);
    if (folded !== undefined) {
      return (value) => !foldedEquals(value, folded);
    }
    return (value) => !valuesEqual(value, readMode(operand).operand);
  },
  renderSql: (operand, target) => {
    const column = target.scope.column(target.field);
    const folded = textOperand(operand);
    if (folded !== undefined) {
      return sqlSequence([
        sqlText('NOT '),
        sqlGroup(sqlSequence([presence(column), sqlText(' AND '), foldedMatch(column, folded, target.path)])),
      ]);
    }
    const { operand: value, insensitive } = readMode(operand);
    if (value === null || value === undefined) {
      return foldedGuard(insensitive, target.path, sqlGroup(sqlSequence([column, sqlText(' IS NOT NULL')])));
    }
    return foldedGuard(
      insensitive,
      target.path,
      sqlSequence([
        sqlText('NOT '),
        sqlGroup(sqlNullSafeEquals(column, sqlParameter(toSqlParameter(value, target.path)))),
      ]),
    );
  },
};

const inOperator: OperatorDefinition = {
  name: 'in',
  operand: 'scalar-list',
  acceptedValueKinds: LIST_ELEMENT_KINDS,
  nullSemantics: {
    nullValue: 'never-matches',
    nullOperand: 'rejected',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => validateListOperand('in', readMode(operand).operand, path, issues),
  compile: (operand) => {
    const folded = textOperands(operand);
    if (folded !== undefined) {
      return (value) => valueKind(value) !== 'null' && folded.some((element) => foldedEquals(value, element));
    }
    const list = readMode(operand).operand as readonly unknown[];
    return (value) => valueKind(value) !== 'null' && list.some((element) => valuesEqual(value, element));
  },
  renderSql: (operand, target) => {
    const column = target.scope.column(target.field);
    const folded = textOperands(operand);
    if (folded !== undefined) {
      if (folded.length === 0) {
        return foldedGuard(true, target.path, SQL_FALSE);
      }
      return sqlGroup(
        sqlSequence([presence(column), sqlText(' AND '), foldedList(column, folded, target.path)]),
      );
    }
    const { operand: rawList, insensitive } = readMode(operand);
    const list = rawList as readonly unknown[];
    if (list.length === 0) {
      return foldedGuard(insensitive, target.path, SQL_FALSE);
    }
    return foldedGuard(
      insensitive,
      target.path,
      sqlGroup(
        sqlSequence([
          column,
          sqlText(' IS NOT NULL AND '),
          column,
          sqlText(' IN ('),
          sqlJoin(
            list.map((element) => sqlParameter(toSqlParameter(element, target.path))),
            ', ',
          ),
          sqlText(')'),
        ]),
      ),
    );
  },
};

const notInOperator: OperatorDefinition = {
  name: 'notIn',
  operand: 'scalar-list',
  acceptedValueKinds: LIST_ELEMENT_KINDS,
  nullSemantics: {
    nullValue: 'always-matches',
    nullOperand: 'rejected',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => validateListOperand('notIn', readMode(operand).operand, path, issues),
  compile: (operand) => {
    const folded = textOperands(operand);
    if (folded !== undefined) {
      return (value) =>
        valueKind(value) === 'null' || !folded.some((element) => foldedEquals(value, element));
    }
    const list = readMode(operand).operand as readonly unknown[];
    return (value) => valueKind(value) === 'null' || !list.some((element) => valuesEqual(value, element));
  },
  renderSql: (operand, target) => {
    const column = target.scope.column(target.field);
    const folded = textOperands(operand);
    if (folded !== undefined) {
      if (folded.length === 0) {
        return foldedGuard(true, target.path, SQL_TRUE);
      }
      return sqlGroup(
        sqlSequence([column, sqlText(' IS NULL OR NOT '), foldedList(column, folded, target.path)]),
      );
    }
    const { operand: rawList, insensitive } = readMode(operand);
    const list = rawList as readonly unknown[];
    if (list.length === 0) {
      return foldedGuard(insensitive, target.path, SQL_TRUE);
    }
    return foldedGuard(
      insensitive,
      target.path,
      sqlGroup(
        sqlSequence([
          column,
          sqlText(' IS NULL OR '),
          column,
          sqlText(' NOT IN ('),
          sqlJoin(
            list.map((element) => sqlParameter(toSqlParameter(element, target.path))),
            ', ',
          ),
          sqlText(')'),
        ]),
      ),
    );
  },
};

const SCALAR_LIST_ONLY_OPERATORS: readonly ScalarListOperatorName[] = [
  'has',
  'hasEvery',
  'hasSome',
  'isEmpty',
];

function validateListColumnOperand(
  operator: ScalarListOperatorName,
  operand: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  if (!Array.isArray(operand)) {
    issues.push(
      issue(
        operator,
        path,
        `operator "${operator}" requires an array operand, received ${describeValue(operand)}`,
        'unsupported-value',
      ),
    );
    return;
  }
  for (const element of operand) {
    const kind = valueKind(element);
    if (kind === 'null') {
      issues.push(
        issue(
          operator,
          path,
          `operator "${operator}" does not accept null in its list: Prisma types a scalar list filter as T[], and Postgres containment never finds a null element`,
          'unsupported-value',
        ),
      );
      continue;
    }
    if (!LIST_ELEMENT_KINDS.includes(kind as ScalarKind)) {
      issues.push(
        issue(
          operator,
          path,
          `operator "${operator}" does not accept a list element of type ${describeValue(element)}`,
          'unsupported-value',
        ),
      );
    }
  }
}

function listColumnOf(target: SqlFieldTarget): SqlNode {
  return target.scope.listColumn === undefined
    ? target.scope.column(target.field)
    : target.scope.listColumn(target.field);
}

function postgresOnly(operator: ScalarListOperatorName, target: SqlFieldTarget, node: SqlNode): SqlNode {
  return sqlDialectal((dialect) => {
    if (dialect.name !== 'postgres') {
      throw new SqlRenderError(
        `operator "${operator}" filters the scalar list column "${target.field}", and the ${dialect.name} provider has no scalar list columns: Prisma refuses a list field on ${dialect.name} at the schema level, so this policy cannot be answered here`,
        target.path,
      );
    }
    return node;
  });
}

function listOf(value: unknown): readonly unknown[] | null {
  return Array.isArray(value) ? value : null;
}

function listContains(list: readonly unknown[], operand: unknown): boolean {
  return valueKind(operand) !== 'null' && list.some((element) => valuesEqual(element, operand));
}

function containsValueSql(column: SqlNode, element: unknown, path: readonly string[]): SqlNode {
  return sqlArrayContainsValue(column, sqlParameter(toSqlParameter(element, path)));
}

const hasOperator: OperatorDefinition = {
  name: 'has',
  operand: 'scalar',
  acceptedValueKinds: SCALAR_KINDS,
  nullSemantics: {
    nullValue: 'never-matches',
    nullOperand: 'never-matches',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => validateScalarOperand('has', operand, SCALAR_KINDS, path, issues),
  compile: (operand) => (value) => {
    const list = listOf(value);
    return list !== null && listContains(list, operand);
  },
  renderSql: (operand, target) => {
    if (valueKind(operand) === 'null') {
      return postgresOnly('has', target, SQL_FALSE);
    }
    return postgresOnly('has', target, containsValueSql(listColumnOf(target), operand, target.path));
  },
};

function quantifiedListOperator(name: 'hasEvery' | 'hasSome'): OperatorDefinition {
  const requiresAll = name === 'hasEvery';
  return {
    name,
    operand: 'scalar-list',
    acceptedValueKinds: LIST_ELEMENT_KINDS,
    nullSemantics: {
      nullValue: 'never-matches',
      nullOperand: 'rejected',
      sqlIsTwoValued: true,
    },
    validate: (operand, path, issues) => validateListColumnOperand(name, operand, path, issues),
    compile: (operand) => {
      const elements = operand as readonly unknown[];
      return (value) => {
        const list = listOf(value);
        if (list === null) {
          return false;
        }
        return requiresAll
          ? elements.every((element) => listContains(list, element))
          : elements.some((element) => listContains(list, element));
      };
    },
    renderSql: (operand, target) => {
      const elements = operand as readonly unknown[];
      const column = listColumnOf(target);
      if (elements.length === 0) {
        return postgresOnly(
          name,
          target,
          requiresAll ? sqlGroup(sqlSequence([column, sqlText(' IS NOT NULL')])) : SQL_FALSE,
        );
      }
      return postgresOnly(
        name,
        target,
        sqlGroup(
          sqlJoin(
            elements.map((element) => containsValueSql(column, element, target.path)),
            requiresAll ? ' AND ' : ' OR ',
          ),
        ),
      );
    },
  };
}

const isEmptyOperator: OperatorDefinition = {
  name: 'isEmpty',
  operand: 'scalar',
  acceptedValueKinds: ['boolean'],
  nullSemantics: {
    nullValue: 'never-matches',
    nullOperand: 'rejected',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => {
    if (typeof operand !== 'boolean') {
      issues.push(
        issue(
          'isEmpty',
          path,
          `operator "isEmpty" requires true or false, received ${describeValue(operand)}`,
          'unsupported-value',
        ),
      );
    }
  },
  compile: (operand) => (value) => {
    const list = listOf(value);
    if (list === null) {
      return false;
    }
    return operand === true ? list.length === 0 : list.length > 0;
  },
  renderSql: (operand, target) => {
    const column = listColumnOf(target);
    return postgresOnly(
      'isEmpty',
      target,
      sqlGroup(
        sqlSequence([
          column,
          sqlText(' IS NOT NULL AND '),
          sqlArrayCardinality(column),
          sqlText(operand === true ? ' = 0' : ' > 0'),
        ]),
      ),
    );
  },
};

const listEqualsOperator: OperatorDefinition = {
  name: 'equals',
  operand: 'scalar-list',
  acceptedValueKinds: LIST_ELEMENT_KINDS,
  nullSemantics: {
    nullValue: 'matches-when-operand-null',
    nullOperand: 'allowed',
    sqlIsTwoValued: true,
  },
  validate: (operand, path, issues) => {
    if (operand === null || operand === undefined) {
      return;
    }
    validateListColumnOperand('equals', operand, path, issues);
  },
  compile: (operand) => {
    if (operand === null || operand === undefined) {
      return (value) => valueKind(value) === 'null';
    }
    const elements = operand as readonly unknown[];
    return (value) => {
      const list = listOf(value);
      return (
        list !== null &&
        list.length === elements.length &&
        elements.every((element, index) => valuesEqual(list[index], element))
      );
    };
  },
  renderSql: (operand, target) => {
    const column = listColumnOf(target);
    if (operand === null || operand === undefined) {
      return postgresOnly('equals', target, sqlGroup(sqlSequence([column, sqlText(' IS NULL')])));
    }
    const elements = operand as readonly unknown[];
    const nodes: SqlNode[] = [
      column,
      sqlText(' IS NOT NULL AND '),
      sqlArrayCardinality(column),
      sqlText(` = ${elements.length}`),
    ];
    elements.forEach((element, index) => {
      nodes.push(sqlText(' AND '));
      nodes.push(
        sqlNullSafeEquals(
          sqlArrayElement(column, index + 1),
          sqlParameter(toSqlParameter(element, target.path)),
        ),
      );
    });
    return postgresOnly('equals', target, sqlGroup(sqlSequence(nodes)));
  },
};

export const SCALAR_LIST_OPERATORS: Readonly<Record<ScalarListOperatorName, OperatorDefinition>> = {
  has: hasOperator,
  hasEvery: quantifiedListOperator('hasEvery'),
  hasSome: quantifiedListOperator('hasSome'),
  isEmpty: isEmptyOperator,
  equals: listEqualsOperator,
};

export const SUPPORTED_SCALAR_LIST_OPERATORS: readonly ScalarListOperatorName[] = Object.keys(
  SCALAR_LIST_OPERATORS,
) as ScalarListOperatorName[];

export function isScalarListOperatorName(key: string): key is ScalarListOperatorName {
  return Object.prototype.hasOwnProperty.call(SCALAR_LIST_OPERATORS, key);
}

export function isScalarListOnlyOperatorName(key: string): key is ScalarListOperatorName {
  return (SCALAR_LIST_ONLY_OPERATORS as readonly string[]).includes(key);
}

export function validateRelationFilter(
  operator: string,
  operand: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
  hooks: OperatorHooks,
): void {
  if (operand === null || operand === undefined) {
    return;
  }
  if (!isPlainObject(operand)) {
    issues.push(
      issue(
        operator,
        path,
        `operator "${operator}" requires a nested condition object or null, received ${describeValue(operand)}`,
        'unsupported-value',
      ),
    );
    return;
  }
  hooks.validateConditions(operand, path, issues);
}

export function compileRelationFilter(operand: unknown, hooks: OperatorHooks): FieldPredicate {
  if (operand === null || operand === undefined) {
    return (value) => !isRelationObject(value);
  }
  const nested = hooks.compileConditions(operand);
  return (value) => (isRelationObject(value) ? nested(value) : false);
}

export function renderRelationFilter(
  operand: unknown,
  target: SqlFieldTarget,
  path: readonly string[],
  hooks: OperatorHooks,
): SqlNode {
  const hop = target.scope.relation(target.field);
  if (hop === undefined) {
    throw new SqlRenderError(`relation "${target.field}" is not available in this scope`, path);
  }
  if (hop.toMany) {
    throw new SqlRenderError(
      `relation "${target.field}" is to-many; "is", "isNot" and the bare condition shorthand scope a to-one relation, and a to-many relation is filtered with "some", "every" or "none"`,
      path,
    );
  }
  if (operand === null || operand === undefined) {
    return sqlSequence([sqlText('NOT '), sqlGroup(hop.wrap(SQL_TRUE))]);
  }
  return sqlGroup(hop.wrap(hooks.renderConditions(operand, hop.scope, path)));
}

function relationOperator(name: 'is' | 'isNot'): OperatorDefinition {
  const negated = name === 'isNot';
  return {
    name,
    operand: 'conditions',
    acceptedValueKinds: ['null'],
    nullSemantics: {
      nullValue: negated ? 'matches-when-operand-not-null' : 'matches-when-operand-null',
      nullOperand: 'allowed',
      sqlIsTwoValued: true,
    },
    validate: (operand, path, issues, hooks) =>
      validateRelationFilter(name, operand, [...path, name], issues, hooks),
    compile: (operand, hooks) => {
      const predicate = compileRelationFilter(operand, hooks);
      return negated ? (value) => !predicate(value) : predicate;
    },
    renderSql: (operand, target, hooks) => {
      const node = renderRelationFilter(operand, target, [...target.path, name], hooks);
      return negated ? sqlSequence([sqlText('NOT '), sqlGroup(node)]) : node;
    },
  };
}

export type ListRelationOperatorName = 'some' | 'every' | 'none';

function relatedList(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : [];
}

export function validateListRelationFilter(
  operator: string,
  operand: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
  hooks: OperatorHooks,
): void {
  if (!isPlainObject(operand)) {
    issues.push(
      issue(
        operator,
        path,
        `operator "${operator}" requires a condition object on the related model, received ${describeValue(operand)}`,
        'unsupported-value',
      ),
    );
    return;
  }
  hooks.validateConditions(operand, path, issues);
}

export function compileListRelationFilter(operand: unknown, hooks: OperatorHooks): FieldPredicate {
  const nested = hooks.compileConditions(operand);
  return (value) => (isRelationObject(value) ? nested(value) : false);
}

export function renderListRelationFilter(
  name: ListRelationOperatorName,
  operand: unknown,
  target: SqlFieldTarget,
  path: readonly string[],
  hooks: OperatorHooks,
): SqlNode {
  const hop = target.scope.relation(target.field);
  if (hop === undefined) {
    throw new SqlRenderError(`relation "${target.field}" is not available in this scope`, path);
  }
  if (!hop.toMany) {
    throw new SqlRenderError(
      `relation "${target.field}" is to-one; "${name}" quantifies over a to-many relation, and a to-one relation is filtered with "is", "isNot" or a condition object`,
      path,
    );
  }
  const condition = hooks.renderConditions(operand, hop.scope, path);
  if (name === 'every') {
    return sqlSequence([sqlText('NOT '), sqlGroup(hop.wrap(sqlIsNotTrue(condition)))]);
  }
  const exists = sqlGroup(hop.wrap(condition));
  return name === 'none' ? sqlSequence([sqlText('NOT '), exists]) : exists;
}

function listRelationOperator(name: ListRelationOperatorName): OperatorDefinition {
  const quantify =
    name === 'some'
      ? (list: readonly unknown[], matches: FieldPredicate) => list.some(matches)
      : name === 'none'
        ? (list: readonly unknown[], matches: FieldPredicate) => !list.some(matches)
        : (list: readonly unknown[], matches: FieldPredicate) => list.every(matches);
  return {
    name,
    operand: 'conditions',
    acceptedValueKinds: [],
    nullSemantics: {
      nullValue: name === 'some' ? 'never-matches' : 'always-matches',
      nullOperand: 'rejected',
      sqlIsTwoValued: true,
    },
    validate: (operand, path, issues, hooks) =>
      validateListRelationFilter(name, operand, [...path, name], issues, hooks),
    compile: (operand, hooks) => {
      const matches = compileListRelationFilter(operand, hooks);
      return (value) => quantify(relatedList(value), matches);
    },
    renderSql: (operand, target, hooks) =>
      renderListRelationFilter(name, operand, target, [...target.path, name], hooks),
  };
}

export const OPERATORS: Readonly<Record<OperatorName, OperatorDefinition>> = {
  equals: equalsOperator,
  not: notOperator,
  in: inOperator,
  notIn: notInOperator,
  lt: comparisonOperator('lt', '<', (comparison) => comparison < 0),
  lte: comparisonOperator('lte', '<=', (comparison) => comparison <= 0),
  gt: comparisonOperator('gt', '>', (comparison) => comparison > 0),
  gte: comparisonOperator('gte', '>=', (comparison) => comparison >= 0),
  contains: stringOperator('contains'),
  startsWith: stringOperator('startsWith'),
  endsWith: stringOperator('endsWith'),
  is: relationOperator('is'),
  isNot: relationOperator('isNot'),
  some: listRelationOperator('some'),
  every: listRelationOperator('every'),
  none: listRelationOperator('none'),
};

export function isRelationOperatorName(key: string): key is 'is' | 'isNot' {
  return key === 'is' || key === 'isNot';
}

export function isListRelationOperatorName(key: string): key is ListRelationOperatorName {
  return key === 'some' || key === 'every' || key === 'none';
}

export const SUPPORTED_OPERATORS: readonly OperatorName[] = Object.keys(OPERATORS) as OperatorName[];

export function isOperatorName(key: string): key is OperatorName {
  return Object.prototype.hasOwnProperty.call(OPERATORS, key);
}

function validateBranches(
  name: CombinatorName,
  operand: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
  hooks: OperatorHooks,
): void {
  if (isPlainObject(operand)) {
    hooks.validateConditions(operand, [...path, name], issues);
    return;
  }
  if (!Array.isArray(operand)) {
    issues.push(
      issue(
        name,
        path,
        `combinator "${name}" requires a condition object or an array of condition objects, received ${describeValue(operand)}`,
        'unsupported-shape',
      ),
    );
    return;
  }
  operand.forEach((branch, index) => {
    if (!isPlainObject(branch)) {
      issues.push(
        issue(
          name,
          [...path, name, String(index)],
          `combinator "${name}" requires each branch to be a condition object, received ${describeValue(branch)}`,
          'unsupported-shape',
        ),
      );
      return;
    }
    hooks.validateConditions(branch, [...path, name, String(index)], issues);
  });
}

function branchesOf(operand: unknown): readonly unknown[] {
  return Array.isArray(operand) ? operand : [operand];
}

function compileBranches(operand: unknown, hooks: OperatorHooks): readonly ObjectPredicate[] {
  return branchesOf(operand).map((branch) => hooks.compileConditions(branch));
}

function renderBranches(
  operand: unknown,
  scope: SqlScope,
  path: readonly string[],
  name: CombinatorName,
  hooks: OperatorHooks,
): readonly SqlNode[] {
  return branchesOf(operand).map((branch, index) =>
    hooks.renderConditions(branch, scope, [...path, name, String(index)]),
  );
}

const andCombinator: CombinatorDefinition = {
  name: 'AND',
  validate: (operand, path, issues, hooks) => validateBranches('AND', operand, path, issues, hooks),
  compile: (operand, hooks) => {
    const predicates = compileBranches(operand, hooks);
    return (object) => predicates.every((predicate) => predicate(object));
  },
  renderSql: (operand, scope, path, hooks) => {
    const nodes = renderBranches(operand, scope, path, 'AND', hooks);
    return nodes.length === 0 ? SQL_TRUE : sqlGroup(sqlJoin(nodes, ' AND '));
  },
};

const orCombinator: CombinatorDefinition = {
  name: 'OR',
  validate: (operand, path, issues, hooks) => validateBranches('OR', operand, path, issues, hooks),
  compile: (operand, hooks) => {
    const predicates = compileBranches(operand, hooks);
    return (object) => predicates.some((predicate) => predicate(object));
  },
  renderSql: (operand, scope, path, hooks) => {
    const nodes = renderBranches(operand, scope, path, 'OR', hooks);
    return nodes.length === 0 ? SQL_FALSE : sqlGroup(sqlJoin(nodes, ' OR '));
  },
};

const notCombinator: CombinatorDefinition = {
  name: 'NOT',
  validate: (operand, path, issues, hooks) => validateBranches('NOT', operand, path, issues, hooks),
  compile: (operand, hooks) => {
    const predicates = compileBranches(operand, hooks);
    return (object) => predicates.every((predicate) => !predicate(object));
  },
  renderSql: (operand, scope, path, hooks) => {
    const nodes = renderBranches(operand, scope, path, 'NOT', hooks);
    return nodes.length === 0 ? SQL_TRUE : sqlSequence([sqlText('NOT '), sqlGroup(sqlJoin(nodes, ' OR '))]);
  },
};

export const COMBINATORS: Readonly<Record<CombinatorName, CombinatorDefinition>> = {
  AND: andCombinator,
  OR: orCombinator,
  NOT: notCombinator,
};

export const SUPPORTED_COMBINATORS: readonly CombinatorName[] = Object.keys(COMBINATORS) as CombinatorName[];

export function isCombinatorName(key: string): key is CombinatorName {
  return Object.prototype.hasOwnProperty.call(COMBINATORS, key);
}

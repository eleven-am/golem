import { valueKind } from '../../src/index';

export const ABSENT = Symbol('absent');

export interface Leaf {
  readonly operator: string;
  readonly operand: unknown;
  readonly value: unknown | typeof ABSENT;
}

export interface CaslDivergence {
  readonly id: string;
  readonly summary: string;
  readonly generated: boolean;
  applies(leaf: Leaf): boolean;
}

const COMPARISONS = ['lt', 'lte', 'gt', 'gte'];

const IDENTITY_SENSITIVE = ['equals', 'not', 'in', 'notIn', 'lt', 'lte', 'gt', 'gte'];

function present(value: unknown | typeof ABSENT): unknown {
  return value === ABSENT ? undefined : value;
}

function nullish(value: unknown | typeof ABSENT): boolean {
  return value === ABSENT || valueKind(value) === 'null';
}

function nonFinite(value: unknown): boolean {
  return typeof value === 'number' && !Number.isFinite(value);
}

function invalidDate(value: unknown): boolean {
  return value instanceof Date && Number.isNaN(value.getTime());
}

function orderComparable(left: unknown, right: unknown): boolean {
  const leftKind = valueKind(left);
  const rightKind = valueKind(right);
  if (leftKind === 'null' || rightKind === 'null') {
    return false;
  }
  if (leftKind === 'unsupported' || rightKind === 'unsupported') {
    return false;
  }
  if (leftKind === 'numeric' && rightKind === 'numeric') {
    return !nonFinite(left) && !nonFinite(right);
  }
  if (leftKind === 'string' && rightKind === 'string') {
    return true;
  }
  if (leftKind === 'date' || rightKind === 'date') {
    const bothTemporal =
      (leftKind === 'date' || leftKind === 'string') && (rightKind === 'date' || rightKind === 'string');
    return bothTemporal && !invalidDate(left) && !invalidDate(right);
  }
  return false;
}

function operandList(leaf: Leaf): readonly unknown[] {
  return Array.isArray(leaf.operand) ? leaf.operand : [leaf.operand];
}

function numericJsType(value: unknown): string | null {
  if (typeof value === 'number' || typeof value === 'bigint') {
    return typeof value;
  }
  return valueKind(value) === 'numeric' ? 'decimal' : null;
}

export const CASL_DIVERGENCES: readonly CaslDivergence[] = [
  {
    id: 'incomparable-comparison',
    summary:
      'lt/lte/gt/gte on a pair golem calls incomparable (null, absent, or mismatched kinds). Golem returns false for all four. @casl/prisma compares with (a === b ? 0 : a > b ? 1 : -1), which yields -1 for every incomparable pair, so lt and lte are true and gt and gte are false: an asymmetry that has no counterpart in SQL.',
    generated: true,
    applies: (leaf) =>
      COMPARISONS.includes(leaf.operator) && !orderComparable(present(leaf.value), leaf.operand),
  },
  {
    id: 'bigint-comparison-operand',
    summary:
      'lt/lte/gt/gte with a bigint operand. @casl/prisma rejects bigint as "not a comparable value" and then crashes building the error message with JSON.stringify, throwing TypeError instead of ParsingQueryError. Golem compares bigints exactly.',
    generated: true,
    applies: (leaf) => COMPARISONS.includes(leaf.operator) && typeof leaf.operand === 'bigint',
  },
  {
    id: 'numeric-identity-across-js-types',
    summary:
      'any scalar operator where the row value and the operand are both numeric but of different JS types (number vs bigint vs Decimal). Golem compares by decimal value, so 1n equals 1. @casl/prisma reaches equality only through ===, which is false across types, so its compare returns -1: equals and gte become false and lt becomes true even when the two numbers are the same.',
    generated: true,
    applies: (leaf) => {
      if (!IDENTITY_SENSITIVE.includes(leaf.operator)) {
        return false;
      }
      const valueType = numericJsType(present(leaf.value));
      if (valueType === null) {
        return false;
      }
      return operandList(leaf).some((element) => {
        const elementType = numericJsType(element);
        return elementType !== null && elementType !== valueType;
      });
    },
  },
  {
    id: 'date-versus-non-date',
    summary:
      'any operator where exactly one side is a Date. @casl/prisma calls valueOf() on objects first, so a Date collapses to its epoch milliseconds and compares equal to that number. Golem treats date and numeric as different kinds and parses strings as dates instead.',
    generated: true,
    applies: (leaf) => {
      const value = present(leaf.value);
      return operandList(leaf).some(
        (element) => (value instanceof Date) !== (element instanceof Date),
      );
    },
  },
  {
    id: 'undefined-versus-null',
    summary:
      'undefined on either side of equals/not. @casl/prisma only takes its null-aware branch when the operand is strictly null, and it decides null-equality with Object.hasOwn, so { equals: undefined } does not match a null column and a present-but-undefined field is not null. Golem folds undefined and null into a single kind.',
    generated: true,
    applies: (leaf) =>
      (leaf.operator === 'equals' || leaf.operator === 'not') &&
      ((leaf.value !== ABSENT && leaf.value === undefined) || leaf.operand === undefined),
  },
  {
    id: 'is-on-nullish-relation',
    summary:
      'the "is" operator when the relation is null or absent. @casl/prisma returns the relation value itself (null or undefined) rather than a boolean, so its matcher is not two-valued. Golem returns false.',
    generated: true,
    applies: (leaf) => leaf.operator === 'is' && nullish(leaf.value),
  },
  {
    id: 'non-finite-number',
    summary:
      'NaN or +/-Infinity on either side. Golem refuses to order or equate them, so lt: Infinity matches nothing and equals: Infinity does not match Infinity. @casl/prisma uses raw JS comparison.',
    generated: true,
    applies: (leaf) =>
      nonFinite(present(leaf.value)) || operandList(leaf).some((element) => nonFinite(element)),
  },
  {
    id: 'invalid-date',
    summary:
      'an Invalid Date on either side. Golem refuses to order or equate it. @casl/prisma reduces it to NaN, which is never === and never >, so its compare returns -1 and lt/lte become true.',
    generated: true,
    applies: (leaf) =>
      invalidDate(present(leaf.value)) || operandList(leaf).some((element) => invalidDate(element)),
  },
  {
    id: 'empty-field-filter',
    summary:
      'a field filter with no operators, such as { age: {} }. @casl/prisma throws ParsingQueryError ("equals does not supports comparison of arrays and objects"). Golem treats it as the identity and matches every row, which is what Prisma SQL does.',
    generated: true,
    applies: (leaf) => leaf.operator === 'empty-field-filter',
  },
  {
    id: 'string-operator-on-a-non-string',
    summary:
      'contains, startsWith or endsWith against a value that is not a string: null, an absent field, or a number. @casl/prisma calls String.prototype.includes on the raw value and throws TypeError, so its matcher is not two-valued and a rule guarding a nullable column crashes rather than denying. Golem answers a non-match, which is what the SQL renders and what negation needs to stay classical. Not generated: the shared arbitraries do not emit string operators.',
    generated: false,
    applies: (leaf) =>
      ['contains', 'startsWith', 'endsWith'].includes(leaf.operator) &&
      typeof present(leaf.value) !== 'string',
  },
  {
    id: 'insensitive-mode-outside-the-string-operators',
    summary:
      'a filter carrying mode "insensitive" beside equals, not, in, notIn or an ordered comparison. @casl/prisma silently ignores the mode there and compares case-sensitively. Prisma on Postgres folds case for all of them, and golem follows Prisma, folding ASCII case under the forced byte-order collation. Not generated: the shared arbitraries do not emit mode.',
    generated: false,
    applies: () => false,
  },
  {
    id: 'dotted-field-name',
    summary:
      'a condition key containing a dot. @casl/prisma splits it into a nested property path; golem reads the literal key. Not generated: no Prisma column can be named this way.',
    generated: false,
    applies: () => false,
  },
  {
    id: 'prototype-field-name',
    summary:
      'a condition key that names an Object.prototype member, such as toString or constructor. @casl/prisma throws while parsing. Golem reads the inherited prototype value instead of treating the field as absent. Not generated: no Prisma column can be named this way.',
    generated: false,
    applies: () => false,
  },
];

export const GENERATED_DIVERGENCE_IDS: readonly string[] = CASL_DIVERGENCES.filter(
  (entry) => entry.generated,
).map((entry) => entry.id);

function classifyLeaf(leaf: Leaf, found: Set<string>): void {
  for (const divergence of CASL_DIVERGENCES) {
    if (divergence.applies(leaf)) {
      found.add(divergence.id);
    }
  }
}

function isPlain(value: unknown): value is Record<string, unknown> {
  return (
    typeof value === 'object' && value !== null && !Array.isArray(value) && !(value instanceof Date)
  );
}

export function classifyCase(conditions: unknown, object: unknown, found = new Set<string>()): Set<string> {
  if (!isPlain(conditions)) {
    return found;
  }
  for (const [key, filter] of Object.entries(conditions)) {
    if (key === 'AND' || key === 'OR' || key === 'NOT') {
      const branches = Array.isArray(filter) ? filter : [filter];
      for (const branch of branches) {
        classifyCase(branch, object, found);
      }
      continue;
    }
    const value = isPlain(object) && key in object ? object[key] : ABSENT;
    if (!isPlain(filter)) {
      classifyLeaf({ operator: 'equals', operand: filter, value }, found);
      continue;
    }
    const operators = Object.entries(filter);
    if (operators.length === 0) {
      classifyLeaf({ operator: 'empty-field-filter', operand: undefined, value }, found);
      continue;
    }
    for (const [operator, operand] of operators) {
      classifyLeaf({ operator, operand, value }, found);
      if (operator === 'is' && isPlain(value)) {
        classifyCase(operand, value, found);
      }
    }
  }
  return found;
}

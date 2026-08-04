import { BEYOND, D0, D1, D2, SAFE } from './rows';

export interface WhereCase {
  readonly id: string;
  readonly where: Record<string, unknown>;
}

interface ColumnSpec {
  readonly column: string;
  readonly nullable: boolean;
  readonly operands: readonly unknown[];
}

const COLUMNS: readonly ColumnSpec[] = [
  { column: 'name', nullable: false, operands: ['alpha', '', 'Zulu'] },
  { column: 'nameNull', nullable: true, operands: ['alpha', '', 'zulu'] },
  { column: 'count', nullable: false, operands: [0, 7, -1] },
  { column: 'countNull', nullable: true, operands: [0, 7, 100] },
  { column: 'big', nullable: false, operands: [0n, 1n, SAFE, BEYOND] },
  { column: 'bigNull', nullable: true, operands: [0n, 2n, SAFE, BEYOND] },
  { column: 'at', nullable: false, operands: [D0, D1, D2] },
  { column: 'atNull', nullable: true, operands: [D0, D1, D2] },
];

const COMPARISONS: readonly string[] = ['lt', 'lte', 'gt', 'gte'];

export function label(value: unknown): string {
  if (value === null) {
    return 'null';
  }
  if (typeof value === 'bigint') {
    return `${value}n`;
  }
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (Array.isArray(value)) {
    return `[${value.map(label).join('|')}]`;
  }
  if (typeof value === 'string') {
    return `'${value}'`;
  }
  return String(value);
}

function scalarCases(): WhereCase[] {
  const cases: WhereCase[] = [];
  for (const spec of COLUMNS) {
    for (const operand of spec.operands) {
      cases.push({ id: `${spec.column}/shorthand/${label(operand)}`, where: { [spec.column]: operand } });
      cases.push({
        id: `${spec.column}/equals/${label(operand)}`,
        where: { [spec.column]: { equals: operand } },
      });
      cases.push({ id: `${spec.column}/not/${label(operand)}`, where: { [spec.column]: { not: operand } } });
      for (const comparison of COMPARISONS) {
        cases.push({
          id: `${spec.column}/${comparison}/${label(operand)}`,
          where: { [spec.column]: { [comparison]: operand } },
        });
      }
    }
    if (spec.nullable) {
      cases.push({ id: `${spec.column}/equals/null`, where: { [spec.column]: { equals: null } } });
      cases.push({ id: `${spec.column}/shorthand/null`, where: { [spec.column]: null } });
      cases.push({ id: `${spec.column}/not/null`, where: { [spec.column]: { not: null } } });
      for (const comparison of COMPARISONS) {
        cases.push({
          id: `${spec.column}/${comparison}/null`,
          where: { [spec.column]: { [comparison]: null } },
        });
      }
    }
    const pair = spec.operands.slice(0, 2);
    cases.push({ id: `${spec.column}/in/${label(pair)}`, where: { [spec.column]: { in: pair } } });
    cases.push({ id: `${spec.column}/notIn/${label(pair)}`, where: { [spec.column]: { notIn: pair } } });
    cases.push({ id: `${spec.column}/in/[]`, where: { [spec.column]: { in: [] } } });
    cases.push({ id: `${spec.column}/notIn/[]`, where: { [spec.column]: { notIn: [] } } });
    cases.push({
      id: `${spec.column}/multi/gte+lte`,
      where: { [spec.column]: { gte: spec.operands[0], lte: spec.operands[1] } },
    });
  }
  return cases;
}

const RELATION_CASES: readonly WhereCase[] = [
  { id: 'related/is/tag-red', where: { related: { is: { tag: 'red' } } } },
  { id: 'related/is/tag-empty', where: { related: { is: { tag: '' } } } },
  { id: 'related/is/tagNull-null', where: { related: { is: { tagNull: null } } } },
  { id: 'related/is/tagNull-not-null', where: { related: { is: { tagNull: { not: null } } } } },
  { id: 'related/is/num-gt-0', where: { related: { is: { num: { gt: 0 } } } } },
  { id: 'related/is/num-lt-1', where: { related: { is: { num: { lt: 1 } } } } },
  { id: 'related/is/numNull-lt-1', where: { related: { is: { numNull: { lt: 1 } } } } },
  { id: 'related/is/numNull-gte-0', where: { related: { is: { numNull: { gte: 0 } } } } },
  { id: 'related/is/big-gte-safe', where: { related: { is: { big: { gte: SAFE } } } } },
  { id: 'related/is/at-lt-D2', where: { related: { is: { at: { lt: D2 } } } } },
  { id: 'related/is/empty', where: { related: { is: {} } } },
  { id: 'related/is/tag-in', where: { related: { is: { tag: { in: ['red', 'blue'] } } } } },
  { id: 'related/is/tag-notIn', where: { related: { is: { tag: { notIn: ['red'] } } } } },
  { id: 'related/is/nested-NOT', where: { related: { is: { NOT: { tag: 'red' } } } } },
  { id: 'related/isNot/tag-red', where: { related: { isNot: { tag: 'red' } } } },
  { id: 'related/isNot/tag-empty', where: { related: { isNot: { tag: '' } } } },
  { id: 'related/isNot/tagNull-null', where: { related: { isNot: { tagNull: null } } } },
  { id: 'related/isNot/tagNull-not-null', where: { related: { isNot: { tagNull: { not: null } } } } },
  { id: 'related/isNot/num-gt-0', where: { related: { isNot: { num: { gt: 0 } } } } },
  { id: 'related/isNot/numNull-lt-1', where: { related: { isNot: { numNull: { lt: 1 } } } } },
  { id: 'related/isNot/empty', where: { related: { isNot: {} } } },
  { id: 'related/isNot/tag-in', where: { related: { isNot: { tag: { in: ['red', 'blue'] } } } } },
  { id: 'related/isNot/nested-NOT', where: { related: { isNot: { NOT: { tag: 'red' } } } } },
  { id: 'related/is+isNot', where: { related: { is: { num: { gte: -5 } }, isNot: { tag: 'red' } } } },
  { id: 'related/is-null', where: { related: { is: null } } },
  { id: 'related/isNot-null', where: { related: { isNot: null } } },
  { id: 'related/bare-null', where: { related: null } },
];

const COMBINATOR_CASES: readonly WhereCase[] = [
  { id: 'combinator/AND-two', where: { AND: [{ count: { gt: 0 } }, { name: 'alpha' }] } },
  { id: 'combinator/AND-null-branch', where: { AND: [{ countNull: { lt: 5 } }, { name: { not: '' } }] } },
  { id: 'combinator/OR-two', where: { OR: [{ countNull: { lt: 5 } }, { name: 'beta' }] } },
  { id: 'combinator/OR-null-branches', where: { OR: [{ countNull: { lt: 5 } }, { atNull: { gt: D0 } }] } },
  { id: 'combinator/NOT-array', where: { NOT: [{ count: 0 }, { count: 7 }] } },
  { id: 'combinator/NOT-object', where: { NOT: { count: 0 } } },
  { id: 'combinator/NOT-nested-null', where: { NOT: { countNull: { lt: 5 } } } },
  { id: 'combinator/AND-empty', where: { AND: [] } },
  { id: 'combinator/OR-empty', where: { OR: [] } },
  { id: 'combinator/NOT-empty', where: { NOT: [] } },
  { id: 'combinator/empty-object', where: {} },
  { id: 'combinator/AND-of-OR', where: { AND: [{ OR: [{ count: 0 }, { count: 7 }] }, { nameNull: { not: null } }] } },
  { id: 'combinator/OR-relation', where: { OR: [{ related: { is: { tag: 'red' } } }, { count: 100 }] } },
];

export const BARE_CASES: readonly WhereCase[] = [
  ...scalarCases(),
  ...RELATION_CASES,
  ...COMBINATOR_CASES,
];

export const ALL_CASES: readonly WhereCase[] = [
  ...BARE_CASES,
  ...BARE_CASES.map((entry) => ({ id: `NOT(${entry.id})`, where: { NOT: entry.where } })),
];

export const NULLABLE_COLUMNS: readonly string[] = [
  'nameNull',
  'countNull',
  'bigNull',
  'atNull',
  'tagNull',
  'numNull',
];

export function mentionsNullableColumn(node: unknown): boolean {
  if (Array.isArray(node)) {
    return node.some(mentionsNullableColumn);
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return false;
  }
  return Object.entries(node).some(
    ([key, value]) => NULLABLE_COLUMNS.includes(key) || mentionsNullableColumn(value),
  );
}

export function isIdentityBranch(node: unknown): boolean {
  if (typeof node !== 'object' || node === null || node instanceof Date || Array.isArray(node)) {
    return false;
  }
  const entries = Object.entries(node);
  if (entries.length === 0) {
    return true;
  }
  return entries.every(
    ([key, value]) => (key === 'AND' || key === 'NOT') && Array.isArray(value) && value.length === 0,
  );
}

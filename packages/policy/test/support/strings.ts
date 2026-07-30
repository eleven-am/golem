import { PolicyDatamodel, SqlDialect, SqlParameter } from '../../src/index';
import { WhereCase } from './matrix';
import { Row } from './measure';

function scalar(name: string, dbName: string, type: string) {
  return { name, dbName, kind: 'scalar' as const, type, isList: false };
}

export const STRING_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'StringRow',
      dbName: 'string_rows',
      fields: [
        scalar('id', 'row_pk', 'Int'),
        scalar('text', 'text_value', 'String'),
        scalar('textNull', 'text_null', 'String'),
      ],
    },
  ],
};

export interface StringSeed {
  readonly id: number;
  readonly text: string;
  readonly textNull: string | null;
}

export const STRING_ROWS: readonly StringSeed[] = [
  { id: 1, text: 'alpha', textNull: 'alpha' },
  { id: 2, text: 'Alpha', textNull: null },
  { id: 3, text: 'a%b', textNull: 'a%b' },
  { id: 4, text: 'axb', textNull: null },
  { id: 5, text: 'a_b', textNull: null },
  { id: 6, text: 'a\\b', textNull: null },
  { id: 7, text: '100%', textNull: null },
  { id: 8, text: '', textNull: '' },
  { id: 9, text: 'Ångström', textNull: 'Ångström' },
  { id: 10, text: 'ångström', textNull: null },
  { id: 11, text: 'ÉCOLE', textNull: null },
  { id: 12, text: 'école', textNull: null },
  { id: 13, text: 'x👍y', textNull: null },
  { id: 14, text: 'percent%_end', textNull: null },
  { id: 15, text: 'ALPHA', textNull: 'ALPHA' },
  { id: 16, text: 'salphax', textNull: null },
];

export function stringRows(): readonly Row[] {
  return STRING_ROWS.map((seed) => ({ ...seed }));
}

export const STRING_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "string_rows"`,
  `CREATE TABLE "string_rows" (
     "row_pk" INTEGER PRIMARY KEY,
     "text_value" TEXT NOT NULL,
     "text_null" TEXT
   )`,
];

export interface Statement {
  readonly sql: string;
  readonly parameters: readonly SqlParameter[];
}

export function stringInserts(dialect: SqlDialect): readonly Statement[] {
  const columns = ['row_pk', 'text_value', 'text_null'];
  const quoted = columns.map((column) => dialect.quoteIdentifier(column)).join(', ');
  const placeholders = columns.map((_column, index) => dialect.placeholder(index + 1)).join(', ');
  return STRING_ROWS.map((seed) => ({
    sql: `INSERT INTO ${dialect.quoteIdentifier('string_rows')} (${quoted}) VALUES (${placeholders})`,
    parameters: [seed.id, seed.text, seed.textNull],
  }));
}

const OPERATORS: readonly string[] = ['contains', 'startsWith', 'endsWith'];

const OPERANDS: readonly string[] = [
  'alpha',
  'ALPHA',
  'a',
  'A',
  '%',
  '_',
  'a%b',
  'a_b',
  '%b',
  'b',
  '\\',
  'a\\b',
  '100%',
  '',
  'ström',
  'Å',
  'ÉCOLE',
  'école',
  '👍',
  'x👍y',
  'nowhere',
];

const COLUMNS: readonly string[] = ['text', 'textNull'];

function operandLabel(operand: string): string {
  return operand === '' ? '<empty>' : operand;
}

function polarities(cases: readonly WhereCase[]): readonly WhereCase[] {
  return [...cases, ...cases.map((entry) => ({ id: `NOT(${entry.id})`, where: { NOT: entry.where } }))];
}

function sensitiveCases(): WhereCase[] {
  const cases: WhereCase[] = [];
  for (const column of COLUMNS) {
    for (const operator of OPERATORS) {
      for (const operand of OPERANDS) {
        cases.push({
          id: `${column}/${operator}/${operandLabel(operand)}`,
          where: { [column]: { [operator]: operand } },
        });
      }
      cases.push({
        id: `${column}/${operator}/null-operand`,
        where: { [column]: { [operator]: null } },
      });
      cases.push({
        id: `${column}/${operator}/default-mode`,
        where: { [column]: { [operator]: 'a', mode: 'default' } },
      });
    }
    cases.push({
      id: `${column}/conjunction`,
      where: { [column]: { startsWith: 'a', endsWith: 'b' } },
    });
    cases.push({
      id: `${column}/with-equals`,
      where: { [column]: { contains: 'a', not: 'alpha' } },
    });
    cases.push({ id: `${column}/mode-only`, where: { [column]: { mode: 'insensitive' } } });
  }
  cases.push({
    id: 'or/contains-either',
    where: { OR: [{ text: { contains: '%' } }, { textNull: { endsWith: 'a' } }] },
  });
  return cases;
}

const FOLDED_SCALAR_OPERATORS: readonly string[] = ['equals', 'not', 'lt', 'lte', 'gt', 'gte'];

const FOLDED_LIST_OPERATORS: readonly string[] = ['in', 'notIn'];

const FOLDED_OPERANDS: readonly string[] = [
  'alpha',
  'ALPHA',
  'Alpha',
  'A',
  'a%b',
  'A%B',
  'a_b',
  '100%',
  'ÉCOLE',
  'école',
  'ÅNGSTRÖM',
  'Ångström',
  'Ström',
  'x👍y',
  '0',
  '',
];

const FOLDED_LISTS: readonly (readonly string[])[] = [
  ['ALPHA'],
  ['ALPHA', 'ZULU'],
  ['a%b', '100%'],
  ['ÉCOLE', 'école'],
  [],
];

function insensitiveCases(): WhereCase[] {
  const cases: WhereCase[] = [];
  for (const column of COLUMNS) {
    for (const operator of OPERATORS) {
      for (const operand of ['alpha', 'ALPHA', 'A', 'a%b', 'ÉCOLE', 'école', 'ÅNGSTRÖM', 'Ström', '']) {
        cases.push({
          id: `${column}/${operator}/insensitive/${operandLabel(operand)}`,
          where: { [column]: { [operator]: operand, mode: 'insensitive' } },
        });
      }
    }
    for (const operator of FOLDED_SCALAR_OPERATORS) {
      for (const operand of FOLDED_OPERANDS) {
        cases.push({
          id: `${column}/${operator}/insensitive/${operandLabel(operand)}`,
          where: { [column]: { [operator]: operand, mode: 'insensitive' } },
        });
      }
    }
    for (const operator of FOLDED_LIST_OPERATORS) {
      for (const list of FOLDED_LISTS) {
        cases.push({
          id: `${column}/${operator}/insensitive/[${list.map(operandLabel).join('|')}]`,
          where: { [column]: { [operator]: list, mode: 'insensitive' } },
        });
      }
    }
    cases.push({
      id: `${column}/equals-null/insensitive`,
      where: { [column]: { equals: null, mode: 'insensitive' } },
    });
    cases.push({
      id: `${column}/not-null/insensitive`,
      where: { [column]: { not: null, mode: 'insensitive' } },
    });
    cases.push({
      id: `${column}/gte+lte/insensitive`,
      where: { [column]: { gte: 'A', lte: 'Z', mode: 'insensitive' } },
    });
    cases.push({
      id: `${column}/contains+gt/insensitive`,
      where: { [column]: { contains: 'A', gt: '0', mode: 'insensitive' } },
    });
  }
  return cases;
}

export const STRING_CASES: readonly WhereCase[] = polarities(sensitiveCases());

export const STRING_INSENSITIVE_CASES: readonly WhereCase[] = polarities(insensitiveCases());

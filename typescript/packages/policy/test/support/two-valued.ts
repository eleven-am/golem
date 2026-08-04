import {
  AnyOperatorName,
  OPERATORS,
  OperatorDefinition,
  OperatorHooks,
  OperatorName,
  PolicyDatamodel,
  SCALAR_LIST_OPERATORS,
  SUPPORTED_OPERATORS,
  SUPPORTED_SCALAR_LIST_OPERATORS,
  ScalarListOperatorName,
  SqlDialect,
  SqlFragment,
  compileConditions,
  createDatamodelSqlScope,
  isFoldedOperatorName,
  renderSql,
  scalarFilterEntries,
} from '../../src/index';
import { D0, D1 } from './rows';

export const TWO_VALUED_TABLE = 'two_valued_rows';

export const TWO_VALUED_ALIAS = 't0';

export const TWO_VALUED_MODEL = 'Node';

export const TWO_VALUED_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: TWO_VALUED_MODEL,
      dbName: TWO_VALUED_TABLE,
      fields: [
        { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
        { name: 'text', dbName: 'text_value', kind: 'scalar', type: 'String', isList: false },
        { name: 'amount', dbName: 'amount_value', kind: 'scalar', type: 'Int', isList: false },
        { name: 'moment', dbName: 'moment_value', kind: 'scalar', type: 'DateTime', isList: false },
        { name: 'tags', dbName: 'tag_list', kind: 'scalar', type: 'String', isList: true },
        { name: 'parentId', dbName: 'parent_ref', kind: 'scalar', type: 'Int', isList: false },
        {
          name: 'parent',
          kind: 'object',
          type: TWO_VALUED_MODEL,
          isList: false,
          relationName: 'NodeSelf',
          relationFromFields: ['parentId'],
          relationToFields: ['id'],
        },
        {
          name: 'children',
          kind: 'object',
          type: TWO_VALUED_MODEL,
          isList: true,
          relationName: 'NodeSelf',
        },
      ],
    },
  ],
};

export const TWO_VALUED_ROW_IDS: readonly number[] = [1, 2, 3, 4];

export const TWO_VALUED_SQLITE_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "${TWO_VALUED_TABLE}"`,
  `CREATE TABLE "${TWO_VALUED_TABLE}" (
     "id" INTEGER PRIMARY KEY,
     "text_value" TEXT,
     "amount_value" INTEGER,
     "moment_value" DATETIME,
     "parent_ref" INTEGER
   )`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (1, 'alpha', 1, '${D0.toISOString()}', NULL)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (2, NULL, NULL, NULL, 1)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (3, 'beta', 7, '${D1.toISOString()}', NULL)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (4, NULL, NULL, NULL, 3)`,
];

export const TWO_VALUED_POSTGRES_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "${TWO_VALUED_TABLE}"`,
  `CREATE TABLE "${TWO_VALUED_TABLE}" (
     "id" INTEGER PRIMARY KEY,
     "text_value" TEXT,
     "amount_value" INTEGER,
     "moment_value" TIMESTAMP(3),
     "tag_list" TEXT[],
     "parent_ref" INTEGER
   )`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (1, 'alpha', 1, '${D0.toISOString()}', '{a}', NULL)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (2, NULL, NULL, NULL, NULL, 1)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (3, 'beta', 7, '${D1.toISOString()}', '{}', NULL)`,
  `INSERT INTO "${TWO_VALUED_TABLE}" VALUES (4, NULL, NULL, NULL, NULL, 3)`,
];

export interface OperatorProbe {
  readonly id: string;
  readonly operator: AnyOperatorName;
  readonly field: string;
  readonly operand: unknown;
  readonly scalarList: boolean;
}

function probe(id: string, operator: OperatorName, field: string, operand: unknown): OperatorProbe {
  return { id, operator, field, operand, scalarList: false };
}

function listProbe(
  id: string,
  operator: ScalarListOperatorName,
  operand: unknown,
): OperatorProbe {
  return { id, operator, field: 'tags', operand, scalarList: true };
}

export const OPERATOR_PROBES: readonly OperatorProbe[] = [
  probe('equals/text', 'equals', 'text', 'alpha'),
  probe('equals/null', 'equals', 'text', null),
  probe('equals/date', 'equals', 'moment', D0),
  probe('not/text', 'not', 'text', 'alpha'),
  probe('not/null', 'not', 'text', null),
  probe('in/two', 'in', 'text', ['alpha', 'beta']),
  probe('in/empty', 'in', 'text', []),
  probe('notIn/one', 'notIn', 'text', ['alpha']),
  probe('notIn/empty', 'notIn', 'text', []),
  probe('lt/amount', 'lt', 'amount', 5),
  probe('lt/null', 'lt', 'amount', null),
  probe('lte/amount', 'lte', 'amount', 5),
  probe('gt/amount', 'gt', 'amount', 5),
  probe('gt/moment', 'gt', 'moment', D0),
  probe('gte/amount', 'gte', 'amount', 5),
  probe('contains/text', 'contains', 'text', 'a'),
  probe('contains/empty', 'contains', 'text', ''),
  probe('startsWith/text', 'startsWith', 'text', 'a'),
  probe('endsWith/text', 'endsWith', 'text', 'a'),
  probe('is/parent', 'is', 'parent', { text: 'alpha' }),
  probe('is/empty', 'is', 'parent', {}),
  probe('is/null', 'is', 'parent', null),
  probe('isNot/parent', 'isNot', 'parent', { text: 'alpha' }),
  probe('isNot/empty', 'isNot', 'parent', {}),
  probe('isNot/null', 'isNot', 'parent', null),
  probe('some/children', 'some', 'children', { text: 'alpha' }),
  probe('some/empty', 'some', 'children', {}),
  probe('every/children', 'every', 'children', { text: 'alpha' }),
  probe('every/nullable-leaf', 'every', 'children', { amount: { lt: 5 } }),
  probe('every/empty', 'every', 'children', {}),
  probe('none/children', 'none', 'children', { text: 'alpha' }),
  probe('none/empty', 'none', 'children', {}),
];

function foldedProbe(id: string, operator: OperatorName, operand: unknown): OperatorProbe {
  const entries = scalarFilterEntries({ [operator]: operand, mode: 'insensitive' });
  return { id, operator, field: 'text', operand: entries[0]![1], scalarList: false };
}

export const INSENSITIVE_PROBES: readonly OperatorProbe[] = [
  foldedProbe('equals/insensitive', 'equals', 'ALPHA'),
  foldedProbe('equals/insensitive-null', 'equals', null),
  foldedProbe('not/insensitive', 'not', 'ALPHA'),
  foldedProbe('not/insensitive-null', 'not', null),
  foldedProbe('in/insensitive', 'in', ['ALPHA', 'BETA']),
  foldedProbe('in/insensitive-empty', 'in', []),
  foldedProbe('notIn/insensitive', 'notIn', ['ALPHA']),
  foldedProbe('notIn/insensitive-empty', 'notIn', []),
  foldedProbe('lt/insensitive', 'lt', 'BETA'),
  foldedProbe('lte/insensitive', 'lte', 'BETA'),
  foldedProbe('gt/insensitive', 'gt', 'ALPHA'),
  foldedProbe('gte/insensitive', 'gte', 'ALPHA'),
  foldedProbe('contains/insensitive', 'contains', 'A'),
  foldedProbe('contains/insensitive-empty', 'contains', ''),
  foldedProbe('startsWith/insensitive', 'startsWith', 'A'),
  foldedProbe('endsWith/insensitive', 'endsWith', 'A'),
];

export const DECLARED_FOLDED_OPERATORS: readonly string[] = [...SUPPORTED_OPERATORS]
  .filter(isFoldedOperatorName)
  .sort();

export const SCALAR_LIST_PROBES: readonly OperatorProbe[] = [
  listProbe('has/tag', 'has', 'a'),
  listProbe('hasEvery/one', 'hasEvery', ['a']),
  listProbe('hasEvery/empty', 'hasEvery', []),
  listProbe('hasSome/one', 'hasSome', ['a']),
  listProbe('hasSome/empty', 'hasSome', []),
  listProbe('isEmpty/true', 'isEmpty', true),
  listProbe('isEmpty/false', 'isEmpty', false),
  listProbe('equals/one', 'equals', ['a']),
  listProbe('equals/null', 'equals', null),
];

export function definitionOf(entry: OperatorProbe): OperatorDefinition {
  return entry.scalarList
    ? SCALAR_LIST_OPERATORS[entry.operator as ScalarListOperatorName]
    : OPERATORS[entry.operator as OperatorName];
}

export function coveredOperators(probes: readonly OperatorProbe[]): readonly string[] {
  return [...new Set(probes.map((entry) => entry.operator))].sort();
}

export const DECLARED_OPERATORS: readonly string[] = [...SUPPORTED_OPERATORS].sort();

export const DECLARED_SCALAR_LIST_OPERATORS: readonly string[] = [
  ...SUPPORTED_SCALAR_LIST_OPERATORS,
].sort();

const HOOKS: OperatorHooks = {
  validateConditions: () => undefined,
  compileConditions: () => () => true,
  renderConditions: (conditions, scope) =>
    compileConditions(conditions, {
      datamodel: TWO_VALUED_DATAMODEL,
      model: TWO_VALUED_MODEL,
    }).toSql(scope),
};

export function renderProbe(entry: OperatorProbe, dialect: SqlDialect): SqlFragment {
  const scope = createDatamodelSqlScope({
    datamodel: TWO_VALUED_DATAMODEL,
    model: TWO_VALUED_MODEL,
    alias: TWO_VALUED_ALIAS,
  });
  const node = definitionOf(entry).renderSql(
    entry.operand,
    { field: entry.field, scope, path: [entry.field] },
    HOOKS,
  );
  return renderSql(node, dialect);
}

export type PredicateShape = 'unknown-count' | 'guarded' | 'naive';

export function statementFor(shape: PredicateShape, predicate: string, dialect: SqlDialect): string {
  const quote = (identifier: string): string => dialect.quoteIdentifier(identifier);
  const from = `FROM ${quote(TWO_VALUED_TABLE)} AS ${quote(TWO_VALUED_ALIAS)}`;
  if (shape === 'unknown-count') {
    return `SELECT ${quote(TWO_VALUED_ALIAS)}.${quote('id')} AS ${quote('id')} ${from} WHERE (${predicate}) IS NULL`;
  }
  const test = shape === 'guarded' ? `(${predicate}) IS NOT TRUE` : `NOT (${predicate})`;
  return `SELECT ${quote(TWO_VALUED_ALIAS)}.${quote('id')} AS ${quote('id')} ${from} WHERE ${test}`;
}

export const THREE_VALUED_CONTROL = `"${TWO_VALUED_ALIAS}"."amount_value" < 5`;

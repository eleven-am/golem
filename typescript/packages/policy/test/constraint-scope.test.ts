import {
  ConstraintNodeOptions,
  DatamodelSqlScope,
  PolicyDatamodel,
  SqlRenderError,
  SqlScope,
  UnsupportedConditionError,
  createDatamodelSqlScope,
  postgresDialect,
  renderConstraintNode,
  renderConstraintSql,
  renderSql,
  sqlIdentifier,
} from '../src/index';

type Assert<T extends true> = T;

type Assignable<From, To> = [From] extends [To] ? true : false;

type BareScopeIsNotAConstraintScope = Assert<
  Assignable<SqlScope, ConstraintNodeOptions['scope']> extends false ? true : false
>;

type ConstraintScopeCarriesADatamodel = Assert<
  Assignable<ConstraintNodeOptions['scope'], DatamodelSqlScope> extends true ? true : false
>;

type ConstraintScopeDatamodelIsRequired = Assert<
  Assignable<undefined, DatamodelSqlScope['datamodel']> extends false ? true : false
>;

type ConstraintOptionsCarryNoContextEscape = Assert<
  'context' extends keyof ConstraintNodeOptions ? false : true
>;

const PROVEN: readonly [
  BareScopeIsNotAConstraintScope,
  ConstraintScopeCarriesADatamodel,
  ConstraintScopeDatamodelIsRequired,
  ConstraintOptionsCarryNoContextEscape,
] = [true, true, true, true];

const DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Post',
      dbName: 'post_rows',
      fields: [
        { name: 'id', dbName: 'post_pk', kind: 'scalar', type: 'Int', isList: false },
        { name: 'title', dbName: 'title_text', kind: 'scalar', type: 'String', isList: false },
        { name: 'views', dbName: 'view_count', kind: 'scalar', type: 'Int', isList: false },
      ],
    },
  ],
};

const ANONYMOUS: SqlScope = {
  column: (field) => sqlIdentifier(['t0', field]),
  relation: () => undefined,
};

describe('a constraint can only be rendered against a scope that knows its datamodel', () => {
  it('proves the unsafe options shape does not typecheck', () => {
    expect(PROVEN).toEqual([true, true, true, true]);
  });

  it('refuses a hand-built scope that cannot name the column types', () => {
    expect(() =>
      renderConstraintNode({ views: { contains: '2' } }, { scope: ANONYMOUS as never, absent: 'deny-all' }),
    ).toThrow(SqlRenderError);
  });

  it('refuses a datamodel scope whose datamodel was stripped back off', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    const stripped = { ...scope, datamodel: undefined } as unknown as DatamodelSqlScope;
    expect(() => renderConstraintNode({ title: 'x' }, { scope: stripped, absent: 'deny-all' })).toThrow(
      SqlRenderError,
    );
  });

  it('refuses a string operator on an Int column wherever the render starts', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    expect(() => renderConstraintNode({ views: { contains: '2' } }, { scope, absent: 'deny-all' })).toThrow(
      UnsupportedConditionError,
    );
    expect(() =>
      renderConstraintSql(
        { views: { contains: '2' } },
        { datamodel: DATAMODEL, model: 'Post', dialect: postgresDialect, absent: 'deny-all' },
      ),
    ).toThrow(UnsupportedConditionError);
  });

  it('renders a String column through the same entry point', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    const node = renderConstraintNode({ title: { contains: 'x' } }, { scope, absent: 'deny-all' });
    expect(renderSql(node, postgresDialect).parameters).toEqual(['%x%']);
  });
});

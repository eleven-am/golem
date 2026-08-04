import { compileConditions, evaluateConditions, validateConditions } from './compile';
import { PolicyDatamodel } from './datamodel';
import { SqlRenderError } from './errors';
import { SCALAR_LIST_OPERATORS, SUPPORTED_SCALAR_LIST_OPERATORS, isScalarListOperatorName } from './operators';
import { createDatamodelSqlScope } from './scope';
import { SqlDialect, postgresDialect, renderSql, sqliteDialect } from './sql';

const DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Row',
      dbName: 'Row',
      fields: [
        { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
        { name: 'tags', dbName: 'tags', kind: 'scalar', type: 'String', isList: true },
        { name: 'scores', dbName: 'scores', kind: 'scalar', type: 'Int', isList: true },
        { name: 'name', dbName: 'name', kind: 'scalar', type: 'String', isList: false },
      ],
    },
  ],
};

const CONTEXT = { datamodel: DATAMODEL, model: 'Row' };

function evaluate(where: unknown, value: unknown): boolean {
  return evaluateConditions(where, { tags: value }, CONTEXT);
}

function sqlFor(where: unknown, dialect: SqlDialect = postgresDialect): string {
  const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Row' });
  return renderSql(compileConditions(where, CONTEXT).toSql(scope), dialect).text;
}

function parametersFor(where: unknown): readonly unknown[] {
  const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Row' });
  return renderSql(compileConditions(where, CONTEXT).toSql(scope), postgresDialect).parameters;
}

type Case = [string, unknown, unknown, boolean];

const hasCases: Case[] = [
  ['has finds an element', { tags: { has: 'a' } }, ['a'], true],
  ['has finds an element among many', { tags: { has: 'a' } }, ['b', 'a', 'c'], true],
  ['has finds an element among duplicates', { tags: { has: 'a' } }, ['a', 'a'], true],
  ['has misses an absent element', { tags: { has: 'z' } }, ['a'], false],
  ['has misses on an empty list', { tags: { has: 'a' } }, [], false],
  ['has misses on a null column', { tags: { has: 'a' } }, null, false],
  ['has misses on an absent column', { tags: { has: 'a' } }, undefined, false],
  ['has finds the empty string', { tags: { has: '' } }, [''], true],
  ['has separates the empty string from an empty list', { tags: { has: '' } }, [], false],
  ['has ignores null elements when looking for a value', { tags: { has: 'a' } }, ['a', null], true],
  ['has null never matches a list holding null', { tags: { has: null } }, ['a', null], false],
  ['has null never matches an empty list', { tags: { has: null } }, [], false],
  ['has null never matches a null column', { tags: { has: null } }, null, false],
  ['has undefined never matches', { tags: { has: undefined } }, ['a'], false],
  ['has does not treat a scalar column value as a list', { tags: { has: 'a' } }, 'a', false],
];

const hasEveryCases: Case[] = [
  ['hasEvery finds every element', { tags: { hasEvery: ['a', 'b'] } }, ['a', 'b'], true],
  ['hasEvery ignores order', { tags: { hasEvery: ['a', 'b'] } }, ['b', 'a'], true],
  ['hasEvery ignores extra elements', { tags: { hasEvery: ['a'] } }, ['a', 'b'], true],
  ['hasEvery ignores repeated operands', { tags: { hasEvery: ['a', 'a'] } }, ['a'], true],
  ['hasEvery misses when one element is absent', { tags: { hasEvery: ['a', 'z'] } }, ['a', 'b'], false],
  ['hasEvery misses on a null column', { tags: { hasEvery: ['a'] } }, null, false],
  ['hasEvery of nothing matches a populated list', { tags: { hasEvery: [] } }, ['a'], true],
  ['hasEvery of nothing matches an empty list', { tags: { hasEvery: [] } }, [], true],
  ['hasEvery of nothing does not match a null column', { tags: { hasEvery: [] } }, null, false],
  ['hasEvery of nothing does not match an absent column', { tags: { hasEvery: [] } }, undefined, false],
  ['hasEvery of nothing does not match a scalar column value', { tags: { hasEvery: [] } }, 'a', false],
];

const hasSomeCases: Case[] = [
  ['hasSome finds one element', { tags: { hasSome: ['a', 'z'] } }, ['a'], true],
  ['hasSome finds the last element', { tags: { hasSome: ['y', 'z'] } }, ['z'], true],
  ['hasSome misses when no element is present', { tags: { hasSome: ['y', 'z'] } }, ['a'], false],
  ['hasSome misses on an empty list', { tags: { hasSome: ['a'] } }, [], false],
  ['hasSome misses on a null column', { tags: { hasSome: ['a'] } }, null, false],
  ['hasSome of nothing matches nothing', { tags: { hasSome: [] } }, ['a'], false],
  ['hasSome of nothing does not match an empty list', { tags: { hasSome: [] } }, [], false],
  ['hasSome of nothing does not match a null column', { tags: { hasSome: [] } }, null, false],
  ['hasSome of nothing is not the complement of hasEvery of nothing', { tags: { hasSome: [] } }, ['a'], false],
];

const isEmptyCases: Case[] = [
  ['isEmpty true matches an empty list', { tags: { isEmpty: true } }, [], true],
  ['isEmpty true misses a populated list', { tags: { isEmpty: true } }, ['a'], false],
  ['isEmpty true misses a null column', { tags: { isEmpty: true } }, null, false],
  ['isEmpty true misses an absent column', { tags: { isEmpty: true } }, undefined, false],
  ['isEmpty false matches a populated list', { tags: { isEmpty: false } }, ['a'], true],
  ['isEmpty false misses an empty list', { tags: { isEmpty: false } }, [], false],
  ['isEmpty false misses a null column', { tags: { isEmpty: false } }, null, false],
  ['isEmpty false misses an absent column', { tags: { isEmpty: false } }, undefined, false],
  ['isEmpty true matches a list holding only null', { tags: { isEmpty: true } }, [null], false],
];

const equalsCases: Case[] = [
  ['equals matches the same elements in the same order', { tags: { equals: ['a', 'b'] } }, ['a', 'b'], true],
  ['equals is order significant', { tags: { equals: ['a', 'b'] } }, ['b', 'a'], false],
  ['equals is duplicate significant', { tags: { equals: ['a'] } }, ['a', 'a'], false],
  ['equals matches duplicates when they line up', { tags: { equals: ['a', 'a'] } }, ['a', 'a'], true],
  ['equals misses a longer list', { tags: { equals: ['a'] } }, ['a', 'b'], false],
  ['equals misses a shorter list', { tags: { equals: ['a', 'b'] } }, ['a'], false],
  ['equals of nothing matches an empty list', { tags: { equals: [] } }, [], true],
  ['equals of nothing misses a populated list', { tags: { equals: [] } }, ['a'], false],
  ['equals of nothing misses a null column', { tags: { equals: [] } }, null, false],
  ['equals null matches a null column', { tags: { equals: null } }, null, true],
  ['equals null matches an absent column', { tags: { equals: null } }, undefined, true],
  ['equals null misses an empty list', { tags: { equals: null } }, [], false],
  ['equals null misses a populated list', { tags: { equals: null } }, ['a'], false],
  ['equals misses a null element against a value', { tags: { equals: ['a', 'b'] } }, ['a', null], false],
];

describe('scalar list operators evaluate rows', () => {
  const all = [...hasCases, ...hasEveryCases, ...hasSomeCases, ...isEmptyCases, ...equalsCases];
  it.each(all)('%s', (_name, where, value, expected) => {
    expect(evaluate(where, value)).toBe(expected);
  });
});

describe('scalar list operators keep the empty operand honest', () => {
  it('never lets an empty hasSome grant a row', () => {
    for (const value of [[], ['a'], ['a', 'b'], null, undefined]) {
      expect(evaluate({ tags: { hasSome: [] } }, value)).toBe(false);
    }
    expect(sqlFor({ tags: { hasSome: [] } })).toBe('(1 = 0)');
  });

  it('lets an empty hasEvery match exactly the rows whose column is a list', () => {
    expect(sqlFor({ tags: { hasEvery: [] } })).toBe('("t0"."tags" IS NOT NULL)');
  });

  it('refuses a list filter that carries no operator at all', () => {
    const result = validateConditions({ tags: {} }, CONTEXT);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('exactly one');
  });

  it('refuses two list operators at once, as Prisma does', () => {
    const result = validateConditions({ tags: { has: 'a', isEmpty: false } }, CONTEXT);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('exactly one');
  });
});

describe('scalar list operators render Postgres', () => {
  it('renders has as a two-valued containment test', () => {
    expect(sqlFor({ tags: { has: 'a' } })).toBe('COALESCE($1 = ANY("t0"."tags"), FALSE)');
    expect(parametersFor({ tags: { has: 'a' } })).toEqual(['a']);
  });

  it('renders has null as a constant false', () => {
    expect(sqlFor({ tags: { has: null } })).toBe('(1 = 0)');
    expect(parametersFor({ tags: { has: null } })).toEqual([]);
  });

  it('renders hasEvery as a conjunction of containment tests', () => {
    expect(sqlFor({ tags: { hasEvery: ['a', 'b'] } })).toBe(
      '(COALESCE($1 = ANY("t0"."tags"), FALSE) AND COALESCE($2 = ANY("t0"."tags"), FALSE))',
    );
  });

  it('renders hasSome as a disjunction of containment tests', () => {
    expect(sqlFor({ tags: { hasSome: ['a', 'b'] } })).toBe(
      '(COALESCE($1 = ANY("t0"."tags"), FALSE) OR COALESCE($2 = ANY("t0"."tags"), FALSE))',
    );
  });

  it('renders isEmpty on cardinality, never on a null column', () => {
    expect(sqlFor({ tags: { isEmpty: true } })).toBe(
      '("t0"."tags" IS NOT NULL AND cardinality("t0"."tags") = 0)',
    );
    expect(sqlFor({ tags: { isEmpty: false } })).toBe(
      '("t0"."tags" IS NOT NULL AND cardinality("t0"."tags") > 0)',
    );
  });

  it('renders equals element by element so order and duplicates count', () => {
    expect(sqlFor({ tags: { equals: ['a', 'b'] } })).toBe(
      '("t0"."tags" IS NOT NULL AND cardinality("t0"."tags") = 2 AND ' +
        '"t0"."tags"[1] IS NOT DISTINCT FROM $1 AND "t0"."tags"[2] IS NOT DISTINCT FROM $2)',
    );
    expect(parametersFor({ tags: { equals: ['a', 'b'] } })).toEqual(['a', 'b']);
  });

  it('renders an empty equals as an empty list, not as a null column', () => {
    expect(sqlFor({ tags: { equals: [] } })).toBe(
      '("t0"."tags" IS NOT NULL AND cardinality("t0"."tags") = 0)',
    );
  });

  it('renders a null equals as a null column test', () => {
    expect(sqlFor({ tags: { equals: null } })).toBe('("t0"."tags" IS NULL)');
  });

  it('leaves a list column uncollated, because Postgres cannot collate an array', () => {
    expect(sqlFor({ tags: { has: 'a' } })).not.toContain('COLLATE');
  });

  it('keeps the scalar equals on a scalar column', () => {
    expect(sqlFor({ name: 'a' })).toBe('("t0"."name" COLLATE "C" IS NOT DISTINCT FROM $1)');
  });
});

describe('scalar list operators refuse providers without list columns', () => {
  const unsupported: readonly SqlDialect[] = [sqliteDialect];
  const filters: readonly unknown[] = [
    { tags: { has: 'a' } },
    { tags: { has: null } },
    { tags: { hasEvery: ['a'] } },
    { tags: { hasEvery: [] } },
    { tags: { hasSome: ['a'] } },
    { tags: { hasSome: [] } },
    { tags: { isEmpty: true } },
    { tags: { isEmpty: false } },
    { tags: { equals: ['a'] } },
    { tags: { equals: [] } },
    { tags: { equals: null } },
  ];

  for (const dialect of unsupported) {
    for (const where of filters) {
      const operator = Object.keys((where as { tags: Record<string, unknown> }).tags)[0]!;
      it(`refuses ${operator} on ${dialect.name}`, () => {
        expect(() => sqlFor(where, dialect)).toThrow(SqlRenderError);
        expect(() => sqlFor(where, dialect)).toThrow(dialect.name);
        expect(() => sqlFor(where, dialect)).toThrow('tags');
      });
    }
  }

  it('refuses an unknown dialect rather than guessing that it has list columns', () => {
    const invented: SqlDialect = { ...postgresDialect, name: 'cockroach' };
    expect(() => sqlFor({ tags: { has: 'a' } }, invented)).toThrow(SqlRenderError);
  });

  it('refuses the empty hasSome on sqlite instead of quietly answering false', () => {
    expect(() => sqlFor({ tags: { hasSome: [] } }, sqliteDialect)).toThrow(SqlRenderError);
  });

  it('refuses the empty hasEvery on sqlite instead of quietly answering true', () => {
    expect(() => sqlFor({ tags: { hasEvery: [] } }, sqliteDialect)).toThrow(SqlRenderError);
  });
});

describe('scalar list operators refuse operands Prisma refuses', () => {
  const rejected: readonly [string, unknown][] = [
    ['has takes a single value, never an array', { tags: { has: ['a'] } }],
    ['hasEvery takes an array', { tags: { hasEvery: 'a' } }],
    ['hasSome takes an array', { tags: { hasSome: 'a' } }],
    ['hasEvery refuses a null element', { tags: { hasEvery: ['a', null] } }],
    ['hasSome refuses a null element', { tags: { hasSome: [null] } }],
    ['equals refuses a null element', { tags: { equals: ['a', null] } }],
    ['equals takes an array or null, never a scalar', { tags: { equals: 'a' } }],
    ['isEmpty takes a boolean', { tags: { isEmpty: 'yes' } }],
    ['isEmpty refuses null', { tags: { isEmpty: null } }],
    ['a list column is not compared to a bare array', { tags: ['a'] }],
    ['a list column is not compared to a bare scalar', { tags: 'a' }],
    ['a list column has no contains operator', { tags: { contains: 'a' } }],
    ['a list column has no not operator', { tags: { not: { has: 'a' } } }],
    ['a list column has no in operator', { tags: { in: ['a'] } }],
    ['a scalar column has no has operator', { name: { has: 'a' } }],
    ['a scalar column has no isEmpty operator', { name: { isEmpty: true } }],
  ];

  it.each(rejected)('%s', (_name, where) => {
    expect(validateConditions(where, CONTEXT).supported).toBe(false);
  });

  it('names the column when a scalar column is filtered with a list operator', () => {
    const result = validateConditions({ name: { has: 'a' } }, CONTEXT);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('"Row.name" is not a list column');
  });
});

describe('scalar list operators without a datamodel', () => {
  it('evaluates has on a plain object, as the CASL matcher must', () => {
    expect(evaluateConditions({ tags: { has: 'a' } }, { tags: ['a', 'b'] })).toBe(true);
    expect(evaluateConditions({ tags: { has: 'z' } }, { tags: ['a', 'b'] })).toBe(false);
  });

  it('accepts every list-only operator as a rule condition', () => {
    for (const where of [
      { tags: { has: 'a' } },
      { tags: { hasEvery: ['a'] } },
      { tags: { hasSome: ['a'] } },
      { tags: { isEmpty: true } },
      { tags: { equals: ['a'] } },
    ]) {
      expect(validateConditions(where).supported).toBe(true);
    }
  });

  it('keeps a scalar equals scalar when no datamodel says otherwise', () => {
    expect(evaluateConditions({ tags: { equals: 'a' } }, { tags: 'a' })).toBe(true);
  });

  it('reads an array equals as a list equals', () => {
    expect(evaluateConditions({ tags: { equals: ['a', 'b'] } }, { tags: ['a', 'b'] })).toBe(true);
    expect(evaluateConditions({ tags: { equals: ['a', 'b'] } }, { tags: ['b', 'a'] })).toBe(false);
  });
});

describe('scalar list operators inside a relation hop', () => {
  const RELATED: PolicyDatamodel = {
    models: [
      {
        name: 'Post',
        dbName: 'Post',
        fields: [
          { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
          { name: 'authorId', dbName: 'authorId', kind: 'scalar', type: 'Int', isList: false },
          {
            name: 'author',
            kind: 'object',
            type: 'User',
            isList: false,
            relationName: 'PostToUser',
            relationFromFields: ['authorId'],
            relationToFields: ['id'],
          },
        ],
      },
      {
        name: 'User',
        dbName: 'User',
        fields: [
          { name: 'id', dbName: 'id', kind: 'scalar', type: 'Int', isList: false },
          { name: 'roles', dbName: 'roles', kind: 'scalar', type: 'String', isList: true },
          {
            name: 'posts',
            kind: 'object',
            type: 'Post',
            isList: true,
            relationName: 'PostToUser',
          },
        ],
      },
    ],
  };

  function relatedSql(where: unknown, model: string): string {
    const scope = createDatamodelSqlScope({ datamodel: RELATED, model });
    return renderSql(
      compileConditions(where, { datamodel: RELATED, model }).toSql(scope),
      postgresDialect,
    ).text;
  }

  it('renders a list filter through a to-one hop', () => {
    expect(relatedSql({ author: { is: { roles: { has: 'admin' } } } }, 'Post')).toContain(
      'COALESCE($1 = ANY("t0_1"."roles"), FALSE)',
    );
  });

  it('renders a list filter through a to-many hop', () => {
    expect(relatedSql({ posts: { some: { id: 1 } } }, 'User')).toContain('EXISTS');
    expect(relatedSql({ roles: { hasSome: ['admin', 'owner'] } }, 'User')).toBe(
      '(COALESCE($1 = ANY("t0"."roles"), FALSE) OR COALESCE($2 = ANY("t0"."roles"), FALSE))',
    );
  });

  it('evaluates a list filter through a to-one hop', () => {
    const where = { author: { is: { roles: { has: 'admin' } } } };
    const context = { datamodel: RELATED, model: 'Post' };
    expect(evaluateConditions(where, { author: { roles: ['admin'] } }, context)).toBe(true);
    expect(evaluateConditions(where, { author: { roles: [] } }, context)).toBe(false);
    expect(evaluateConditions(where, { author: null }, context)).toBe(false);
  });
});

describe('the scalar list operator table', () => {
  it('carries exactly the operators Prisma generates for a list field', () => {
    expect([...SUPPORTED_SCALAR_LIST_OPERATORS].sort()).toEqual(
      ['equals', 'has', 'hasEvery', 'hasSome', 'isEmpty'].sort(),
    );
  });

  it('recognises its own names', () => {
    for (const name of SUPPORTED_SCALAR_LIST_OPERATORS) {
      expect(isScalarListOperatorName(name)).toBe(true);
    }
    expect(isScalarListOperatorName('contains')).toBe(false);
  });

  it('declares two-valued SQL for every entry, so NOT complements it', () => {
    for (const name of SUPPORTED_SCALAR_LIST_OPERATORS) {
      expect(SCALAR_LIST_OPERATORS[name].nullSemantics.sqlIsTwoValued).toBe(true);
    }
  });

  it('complements every list filter under NOT', () => {
    const values: readonly unknown[] = [[], ['a'], ['a', 'b'], null];
    const filters: readonly unknown[] = [
      { tags: { has: 'a' } },
      { tags: { hasEvery: ['a'] } },
      { tags: { hasEvery: [] } },
      { tags: { hasSome: ['a'] } },
      { tags: { hasSome: [] } },
      { tags: { isEmpty: true } },
      { tags: { equals: ['a'] } },
      { tags: { equals: null } },
    ];
    for (const where of filters) {
      for (const value of values) {
        expect(evaluate({ NOT: where }, value)).toBe(!evaluate(where, value));
      }
    }
  });
});

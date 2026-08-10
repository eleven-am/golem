import { ConditionContext, compileConditions, evaluateConditions, validateConditions } from './compile';
import { PolicyDatamodel, PolicyDatamodelField } from './datamodel';
import { SqlRenderError } from './errors';
import { createDatamodelSqlScope } from './scope';
import { SqlFragment, renderSql, sqliteDialect } from './sql';

function scalar(name: string, type: string, dbName: string): PolicyDatamodelField {
  return { name, dbName, kind: 'scalar', type, isList: false };
}

function toOne(
  name: string,
  type: string,
  from: readonly string[],
  to: readonly string[],
): PolicyDatamodelField {
  return {
    name,
    dbName: name,
    kind: 'object',
    type,
    isList: false,
    relationName: `${name}Relation`,
    relationFromFields: from,
    relationToFields: to,
  };
}

const DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Post',
      dbName: 'posts',
      fields: [
        scalar('id', 'Int', 'id'),
        scalar('title', 'String', 'title'),
        scalar('authorId', 'Int', 'author_id'),
        toOne('author', 'User', ['authorId'], ['id']),
        {
          name: 'comments',
          dbName: 'comments',
          kind: 'object',
          type: 'Comment',
          isList: true,
          relationName: 'CommentToPost',
        },
        { name: 'labels', dbName: 'labels', kind: 'scalar', type: 'String', isList: true },
      ],
    },
    {
      name: 'User',
      dbName: 'users',
      fields: [
        scalar('id', 'Int', 'id'),
        scalar('name', 'String', 'name'),
        scalar('is', 'Boolean', 'is_flag'),
        scalar('orgId', 'Int', 'org_id'),
        toOne('org', 'Org', ['orgId'], ['id']),
      ],
    },
    {
      name: 'Org',
      dbName: 'orgs',
      fields: [scalar('id', 'Int', 'id'), scalar('tier', 'Int', 'tier')],
    },
    {
      name: 'Comment',
      dbName: 'post_comments',
      fields: [
        scalar('id', 'Int', 'id'),
        scalar('postId', 'Int', 'post_id'),
        scalar('flag', 'String', 'flag'),
        {
          name: 'post',
          dbName: 'post',
          kind: 'object',
          type: 'Post',
          isList: false,
          relationName: 'CommentToPost',
          relationFromFields: ['postId'],
          relationToFields: ['id'],
        },
      ],
    },
  ],
};

const SCHEMA: ConditionContext = { datamodel: DATAMODEL, model: 'Post' };

const ADA = { id: 1, authorId: 7, author: { id: 7, name: 'ada', is: true, orgId: 3, org: { id: 3, tier: 2 } } };
const BOB = { id: 2, authorId: 8, author: { id: 8, name: 'bob', is: false, orgId: 4, org: { id: 4, tier: 5 } } };
const NULL_AUTHOR = { id: 3, authorId: null, author: null };
const ABSENT_AUTHOR = { id: 4, authorId: null };

const OBJECTS: readonly unknown[] = [ADA, BOB, NULL_AUTHOR, ABSENT_AUTHOR];

function sqlFor(conditions: unknown, context?: ConditionContext): SqlFragment {
  const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
  return renderSql(compileConditions(conditions, context).toSql(scope), sqliteDialect);
}

const AUTHOR_HOP = 'SELECT 1 FROM "users" AS "t0_1" WHERE "t0_1"."id" = "t0"."author_id"';

describe('the bare to-one shorthand', () => {
  it('matches when the related object satisfies the conditions', () => {
    expect(evaluateConditions({ author: { name: 'ada' } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { name: 'ada' } }, BOB)).toBe(false);
  });

  it('does not match a null relation', () => {
    expect(evaluateConditions({ author: { name: 'ada' } }, NULL_AUTHOR)).toBe(false);
  });

  it('does not match an absent relation', () => {
    expect(evaluateConditions({ author: { name: 'ada' } }, ABSENT_AUTHOR)).toBe(false);
  });

  it('conjoins several keys', () => {
    expect(evaluateConditions({ author: { name: 'ada', orgId: 3 } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { name: 'ada', orgId: 4 } }, ADA)).toBe(false);
    expect(evaluateConditions({ author: { name: 'bob', orgId: 3 } }, ADA)).toBe(false);
  });

  it('accepts operator objects on the related fields', () => {
    expect(evaluateConditions({ author: { orgId: { in: [3, 9] } } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { orgId: { gt: 3 } } }, ADA)).toBe(false);
    expect(evaluateConditions({ author: { orgId: { gt: 3 } } }, BOB)).toBe(true);
  });

  it('nests through a second to-one hop', () => {
    expect(evaluateConditions({ author: { org: { tier: 2 } } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { org: { tier: 2 } } }, BOB)).toBe(false);
    expect(evaluateConditions({ author: { org: { tier: 2 } } }, NULL_AUTHOR)).toBe(false);
  });

  it('agrees with the explicit is form on every object', () => {
    const inner = { name: 'ada', org: { tier: 2 } };
    for (const object of OBJECTS) {
      expect(evaluateConditions({ author: inner }, object)).toBe(
        evaluateConditions({ author: { is: inner } }, object),
      );
    }
  });

  it('renders the same SQL as the explicit is form', () => {
    expect(sqlFor({ author: { name: 'ada' } })).toEqual(sqlFor({ author: { is: { name: 'ada' } } }));
  });

  it('renders a correlated EXISTS', () => {
    expect(sqlFor({ author: { name: 'ada' } })).toEqual({
      text: `(EXISTS (${AUTHOR_HOP} AND (("t0_1"."name" COLLATE BINARY IS ?))))`,
      parameters: ['ada'],
    });
  });

  it('renders several keys as one EXISTS with a conjunction inside it', () => {
    expect(sqlFor({ author: { name: 'ada', orgId: 3 } })).toEqual({
      text: `(EXISTS (${AUTHOR_HOP} AND ((("t0_1"."name" COLLATE BINARY IS ?) AND ("t0_1"."org_id" IS ?)))))`,
      parameters: ['ada', 3],
    });
  });

  it('renders a nested hop as a nested EXISTS', () => {
    expect(sqlFor({ author: { org: { tier: 2 } } })).toEqual({
      text:
        `(EXISTS (${AUTHOR_HOP} AND ((EXISTS (SELECT 1 FROM "orgs" AS "t0_2" ` +
        `WHERE "t0_2"."id" = "t0_1"."org_id" AND (("t0_2"."tier" IS ?)))))))`,
      parameters: [2],
    });
  });
});

describe('the bare shorthand under combinators', () => {
  const cases: readonly (readonly [string, unknown, boolean, boolean])[] = [
    ['AND', { AND: [{ author: { name: 'ada' } }, { id: 1 }] }, true, false],
    ['OR', { OR: [{ author: { name: 'ada' } }, { id: 2 }] }, true, true],
    ['NOT', { NOT: { author: { name: 'ada' } } }, false, true],
  ];

  it.each(cases)('evaluates a shorthand inside %s', (_name, conditions, forAda, forBob) => {
    expect(evaluateConditions(conditions, ADA)).toBe(forAda);
    expect(evaluateConditions(conditions, BOB)).toBe(forBob);
  });

  it('is true under NOT for a null relation, because the shorthand itself is false there', () => {
    expect(evaluateConditions({ author: { name: 'ada' } }, NULL_AUTHOR)).toBe(false);
    expect(evaluateConditions({ NOT: { author: { name: 'ada' } } }, NULL_AUTHOR)).toBe(true);
  });

  it('renders a shorthand inside NOT as a negated EXISTS', () => {
    expect(sqlFor({ NOT: { author: { name: 'ada' } } })).toEqual({
      text: `NOT ((EXISTS (${AUTHOR_HOP} AND (("t0_1"."name" COLLATE BINARY IS ?)))))`,
      parameters: ['ada'],
    });
  });

  it('carries the field path into issues raised inside a shorthand', () => {
    const result = validateConditions({ author: { name: { search: 'a' } } });
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.path).toEqual(['author', 'name']);
    expect(result.issues[0]!.operator).toBe('search');
  });
});

describe('isNot', () => {
  it('matches when the related object fails the conditions', () => {
    expect(evaluateConditions({ author: { isNot: { name: 'ada' } } }, BOB)).toBe(true);
  });

  it('does not match when the related object satisfies the conditions', () => {
    expect(evaluateConditions({ author: { isNot: { name: 'ada' } } }, ADA)).toBe(false);
  });

  it('matches a null relation, because is is false there', () => {
    expect(evaluateConditions({ author: { is: { name: 'ada' } } }, NULL_AUTHOR)).toBe(false);
    expect(evaluateConditions({ author: { isNot: { name: 'ada' } } }, NULL_AUTHOR)).toBe(true);
  });

  it('matches an absent relation', () => {
    expect(evaluateConditions({ author: { isNot: { name: 'ada' } } }, ABSENT_AUTHOR)).toBe(true);
  });

  it('is exactly the negation of is on every object', () => {
    const inner = { name: 'ada', org: { tier: 2 } };
    for (const object of OBJECTS) {
      expect(evaluateConditions({ author: { isNot: inner } }, object)).toBe(
        !evaluateConditions({ author: { is: inner } }, object),
      );
    }
  });

  it('agrees with wrapping the shorthand in NOT', () => {
    for (const object of OBJECTS) {
      expect(evaluateConditions({ author: { isNot: { name: 'ada' } } }, object)).toBe(
        evaluateConditions({ NOT: { author: { name: 'ada' } } }, object),
      );
    }
  });

  it('renders a negated EXISTS', () => {
    expect(sqlFor({ author: { isNot: { name: 'ada' } } })).toEqual({
      text: `NOT ((EXISTS (${AUTHOR_HOP} AND (("t0_1"."name" COLLATE BINARY IS ?)))))`,
      parameters: ['ada'],
    });
  });

  it('conjoins is and isNot in one filter', () => {
    expect(evaluateConditions({ author: { is: { orgId: 3 }, isNot: { name: 'bob' } } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { is: { orgId: 3 }, isNot: { name: 'ada' } } }, ADA)).toBe(false);
    expect(evaluateConditions({ author: { is: { orgId: 3 }, isNot: { name: 'bob' } } }, NULL_AUTHOR)).toBe(
      false,
    );
  });
});

describe('a null to-one relation filter', () => {
  const absent: readonly unknown[] = [NULL_AUTHOR, ABSENT_AUTHOR];
  const present: readonly unknown[] = [ADA, BOB];

  it('matches only the objects with no related object', () => {
    for (const object of absent) {
      expect(evaluateConditions({ author: null }, object, SCHEMA)).toBe(true);
      expect(evaluateConditions({ author: { is: null } }, object, SCHEMA)).toBe(true);
      expect(evaluateConditions({ author: { isNot: null } }, object, SCHEMA)).toBe(false);
    }
    for (const object of present) {
      expect(evaluateConditions({ author: null }, object, SCHEMA)).toBe(false);
      expect(evaluateConditions({ author: { is: null } }, object, SCHEMA)).toBe(false);
      expect(evaluateConditions({ author: { isNot: null } }, object, SCHEMA)).toBe(true);
    }
  });

  it('renders a negated EXISTS with no inner condition', () => {
    expect(sqlFor({ author: null }, SCHEMA)).toEqual({
      text: `NOT (EXISTS (${AUTHOR_HOP} AND ((1 = 1))))`,
      parameters: [],
    });
    expect(sqlFor({ author: { is: null } }, SCHEMA)).toEqual(sqlFor({ author: null }, SCHEMA));
  });

  it('renders isNot null as the double negation of is null', () => {
    expect(sqlFor({ author: { isNot: null } }, SCHEMA)).toEqual({
      text: `NOT (NOT (EXISTS (${AUTHOR_HOP} AND ((1 = 1)))))`,
      parameters: [],
    });
  });

  it('needs the datamodel: without it a bare null stays a scalar equals, and the SQL refuses', () => {
    expect(evaluateConditions({ author: null }, NULL_AUTHOR)).toBe(true);
    expect(evaluateConditions({ author: null }, ADA)).toBe(false);
    expect(() => sqlFor({ author: null })).toThrow(SqlRenderError);
  });

  it('refuses a scalar operand against a relation once the datamodel is known', () => {
    const result = validateConditions({ author: 7 }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-shape');
    expect(result.issues[0]!.message).toContain('author');
  });
});

describe('disambiguating the shorthand from the relation filter', () => {
  it('reads is as the operator even when the related model has a field named is', () => {
    expect(evaluateConditions({ author: { is: { name: 'ada' } } }, ADA, SCHEMA)).toBe(true);
    expect(evaluateConditions({ author: { is: { name: 'ada' } } }, ADA)).toBe(true);
  });

  it('reaches a related field named is through a combinator, where no operator position exists', () => {
    expect(evaluateConditions({ author: { AND: [{ is: true }] } }, ADA, SCHEMA)).toBe(true);
    expect(evaluateConditions({ author: { AND: [{ is: true }] } }, BOB, SCHEMA)).toBe(false);
    expect(sqlFor({ author: { AND: [{ is: true }] } }, SCHEMA)).toEqual({
      text: `(EXISTS (${AUTHOR_HOP} AND ((("t0_1"."is_flag" IS ?)))))`,
      parameters: [true],
    });
  });

  it('refuses a filter that mixes is with keys of the related model', () => {
    const result = validateConditions({ author: { is: { name: 'ada' }, name: 'bob' } });
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-shape');
    expect(result.issues[0]!.message).toContain('name');
  });

  it('treats a filter whose keys are all Prisma filter operators as a scalar filter', () => {
    expect(evaluateConditions({ title: { equals: 'x' } }, { title: 'x' })).toBe(true);
    expect(validateConditions({ title: { search: 'x' } }).supported).toBe(false);
  });

  it('treats a filter with any non-operator key as a condition object on the related model', () => {
    expect(evaluateConditions({ author: { name: 'ada' } }, ADA)).toBe(true);
    expect(evaluateConditions({ author: { equals: 1, name: 'ada' } }, ADA)).toBe(false);
    expect(evaluateConditions({ author: { equals: 1, name: 'ada' } }, { author: { equals: 1, name: 'ada' } })).toBe(
      true,
    );
  });

  it('treats an empty filter as no constraint when the position is unknown', () => {
    expect(evaluateConditions({ author: {} }, ADA)).toBe(true);
    expect(evaluateConditions({ author: {} }, NULL_AUTHOR)).toBe(true);
  });

  it('treats an empty filter as an existence check once the datamodel says it is a relation', () => {
    expect(evaluateConditions({ author: {} }, ADA, SCHEMA)).toBe(true);
    expect(evaluateConditions({ author: {} }, NULL_AUTHOR, SCHEMA)).toBe(false);
    expect(sqlFor({ author: {} }, SCHEMA)).toEqual({
      text: `(EXISTS (${AUTHOR_HOP} AND ((1 = 1))))`,
      parameters: [],
    });
  });

  it('rejects a relation operator on a scalar field once the datamodel is known', () => {
    const result = validateConditions({ title: { is: { a: 1 } } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-operator');
    expect(result.issues[0]!.operator).toBe('is');
  });

  it('rejects an unknown key on a scalar field once the datamodel is known', () => {
    const result = validateConditions({ title: { nope: 1 } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-operator');
    expect(result.issues[0]!.operator).toBe('nope');
  });

  it('accepts the same unknown key as a related field name when the position is unknown', () => {
    expect(validateConditions({ title: { nope: 1 } }).supported).toBe(true);
    expect(evaluateConditions({ title: { nope: 1 } }, { title: 'x' })).toBe(false);
    expect(() => sqlFor({ title: { nope: 1 } })).toThrow(SqlRenderError);
  });

  it('rejects a field the related model does not declare', () => {
    const result = validateConditions({ author: { nope: 1 } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-shape');
    expect(result.issues[0]!.message).toContain('User.nope');
  });

  it('rejects a bare condition object on a to-many relation, and a list column', () => {
    const many = validateConditions({ comments: { id: 1 } }, SCHEMA);
    expect(many.supported).toBe(false);
    expect(many.issues[0]!.message).toContain('some, every and none');
    const list = validateConditions({ labels: 'x' }, SCHEMA);
    expect(list.supported).toBe(false);
    expect(list.issues[0]!.message).toContain('list column');
  });
});

const NONE = { id: 10, comments: [] };
const ALL_OPEN = { id: 11, comments: [{ id: 1, flag: 'open' }, { id: 2, flag: 'open' }] };
const MIXED = { id: 12, comments: [{ id: 3, flag: 'open' }, { id: 4, flag: 'shut' }] };
const NULL_FLAG = { id: 13, comments: [{ id: 5, flag: 'open' }, { id: 6, flag: null }] };
const ABSENT_LIST = { id: 14 };

const POSTS: readonly unknown[] = [NONE, ALL_OPEN, MIXED, NULL_FLAG, ABSENT_LIST];

function matched(conditions: unknown, context?: ConditionContext): number[] {
  return POSTS.filter((post) => evaluateConditions(conditions, post, context)).map(
    (post) => (post as { id: number }).id,
  );
}

const COMMENT_HOP = 'SELECT 1 FROM "post_comments" AS "t0_1" WHERE "t0_1"."post_id" = "t0"."id" AND';

describe('some', () => {
  it('matches when at least one related object satisfies the conditions', () => {
    expect(matched({ comments: { some: { flag: 'open' } } })).toEqual([11, 12, 13]);
  });

  it('is an existence check when the condition is empty', () => {
    expect(matched({ comments: { some: {} } })).toEqual([11, 12, 13]);
  });

  it('does not match an empty or absent list', () => {
    expect(evaluateConditions({ comments: { some: {} } }, NONE)).toBe(false);
    expect(evaluateConditions({ comments: { some: {} } }, ABSENT_LIST)).toBe(false);
  });

  it('renders a correlated EXISTS', () => {
    expect(sqlFor({ comments: { some: { flag: 'open' } } }, SCHEMA)).toEqual({
      text: `(EXISTS (${COMMENT_HOP} (("t0_1"."flag" COLLATE BINARY IS ?))))`,
      parameters: ['open'],
    });
  });
});

describe('none', () => {
  it('matches when no related object satisfies the conditions', () => {
    expect(matched({ comments: { none: { flag: 'shut' } } })).toEqual([10, 11, 13, 14]);
  });

  it('means no related rows at all when the condition is empty', () => {
    expect(matched({ comments: { none: {} } })).toEqual([10, 14]);
  });

  it('is exactly the negation of some on every object', () => {
    for (const post of POSTS) {
      expect(evaluateConditions({ comments: { none: { flag: 'open' } } }, post)).toBe(
        !evaluateConditions({ comments: { some: { flag: 'open' } } }, post),
      );
    }
  });

  it('renders a negated EXISTS', () => {
    expect(sqlFor({ comments: { none: { flag: 'open' } } }, SCHEMA)).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} (("t0_1"."flag" COLLATE BINARY IS ?))))`,
      parameters: ['open'],
    });
  });
});

describe('every', () => {
  it('matches when every related object satisfies the conditions', () => {
    expect(matched({ comments: { every: { flag: 'open' } } })).toEqual([10, 11, 14]);
  });

  it('is vacuously true for an empty list, an absent list and an empty condition', () => {
    expect(evaluateConditions({ comments: { every: { flag: 'open' } } }, NONE)).toBe(true);
    expect(evaluateConditions({ comments: { every: { flag: 'open' } } }, ABSENT_LIST)).toBe(true);
    expect(matched({ comments: { every: {} } })).toEqual([10, 11, 12, 13, 14]);
  });

  it('counts a related object whose condition is neither true nor false as a violation', () => {
    expect(evaluateConditions({ comments: { every: { flag: 'open' } } }, NULL_FLAG)).toBe(false);
    expect(evaluateConditions({ comments: { every: { flag: { not: 'shut' } } } }, NULL_FLAG)).toBe(true);
  });

  it('renders a negated EXISTS over the rows the condition does not make true', () => {
    expect(sqlFor({ comments: { every: { flag: 'open' } } }, SCHEMA)).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} (((("t0_1"."flag" COLLATE BINARY IS ?)) IS NOT TRUE))))`,
      parameters: ['open'],
    });
  });
});

describe('a to-many filter', () => {
  it('conjoins some, every and none as Prisma does', () => {
    expect(matched({ comments: { some: { flag: 'open' }, none: { flag: 'shut' } } })).toEqual([11, 13]);
    expect(
      matched({ comments: { some: {}, every: { flag: 'open' }, none: { flag: 'shut' } } }),
    ).toEqual([11]);
  });

  it('is no constraint at all when it carries no quantifier', () => {
    expect(matched({ comments: {} }, SCHEMA)).toEqual([10, 11, 12, 13, 14]);
    expect(sqlFor({ comments: {} }, SCHEMA)).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it('keeps an empty disjunction inside a quantifier false, unlike an empty condition object', () => {
    expect(matched({ comments: { some: { OR: [] } } })).toEqual([]);
    expect(matched({ comments: { none: { OR: [] } } })).toEqual([10, 11, 12, 13, 14]);
    expect(matched({ comments: { every: { OR: [] } } })).toEqual([10, 14]);
    expect(sqlFor({ comments: { some: { OR: [] } } }, SCHEMA).text).toContain('1 = 0');
  });

  it('reads a to-one shorthand nested inside a quantifier', () => {
    expect(
      sqlFor({ comments: { some: { post: { author: { name: 'ada' } } } } }, SCHEMA).text,
    ).toContain('EXISTS (SELECT 1 FROM "users" AS "t0_3"');
  });

  it('carries the field path into issues raised inside a quantifier', () => {
    const result = validateConditions({ comments: { some: { flag: { search: 'a' } } } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.path).toEqual(['comments', 'some', 'flag']);
    expect(result.issues[0]!.operator).toBe('search');
  });

  it('refuses a quantifier on a to-one relation and on a scalar', () => {
    const toOne = validateConditions({ author: { some: { name: 'ada' } } }, SCHEMA);
    expect(toOne.supported).toBe(false);
    expect(toOne.issues[0]!.message).toContain('is a to-one relation');
    const onScalar = validateConditions({ title: { every: {} } }, SCHEMA);
    expect(onScalar.supported).toBe(false);
    expect(onScalar.issues[0]!.message).toContain('is a scalar field');
  });

  it('refuses is and isNot on a to-many relation', () => {
    const result = validateConditions({ comments: { is: { id: 1 } } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.operator).toBe('is');
    expect(result.issues[0]!.message).toContain('is a to-many relation');
    expect(() => sqlFor({ comments: { is: { id: 1 } } })).toThrow(SqlRenderError);
  });

  it('refuses mixing a quantifier with a field of the related model', () => {
    const result = validateConditions({ comments: { some: { id: 1 }, id: 2 } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('mixes the to-many operators');
  });

  it('refuses a quantifier whose operand is not a condition object', () => {
    const result = validateConditions({ comments: { some: null } }, SCHEMA);
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.reason).toBe('unsupported-value');
    expect(result.issues[0]!.operator).toBe('some');
  });

  it('accepts a quantifier without a datamodel, exactly as it accepts is', () => {
    expect(validateConditions({ comments: { some: { flag: 'open' } } })).toEqual({
      supported: true,
      issues: [],
    });
    expect(matched({ comments: { some: { flag: 'open' } } })).toEqual([11, 12, 13]);
  });
});

describe('the condition context', () => {
  it('refuses a datamodel without a model name', () => {
    const result = validateConditions({ id: 1 }, { datamodel: DATAMODEL });
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('both a datamodel and a model name');
  });

  it('refuses a model that the datamodel does not declare', () => {
    const result = validateConditions({ id: 1 }, { datamodel: DATAMODEL, model: 'Ghost' });
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('"Ghost"');
  });

  it('classifies from the context alone, so the renderer never reclassifies from the scope', () => {
    const conditions = { author: {} };
    expect(evaluateConditions(conditions, NULL_AUTHOR)).toBe(true);
    expect(sqlFor(conditions)).toEqual({ text: '(1 = 1)', parameters: [] });
    expect(evaluateConditions(conditions, NULL_AUTHOR, SCHEMA)).toBe(false);
    expect(sqlFor(conditions, SCHEMA).text).toContain('EXISTS');
  });

  it('lets the scope refuse a related field the context never checked', () => {
    expect(validateConditions({ author: { nope: 1 } })).toEqual({ supported: true, issues: [] });
    expect(evaluateConditions({ author: { nope: 1 } }, ADA)).toBe(false);
    expect(() => sqlFor({ author: { nope: 1 } })).toThrow(SqlRenderError);
    expect(validateConditions({ author: { nope: 1 } }, SCHEMA).supported).toBe(false);
  });
});

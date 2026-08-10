import {
  RESERVED_PRISMA_FILTER_KEYS,
  assertSupportedConditions,
  compileConditions,
  evaluateConditions,
  validateConditions,
} from './compile';
import { PolicyDatamodel } from './datamodel';
import { UnsupportedConditionError, isUnsupportedConditionError } from './errors';

function issuesOf(conditions: unknown) {
  const result = validateConditions(conditions);
  if (result.supported) {
    throw new Error(`expected ${JSON.stringify(conditions)} to be rejected`);
  }
  return result.issues;
}

describe('rejection of unsupported operators', () => {
  const unsupported = [
    'search',
    'isSet',
  ];

  it.each(unsupported)('rejects the %s operator', (operator) => {
    const issues = issuesOf({ title: { [operator]: 'x' } });
    expect(issues).toHaveLength(1);
    expect(issues[0].reason).toBe('unsupported-operator');
    expect(issues[0].operator).toBe(operator);
    expect(issues[0].path).toEqual(['title']);
    expect(issues[0].message).toContain(`"${operator}"`);
  });

  it.each(unsupported)('rejects the %s operator inside a NOT', (operator) => {
    const issues = issuesOf({ NOT: { title: { [operator]: 'x' } } });
    expect(issues).toHaveLength(1);
    expect(issues[0].operator).toBe(operator);
    expect(issues[0].path).toEqual(['NOT', 'title']);
  });

  it('reports the path through a one-hop relation', () => {
    const issues = issuesOf({ post: { is: { title: { search: 'x' } } } });
    expect(issues).toHaveLength(1);
    expect(issues[0].operator).toBe('search');
    expect(issues[0].path).toEqual(['post', 'is', 'title']);
  });

  it('reports the path through combinator branches', () => {
    const issues = issuesOf({ OR: [{ a: 1 }, { title: { search: 'x' } }] });
    expect(issues).toHaveLength(1);
    expect(issues[0].path).toEqual(['OR', '1', 'title']);
  });

  it('collects every issue rather than stopping at the first', () => {
    const issues = issuesOf({
      title: { search: 'x' },
      body: { search: 'y' },
      tags: { isSet: true },
    });
    expect(issues).toHaveLength(3);
    expect(issues.map((entry) => entry.operator)).toEqual(['search', 'search', 'isSet']);
  });

  it('accepts the scalar list filters, which golem implements', () => {
    for (const filter of [{ has: 'x' }, { hasEvery: ['x'] }, { hasSome: ['x'] }, { isEmpty: true }]) {
      expect(validateConditions({ tags: filter })).toEqual({ supported: true, issues: [] });
    }
  });

  it('accepts the to-many quantifiers, which golem implements', () => {
    for (const operator of ['some', 'every', 'none']) {
      expect(validateConditions({ comments: { [operator]: { id: 1 } } })).toEqual({
        supported: true,
        issues: [],
      });
    }
  });

  it('rejects a reserved Prisma operator that golem has not implemented, rather than reading it as a field name', () => {
    for (const operator of unsupported) {
      expect(RESERVED_PRISMA_FILTER_KEYS).toContain(operator);
    }
  });

  it('reads a key that is not a Prisma filter operator as a field of the related model', () => {
    for (const conditions of [{ v: { foo: 'x' } }, { v: { notContains: 'x' } }]) {
      expect(validateConditions(conditions)).toEqual({ supported: true, issues: [] });
      expect(evaluateConditions(conditions, { v: { foo: 'x', notContains: 'x' } })).toBe(true);
      expect(evaluateConditions(conditions, { v: null })).toBe(false);
    }
  });

  it('reads a combinator under a field as a condition on the related model', () => {
    const conditions = { v: { AND: [{ equals: 1 }] } };
    expect(validateConditions(conditions)).toEqual({ supported: true, issues: [] });
    expect(evaluateConditions(conditions, { v: { equals: 1 } })).toBe(true);
    expect(evaluateConditions(conditions, { v: { equals: 2 } })).toBe(false);
  });

  it('names the supported operators in the message', () => {
    const issues = issuesOf({ title: { search: 'x' } });
    expect(issues[0].message).toContain('equals');
    expect(issues[0].message).toContain('notIn');
    expect(issues[0].message).toContain('is');
    expect(issues[0].message).toContain('contains');
  });

  it('accepts the string operators, which golem implements', () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      expect(validateConditions({ title: { [operator]: 'x' } })).toEqual({
        supported: true,
        issues: [],
      });
    }
  });
});

describe('the mode key', () => {
  it('accepts the two query modes Prisma defines', () => {
    for (const mode of ['default', 'insensitive']) {
      expect(validateConditions({ title: { contains: 'x', mode } })).toEqual({
        supported: true,
        issues: [],
      });
    }
  });

  it('accepts a mode with no string operator beside it, which Prisma reads as a no-op', () => {
    expect(validateConditions({ title: { mode: 'insensitive' } })).toEqual({
      supported: true,
      issues: [],
    });
  });

  it('rejects a mode that is neither default nor insensitive', () => {
    const issues = issuesOf({ title: { contains: 'x', mode: 'loose' } });
    expect(issues).toHaveLength(1);
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].operator).toBe('mode');
    expect(issues[0].message).toContain('insensitive');
  });

  it('rejects a non-string mode', () => {
    expect(issuesOf({ title: { contains: 'x', mode: true } })).toHaveLength(1);
    expect(issuesOf({ title: { contains: 'x', mode: null } })).toHaveLength(1);
  });

  it.each(['equals', 'not', 'in', 'notIn', 'lt', 'lte', 'gt', 'gte', 'contains', 'startsWith', 'endsWith'])(
    'folds case for %s, as Prisma does',
    (operator) => {
      const operand = operator === 'in' || operator === 'notIn' ? ['x'] : 'x';
      expect(validateConditions({ title: { [operator]: operand, mode: 'insensitive' } })).toEqual({
        supported: true,
        issues: [],
      });
    },
  );

  it.each(['is', 'isNot', 'some', 'every', 'none'])(
    'refuses mode beside %s, which Prisma\'s relation filter does not carry',
    (operator) => {
      const issues = issuesOf({ rel: { [operator]: { a: 1 }, mode: 'insensitive' } });
      expect(issues.length).toBeGreaterThan(0);
    },
  );

  it('folds case for equals in the evaluator, matching only outside-ASCII-exactly', () => {
    const insensitive = { title: { equals: 'ALPHA', mode: 'insensitive' } };
    expect(evaluateConditions(insensitive, { title: 'alpha' })).toBe(true);
    expect(evaluateConditions(insensitive, { title: 'Alpha' })).toBe(true);
    expect(evaluateConditions(insensitive, { title: 'beta' })).toBe(false);
    expect(evaluateConditions(insensitive, { title: null })).toBe(false);
    expect(evaluateConditions({ title: { equals: 'ECOLE', mode: 'insensitive' } }, { title: 'École' })).toBe(
      false,
    );
  });

  it('folds case for an ordered comparison, for in and for notIn', () => {
    expect(evaluateConditions({ title: { gt: 'ALPHA', mode: 'insensitive' } }, { title: 'beta' })).toBe(true);
    expect(evaluateConditions({ title: { lt: 'BETA', mode: 'insensitive' } }, { title: 'Alpha' })).toBe(true);
    expect(evaluateConditions({ title: { in: ['ALPHA'], mode: 'insensitive' } }, { title: 'alpha' })).toBe(
      true,
    );
    expect(evaluateConditions({ title: { notIn: ['ALPHA'], mode: 'insensitive' } }, { title: 'alpha' })).toBe(
      false,
    );
    expect(evaluateConditions({ title: { notIn: ['ALPHA'], mode: 'insensitive' } }, { title: null })).toBe(
      true,
    );
  });

  it('rejects a non-string operand under insensitive, which Prisma types as a string filter', () => {
    expect(issuesOf({ title: { equals: 5, mode: 'insensitive' } })[0].operator).toBe('equals');
    expect(issuesOf({ title: { gt: 5, mode: 'insensitive' } })[0].operator).toBe('gt');
    expect(validateConditions({ title: { equals: null, mode: 'insensitive' } })).toEqual({
      supported: true,
      issues: [],
    });
  });

  it('leaves a default mode beside any operator alone, because it changes nothing', () => {
    expect(validateConditions({ title: { equals: 'x', mode: 'default' } })).toEqual({
      supported: true,
      issues: [],
    });
    expect(evaluateConditions({ title: { equals: 'x', mode: 'default' } }, { title: 'x' })).toBe(true);
  });

  it('rejects a string operand that is not a string or null', () => {
    for (const operand of [1, true, ['x'], { a: 1 }]) {
      const issues = issuesOf({ title: { contains: operand } });
      expect(issues).toHaveLength(1);
      expect(issues[0].reason).toBe('unsupported-value');
      expect(issues[0].operator).toBe('contains');
    }
  });

  it('names the operator, not the wrapper, when an insensitive operand is rejected', () => {
    const issues = issuesOf({ title: { contains: 1, mode: 'insensitive' } });
    expect(issues).toHaveLength(1);
    expect(issues[0].operator).toBe('contains');
    expect(issues[0].message).toContain('number');
  });
});

describe('rejection of unsupported operand values', () => {
  it('rejects null inside an in list', () => {
    const issues = issuesOf({ v: { in: [1, null] } });
    expect(issues).toHaveLength(1);
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].operator).toBe('in');
    expect(issues[0].message).toContain('null');
  });

  it('rejects null inside a notIn list', () => {
    const issues = issuesOf({ v: { notIn: [1, null] } });
    expect(issues).toHaveLength(1);
    expect(issues[0].operator).toBe('notIn');
  });

  it('rejects undefined inside an in list', () => {
    expect(issuesOf({ v: { in: [1, undefined] } })).toHaveLength(1);
  });

  it('rejects a lone null in an in list', () => {
    expect(issuesOf({ v: { in: [null] } })).toHaveLength(1);
  });

  it('rejects a non-array in operand', () => {
    const issues = issuesOf({ v: { in: 1 } });
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].message).toContain('array');
  });

  it('rejects a nested object inside an in list', () => {
    expect(issuesOf({ v: { in: [{ a: 1 }] } })).toHaveLength(1);
  });

  it('rejects an array operand for equals', () => {
    const issues = issuesOf({ v: ['a', 'b'] });
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].operator).toBe('equals');
  });

  it('rejects an object operand for equals', () => {
    expect(issuesOf({ v: { equals: { a: 1 } } })).toHaveLength(1);
  });

  it('rejects a boolean operand for a comparison', () => {
    const issues = issuesOf({ v: { gt: true } });
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].operator).toBe('gt');
  });

  it('rejects an object operand for a comparison', () => {
    expect(issuesOf({ v: { lte: { a: 1 } } })).toHaveLength(1);
  });

  it('rejects a non-object operand for is', () => {
    const issues = issuesOf({ p: { is: 'x' } });
    expect(issues[0].reason).toBe('unsupported-value');
    expect(issues[0].operator).toBe('is');
  });

  it('accepts a null operand for is, which Prisma spells on a nullable to-one relation', () => {
    expect(validateConditions({ p: { is: null } })).toEqual({ supported: true, issues: [] });
    expect(validateConditions({ p: { isNot: null } })).toEqual({ supported: true, issues: [] });
  });

  it('rejects a filter that mixes is with keys of the related model', () => {
    const issues = issuesOf({ p: { is: { a: 1 }, b: 2 } });
    expect(issues).toHaveLength(1);
    expect(issues[0].reason).toBe('unsupported-shape');
    expect(issues[0].message).toContain('b');
  });

  it('rejects an array operand for is', () => {
    expect(issuesOf({ p: { is: [{ a: 1 }] } })).toHaveLength(1);
  });

  it('rejects a non-object branch in a combinator', () => {
    const issues = issuesOf({ OR: [1] });
    expect(issues[0].reason).toBe('unsupported-shape');
    expect(issues[0].operator).toBe('OR');
  });

  it('rejects a non-object combinator operand', () => {
    const issues = issuesOf({ AND: 'x' });
    expect(issues[0].reason).toBe('unsupported-shape');
  });

  it('rejects non-object conditions', () => {
    const issues = issuesOf('x');
    expect(issues[0].reason).toBe('unsupported-shape');
    expect(issues[0].operator).toBeNull();
    expect(issues[0].path).toEqual([]);
  });

  it('rejects null conditions', () => {
    expect(issuesOf(null)).toHaveLength(1);
  });
});

describe('supported conditions', () => {
  const supported: unknown[] = [
    {},
    { userId: 'x' },
    { viewCount: { gte: 100 } },
    { viewCount: { gt: 9007199254740992 } },
    { post: { is: { authorId: 'u1' } } },
    { article: { is: { userId: 'u1' } }, collection: { is: { userId: 'u1' } } },
    { v: { in: [1, 2, 3] } },
    { v: { in: [] } },
    { v: { notIn: [] } },
    { v: { equals: null } },
    { v: { not: null } },
    { v: { lt: null } },
    { v: null },
    { v: { contains: 'x' } },
    { v: { startsWith: '', endsWith: '%' } },
    { v: { contains: 'x', mode: 'insensitive' } },
    { v: { contains: null } },
    { OR: [] },
    { AND: [] },
    { NOT: [] },
    { OR: [{ a: 1 }, { NOT: { b: { in: ['x'] } } }] },
  ];

  it.each(supported.map((conditions, index) => [index, conditions] as const))(
    'accepts supported conditions #%i',
    (_index, conditions) => {
      expect(validateConditions(conditions)).toEqual({ supported: true, issues: [] });
      expect(() => assertSupportedConditions(conditions)).not.toThrow();
    },
  );
});

describe('rejection is distinct from a non-match', () => {
  it('throws rather than returning false', () => {
    expect(() => evaluateConditions({ title: { search: 'x' } }, { title: 'x' })).toThrow(
      UnsupportedConditionError,
    );
  });

  it('throws even when an unsupported operator sits in an unreached OR branch', () => {
    expect(() => evaluateConditions({ OR: [{ a: 1 }, { b: { search: 'x' } }] }, { a: 1 })).toThrow(
      UnsupportedConditionError,
    );
  });

  it('throws even when an unsupported operator sits behind a failing AND branch', () => {
    expect(() =>
      evaluateConditions({ AND: [{ a: 1 }, { b: { isSet: true } }] }, { a: 2 }),
    ).toThrow(UnsupportedConditionError);
  });

  it('throws at compile time, before any object is seen', () => {
    expect(() => compileConditions({ title: { search: 'x' } })).toThrow(UnsupportedConditionError);
  });

  it('carries the issues on the error', () => {
    let caught: unknown;
    try {
      compileConditions({ title: { isSet: true }, body: { search: 'y' } });
    } catch (error) {
      caught = error;
    }
    expect(isUnsupportedConditionError(caught)).toBe(true);
    const error = caught as UnsupportedConditionError;
    expect(error.name).toBe('UnsupportedConditionError');
    expect(error.issues).toHaveLength(2);
    expect(error.issues.map((entry) => entry.operator)).toEqual(['isSet', 'search']);
    expect(error.message).toContain('isSet');
    expect(error.message).toContain('title');
  });

  it('does not treat an ordinary error as a rejection', () => {
    expect(isUnsupportedConditionError(new Error('nope'))).toBe(false);
    expect(isUnsupportedConditionError(null)).toBe(false);
    expect(isUnsupportedConditionError({ name: 'UnsupportedConditionError' })).toBe(false);
  });

  it('recognises a structurally equivalent error from another package copy', () => {
    expect(
      isUnsupportedConditionError({ name: 'UnsupportedConditionError', issues: [] }),
    ).toBe(true);
  });

  it('reports supported conditions as supported with no issues', () => {
    const result = validateConditions({ viewCount: { gte: 100 } });
    expect(result.supported).toBe(true);
    expect(result.issues).toEqual([]);
  });
});

const TEXT_GUARD_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Post',
      dbName: 'post_rows',
      fields: [
        { name: 'id', dbName: 'post_pk', kind: 'scalar', type: 'Int', isList: false },
        { name: 'title', dbName: 'title_text', kind: 'scalar', type: 'String', isList: false },
        { name: 'subtitle', dbName: 'subtitle_text', kind: 'scalar', type: 'String', isList: false },
        { name: 'views', dbName: 'view_count', kind: 'scalar', type: 'Int', isList: false },
        { name: 'weight', dbName: 'weight_value', kind: 'scalar', type: 'Float', isList: false },
        { name: 'huge', dbName: 'huge_value', kind: 'scalar', type: 'BigInt', isList: false },
        { name: 'createdAt', dbName: 'created_at', kind: 'scalar', type: 'DateTime', isList: false },
        { name: 'published', dbName: 'is_published', kind: 'scalar', type: 'Boolean', isList: false },
        { name: 'status', dbName: 'status_value', kind: 'enum', type: 'Status', isList: false },
      ],
    },
  ],
};

const TEXT_GUARD_CONTEXT = { datamodel: TEXT_GUARD_DATAMODEL, model: 'Post' };

describe('the text operators against the column type', () => {
  const textual = ['contains', 'startsWith', 'endsWith'];

  const rejected: readonly (readonly [string, string])[] = [
    ['views', 'Int'],
    ['weight', 'Float'],
    ['huge', 'BigInt'],
    ['createdAt', 'DateTime'],
    ['published', 'Boolean'],
  ];

  for (const operator of textual) {
    for (const [field, type] of rejected) {
      it(`rejects ${operator} on the ${type} column ${field}`, () => {
        const result = validateConditions({ [field]: { [operator]: '0' } }, TEXT_GUARD_CONTEXT);
        expect(result.supported).toBe(false);
        expect(result.issues).toHaveLength(1);
        expect(result.issues[0]!.reason).toBe('unsupported-operator');
        expect(result.issues[0]!.operator).toBe(operator);
        expect(result.issues[0]!.path).toEqual([field]);
        expect(result.issues[0]!.message).toContain(`"Post.${field}" is a ${type} column`);
      });
    }

    it(`accepts ${operator} on a String column`, () => {
      expect(validateConditions({ title: { [operator]: 'a' } }, TEXT_GUARD_CONTEXT)).toEqual({
        supported: true,
        issues: [],
      });
    });

    it(`accepts ${operator} on an enum column, which golem renders as collated text`, () => {
      expect(validateConditions({ status: { [operator]: 'OPEN' } }, TEXT_GUARD_CONTEXT)).toEqual({
        supported: true,
        issues: [],
      });
    });

    it(`leaves ${operator} alone with no datamodel, where the column type is unknown`, () => {
      expect(validateConditions({ views: { [operator]: '0' } })).toEqual({
        supported: true,
        issues: [],
      });
    });
  }

  it('rejects the text operator wherever it appears, not only at the root', () => {
    const result = validateConditions(
      { AND: [{ title: 'a' }, { NOT: { views: { contains: '0' } } }] },
      TEXT_GUARD_CONTEXT,
    );
    expect(result.supported).toBe(false);
    expect(result.issues[0]!.message).toContain('"Post.views" is a Int column');
  });

  it('leaves the ordered and equality operators on a numeric column alone', () => {
    for (const operator of ['equals', 'not', 'lt', 'lte', 'gt', 'gte']) {
      expect(validateConditions({ views: { [operator]: 0 } }, TEXT_GUARD_CONTEXT)).toEqual({
        supported: true,
        issues: [],
      });
    }
  });
});

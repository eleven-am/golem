import { compileConditions } from './compile';
import { SqlRenderError } from './errors';
import {
  SqlDialect,
  SqlFragment,
  SqlScope,
  mysqlDialect,
  postgresDialect,
  renderSql,
  sqlIdentifier,
  sqlSequence,
  sqlText,
  sqliteDialect,
} from './sql';

interface RelationSpec {
  readonly table: string;
  readonly from: string;
  readonly to: string;
  readonly toMany?: boolean;
}

function scopeFor(table: string, relations: Record<string, RelationSpec> = {}): SqlScope {
  return {
    column: (field) => sqlIdentifier([table, field]),
    relation: (field) => {
      const relation = relations[field];
      if (relation === undefined) {
        return undefined;
      }
      return {
        scope: scopeFor(relation.table),
        toMany: relation.toMany === true,
        wrap: (predicate) =>
          sqlSequence([
            sqlText('EXISTS (SELECT 1 FROM '),
            sqlIdentifier([relation.table]),
            sqlText(' WHERE '),
            sqlIdentifier([relation.table, relation.to]),
            sqlText(' = '),
            sqlIdentifier([table, relation.from]),
            sqlText(' AND '),
            predicate,
            sqlText(')'),
          ]),
      };
    },
  };
}

const post = scopeFor('Post', {
  author: { table: 'User', from: 'authorId', to: 'id' },
  comments: { table: 'Comment', from: 'id', to: 'postId', toMany: true },
});

const COMMENT_HOP = 'SELECT 1 FROM "Comment" WHERE "Comment"."postId" = "Post"."id" AND';

function render(conditions: unknown, dialect: SqlDialect = postgresDialect, scope: SqlScope = post): SqlFragment {
  return renderSql(compileConditions(conditions).toSql(scope), dialect);
}

describe('sql rendering', () => {
  it('renders the implicit equals shorthand null-safely', () => {
    expect(render({ authorId: 'u1' })).toEqual({
      text: '("Post"."authorId" IS NOT DISTINCT FROM $1)',
      parameters: ['u1'],
    });
  });

  it('renders equals null as IS NULL with no parameters', () => {
    expect(render({ v: { equals: null } })).toEqual({ text: '("Post"."v" IS NULL)', parameters: [] });
  });

  it('renders not as a negated null-safe equality', () => {
    expect(render({ v: { not: 1 } })).toEqual({
      text: 'NOT ("Post"."v" IS NOT DISTINCT FROM $1)',
      parameters: [1],
    });
  });

  it('renders not null as IS NOT NULL', () => {
    expect(render({ v: { not: null } })).toEqual({ text: '("Post"."v" IS NOT NULL)', parameters: [] });
  });

  it('renders in with a null guard', () => {
    expect(render({ v: { in: [1, 2] } })).toEqual({
      text: '("Post"."v" IS NOT NULL AND "Post"."v" IN ($1, $2))',
      parameters: [1, 2],
    });
  });

  it('renders an empty in list as a false predicate', () => {
    expect(render({ v: { in: [] } })).toEqual({ text: '(1 = 0)', parameters: [] });
  });

  it('renders notIn so that null matches', () => {
    expect(render({ v: { notIn: [1] } })).toEqual({
      text: '("Post"."v" IS NULL OR "Post"."v" NOT IN ($1))',
      parameters: [1],
    });
  });

  it('renders an empty notIn list as a true predicate', () => {
    expect(render({ v: { notIn: [] } })).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it.each([
    ['lt', '<'],
    ['lte', '<='],
    ['gt', '>'],
    ['gte', '>='],
  ])('renders %s with a null guard', (operator, symbol) => {
    expect(render({ v: { [operator]: 15 } })).toEqual({
      text: `("Post"."v" IS NOT NULL AND "Post"."v" ${symbol} $1)`,
      parameters: [15],
    });
  });

  it('renders a comparison against a null operand as a false predicate', () => {
    expect(render({ v: { lt: null } })).toEqual({ text: '(1 = 0)', parameters: [] });
  });

  it('renders multiple operators on one field as a conjunction', () => {
    expect(render({ v: { gte: 1, lt: 10 } })).toEqual({
      text: '(("Post"."v" IS NOT NULL AND "Post"."v" >= $1) AND ("Post"."v" IS NOT NULL AND "Post"."v" < $2))',
      parameters: [1, 10],
    });
  });

  it('renders multiple fields as a conjunction', () => {
    expect(render({ authorId: 'u1', type: 'PERSONAL' })).toEqual({
      text: '(("Post"."authorId" IS NOT DISTINCT FROM $1) AND ("Post"."type" IS NOT DISTINCT FROM $2))',
      parameters: ['u1', 'PERSONAL'],
    });
  });

  it('renders empty conditions as a true predicate', () => {
    expect(render({})).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it('renders an empty AND as a true predicate', () => {
    expect(render({ AND: [] })).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it('renders an empty OR as a false predicate', () => {
    expect(render({ OR: [] })).toEqual({ text: '(1 = 0)', parameters: [] });
  });

  it('renders an empty NOT as a true predicate', () => {
    expect(render({ NOT: [] })).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it('renders OR branches', () => {
    expect(render({ OR: [{ a: 1 }, { b: 2 }] })).toEqual({
      text: '(("Post"."a" IS NOT DISTINCT FROM $1) OR ("Post"."b" IS NOT DISTINCT FROM $2))',
      parameters: [1, 2],
    });
  });

  it('renders NOT as the negation of the disjunction of its branches', () => {
    expect(render({ NOT: [{ a: 1 }, { b: 2 }] })).toEqual({
      text: 'NOT (("Post"."a" IS NOT DISTINCT FROM $1) OR ("Post"."b" IS NOT DISTINCT FROM $2))',
      parameters: [1, 2],
    });
  });

  it('renders a one-hop relation through the scope', () => {
    expect(render({ author: { is: { id: 'u1' } } })).toEqual({
      text: '(EXISTS (SELECT 1 FROM "User" WHERE "User"."id" = "Post"."authorId" AND ("User"."id" IS NOT DISTINCT FROM $1)))',
      parameters: ['u1'],
    });
  });

  it('renders some as a correlated EXISTS', () => {
    expect(render({ comments: { some: { body: 'x' } } })).toEqual({
      text: `(EXISTS (${COMMENT_HOP} ("Comment"."body" IS NOT DISTINCT FROM $1)))`,
      parameters: ['x'],
    });
  });

  it('renders none as a negated correlated EXISTS', () => {
    expect(render({ comments: { none: { body: 'x' } } })).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} ("Comment"."body" IS NOT DISTINCT FROM $1)))`,
      parameters: ['x'],
    });
  });

  it('renders every as a negated EXISTS over the rows the condition does not make true', () => {
    expect(render({ comments: { every: { body: 'x' } } })).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} ((("Comment"."body" IS NOT DISTINCT FROM $1)) IS NOT TRUE)))`,
      parameters: ['x'],
    });
  });

  it('never renders every as the negation of a negation, which an unknown row would slip through', () => {
    const { text } = render({ comments: { every: { body: 'x' } } });
    expect(text).toContain('IS NOT TRUE');
    expect(text).not.toContain('AND NOT');
  });

  it('conjoins some, every and none in one filter', () => {
    const { text, parameters } = render({
      comments: { some: { body: 'a' }, every: { body: 'b' }, none: { body: 'c' } },
    });
    expect(text).toBe(
      `((EXISTS (${COMMENT_HOP} ("Comment"."body" IS NOT DISTINCT FROM $1))) AND ` +
        `NOT (EXISTS (${COMMENT_HOP} ((("Comment"."body" IS NOT DISTINCT FROM $2)) IS NOT TRUE))) AND ` +
        `NOT (EXISTS (${COMMENT_HOP} ("Comment"."body" IS NOT DISTINCT FROM $3))))`,
    );
    expect(parameters).toEqual(['a', 'b', 'c']);
  });

  it('separates an empty condition object from an empty disjunction inside a quantifier', () => {
    expect(render({ comments: { some: {} } })).toEqual({
      text: `(EXISTS (${COMMENT_HOP} (1 = 1)))`,
      parameters: [],
    });
    expect(render({ comments: { every: {} } })).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} (((1 = 1)) IS NOT TRUE)))`,
      parameters: [],
    });
    expect(render({ comments: { none: {} } })).toEqual({
      text: `NOT (EXISTS (${COMMENT_HOP} (1 = 1)))`,
      parameters: [],
    });
  });

  it('renders an empty disjunction inside a to-many filter as a false predicate', () => {
    expect(render({ comments: { some: { OR: [] } } })).toEqual({
      text: `(EXISTS (${COMMENT_HOP} (1 = 0)))`,
      parameters: [],
    });
  });

  it('refuses a to-one operator on a to-many relation', () => {
    expect(() => render({ comments: { is: { body: 'x' } } })).toThrow(SqlRenderError);
    expect(() => render({ comments: { is: { body: 'x' } } })).toThrow('is to-many');
  });

  it('refuses a to-many operator on a to-one relation', () => {
    expect(() => render({ author: { some: { id: 'u1' } } })).toThrow(SqlRenderError);
    expect(() => render({ author: { some: { id: 'u1' } } })).toThrow('is to-one');
  });

  it('fails loudly when the scope cannot resolve the relation', () => {
    expect(() => render({ reviewer: { is: { id: 'u1' } } })).toThrow(SqlRenderError);
    expect(() => render({ reviewer: { is: { id: 'u1' } } })).toThrow('reviewer');
  });

  it('never inlines a value into the text', () => {
    const fragment = render({ authorId: "u1'; DROP TABLE Post; --" });
    expect(fragment.text).not.toContain('DROP');
    expect(fragment.parameters).toEqual(["u1'; DROP TABLE Post; --"]);
  });

  it('preserves bigint parameters exactly', () => {
    expect(render({ v: { gt: 9007199254740993n } }).parameters).toEqual([9007199254740993n]);
  });

  it('binds decimal operands as strings', () => {
    const value = { toFixed: () => '1.50', toString: () => '1.50' };
    expect(render({ v: { equals: value } }).parameters).toEqual(['1.50']);
  });

  it('binds date operands as dates', () => {
    const value = new Date(0);
    expect(render({ v: { gt: value } }).parameters).toEqual([value]);
  });

  it('numbers parameters in order of appearance', () => {
    expect(render({ OR: [{ a: 1 }, { AND: [{ b: 2 }, { c: 3 }] }] })).toEqual({
      text:
        '(("Post"."a" IS NOT DISTINCT FROM $1) OR (("Post"."b" IS NOT DISTINCT FROM $2) AND ("Post"."c" IS NOT DISTINCT FROM $3)))',
      parameters: [1, 2, 3],
    });
  });
});

describe('string operators', () => {
  it.each([
    ['contains', '%a%'],
    ['startsWith', 'a%'],
    ['endsWith', '%a'],
  ])('renders %s as a guarded LIKE with an explicit escape', (operator, pattern) => {
    expect(render({ v: { [operator]: 'a' } })).toEqual({
      text: `("Post"."v" IS NOT NULL AND "Post"."v" LIKE $1 ESCAPE '\\')`,
      parameters: [pattern],
    });
  });

  it.each([
    ['contains', 'instr("Post"."v", ?) > 0'],
    ['startsWith', 'instr("Post"."v", ?) = 1'],
  ])('renders %s on SQLite without LIKE, which SQLite cannot make case-sensitive', (operator, match) => {
    expect(render({ v: { [operator]: 'a' } }, sqliteDialect)).toEqual({
      text: `("Post"."v" IS NOT NULL AND ${match})`,
      parameters: ['a'],
    });
  });

  it('renders endsWith on SQLite as a substring taken from the right', () => {
    expect(render({ v: { endsWith: 'ab' } }, sqliteDialect)).toEqual({
      text: '("Post"."v" IS NOT NULL AND substr("Post"."v", ?) = ?)',
      parameters: [-2, 'ab'],
    });
  });

  it('counts an astral character once when taking a suffix, as SQLite does', () => {
    expect(render({ v: { endsWith: '👍' } }, sqliteDialect).parameters).toEqual([-1, '👍']);
  });

  it('never emits LIKE on SQLite, where LIKE ignores collation and folds ASCII case', () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      expect(render({ v: { [operator]: 'a' } }, sqliteDialect).text).not.toContain('LIKE');
    }
  });

  it('escapes the wildcards and the escape character in the pattern, and nothing else', () => {
    expect(render({ v: { contains: 'a%b_c\\d' } }).parameters).toEqual(['%a\\%b\\_c\\\\d%']);
    expect(render({ v: { startsWith: '%' } }).parameters).toEqual(['\\%%']);
    expect(render({ v: { endsWith: '_' } }).parameters).toEqual(['%\\_']);
    expect(render({ v: { contains: 'plain-text' } }).parameters).toEqual(['%plain-text%']);
  });

  it('leaves the operand untouched on SQLite, where no character is a wildcard', () => {
    expect(render({ v: { contains: 'a%b_c\\d' } }, sqliteDialect).parameters).toEqual(['a%b_c\\d']);
  });

  it('spells the escape literal the way each engine parses a string literal', () => {
    expect(render({ v: { contains: 'a' } }, postgresDialect).text).toContain(`ESCAPE '\\'`);
    expect(render({ v: { contains: 'a' } }, mysqlDialect).text).toContain(`ESCAPE '\\\\'`);
  });

  it('renders an empty operand as a presence test, which is what it means', () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      for (const dialect of [postgresDialect, mysqlDialect, sqliteDialect]) {
        expect(render({ v: { [operator]: '' } }, dialect).parameters).toEqual([]);
      }
      expect(render({ v: { [operator]: '' } }).text).toBe('("Post"."v" IS NOT NULL)');
    }
  });

  it('renders a null operand as a false predicate', () => {
    expect(render({ v: { contains: null } })).toEqual({ text: '(1 = 0)', parameters: [] });
    expect(render({ v: { startsWith: null } }, sqliteDialect)).toEqual({ text: '(1 = 0)', parameters: [] });
  });

  it('guards on null so the negation of a non-match is a match, as the evaluator has it', () => {
    expect(render({ NOT: { v: { contains: 'a' } } }).text).toBe(
      `NOT (("Post"."v" IS NOT NULL AND "Post"."v" LIKE $1 ESCAPE '\\'))`,
    );
  });

  it('renders insensitive mode as ILIKE where the engine has one', () => {
    expect(render({ v: { contains: 'a', mode: 'insensitive' } })).toEqual({
      text: `("Post"."v" IS NOT NULL AND "Post"."v" ILIKE $1 ESCAPE '\\')`,
      parameters: ['%a%'],
    });
  });

  it('keeps escaping the pattern under insensitive mode', () => {
    expect(render({ v: { contains: 'a%b', mode: 'insensitive' } }).parameters).toEqual(['%a\\%b%']);
  });

  it('drops a default mode without changing the match', () => {
    expect(render({ v: { contains: 'a', mode: 'default' } })).toEqual(render({ v: { contains: 'a' } }));
  });

  it('renders a filter carrying only a mode as no constraint at all', () => {
    expect(render({ v: { mode: 'insensitive' } })).toEqual({ text: '(1 = 1)', parameters: [] });
  });

  it.each([
    [mysqlDialect, 'mysql'],
    [sqliteDialect, 'sqlite'],
  ])('refuses insensitive mode on an engine without one, naming it', (dialect, name) => {
    expect(() => render({ v: { contains: 'a', mode: 'insensitive' } }, dialect)).toThrow(SqlRenderError);
    expect(() => render({ v: { contains: 'a', mode: 'insensitive' } }, dialect)).toThrow(name);
  });

  it('binds the operand as a parameter, never as inlined pattern text', () => {
    for (const dialect of [postgresDialect, mysqlDialect, sqliteDialect]) {
      const fragment = render({ v: { contains: "x'; DROP TABLE Post; --" } }, dialect);
      expect(fragment.text).not.toContain('DROP');
      expect(fragment.parameters).toHaveLength(1);
      expect(String(fragment.parameters[0])).toContain("x'; DROP TABLE Post; --");
    }
  });

  it('reaches a string operator through a relation hop', () => {
    expect(render({ author: { is: { id: { startsWith: 'u' } } } })).toEqual({
      text: `(EXISTS (SELECT 1 FROM "User" WHERE "User"."id" = "Post"."authorId" AND ("User"."id" IS NOT NULL AND "User"."id" LIKE $1 ESCAPE '\\')))`,
      parameters: ['u%'],
    });
  });
});

describe('dialects', () => {
  it('renders null-safe equality per engine', () => {
    expect(render({ a: 1 }, postgresDialect).text).toBe('("Post"."a" IS NOT DISTINCT FROM $1)');
    expect(render({ a: 1 }, mysqlDialect).text).toBe('(`Post`.`a` <=> ?)');
    expect(render({ a: 1 }, sqliteDialect).text).toBe('("Post"."a" IS ?)');
  });

  it('renders placeholders per engine', () => {
    expect(render({ a: 1, b: 2 }, postgresDialect).text).toContain('$2');
    expect(render({ a: 1, b: 2 }, mysqlDialect).text).not.toContain('$');
  });

  it('escapes identifier quoting', () => {
    expect(postgresDialect.quoteIdentifier('we"ird')).toBe('"we""ird"');
    expect(mysqlDialect.quoteIdentifier('we`ird')).toBe('`we``ird`');
    expect(sqliteDialect.quoteIdentifier('we"ird')).toBe('"we""ird"');
  });

  it('keeps parameters identical across dialects', () => {
    const conditions = { OR: [{ a: 1 }, { b: { in: [2, 3] } }] };
    const parameters = [postgresDialect, mysqlDialect, sqliteDialect].map(
      (dialect) => render(conditions, dialect).parameters,
    );
    expect(parameters[0]).toEqual(parameters[1]);
    expect(parameters[1]).toEqual(parameters[2]);
  });
});

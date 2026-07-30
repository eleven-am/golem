import { ConditionIssue, SqlRenderError } from './errors';
import {
  JSON_ABSENT,
  JsonPathSegment,
  JsonSlot,
  compileJsonFilter,
  jsonContains,
  jsonEquals,
  navigateJson,
  readNullSentinel,
  renderJsonFilter,
  validateJsonFilter,
} from './json';
import { SqlFieldTarget } from './operators';
import {
  SqlDialect,
  SqlNode,
  SqlScope,
  mysqlDialect,
  postgresDialect,
  renderSql,
  sqlIdentifier,
  sqliteDialect,
} from './sql';

const SCOPE: SqlScope = {
  column: (field) => sqlIdentifier(['t0', field]),
  relation: () => undefined,
};

function target(field = 'payload'): SqlFieldTarget {
  return { field, scope: SCOPE, path: [field] };
}

function render(filter: unknown, dialect: SqlDialect): { text: string; parameters: readonly unknown[] } {
  const node: SqlNode = renderJsonFilter(filter, target());
  const fragment = renderSql(node, dialect);
  return { text: fragment.text, parameters: fragment.parameters };
}

function refusal(filter: unknown, dialect: SqlDialect): string {
  try {
    render(filter, dialect);
  } catch (error) {
    if (error instanceof SqlRenderError) {
      return error.message;
    }
    throw error;
  }
  throw new Error('expected the render to be refused');
}

function issuesOf(filter: unknown): readonly ConditionIssue[] {
  const issues: ConditionIssue[] = [];
  validateJsonFilter('payload', filter, ['payload'], issues);
  return issues;
}

function matches(filter: unknown, value: unknown): boolean {
  return compileJsonFilter(filter)(value);
}

describe('json path navigation', () => {
  const document = { a: { b: 'hello' }, list: [1, { c: 2 }], nulled: null, '': 'empty' };

  it('separates a missing path from a path holding JSON null', () => {
    expect(navigateJson(document, [{ text: 'missing', index: null }] as JsonPathSegment[])).toBe(JSON_ABSENT);
    expect(navigateJson(document, [{ text: 'nulled', index: null }] as JsonPathSegment[])).toBeNull();
  });

  it('treats an undefined column as absent and a null column as JSON null', () => {
    expect(navigateJson(undefined, [])).toBe(JSON_ABSENT);
    expect(navigateJson(null, [])).toBeNull();
  });

  it('stops at JSON null rather than reading through it', () => {
    expect(
      navigateJson(document, [
        { text: 'nulled', index: null },
        { text: 'a', index: null },
      ] as JsonPathSegment[]),
    ).toBe(JSON_ABSENT);
  });

  it('indexes arrays by numeric segment and refuses a name', () => {
    expect(navigateJson(document, [{ text: 'list', index: null }, { text: '0', index: 0 }] as JsonPathSegment[])).toBe(1);
    expect(
      navigateJson(document, [{ text: 'list', index: null }, { text: 'x', index: null }] as JsonPathSegment[]),
    ).toBe(JSON_ABSENT);
    expect(
      navigateJson(document, [{ text: 'list', index: null }, { text: '9', index: 9 }] as JsonPathSegment[]),
    ).toBe(JSON_ABSENT);
  });

  it('reads a JSONPath string and an array path to the same place', () => {
    expect(matches({ path: '$.a.b', equals: 'hello' }, document)).toBe(true);
    expect(matches({ path: ['a', 'b'], equals: 'hello' }, document)).toBe(true);
    expect(matches({ path: '$.list[1].c', equals: 2 }, document)).toBe(true);
    expect(matches({ path: '$."a"."b"', equals: 'hello' }, document)).toBe(true);
  });

  it('addresses the empty-string key', () => {
    expect(matches({ path: ['' ], equals: 'empty' }, document)).toBe(true);
    expect(matches({ path: '$.""', equals: 'empty' }, document)).toBe(true);
  });
});

describe('json null sentinels', () => {
  it('reads the tagged form and the Prisma runtime objects', () => {
    expect(readNullSentinel({ $type: 'DbNull' })).toBe('DbNull');
    expect(readNullSentinel({ $type: 'AnyNull' })).toBe('AnyNull');
    expect(readNullSentinel({ $type: 'nonsense' })).toBeNull();
    expect(readNullSentinel('DbNull')).toBeNull();
    class JsonNull {
      toString(): string {
        return 'Prisma.JsonNull';
      }
    }
    expect(readNullSentinel(new JsonNull())).toBe('JsonNull');
  });

  it('separates DbNull, JsonNull and AnyNull in the evaluator', () => {
    const absent = undefined;
    const jsonNull = { a: null };
    expect(matches({ equals: { $type: 'DbNull' } }, absent)).toBe(true);
    expect(matches({ equals: { $type: 'DbNull' } }, null)).toBe(false);
    expect(matches({ path: ['a'], equals: { $type: 'DbNull' } }, jsonNull)).toBe(false);
    expect(matches({ path: ['a'], equals: { $type: 'JsonNull' } }, jsonNull)).toBe(true);
    expect(matches({ path: ['zz'], equals: { $type: 'JsonNull' } }, jsonNull)).toBe(false);
    expect(matches({ path: ['zz'], equals: { $type: 'DbNull' } }, jsonNull)).toBe(true);
    expect(matches({ path: ['zz'], equals: { $type: 'AnyNull' } }, jsonNull)).toBe(true);
    expect(matches({ path: ['a'], equals: { $type: 'AnyNull' } }, jsonNull)).toBe(true);
    expect(matches({ path: ['b'], equals: { $type: 'AnyNull' } }, { a: null, b: 1 })).toBe(false);
  });

  it('leaves an absent value unmatched by not, as Prisma does', () => {
    expect(matches({ path: ['a'], not: 'hello' }, { a: 'hello' })).toBe(false);
    expect(matches({ path: ['a'], not: 'hello' }, { a: 'other' })).toBe(true);
    expect(matches({ path: ['a'], not: 'hello' }, { b: 1 })).toBe(false);
    expect(matches({ path: ['a'], not: 'hello' }, { a: null })).toBe(true);
  });

  it('matches an absent value with not: DbNull only when it is present', () => {
    expect(matches({ path: ['a'], not: { $type: 'DbNull' } }, { a: null })).toBe(true);
    expect(matches({ path: ['a'], not: { $type: 'DbNull' } }, { b: 1 })).toBe(false);
    expect(matches({ path: ['a'], not: { $type: 'JsonNull' } }, { a: null })).toBe(false);
    expect(matches({ path: ['a'], not: { $type: 'JsonNull' } }, { a: 1 })).toBe(true);
    expect(matches({ path: ['a'], not: { $type: 'JsonNull' } }, { b: 1 })).toBe(false);
  });
});

describe('json value comparison', () => {
  it('compares objects without regard to key order', () => {
    expect(jsonEquals({ a: 1, b: 2 }, { b: 2, a: 1 })).toBe(true);
    expect(jsonEquals({ a: 1 }, { a: 1, b: 2 })).toBe(false);
    expect(jsonEquals([1, 2], [2, 1])).toBe(false);
    expect(jsonEquals(1, '1' as never)).toBe(false);
    expect(jsonEquals(null, false as never)).toBe(false);
  });

  it('reproduces the containment rules golem renders as @>', () => {
    expect(jsonContains([1, 2, 3], 2)).toBe(true);
    expect(jsonContains([1, 2, 3], [1, 2])).toBe(true);
    expect(jsonContains([[1, 2]], [[1]])).toBe(true);
    expect(jsonContains([[1, 2]], [1])).toBe(false);
    expect(jsonContains([{ a: 1, b: 2 }], [{ a: 1 }])).toBe(true);
    expect(jsonContains([1], [])).toBe(true);
    expect(jsonContains({ a: 1, b: 2 }, { a: 1 })).toBe(true);
    expect(jsonContains([1, 2], { a: 1 })).toBe(false);
  });
});

describe('json filter validation', () => {
  it('rejects a bare value where Prisma has no shorthand', () => {
    expect(issuesOf(5)[0]!.message).toContain('no bare-value shorthand');
    expect(issuesOf(null)[0]!.reason).toBe('unsupported-shape');
  });

  it('names a key that is not part of the Json filter', () => {
    const issue = issuesOf({ contains: 'x' })[0]!;
    expect(issue.operator).toBe('contains');
    expect(issue.message).toContain('string_contains');
  });

  it('refuses a path that is neither a segment array nor a JSONPath', () => {
    expect(issuesOf({ path: 7, equals: 1 })[0]!.message).toContain('JSONPath string');
    expect(issuesOf({ path: ['a', 1], equals: 1 })[0]!.message).toContain('string[]');
    expect(issuesOf({ path: 'a.b', equals: 1 })[0]!.message).toContain('rooted at "$"');
    expect(issuesOf({ path: '$.a[', equals: 1 })[0]!.message).toContain('rooted at "$"');
    expect(issuesOf({ path: '$.a.b', equals: 1 })).toHaveLength(0);
  });

  it('refuses an operand with no JSON representation', () => {
    expect(issuesOf({ equals: new Date() })[0]!.message).toContain('no JSON representation');
    expect(issuesOf({ equals: Number.NaN })[0]!.message).toContain('no JSON representation');
    expect(issuesOf({ equals: { a: [1, undefined] } })[0]!.message).toContain('no JSON representation');
    expect(issuesOf({ equals: { a: [1, 2] } })).toHaveLength(0);
  });

  it('refuses a number the evaluator cannot tell apart from its neighbour', () => {
    expect(issuesOf({ equals: 9007199254740993 })[0]!.message).toContain('9007199254740993');
    expect(issuesOf({ equals: 9007199254740992 })[0]!.reason).toBe('unsupported-value');
    expect(issuesOf({ not: -9007199254740992 })[0]!.operator).toBe('not');
    expect(issuesOf({ gt: 1e21 })[0]!.message).toContain('safe integer range');
    expect(issuesOf({ lte: 9007199254740992 })[0]!.message).toContain('safe integer range');
    expect(issuesOf({ array_contains: [1, 9007199254740992] })[0]!.message).toContain('safe integer range');
    expect(issuesOf({ equals: { a: [9007199254740992] } })[0]!.message).toContain('safe integer range');
  });

  it('accepts every number inside the safe integer range', () => {
    expect(issuesOf({ equals: 9007199254740991 })).toHaveLength(0);
    expect(issuesOf({ equals: -9007199254740991 })).toHaveLength(0);
    expect(issuesOf({ gte: 9007199254740991 })).toHaveLength(0);
    expect(issuesOf({ equals: 0.1 })).toHaveLength(0);
    expect(issuesOf({ equals: 1e15 })).toHaveLength(0);
  });

  it('requires a string for the string operators', () => {
    expect(issuesOf({ string_contains: 5 })[0]!.message).toContain('against a string');
    expect(issuesOf({ string_starts_with: null })[0]!.operator).toBe('string_starts_with');
  });

  it('orders only numbers and strings', () => {
    expect(issuesOf({ lt: 5 })).toHaveLength(0);
    expect(issuesOf({ lt: 'x' })).toHaveLength(0);
    expect(issuesOf({ lt: true })[0]!.message).toContain('will not order a JSON boolean');
    expect(issuesOf({ gte: [1] })[0]!.message).toContain('will not order a JSON array');
  });

  it('folds case only for the string operators', () => {
    expect(issuesOf({ mode: 'insensitive', string_contains: 'a' })).toHaveLength(0);
    expect(issuesOf({ mode: 'default', equals: 1 })).toHaveLength(0);
    expect(issuesOf({ mode: 'loud', string_contains: 'a' })[0]!.message).toContain('"default" or "insensitive"');
    expect(issuesOf({ mode: 'insensitive', equals: 1 })[0]!.message).toContain('folds case for');
  });
});

describe('json filter rendering on postgres', () => {
  it('navigates with #> and compares a normalised document', () => {
    expect(render({ path: ['a', 'b'], equals: 'hello' }, postgresDialect)).toEqual({
      text:
        '(("t0"."payload" #> ARRAY[$1, $2]::text[]) IS NOT NULL AND ' +
        '("t0"."payload" #> ARRAY[$3, $4]::text[]) = $5::jsonb)',
      parameters: ['a', 'b', 'a', 'b', '"hello"'],
    });
  });

  it('touches the bare column when no path is given, so an index can still apply', () => {
    expect(render({ equals: { a: 1 } }, postgresDialect)).toEqual({
      text: '("t0"."payload" IS NOT NULL AND "t0"."payload" = $1::jsonb)',
      parameters: ['{"a":1}'],
    });
    expect(render({ array_contains: 1 }, postgresDialect).text).toContain('"t0"."payload" @> ');
  });

  it('renders DbNull as SQL NULL and JsonNull as a JSON type test', () => {
    expect(render({ path: ['a'], equals: { $type: 'DbNull' } }, postgresDialect).text).toBe(
      '(("t0"."payload" #> ARRAY[$1]::text[]) IS NULL)',
    );
    const jsonNull = render({ path: ['a'], equals: { $type: 'JsonNull' } }, postgresDialect);
    expect(jsonNull.text).toContain('jsonb_typeof');
    expect(jsonNull.parameters).toEqual(['a', 'a', 'a', 'null']);
    const anyNull = render({ path: ['a'], equals: { $type: 'AnyNull' } }, postgresDialect);
    expect(anyNull.text).toContain(' IS NULL) OR (');
  });

  it('guards every operator with the JSON type Prisma guards it with', () => {
    expect(render({ path: ['a'], string_contains: 'x' }, postgresDialect).parameters).toContain('string');
    expect(render({ path: ['a'], array_contains: 1 }, postgresDialect).parameters).toContain('array');
    expect(render({ path: ['a'], lt: 5 }, postgresDialect).parameters).toContain('number');
    expect(render({ path: ['a'], lt: 'z' }, postgresDialect).parameters).toContain('string');
  });

  it('escapes LIKE wildcards in a Json string operand', () => {
    const { text, parameters } = render({ path: ['a'], string_contains: '50%_x\\y' }, postgresDialect);
    expect(text).toContain(" ESCAPE '\\'");
    expect(parameters).toContain('%50\\%\\_x\\\\y%');
  });

  it('compares extracted strings in byte order', () => {
    expect(render({ path: ['a'], gt: 'B' }, postgresDialect).text).toContain('COLLATE "C" > ');
  });

  it('compares numbers as documents rather than as text', () => {
    const { text, parameters } = render({ path: ['a'], gt: 5 }, postgresDialect);
    expect(text).toContain('> $5::jsonb');
    expect(parameters).toEqual(['a', 'a', 'number', 'a', '5']);
  });

  it('renders array_contains as containment and the edges as element equality', () => {
    expect(render({ path: ['a'], array_contains: [1] }, postgresDialect).text).toContain(' @> ');
    expect(render({ path: ['a'], array_starts_with: 1 }, postgresDialect).text).toContain(' -> 0');
    expect(render({ path: ['a'], array_ends_with: 1 }, postgresDialect).text).toContain(' -> -1');
  });

  it('conjoins several operators under one path', () => {
    const { text } = render({ path: ['a'], gt: 1, lt: 100 }, postgresDialect);
    expect(text).toContain(') AND (');
  });

  it('refuses a JSONPath string', () => {
    expect(refusal({ path: '$.a', equals: 1 }, postgresDialect)).toContain('postgres takes an array of segments');
  });
});

describe('json filter rendering on sqlite', () => {
  it('navigates with -> and a JSONPath string', () => {
    const { text, parameters } = render({ path: '$.a.b', equals: 'hello' }, sqliteDialect);
    expect(text).toContain('"t0"."payload" -> ?');
    expect(parameters).toContain('$.a.b');
    expect(parameters).toContain('hello');
  });

  it('compares scalars structurally rather than as document text', () => {
    const { text, parameters } = render({ path: '$.a', equals: 12 }, sqliteDialect);
    expect(text).toContain('json_type(');
    expect(text).toContain(" ->> '$'");
    expect(parameters).toEqual(['$.a', '$.a', 'integer', 'real', '$.a', 12]);
  });

  it('refuses a document operand whose answer would turn on key order', () => {
    expect(refusal({ path: '$.a', equals: { b: 1 } }, sqliteDialect)).toContain('key order');
    expect(refusal({ equals: [1, 2] }, sqliteDialect)).toContain('compares JSON documents as text');
  });

  it('refuses the operators Prisma does not generate for sqlite', () => {
    expect(refusal({ path: '$.a', array_contains: 1 }, sqliteDialect)).toContain('containment');
    expect(refusal({ path: '$.a', lt: 1 }, sqliteDialect)).toContain('no ordering comparison');
    expect(refusal({ path: '$.a', gte: 'z' }, sqliteDialect)).toContain('sqlite');
  });

  it('refuses an array path', () => {
    expect(refusal({ path: ['a'], equals: 1 }, sqliteDialect)).toContain('a JSONPath string');
  });

  it('matches strings with instr rather than a case-folding LIKE', () => {
    expect(render({ path: '$.a', string_contains: 'x' }, sqliteDialect).text).toContain('instr(');
    expect(render({ path: '$.a', string_ends_with: 'xy' }, sqliteDialect).text).toContain('substr(');
  });

  it('refuses insensitive matching where no case fold agrees with the evaluator', () => {
    expect(refusal({ path: '$.a', string_contains: 'x', mode: 'insensitive' }, sqliteDialect)).toContain(
      'operator "string_contains" was given mode "insensitive", and sqlite',
    );
    expect(refusal({ path: '$.a', string_starts_with: 'x', mode: 'insensitive' }, sqliteDialect)).toContain(
      'the evaluator folds ASCII case only',
    );
  });

  it('refuses insensitive matching for an empty operand too, so renderability never turns on the operand', () => {
    expect(refusal({ path: '$."a"', string_contains: '', mode: 'insensitive' }, sqliteDialect)).toContain(
      'operator "string_contains" was given mode "insensitive", and sqlite',
    );
    expect(refusal({ path: '$."a"', string_starts_with: '', mode: 'insensitive' }, sqliteDialect)).toContain(
      'operator "string_starts_with" was given mode "insensitive", and sqlite',
    );
    expect(refusal({ path: '$."a"', string_contains: '', mode: 'insensitive' }, mysqlDialect)).toContain(
      'operator "string_contains" was given mode "insensitive", and mysql',
    );
    expect(render({ path: ['a'], string_contains: '', mode: 'insensitive' }, postgresDialect).text).toContain(
      'jsonb_typeof',
    );
  });

  it('keeps the array edges as element comparisons', () => {
    expect(render({ path: '$.a', array_starts_with: 1 }, sqliteDialect).text).toContain("-> '$[0]'");
    expect(render({ path: '$.a', array_ends_with: 1 }, sqliteDialect).text).toContain("-> '$[#-1]'");
  });
});

describe('json filter rendering on mysql', () => {
  it('navigates with JSON_EXTRACT and a JSONPath string', () => {
    const { text, parameters } = render({ path: '$.a', equals: 1 }, mysqlDialect);
    expect(text).toContain('JSON_EXTRACT(`t0`.`payload`, ?)');
    expect(text).toContain('CAST(? AS JSON)');
    expect(parameters).toEqual(['$.a', '$.a', '1']);
  });

  it('renders containment with JSON_CONTAINS', () => {
    expect(render({ path: '$.a', array_contains: 1 }, mysqlDialect).text).toContain('JSON_CONTAINS(');
  });

  it('names the mysql JSON type in the guard', () => {
    expect(render({ path: '$.a', lt: 5 }, mysqlDialect).parameters).toEqual([
      '$.a',
      '$.a',
      'INTEGER',
      'DOUBLE',
      'DECIMAL',
      '$.a',
      '5',
    ]);
  });

  it('refuses an array path and insensitive matching', () => {
    expect(refusal({ path: ['a'], equals: 1 }, mysqlDialect)).toContain('mysql takes a JSONPath string');
    expect(refusal({ string_contains: 'x', mode: 'insensitive' }, mysqlDialect)).toContain(
      'operator "string_contains" was given mode "insensitive", and mysql',
    );
  });
});

describe('json evaluator', () => {
  const rows: readonly { readonly id: number; readonly payload: unknown }[] = [
    { id: 1, payload: { a: { b: 'hello' } } },
    { id: 2, payload: { a: { b: null } } },
    { id: 3, payload: { a: [1, 2, 3] } },
    { id: 4, payload: { z: 1 } },
    { id: 5, payload: [1, 2, 3] },
    { id: 6, payload: { a: 10 } },
    { id: 7, payload: { a: 'x%y_z\\w' } },
    { id: 8, payload: null },
  ];

  function selected(filter: unknown): readonly number[] {
    const predicate = compileJsonFilter(filter);
    return rows.filter((row) => predicate(row.payload)).map((row) => row.id);
  }

  it('reads a nested string, a nested array and a nested object', () => {
    expect(selected({ path: ['a', 'b'], equals: 'hello' })).toEqual([1]);
    expect(selected({ path: ['a'], equals: [1, 2, 3] })).toEqual([3]);
    expect(selected({ path: ['a'], equals: { b: 'hello' } })).toEqual([1]);
  });

  it('separates a missing path from a path holding JSON null', () => {
    expect(selected({ path: ['a', 'b'], equals: { $type: 'JsonNull' } })).toEqual([2]);
    expect(selected({ path: ['a', 'b'], equals: { $type: 'DbNull' } })).toEqual([3, 4, 5, 6, 7, 8]);
    expect(selected({ path: ['a', 'b'], equals: { $type: 'AnyNull' } })).toEqual([2, 3, 4, 5, 6, 7, 8]);
    expect(selected({ equals: { $type: 'JsonNull' } })).toEqual([8]);
  });

  it('matches the string operators against wildcard content literally', () => {
    expect(selected({ path: ['a'], string_contains: '%' })).toEqual([7]);
    expect(selected({ path: ['a'], string_contains: '_' })).toEqual([7]);
    expect(selected({ path: ['a'], string_contains: '\\' })).toEqual([7]);
    expect(selected({ path: ['a'], string_starts_with: 'x%' })).toEqual([7]);
    expect(selected({ path: ['a'], string_ends_with: '\\w' })).toEqual([7]);
    expect(selected({ path: ['a'], string_contains: 'q%' })).toEqual([]);
  });

  it('holds the array operators to the array type', () => {
    expect(selected({ path: ['a'], array_contains: 2 })).toEqual([3]);
    expect(selected({ path: ['a'], array_starts_with: 1 })).toEqual([3]);
    expect(selected({ path: ['a'], array_ends_with: 3 })).toEqual([3]);
    expect(selected({ array_contains: 2 })).toEqual([5]);
    expect(selected({ path: ['a'], array_starts_with: 'hello' })).toEqual([]);
  });

  it('orders only same-typed values', () => {
    expect(selected({ path: ['a'], gte: 10 })).toEqual([6]);
    expect(selected({ path: ['a'], lt: 20 })).toEqual([6]);
    expect(selected({ path: ['a'], lt: 'y' })).toEqual([7]);
    expect(selected({ path: ['a'], gt: 1 })).toEqual([6]);
  });

  it('leaves an empty filter matching every row', () => {
    expect(selected({})).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(selected({ path: ['a'] })).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
  });

  it('treats an empty needle as a type test', () => {
    expect(selected({ path: ['a'], string_contains: '' })).toEqual([7]);
  });
});

describe('json slot typing', () => {
  it('exposes absence as a distinct value', () => {
    const slot: JsonSlot = navigateJson({ a: 1 }, [{ text: 'b', index: null }] as JsonPathSegment[]);
    expect(slot).toBe(JSON_ABSENT);
    expect(slot === null).toBe(false);
  });
});

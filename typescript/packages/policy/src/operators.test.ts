import { evaluateConditions } from './compile';
import { OPERATORS, SUPPORTED_COMBINATORS, SUPPORTED_OPERATORS } from './operators';

type Case = [string, Record<string, unknown>, unknown, boolean];

function decimal(text: string): { toFixed(digits?: number): string; toString(): string } {
  return {
    toFixed: () => text,
    toString: () => text,
  };
}

const BEYOND_SAFE = 9007199254740993n;
const AT_SAFE_BOUNDARY = 9007199254740992n;

const equalsCases: Case[] = [
  ['equals matches an identical number', { v: { equals: 1 } }, { v: 1 }, true],
  ['equals rejects a different number', { v: { equals: 1 } }, { v: 0 }, false],
  ['equals matches zero', { v: { equals: 0 } }, { v: 0 }, true],
  ['equals does not match zero against null', { v: { equals: 0 } }, { v: null }, false],
  ['equals does not match zero against an absent field', { v: { equals: 0 } }, {}, false],
  ['equals does not conflate zero and false', { v: { equals: 0 } }, { v: false }, false],
  ['equals matches false', { v: { equals: false } }, { v: false }, true],
  ['equals does not conflate false and zero', { v: { equals: false } }, { v: 0 }, false],
  ['equals does not match false against null', { v: { equals: false } }, { v: null }, false],
  ['equals matches the empty string', { v: { equals: '' } }, { v: '' }, true],
  ['equals does not match the empty string against null', { v: { equals: '' } }, { v: null }, false],
  ['equals does not match the empty string against zero', { v: { equals: '' } }, { v: 0 }, false],
  ['equals null matches null', { v: { equals: null } }, { v: null }, true],
  ['equals null matches an absent field', { v: { equals: null } }, {}, true],
  ['equals null does not match zero', { v: { equals: null } }, { v: 0 }, false],
  ['equals null does not match the empty string', { v: { equals: null } }, { v: '' }, false],
  ['equals null does not match false', { v: { equals: null } }, { v: false }, false],
  ['equals undefined behaves as equals null', { v: { equals: undefined } }, { v: null }, true],
  ['equals undefined does not match a present value', { v: { equals: undefined } }, { v: 0 }, false],
  ['equals matches a bigint operand against a number', { v: { equals: 1n } }, { v: 1 }, true],
  ['equals matches a bigint operand against a bigint', { v: { equals: 1n } }, { v: 1n }, true],
  ['equals rejects a bigint operand against a different number', { v: { equals: 1n } }, { v: 2 }, false],
  ['equals matches beyond the safe integer boundary', { v: { equals: BEYOND_SAFE } }, { v: BEYOND_SAFE }, true],
  [
    'equals separates adjacent bigints beyond the safe boundary',
    { v: { equals: BEYOND_SAFE } },
    { v: AT_SAFE_BOUNDARY },
    false,
  ],
  [
    'equals does not collapse a bigint onto an imprecise number',
    { v: { equals: BEYOND_SAFE } },
    { v: 9007199254740992 },
    false,
  ],
  ['equals matches a decimal against a number', { v: { equals: 1.5 } }, { v: decimal('1.50') }, true],
  ['equals matches a decimal operand against a bigint', { v: { equals: decimal('2') } }, { v: 2n }, true],
  [
    'equals distinguishes decimals below number precision',
    { v: { equals: decimal('0.1000000000000000000001') } },
    { v: 0.1 },
    false,
  ],
  ['equals matches an identical date', { v: { equals: new Date(0) } }, { v: new Date(0) }, true],
  ['equals rejects a different date', { v: { equals: new Date(0) } }, { v: new Date(1) }, false],
  [
    'equals matches an ISO string against a date',
    { v: { equals: new Date(0) } },
    { v: '1970-01-01T00:00:00.000Z' },
    true,
  ],
  ['equals does not match a date against null', { v: { equals: new Date(0) } }, { v: null }, false],
  ['equals does not match across scalar types', { v: { equals: '1' } }, { v: 1 }, false],
];

const notCases: Case[] = [
  ['not matches a different value', { v: { not: 1 } }, { v: 2 }, true],
  ['not rejects an equal value', { v: { not: 1 } }, { v: 1 }, false],
  ['not matches null', { v: { not: 1 } }, { v: null }, true],
  ['not matches an absent field', { v: { not: 1 } }, {}, true],
  ['not null rejects null', { v: { not: null } }, { v: null }, false],
  ['not null rejects an absent field', { v: { not: null } }, {}, false],
  ['not null matches zero', { v: { not: null } }, { v: 0 }, true],
  ['not null matches the empty string', { v: { not: null } }, { v: '' }, true],
  ['not null matches false', { v: { not: null } }, { v: false }, true],
  ['not false rejects false', { v: { not: false } }, { v: false }, false],
  ['not false matches zero', { v: { not: false } }, { v: 0 }, true],
  ['not zero rejects zero', { v: { not: 0 } }, { v: 0 }, false],
  ['not zero matches the empty string', { v: { not: 0 } }, { v: '' }, true],
  ['not is bigint exact', { v: { not: 1n } }, { v: 1 }, false],
  ['not separates adjacent bigints', { v: { not: BEYOND_SAFE } }, { v: AT_SAFE_BOUNDARY }, true],
];

const inCases: Case[] = [
  ['in matches a member', { v: { in: [1, 2] } }, { v: 1 }, true],
  ['in rejects a non-member', { v: { in: [1, 2] } }, { v: 3 }, false],
  ['in rejects null', { v: { in: [1, 2] } }, { v: null }, false],
  ['in rejects an absent field', { v: { in: [1, 2] } }, {}, false],
  ['an empty in list matches nothing', { v: { in: [] } }, { v: 1 }, false],
  ['an empty in list does not match null', { v: { in: [] } }, { v: null }, false],
  ['an empty in list does not match an absent field', { v: { in: [] } }, {}, false],
  ['in matches zero', { v: { in: [0] } }, { v: 0 }, true],
  ['in does not conflate zero and false', { v: { in: [0] } }, { v: false }, false],
  ['in matches the empty string', { v: { in: [''] } }, { v: '' }, true],
  ['in matches false', { v: { in: [false] } }, { v: false }, true],
  ['in is bigint exact', { v: { in: [1n] } }, { v: 1 }, true],
  ['in separates adjacent bigints', { v: { in: [BEYOND_SAFE] } }, { v: AT_SAFE_BOUNDARY }, false],
  ['in matches a date member', { v: { in: [new Date(0)] } }, { v: new Date(0) }, true],
];

const notInCases: Case[] = [
  ['notIn matches a non-member', { v: { notIn: [1, 2] } }, { v: 3 }, true],
  ['notIn rejects a member', { v: { notIn: [1, 2] } }, { v: 1 }, false],
  ['notIn matches null', { v: { notIn: [1, 2] } }, { v: null }, true],
  ['notIn matches an absent field', { v: { notIn: [1, 2] } }, {}, true],
  ['an empty notIn list matches everything', { v: { notIn: [] } }, { v: 1 }, true],
  ['an empty notIn list matches null', { v: { notIn: [] } }, { v: null }, true],
  ['notIn rejects zero when zero is listed', { v: { notIn: [0] } }, { v: 0 }, false],
  ['notIn matches false when zero is listed', { v: { notIn: [0] } }, { v: false }, true],
  ['notIn is bigint exact', { v: { notIn: [1n] } }, { v: 1 }, false],
];

const comparisonCases: Case[] = [
  ['lt matches a smaller value', { v: { lt: 15 } }, { v: 10 }, true],
  ['lt rejects an equal value', { v: { lt: 15 } }, { v: 15 }, false],
  ['lt rejects a larger value', { v: { lt: 15 } }, { v: 20 }, false],
  ['lt never matches null', { v: { lt: 15 } }, { v: null }, false],
  ['lt never matches an absent field', { v: { lt: 15 } }, {}, false],
  ['lt does not coerce false to zero', { v: { lt: 15 } }, { v: false }, false],
  ['lt does not coerce the empty string to zero', { v: { lt: 15 } }, { v: '' }, false],
  ['lt matches zero below a positive bound', { v: { lt: 15 } }, { v: 0 }, true],
  ['lte matches an equal value', { v: { lte: 15 } }, { v: 15 }, true],
  ['lte matches a smaller value', { v: { lte: 15 } }, { v: 14 }, true],
  ['lte rejects a larger value', { v: { lte: 15 } }, { v: 16 }, false],
  ['lte never matches null', { v: { lte: 15 } }, { v: null }, false],
  ['gt matches a larger value', { v: { gt: 0 } }, { v: 1 }, true],
  ['gt rejects an equal value', { v: { gt: 0 } }, { v: 0 }, false],
  ['gt rejects a smaller value', { v: { gt: 0 } }, { v: -1 }, false],
  ['gt never matches null', { v: { gt: 0 } }, { v: null }, false],
  ['gt never matches an absent field', { v: { gt: 0 } }, {}, false],
  ['gte matches an equal value', { v: { gte: 0 } }, { v: 0 }, true],
  ['gte matches a larger value', { v: { gte: 0 } }, { v: 1 }, true],
  ['gte rejects a smaller value', { v: { gte: 0 } }, { v: -1 }, false],
  ['gte never matches null', { v: { gte: 0 } }, { v: null }, false],
  ['gte never matches an absent field', { v: { gte: 0 } }, {}, false],
  ['gte matches the demo analyst threshold', { v: { gte: 100 } }, { v: 100 }, true],
  ['gte rejects below the demo analyst threshold', { v: { gte: 100 } }, { v: 99 }, false],
  [
    'gt is exact just past the safe integer boundary',
    { v: { gt: 9007199254740992 } },
    { v: BEYOND_SAFE },
    true,
  ],
  [
    'gt rejects a value equal to the safe integer boundary',
    { v: { gt: 9007199254740992 } },
    { v: AT_SAFE_BOUNDARY },
    false,
  ],
  [
    'gte matches at the boundary the demo rule excludes',
    { v: { gte: 9007199254740992 } },
    { v: AT_SAFE_BOUNDARY },
    true,
  ],
  [
    'comparison compares decimals without precision loss',
    { v: { gt: decimal('0.1') } },
    { v: decimal('0.100000000000000000001') },
    true,
  ],
  ['comparison compares strings lexicographically', { v: { gt: 'a' } }, { v: 'b' }, true],
  ['comparison rejects a smaller string', { v: { gt: 'a' } }, { v: 'A' }, false],
  ['comparison rejects the empty string as smaller', { v: { gt: 'a' } }, { v: '' }, false],
  ['lt matches the empty string as smaller', { v: { lt: 'a' } }, { v: '' }, true],
  ['comparison compares dates', { v: { gt: new Date(0) } }, { v: new Date(1) }, true],
  ['comparison rejects an equal date for gt', { v: { gt: new Date(0) } }, { v: new Date(0) }, false],
  ['comparison accepts an equal date for gte', { v: { gte: new Date(0) } }, { v: new Date(0) }, true],
  ['comparison never matches across scalar types', { v: { gt: 1 } }, { v: '2' }, false],
  ['a null operand never matches lt', { v: { lt: null } }, { v: 1 }, false],
  ['a null operand never matches gt', { v: { gt: null } }, { v: 1 }, false],
  ['a null operand never matches a null value', { v: { gte: null } }, { v: null }, false],
];

const stringCases: Case[] = [
  ['contains matches a substring', { v: { contains: 'lph' } }, { v: 'alpha' }, true],
  ['contains rejects a missing substring', { v: { contains: 'zzz' } }, { v: 'alpha' }, false],
  ['contains is case-sensitive', { v: { contains: 'Alpha' } }, { v: 'alpha' }, false],
  ['contains matches the whole value', { v: { contains: 'alpha' } }, { v: 'alpha' }, true],
  ['contains never matches null', { v: { contains: 'a' } }, { v: null }, false],
  ['contains never matches an absent field', { v: { contains: 'a' } }, {}, false],
  ['contains never matches a number', { v: { contains: '2' } }, { v: 123 }, false],
  ['contains never matches a boolean', { v: { contains: 'true' } }, { v: true }, false],
  ['contains never matches a date', { v: { contains: '1970' } }, { v: new Date(0) }, false],
  ['an empty contains matches any string', { v: { contains: '' } }, { v: 'alpha' }, true],
  ['an empty contains matches the empty string', { v: { contains: '' } }, { v: '' }, true],
  ['an empty contains never matches null', { v: { contains: '' } }, { v: null }, false],
  ['an empty contains never matches an absent field', { v: { contains: '' } }, {}, false],
  ['a null contains operand never matches', { v: { contains: null } }, { v: 'alpha' }, false],
  ['a null contains operand never matches null', { v: { contains: null } }, { v: null }, false],
  ['contains reads a percent as a literal', { v: { contains: 'a%b' } }, { v: 'a%b' }, true],
  ['contains does not read a percent as a wildcard', { v: { contains: 'a%b' } }, { v: 'axb' }, false],
  ['contains reads an underscore as a literal', { v: { contains: 'a_b' } }, { v: 'a_b' }, true],
  ['contains does not read an underscore as a wildcard', { v: { contains: 'a_b' } }, { v: 'axb' }, false],
  ['contains reads a backslash as a literal', { v: { contains: '\\' } }, { v: 'a\\b' }, true],
  ['contains matches a non-ASCII substring', { v: { contains: 'ström' } }, { v: 'Ångström' }, true],
  ['contains separates non-ASCII case', { v: { contains: 'STRÖM' } }, { v: 'Ångström' }, false],
  ['startsWith matches a prefix', { v: { startsWith: 'al' } }, { v: 'alpha' }, true],
  ['startsWith rejects an inner match', { v: { startsWith: 'lph' } }, { v: 'alpha' }, false],
  ['startsWith is case-sensitive', { v: { startsWith: 'Al' } }, { v: 'alpha' }, false],
  ['startsWith never matches null', { v: { startsWith: 'a' } }, { v: null }, false],
  ['an empty startsWith matches any string', { v: { startsWith: '' } }, { v: '' }, true],
  ['startsWith reads a percent as a literal', { v: { startsWith: '%' } }, { v: '%a' }, true],
  ['startsWith does not read a percent as a wildcard', { v: { startsWith: '%' } }, { v: 'xa' }, false],
  ['endsWith matches a suffix', { v: { endsWith: 'ha' } }, { v: 'alpha' }, true],
  ['endsWith rejects an inner match', { v: { endsWith: 'lph' } }, { v: 'alpha' }, false],
  ['endsWith is case-sensitive', { v: { endsWith: 'HA' } }, { v: 'alpha' }, false],
  ['endsWith never matches null', { v: { endsWith: 'a' } }, { v: null }, false],
  ['endsWith rejects an operand longer than the value', { v: { endsWith: 'alpha' } }, { v: 'ha' }, false],
  ['an empty endsWith matches any string', { v: { endsWith: '' } }, { v: 'alpha' }, true],
  ['endsWith reads a percent as a literal', { v: { endsWith: '%' } }, { v: '100%' }, true],
  ['endsWith counts an astral character once', { v: { endsWith: '👍' } }, { v: 'x👍' }, true],
  ['endsWith rejects half of an astral character', { v: { endsWith: '👍' } }, { v: '👍x' }, false],
  [
    'insensitive contains folds ASCII case',
    { v: { contains: 'ALPHA', mode: 'insensitive' } },
    { v: 'xAlphax' },
    true,
  ],
  [
    'insensitive startsWith folds ASCII case',
    { v: { startsWith: 'al', mode: 'insensitive' } },
    { v: 'ALPHA' },
    true,
  ],
  [
    'insensitive endsWith folds ASCII case',
    { v: { endsWith: 'HA', mode: 'insensitive' } },
    { v: 'alpha' },
    true,
  ],
  [
    'insensitive matching leaves non-ASCII case alone, as the forced collation does',
    { v: { contains: 'ÉCOLE', mode: 'insensitive' } },
    { v: 'école' },
    false,
  ],
  [
    'insensitive matching still folds the ASCII letters beside a non-ASCII one',
    { v: { contains: 'ÉCOLE', mode: 'insensitive' } },
    { v: 'École' },
    true,
  ],
  [
    'insensitive matching never matches null',
    { v: { contains: 'a', mode: 'insensitive' } },
    { v: null },
    false,
  ],
  [
    'insensitive matching keeps an escaped wildcard literal',
    { v: { contains: 'A%B', mode: 'insensitive' } },
    { v: 'axb' },
    false,
  ],
  ['the default mode is case-sensitive', { v: { contains: 'A', mode: 'default' } }, { v: 'a' }, false],
  ['a mode with no string operator is no constraint', { v: { mode: 'insensitive' } }, { v: 'a' }, true],
  ['a mode with no string operator matches null too', { v: { mode: 'insensitive' } }, { v: null }, true],
  [
    'string operators conjoin with one another',
    { v: { startsWith: 'a', endsWith: 'b' } },
    { v: 'axb' },
    true,
  ],
  [
    'a conjunction of string operators fails when one fails',
    { v: { startsWith: 'a', endsWith: 'b' } },
    { v: 'axc' },
    false,
  ],
];

const isCases: Case[] = [
  ['is matches a nested field', { p: { is: { a: 1 } } }, { p: { a: 1 } }, true],
  ['is rejects a nested mismatch', { p: { is: { a: 1 } } }, { p: { a: 2 } }, false],
  ['is never matches a null relation', { p: { is: { a: 1 } } }, { p: null }, false],
  ['is never matches an absent relation', { p: { is: { a: 1 } } }, {}, false],
  [
    'is never matches a null relation even when the nested condition tests for null',
    { p: { is: { a: { equals: null } } } },
    { p: null },
    false,
  ],
  [
    'is matches a present relation whose nested field is null',
    { p: { is: { a: { equals: null } } } },
    { p: {} },
    true,
  ],
  ['an empty is condition matches a present relation', { p: { is: {} } }, { p: {} }, true],
  ['an empty is condition rejects a null relation', { p: { is: {} } }, { p: null }, false],
  [
    'is threads the demo ReadingSession rule',
    { post: { is: { authorId: 'u1' } } },
    { post: { authorId: 'u1' } },
    true,
  ],
  [
    'is rejects the demo ReadingSession rule for another author',
    { post: { is: { authorId: 'u1' } } },
    { post: { authorId: 'u2' } },
    false,
  ],
  [
    'is supports nested operators',
    { p: { is: { a: { gte: 100 } } } },
    { p: { a: 100 } },
    true,
  ],
  [
    'is supports nested combinators',
    { p: { is: { OR: [{ a: 1 }, { b: 2 }] } } },
    { p: { b: 2 } },
    true,
  ],
];

const quantifierCases: Case[] = [
  ['some matches when one related object satisfies the conditions', { p: { some: { a: 1 } } }, { p: [{ a: 2 }, { a: 1 }] }, true],
  ['some rejects when none satisfies them', { p: { some: { a: 1 } } }, { p: [{ a: 2 }] }, false],
  ['some rejects an empty list', { p: { some: { a: 1 } } }, { p: [] }, false],
  ['some rejects an absent list', { p: { some: { a: 1 } } }, {}, false],
  ['some rejects a null list', { p: { some: { a: 1 } } }, { p: null }, false],
  ['an empty some condition is an existence check', { p: { some: {} } }, { p: [{ a: 1 }] }, true],
  ['an empty some condition rejects an empty list', { p: { some: {} } }, { p: [] }, false],
  ['none matches when no related object satisfies the conditions', { p: { none: { a: 1 } } }, { p: [{ a: 2 }] }, true],
  ['none rejects when one satisfies them', { p: { none: { a: 1 } } }, { p: [{ a: 2 }, { a: 1 }] }, false],
  ['none matches an empty list', { p: { none: { a: 1 } } }, { p: [] }, true],
  ['none matches an absent list', { p: { none: { a: 1 } } }, {}, true],
  ['an empty none condition means no related rows at all', { p: { none: {} } }, { p: [{ a: 1 }] }, false],
  ['an empty none condition matches an empty list', { p: { none: {} } }, { p: [] }, true],
  ['every matches when all related objects satisfy the conditions', { p: { every: { a: 1 } } }, { p: [{ a: 1 }, { a: 1 }] }, true],
  ['every rejects when one does not', { p: { every: { a: 1 } } }, { p: [{ a: 1 }, { a: 2 }] }, false],
  ['every matches an empty list vacuously', { p: { every: { a: 1 } } }, { p: [] }, true],
  ['every matches an absent list vacuously', { p: { every: { a: 1 } } }, {}, true],
  ['every matches a null list vacuously', { p: { every: { a: 1 } } }, { p: null }, true],
  ['an empty every condition matches everything', { p: { every: {} } }, { p: [{ a: 1 }, { a: 2 }] }, true],
  ['every counts a related object with a null field as a violation', { p: { every: { a: 1 } } }, { p: [{ a: null }] }, false],
  ['every accepts a related object whose null field passes the condition', { p: { every: { a: { not: 2 } } } }, { p: [{ a: null }] }, true],
  ['every rejects a related element that is not an object', { p: { every: { a: 1 } } }, { p: [7] }, false],
  ['a quantifier reaches through a nested to-one shorthand', { p: { some: { q: { a: 1 } } } }, { p: [{ q: { a: 1 } }] }, true],
  ['a quantifier reaches through a nested quantifier', { p: { some: { q: { some: { a: 1 } } } } }, { p: [{ q: [{ a: 1 }] }] }, true],
  ['quantifiers conjoin in one filter', { p: { some: { a: 1 }, none: { a: 2 } } }, { p: [{ a: 1 }, { a: 3 }] }, true],
  ['a conjunction of quantifiers fails when one fails', { p: { some: { a: 1 }, none: { a: 2 } } }, { p: [{ a: 1 }, { a: 2 }] }, false],
  ['a to-many filter with no quantifier is no constraint', { p: {} }, { p: [] }, true],
  ['an empty disjunction inside a quantifier matches nothing', { p: { some: { OR: [] } } }, { p: [{ a: 1 }] }, false],
  ['an empty disjunction under every leaves only the empty list', { p: { every: { OR: [] } } }, { p: [{ a: 1 }] }, false],
  ['an empty disjunction under every is still vacuously true', { p: { every: { OR: [] } } }, { p: [] }, true],
];

const shorthandCases: Case[] = [
  ['the implicit equals shorthand matches', { userId: 'x' }, { userId: 'x' }, true],
  ['the implicit equals shorthand rejects a mismatch', { userId: 'x' }, { userId: 'y' }, false],
  ['the implicit equals shorthand rejects null', { userId: 'x' }, { userId: null }, false],
  ['the implicit equals shorthand rejects an absent field', { userId: 'x' }, {}, false],
  ['the implicit equals shorthand handles false', { published: true }, { published: false }, false],
  ['the implicit equals shorthand matches false', { published: false }, { published: false }, true],
  ['the implicit equals shorthand is bigint exact', { v: 1n }, { v: 1 }, true],
  ['the implicit equals shorthand accepts a null operand', { v: null }, {}, true],
  ['the implicit equals shorthand accepts a date operand', { v: new Date(0) }, { v: new Date(0) }, true],
  ['multiple keys are an implicit AND', { a: 1, b: 2 }, { a: 1, b: 2 }, true],
  ['an implicit AND fails when one key fails', { a: 1, b: 2 }, { a: 1, b: 3 }, false],
  [
    'an implicit AND fails when the other key fails',
    { a: 1, b: 2 },
    { a: 9, b: 2 },
    false,
  ],
  [
    'the demo Post rule combines an implicit AND of shorthands',
    { authorId: 'u1', type: 'PERSONAL' },
    { authorId: 'u1', type: 'PERSONAL' },
    true,
  ],
  [
    'the demo Post rule rejects another type',
    { authorId: 'u1', type: 'PERSONAL' },
    { authorId: 'u1', type: 'SHARED' },
    false,
  ],
  ['multiple operators on one field are an implicit AND', { v: { gte: 1, lt: 10 } }, { v: 5 }, true],
  ['multiple operators on one field must all hold', { v: { gte: 1, lt: 10 } }, { v: 10 }, false],
  ['an empty operator object matches everything', { v: {} }, { v: null }, true],
  ['empty conditions match everything', {}, { v: 1 }, true],
];

const combinatorCases: Case[] = [
  ['AND requires every branch', { AND: [{ a: 1 }, { b: 2 }] }, { a: 1, b: 2 }, true],
  ['AND fails when a branch fails', { AND: [{ a: 1 }, { b: 2 }] }, { a: 1, b: 9 }, false],
  ['an empty AND matches everything', { AND: [] }, { a: 1 }, true],
  ['AND accepts a single object', { AND: { a: 1 } }, { a: 1 }, true],
  ['OR accepts one branch', { OR: [{ a: 1 }, { b: 2 }] }, { b: 2 }, true],
  ['OR rejects when no branch holds', { OR: [{ a: 1 }, { b: 2 }] }, { a: 9, b: 9 }, false],
  ['an empty OR matches nothing', { OR: [] }, { a: 1 }, false],
  ['an empty OR matches nothing for an empty object', { OR: [] }, {}, false],
  ['NOT negates a single object', { NOT: { a: 1 } }, { a: 2 }, true],
  ['NOT rejects a matching object', { NOT: { a: 1 } }, { a: 1 }, false],
  ['NOT requires every branch to fail', { NOT: [{ a: 1 }, { b: 2 }] }, { a: 9, b: 9 }, true],
  ['NOT fails when one branch holds', { NOT: [{ a: 1 }, { b: 2 }] }, { a: 9, b: 2 }, false],
  ['an empty NOT matches everything', { NOT: [] }, { a: 1 }, true],
  ['NOT of an implicit AND negates the conjunction', { NOT: { a: 1, b: 2 } }, { a: 1, b: 9 }, true],
  ['combinators compose with field keys', { a: 1, OR: [{ b: 2 }, { c: 3 }] }, { a: 1, c: 3 }, true],
  [
    'combinators compose with field keys and fail on the field',
    { a: 1, OR: [{ b: 2 }, { c: 3 }] },
    { a: 9, c: 3 },
    false,
  ],
  [
    'nested combinators evaluate classically',
    { OR: [{ AND: [{ a: 1 }, { b: 2 }] }, { NOT: { c: 3 } }] },
    { a: 1, b: 9, c: 3 },
    false,
  ],
  [
    'nested combinators evaluate classically when a branch holds',
    { OR: [{ AND: [{ a: 1 }, { b: 2 }] }, { NOT: { c: 3 } }] },
    { a: 1, b: 9, c: 4 },
    true,
  ],
  [
    'NOT over a null field is classical negation of the leaf',
    { NOT: { v: { lt: 15 } } },
    { v: null },
    true,
  ],
  [
    'NOT over a null relation is classical negation of the leaf',
    { NOT: { p: { is: { a: 1 } } } },
    { p: null },
    true,
  ],
];

describe.each([
  ['equals', equalsCases],
  ['not', notCases],
  ['in', inCases],
  ['notIn', notInCases],
  ['comparisons', comparisonCases],
  ['strings', stringCases],
  ['is', isCases],
  ['quantifiers', quantifierCases],
  ['shorthands', shorthandCases],
  ['combinators', combinatorCases],
] as const)('%s', (_group, cases) => {
  it.each(cases)('%s', (_name, conditions, object, expected) => {
    expect(evaluateConditions(conditions, object)).toBe(expected);
  });

  it.each(cases)('%s (wrapped in NOT)', (_name, conditions, object, expected) => {
    expect(evaluateConditions({ NOT: conditions }, object)).toBe(!expected);
  });
});

describe('operator table', () => {
  it('exposes exactly the supported operator set', () => {
    expect([...SUPPORTED_OPERATORS].sort()).toEqual(
      [
        'equals', 'gt', 'gte', 'in', 'is', 'isNot', 'lt', 'lte', 'not', 'notIn',
        'contains', 'startsWith', 'endsWith',
        'some', 'every', 'none',
      ].sort(),
    );
  });

  it('exposes exactly the supported combinator set', () => {
    expect([...SUPPORTED_COMBINATORS].sort()).toEqual(['AND', 'NOT', 'OR']);
  });

  it('keys every entry by its own name', () => {
    for (const [key, definition] of Object.entries(OPERATORS)) {
      expect(definition.name).toBe(key);
    }
  });

  it('declares the null semantics of every operator', () => {
    expect(OPERATORS.equals.nullSemantics).toEqual({
      nullValue: 'matches-when-operand-null',
      nullOperand: 'allowed',
      sqlIsTwoValued: true,
    });
    expect(OPERATORS.not.nullSemantics.nullValue).toBe('matches-when-operand-not-null');
    expect(OPERATORS.in.nullSemantics).toEqual({
      nullValue: 'never-matches',
      nullOperand: 'rejected',
      sqlIsTwoValued: true,
    });
    expect(OPERATORS.notIn.nullSemantics.nullValue).toBe('always-matches');
    expect(OPERATORS.is.nullSemantics).toEqual({
      nullValue: 'matches-when-operand-null',
      nullOperand: 'allowed',
      sqlIsTwoValued: true,
    });
    expect(OPERATORS.isNot.nullSemantics).toEqual({
      nullValue: 'matches-when-operand-not-null',
      nullOperand: 'allowed',
      sqlIsTwoValued: true,
    });
    for (const name of ['lt', 'lte', 'gt', 'gte', 'contains', 'startsWith', 'endsWith'] as const) {
      expect(OPERATORS[name].nullSemantics).toEqual({
        nullValue: 'never-matches',
        nullOperand: 'never-matches',
        sqlIsTwoValued: true,
      });
    }
    expect(OPERATORS.some.nullSemantics).toEqual({
      nullValue: 'never-matches',
      nullOperand: 'rejected',
      sqlIsTwoValued: true,
    });
    for (const name of ['every', 'none'] as const) {
      expect(OPERATORS[name].nullSemantics).toEqual({
        nullValue: 'always-matches',
        nullOperand: 'rejected',
        sqlIsTwoValued: true,
      });
    }
  });

  it('declares operand shapes and accepted value kinds', () => {
    expect(OPERATORS.equals.operand).toBe('scalar');
    expect(OPERATORS.in.operand).toBe('scalar-list');
    expect(OPERATORS.is.operand).toBe('conditions');
    expect(OPERATORS.isNot.operand).toBe('conditions');
    for (const name of ['some', 'every', 'none'] as const) {
      expect(OPERATORS[name].operand).toBe('conditions');
      expect(OPERATORS[name].acceptedValueKinds).toEqual([]);
    }
    expect([...OPERATORS.equals.acceptedValueKinds].sort()).toEqual(
      ['boolean', 'date', 'null', 'numeric', 'string'].sort(),
    );
    expect([...OPERATORS.gt.acceptedValueKinds].sort()).toEqual(['date', 'null', 'numeric', 'string'].sort());
    for (const name of ['contains', 'startsWith', 'endsWith'] as const) {
      expect(OPERATORS[name].operand).toBe('scalar');
      expect([...OPERATORS[name].acceptedValueKinds].sort()).toEqual(['null', 'string']);
    }
    expect([...OPERATORS.in.acceptedValueKinds].sort()).toEqual(
      ['boolean', 'date', 'numeric', 'string'].sort(),
    );
  });
});

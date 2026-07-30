import { ParsingQueryError, createPrismaAbility, prismaQuery } from '@casl/prisma';
import { evaluateConditions } from '../src/index';
import { CASL_DIVERGENCES, classifyCase } from './support/casl-divergences';

type Answer = boolean | string;

function casl(conditions: unknown, object: unknown): Answer {
  let result: unknown;
  try {
    result = (prismaQuery as (c: unknown) => (o: unknown) => unknown)(conditions)(object);
  } catch (error) {
    if (error instanceof ParsingQueryError) {
      return 'throw:ParsingQueryError';
    }
    return `throw:${(error as Error).constructor.name}`;
  }
  return typeof result === 'boolean' ? result : `non-boolean:${String(result)}`;
}

function golem(conditions: unknown, object: unknown): Answer {
  try {
    return evaluateConditions(conditions, object);
  } catch (error) {
    return `throw:${(error as Error).name}`;
  }
}

function answers(conditions: unknown, object: unknown): { casl: Answer; golem: Answer } {
  return { casl: casl(conditions, object), golem: golem(conditions, object) };
}

function classes(conditions: unknown, object: unknown): string[] {
  return [...classifyCase(conditions, object)].sort();
}

describe('@casl/prisma matcher identity', () => {
  it('is the matcher createPrismaAbility installs', () => {
    const ability = createPrismaAbility([
      { action: 'read', subject: 'Post', conditions: { count: { lt: 15 } } },
    ] as never);
    const cases: [Record<string, unknown>, boolean][] = [
      [{ count: 3 }, true],
      [{ count: 30 }, false],
      [{ count: null }, true],
    ];
    for (const [object, expected] of cases) {
      const subject = { __caslSubjectType__: 'Post', ...object };
      expect(ability.can('read', subject as never)).toBe(expected);
      expect(casl({ count: { lt: 15 } }, object)).toBe(expected);
    }
  });
});

describe('divergence: incomparable-comparison', () => {
  it('lt matches null in @casl/prisma but never in golem', () => {
    expect(answers({ age: { lt: 15 } }, { age: null })).toEqual({ casl: true, golem: false });
  });

  it('gte does not match null in either, which is the asymmetry', () => {
    expect(answers({ age: { gte: 15 } }, { age: null })).toEqual({ casl: false, golem: false });
    expect(answers({ age: { gt: 15 } }, { age: null })).toEqual({ casl: false, golem: false });
  });

  it('lte matches an absent field in @casl/prisma but never in golem', () => {
    expect(answers({ age: { lte: 15 } }, {})).toEqual({ casl: true, golem: false });
  });

  it('lt matches a boolean value in @casl/prisma but never in golem', () => {
    expect(answers({ age: { lt: 0 } }, { age: false })).toEqual({ casl: true, golem: false });
  });

  it('lt matches a string value against a numeric operand in @casl/prisma but never in golem', () => {
    expect(answers({ age: { lt: 15 } }, { age: 'x' })).toEqual({ casl: true, golem: false });
    expect(answers({ age: { lte: '' } }, { age: 0 })).toEqual({ casl: true, golem: false });
  });

  it('flips under NOT, because golem negates a two-valued leaf', () => {
    expect(answers({ NOT: { age: { lt: 15 } } }, { age: null })).toEqual({ casl: false, golem: true });
    expect(answers({ NOT: { age: { gte: 15 } } }, { age: null })).toEqual({ casl: true, golem: true });
  });

  it('is what the classifier reports', () => {
    expect(classes({ age: { lt: 15 } }, { age: null })).toContain('incomparable-comparison');
  });
});

describe('divergence: bigint-comparison-operand', () => {
  it('crashes @casl/prisma with a TypeError from JSON.stringify on the bigint', () => {
    expect(answers({ n: { gt: 9007199254740992n } }, { n: 9007199254740993n })).toEqual({
      casl: 'throw:TypeError',
      golem: true,
    });
  });

  it('is a crash even when the bigint operand would have been rejected as unsupported', () => {
    let message = '';
    try {
      (prismaQuery as (c: unknown) => (o: unknown) => unknown)({ n: { lt: 1n } })({ n: 0 });
    } catch (error) {
      message = (error as Error).message;
    }
    expect(message).toBe('Do not know how to serialize a BigInt');
  });

  it('is what the classifier reports', () => {
    expect(classes({ n: { gt: 1n } }, { n: 2n })).toContain('bigint-comparison-operand');
  });
});

describe('divergence: numeric-identity-across-js-types', () => {
  it('equates a bigint with a numerically equal number in golem only', () => {
    expect(answers({ n: { equals: 1n } }, { n: 1 })).toEqual({ casl: false, golem: true });
    expect(answers({ n: { not: 1n } }, { n: 1 })).toEqual({ casl: true, golem: false });
  });

  it('makes lt true in @casl/prisma for two numerically equal values', () => {
    expect(answers({ n: { lt: -1 } }, { n: -1n })).toEqual({ casl: true, golem: false });
  });

  it('makes gte false in @casl/prisma at the safe-integer boundary', () => {
    expect(answers({ n: { gte: 9007199254740992 } }, { n: 9007199254740992n })).toEqual({
      casl: false,
      golem: true,
    });
  });

  it('carries through in and notIn', () => {
    expect(answers({ n: { in: [1n] } }, { n: 1 })).toEqual({ casl: false, golem: true });
    expect(answers({ n: { notIn: [1n] } }, { n: 1 })).toEqual({ casl: true, golem: false });
  });

  it('agrees once both sides are bigints, including past 2^53', () => {
    expect(answers({ n: { equals: 9007199254740993n } }, { n: 9007199254740993n })).toEqual({
      casl: true,
      golem: true,
    });
  });

  it('is what the classifier reports', () => {
    expect(classes({ n: { equals: 1n } }, { n: 1 })).toContain('numeric-identity-across-js-types');
  });
});

describe('divergence: date-versus-non-date', () => {
  it('equates a Date with its epoch milliseconds in @casl/prisma only', () => {
    expect(answers({ d: new Date(0) }, { d: 0 })).toEqual({ casl: true, golem: false });
  });

  it('equates a Date with an equivalent ISO string in golem only', () => {
    expect(
      answers({ d: new Date('2020-01-01T00:00:00.000Z') }, { d: '2020-01-01T00:00:00.000Z' }),
    ).toEqual({ casl: false, golem: true });
  });

  it('is what the classifier reports', () => {
    expect(classes({ d: new Date(0) }, { d: 0 })).toContain('date-versus-non-date');
  });
});

describe('divergence: undefined-versus-null', () => {
  it('does not treat an undefined operand as null in @casl/prisma', () => {
    expect(answers({ a: { equals: undefined } }, { a: null })).toEqual({ casl: false, golem: true });
  });

  it('does not treat a present undefined field as null in @casl/prisma', () => {
    expect(answers({ a: { equals: null } }, { a: undefined })).toEqual({ casl: false, golem: true });
    expect(answers({ a: { not: null } }, { a: undefined })).toEqual({ casl: true, golem: false });
  });

  it('agrees when the field is absent rather than present-and-undefined', () => {
    expect(answers({ a: { equals: null } }, {})).toEqual({ casl: true, golem: true });
  });

  it('is what the classifier reports', () => {
    expect(classes({ a: { equals: undefined } }, { a: null })).toContain('undefined-versus-null');
  });
});

describe('divergence: is-on-nullish-relation', () => {
  it('returns the relation value itself from @casl/prisma instead of a boolean', () => {
    expect(answers({ rel: { is: { a: 1 } } }, { rel: null })).toEqual({
      casl: 'non-boolean:null',
      golem: false,
    });
    expect(answers({ rel: { is: { a: 1 } } }, {})).toEqual({
      casl: 'non-boolean:undefined',
      golem: false,
    });
  });

  it('agrees once the relation is present', () => {
    expect(answers({ rel: { is: { a: 1 } } }, { rel: { a: 1 } })).toEqual({ casl: true, golem: true });
    expect(answers({ rel: { is: { a: 1 } } }, { rel: { a: 2 } })).toEqual({ casl: false, golem: false });
  });

  it('is what the classifier reports', () => {
    expect(classes({ rel: { is: { a: 1 } } }, { rel: null })).toContain('is-on-nullish-relation');
  });
});

describe('divergence: non-finite-number', () => {
  it('makes lt: Infinity match nothing in golem, where @casl/prisma refuses the operand outright', () => {
    expect(answers({ n: { lt: Number.POSITIVE_INFINITY } }, { n: 1 })).toEqual({
      casl: 'throw:ParsingQueryError',
      golem: false,
    });
    expect(answers({ n: { gte: Number.NEGATIVE_INFINITY } }, { n: 1 })).toEqual({
      casl: 'throw:ParsingQueryError',
      golem: false,
    });
  });

  it('makes an Infinity row value fail every comparison in golem', () => {
    expect(answers({ n: { gt: 5 } }, { n: Number.POSITIVE_INFINITY })).toEqual({
      casl: true,
      golem: false,
    });
    expect(answers({ n: { lt: 5 } }, { n: Number.NEGATIVE_INFINITY })).toEqual({
      casl: true,
      golem: false,
    });
  });

  it('makes equality with Infinity non-reflexive in golem', () => {
    expect(answers({ n: { equals: Number.POSITIVE_INFINITY } }, { n: Number.POSITIVE_INFINITY })).toEqual({
      casl: true,
      golem: false,
    });
  });

  it('agrees that NaN equals nothing, including itself', () => {
    expect(answers({ n: { equals: Number.NaN } }, { n: Number.NaN })).toEqual({
      casl: false,
      golem: false,
    });
  });

  it('is what the classifier reports', () => {
    expect(classes({ n: { lt: Number.POSITIVE_INFINITY } }, { n: 1 })).toContain('non-finite-number');
  });
});

describe('divergence: invalid-date', () => {
  it('makes lt true in @casl/prisma against an Invalid Date operand', () => {
    expect(answers({ d: { lt: new Date('nope') } }, { d: new Date(0) })).toEqual({
      casl: true,
      golem: false,
    });
  });

  it('never equates two Invalid Dates in either engine', () => {
    expect(answers({ d: new Date('nope') }, { d: new Date('nope') })).toEqual({
      casl: false,
      golem: false,
    });
  });

  it('is what the classifier reports', () => {
    expect(classes({ d: { lt: new Date('nope') } }, { d: new Date(0) })).toContain('invalid-date');
  });
});

describe('divergence: empty-field-filter', () => {
  it('throws ParsingQueryError in @casl/prisma and is the identity in golem', () => {
    expect(answers({ a: {} }, { a: 1 })).toEqual({ casl: 'throw:ParsingQueryError', golem: true });
    expect(answers({ a: {} }, { a: null })).toEqual({ casl: 'throw:ParsingQueryError', golem: true });
  });

  it('is what the classifier reports', () => {
    expect(classes({ a: {} }, { a: 1 })).toEqual(['empty-field-filter']);
  });
});

describe('divergence: dotted-field-name', () => {
  it('is a nested property path in @casl/prisma and a literal key in golem', () => {
    expect(answers({ 'a.b': 1 }, { a: { b: 1 } })).toEqual({ casl: true, golem: false });
    expect(answers({ 'a.b': 1 }, { 'a.b': 1 })).toEqual({ casl: false, golem: true });
  });
});

describe('divergence: prototype-field-name', () => {
  it('is rejected by @casl/prisma and read off the prototype by golem', () => {
    expect(answers({ toString: { equals: null } }, {})).toEqual({ casl: 'throw:Error', golem: false });
    expect(answers({ constructor: { equals: null } }, {})).toEqual({
      casl: 'throw:Error',
      golem: false,
    });
  });

  it('reads the inherited member instead of treating the column as absent', () => {
    expect(golem({ toString: { not: 'x' } }, {})).toBe(true);
    expect(golem({ toString: { equals: null } }, Object.create(null) as object)).toBe(true);
  });
});

describe('divergence: string-operator-on-a-non-string', () => {
  it('throws in @casl/prisma against a null field, where golem answers a non-match', () => {
    for (const operator of ['contains', 'startsWith', 'endsWith']) {
      expect(answers({ title: { [operator]: 'a' } }, { title: null })).toEqual({
        casl: 'throw:TypeError',
        golem: false,
      });
    }
  });

  it('throws against an absent field too', () => {
    expect(answers({ title: { contains: 'a' } }, {})).toEqual({ casl: 'throw:TypeError', golem: false });
  });

  it('throws against a value that is not a string', () => {
    expect(answers({ title: { contains: '2' } }, { title: 123 })).toEqual({
      casl: 'throw:TypeError',
      golem: false,
    });
  });

  it('throws even for an empty operand, which matches every string', () => {
    expect(answers({ title: { contains: '' } }, { title: null })).toEqual({
      casl: 'throw:TypeError',
      golem: false,
    });
    expect(answers({ title: { contains: '' } }, { title: '' })).toEqual({ casl: true, golem: true });
  });

  it('takes the whole rule down under NOT, where golem negates a two-valued leaf', () => {
    expect(answers({ NOT: { title: { contains: 'a' } } }, { title: null })).toEqual({
      casl: 'throw:TypeError',
      golem: true,
    });
  });

  it('agrees with golem wherever the value is a string', () => {
    expect(answers({ title: { contains: 'lph' } }, { title: 'alpha' })).toEqual({
      casl: true,
      golem: true,
    });
    expect(answers({ title: { contains: 'A' } }, { title: 'alpha' })).toEqual({
      casl: false,
      golem: false,
    });
    expect(answers({ title: { startsWith: 'al' } }, { title: 'alpha' })).toEqual({
      casl: true,
      golem: true,
    });
    expect(answers({ title: { endsWith: 'ha' } }, { title: 'alpha' })).toEqual({
      casl: true,
      golem: true,
    });
    expect(answers({ title: { contains: 'a%b' } }, { title: 'axb' })).toEqual({
      casl: false,
      golem: false,
    });
  });

  it('classifies the leaf, so the property suite cannot mistake it for agreement', () => {
    expect(classes({ title: { contains: 'a' } }, { title: null })).toContain(
      'string-operator-on-a-non-string',
    );
    expect(classes({ title: { contains: 'a' } }, { title: 'alpha' })).toEqual([]);
  });
});

describe('divergence: insensitive-mode-outside-the-string-operators', () => {
  it('folds case for contains in both, so mode agrees where golem implements it', () => {
    expect(answers({ title: { contains: 'ALPHA', mode: 'insensitive' } }, { title: 'alpha' })).toEqual({
      casl: true,
      golem: true,
    });
  });

  it('silently ignores the mode for equals, where golem folds case as Prisma does', () => {
    expect(answers({ title: { equals: 'alpha', mode: 'insensitive' } }, { title: 'ALPHA' })).toEqual({
      casl: false,
      golem: true,
    });
  });

  it('silently ignores the mode for an ordered comparison, where golem folds case', () => {
    expect(answers({ title: { gt: 'alpha', mode: 'insensitive' } }, { title: 'BETA' })).toEqual({
      casl: false,
      golem: true,
    });
  });

  it('silently ignores the mode for in, where golem folds case', () => {
    expect(answers({ title: { in: ['ALPHA'], mode: 'insensitive' } }, { title: 'alpha' })).toEqual({
      casl: false,
      golem: true,
    });
  });
});

describe('the divergence list itself', () => {
  it('has a unique id and a summary for every entry', () => {
    const ids = CASL_DIVERGENCES.map((entry) => entry.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const entry of CASL_DIVERGENCES) {
      expect(entry.summary.length).toBeGreaterThan(40);
    }
  });

  it('records thirteen divergences, nine of them reachable by generation', () => {
    expect(CASL_DIVERGENCES).toHaveLength(13);
    expect(CASL_DIVERGENCES.filter((entry) => entry.generated)).toHaveLength(9);
  });

  it('classifies a plain agreeing case as no divergence at all', () => {
    expect(classes({ a: { gt: 1 } }, { a: 5 })).toEqual([]);
    expect(answers({ a: { gt: 1 } }, { a: 5 })).toEqual({ casl: true, golem: true });
  });
});

import fc from 'fast-check';
import {
  assertSupportedConditions,
  compileConditions,
  evaluateConditions,
  isUnsupportedConditionError,
  validateConditions,
} from '../src/index';
import { NUM_RUNS, SCALAR, SEED, conditionArbitrary, objectArbitrary } from './support/arbitraries';

jest.setTimeout(300000);

const ORDERED_SCALAR = SCALAR.filter((value) => typeof value !== 'boolean');

const options = { seed: SEED, numRuns: NUM_RUNS };

function run(conditions: unknown, object: unknown): boolean {
  return evaluateConditions(conditions, object);
}

describe('golem is classical over two-valued leaves', () => {
  it('negates a condition exactly', () => {
    fc.assert(
      fc.property(conditionArbitrary(), objectArbitrary(), (conditions, object) => {
        expect(run({ NOT: conditions }, object)).toBe(!run(conditions, object));
      }),
      options,
    );
  });

  it('reads NOT over an array as a NOR', () => {
    fc.assert(
      fc.property(conditionArbitrary(), conditionArbitrary(), objectArbitrary(), (left, right, object) => {
        expect(run({ NOT: [left, right] }, object)).toBe(!run(left, object) && !run(right, object));
      }),
      options,
    );
  });

  it('reads AND and OR classically', () => {
    fc.assert(
      fc.property(conditionArbitrary(), conditionArbitrary(), objectArbitrary(), (left, right, object) => {
        expect(run({ AND: [left, right] }, object)).toBe(run(left, object) && run(right, object));
        expect(run({ OR: [left, right] }, object)).toBe(run(left, object) || run(right, object));
      }),
      options,
    );
  });

  it('reads several keys in one condition object as a conjunction', () => {
    fc.assert(
      fc.property(conditionArbitrary(), conditionArbitrary(), objectArbitrary(), (left, right, object) => {
        expect(run({ AND: [left, right] }, object)).toBe(run({ AND: [left] }, object) && run(right, object));
      }),
      options,
    );
  });

  it('gives the identity for empty combinators', () => {
    fc.assert(
      fc.property(objectArbitrary(), (object) => {
        expect(run({}, object)).toBe(true);
        expect(run({ AND: [] }, object)).toBe(true);
        expect(run({ OR: [] }, object)).toBe(false);
        expect(run({ NOT: [] }, object)).toBe(true);
        expect(run({ NOT: {} }, object)).toBe(false);
      }),
      options,
    );
  });
});

describe('golem operators relate to each other as the specification says', () => {
  it('reads the bare value as equals', () => {
    fc.assert(
      fc.property(SCALAR, objectArbitrary(), (operand, object) => {
        expect(run({ a: operand }, object)).toBe(run({ a: { equals: operand } }, object));
      }),
      options,
    );
  });

  it('makes not the negation of equals, including against null', () => {
    fc.assert(
      fc.property(SCALAR, objectArbitrary(), (operand, object) => {
        expect(run({ a: { not: operand } }, object)).toBe(!run({ a: { equals: operand } }, object));
      }),
      options,
    );
  });

  it('makes notIn the negation of in, including the empty list', () => {
    fc.assert(
      fc.property(fc.array(ORDERED_SCALAR.filter((v) => v !== null && v !== undefined), { maxLength: 3 }), objectArbitrary(), (list, object) => {
        expect(run({ a: { notIn: list } }, object)).toBe(!run({ a: { in: list } }, object));
      }),
      options,
    );
  });

  it('makes lt and gte a partition, and lte and gt a partition, or leaves both false', () => {
    fc.assert(
      fc.property(ORDERED_SCALAR, objectArbitrary(), (operand, object) => {
        const lt = run({ a: { lt: operand } }, object);
        const gte = run({ a: { gte: operand } }, object);
        const lte = run({ a: { lte: operand } }, object);
        const gt = run({ a: { gt: operand } }, object);
        expect(lt && gte).toBe(false);
        expect(lte && gt).toBe(false);
        expect(lt || gte).toBe(lte || gt);
        if (lt) {
          expect(lte).toBe(true);
        }
        if (gt) {
          expect(gte).toBe(true);
        }
      }),
      options,
    );
  });

  it('reads two operators on one field as a conjunction', () => {
    fc.assert(
      fc.property(ORDERED_SCALAR, ORDERED_SCALAR, objectArbitrary(), (low, high, object) => {
        expect(run({ a: { gte: low, lte: high } }, object)).toBe(
          run({ a: { gte: low } }, object) && run({ a: { lte: high } }, object),
        );
      }),
      options,
    );
  });

  it('distributes a one-hop is over the combinators inside it', () => {
    fc.assert(
      fc.property(conditionArbitrary(), conditionArbitrary(), objectArbitrary(), (left, right, object) => {
        expect(run({ rel: { is: { AND: [left, right] } } }, object)).toBe(
          run({ AND: [{ rel: { is: left } }, { rel: { is: right } }] }, object),
        );
        expect(run({ rel: { is: { OR: [left, right] } } }, object)).toBe(
          run({ OR: [{ rel: { is: left } }, { rel: { is: right } }] }, object),
        );
      }),
      options,
    );
  });
});

describe('golem is deterministic and its surfaces agree', () => {
  it('gives the same answer twice, and the same answer through a compiled condition', () => {
    fc.assert(
      fc.property(conditionArbitrary(), objectArbitrary(), (conditions, object) => {
        const first = run(conditions, object);
        expect(run(conditions, object)).toBe(first);
        expect(compileConditions(conditions).test(object)).toBe(first);
      }),
      options,
    );
  });

  it('keeps validateConditions and assertSupportedConditions in step', () => {
    fc.assert(
      fc.property(conditionArbitrary(), (conditions) => {
        const result = validateConditions(conditions);
        if (result.supported) {
          expect(() => assertSupportedConditions(conditions)).not.toThrow();
          return;
        }
        let caught: unknown;
        try {
          assertSupportedConditions(conditions);
        } catch (error) {
          caught = error;
        }
        expect(isUnsupportedConditionError(caught)).toBe(true);
      }),
      options,
    );
  });
});

describe('golem equality is reflexive except where it drops a value', () => {
  it('matches a value against itself for every finite, valid scalar', () => {
    fc.assert(
      fc.property(SCALAR, (value) => {
        const nonFinite = typeof value === 'number' && !Number.isFinite(value);
        const invalidDate = value instanceof Date && Number.isNaN(value.getTime());
        fc.pre(!nonFinite && !invalidDate);
        expect(run({ a: { equals: value } }, { a: value })).toBe(true);
      }),
      options,
    );
  });

  it('fails to match a non-finite number or an Invalid Date against itself', () => {
    for (const value of [
      Number.POSITIVE_INFINITY,
      Number.NEGATIVE_INFINITY,
      Number.NaN,
      new Date('nope'),
    ]) {
      expect(run({ a: { equals: value } }, { a: value })).toBe(false);
      expect(run({ a: { not: value } }, { a: value })).toBe(true);
    }
  });
});

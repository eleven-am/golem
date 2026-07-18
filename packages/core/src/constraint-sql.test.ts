import {
  UnsupportedConstraintError,
  compileConstraint,
} from './constraint-sql';
import type { DatamodelModel } from './datamodel';
import { field } from './testing';

const play: DatamodelModel = {
  name: 'Play',
  dbName: 'plays',
  fields: [
    { ...field({ name: 'id', type: 'String', isId: true }), dbName: 'id' },
    { ...field({ name: 'userId', type: 'String' }), dbName: 'user_id' },
    { ...field({ name: 'trackId', type: 'String' }), dbName: 'track_id' },
    {
      ...field({ name: 'track', type: 'Track', kind: 'object' }),
      dbName: 'track',
    },
  ],
};

const options = { model: play, dialect: 'sqlite' as const };

describe('constraint compiler', () => {
  it('treats an absent constraint as unconstrained', () => {
    expect(compileConstraint(undefined, options)).toEqual({
      sql: '1 = 1',
      params: [],
    });
  });

  it('compiles scalar equality using physical names', () => {
    expect(compileConstraint({ userId: 'user-1' }, options)).toEqual({
      sql: '"plays"."user_id" = ?',
      params: ['user-1'],
    });
  });

  it('compiles an explicit equals filter', () => {
    expect(
      compileConstraint({ userId: { equals: 'user-1' } }, options),
    ).toEqual({ sql: '"plays"."user_id" = ?', params: ['user-1'] });
  });

  it('compiles an in filter with one placeholder per value', () => {
    expect(compileConstraint({ userId: { in: ['a', 'b'] } }, options)).toEqual({
      sql: '"plays"."user_id" IN (?, ?)',
      params: ['a', 'b'],
    });
  });

  it('compiles an empty in filter to a false predicate', () => {
    expect(compileConstraint({ userId: { in: [] } }, options)).toEqual({
      sql: '1 = 0',
      params: [],
    });
  });

  it('compiles null equality as IS NULL without a parameter', () => {
    expect(compileConstraint({ trackId: null }, options)).toEqual({
      sql: '"plays"."track_id" IS NULL',
      params: [],
    });
  });

  it('compiles an AND composition', () => {
    const compiled = compileConstraint(
      { AND: [{ userId: 'u' }, { trackId: 't' }] },
      options,
    );
    expect(compiled.sql).toBe(
      '("plays"."user_id" = ? AND "plays"."track_id" = ?)',
    );
    expect(compiled.params).toEqual(['u', 't']);
  });

  it('quotes identifiers per dialect', () => {
    const compiled = compileConstraint(
      { userId: 'u' },
      { model: play, dialect: 'mysql' },
    );
    expect(compiled.sql).toBe('`plays`.`user_id` = ?');
  });

  it('never inlines a value into the sql', () => {
    const compiled = compileConstraint(
      { userId: "'; DROP TABLE plays; --" },
      options,
    );
    expect(compiled.sql).toBe('"plays"."user_id" = ?');
    expect(compiled.params).toEqual(["'; DROP TABLE plays; --"]);
  });

  describe('refuses rather than approximates', () => {
    it.each([
      ['OR', { OR: [{ userId: 'a' }, { userId: 'b' }] }],
      ['NOT', { NOT: { userId: 'a' } }],
      ['a relation-scoped condition', { track: { is: { artistId: 'a' } } }],
      ['an unknown field', { nope: 'x' }],
      ['an unsupported operator', { userId: { contains: 'x' } }],
      ['a non-scalar equals', { userId: { equals: { nested: true } } }],
      ['a non-scalar in', { userId: { in: [{ nested: true }] } }],
    ])('refuses %s', (_label, constraint) => {
      expect(() => compileConstraint(constraint, options)).toThrow(
        UnsupportedConstraintError,
      );
    });
  });

  it('refuses a datamodel generated before physical names were captured', () => {
    const legacy: DatamodelModel = {
      name: 'Play',
      fields: [field({ name: 'userId', type: 'String' })],
    };
    expect(() =>
      compileConstraint({ userId: 'u' }, { model: legacy, dialect: 'sqlite' }),
    ).toThrow(/regenerate the Golem client/);
  });
});

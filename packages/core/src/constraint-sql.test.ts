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
      relationFromFields: ['trackId'],
      relationToFields: ['id'],
    },
    {
      ...field({ name: 'tags', type: 'Tag', kind: 'object', isList: true }),
    },
  ],
};

const track: DatamodelModel = {
  name: 'Track',
  dbName: 'tracks',
  fields: [
    { ...field({ name: 'id', type: 'String', isId: true }), dbName: 'id' },
    { ...field({ name: 'artistId', type: 'String' }), dbName: 'artist_id' },
  ],
};

const modelsByName = new Map([
  ['Play', play],
  ['Track', track],
]);

const options = { model: play, dialect: 'sqlite' as const, modelsByName };

describe('constraint compiler', () => {
  it('treats an absent constraint as unconstrained', () => {
    expect(compileConstraint(undefined, options)).toMatchObject({
      sql: '1 = 1',
      params: [],
    });
  });

  it('compiles scalar equality using physical names', () => {
    expect(compileConstraint({ userId: 'user-1' }, options)).toMatchObject({
      sql: '"t0"."user_id" = ?',
      params: ['user-1'],
    });
  });

  it('compiles an explicit equals filter', () => {
    expect(
      compileConstraint({ userId: { equals: 'user-1' } }, options),
    ).toMatchObject({ sql: '"t0"."user_id" = ?', params: ['user-1'] });
  });

  it('compiles an in filter with one placeholder per value', () => {
    expect(compileConstraint({ userId: { in: ['a', 'b'] } }, options)).toMatchObject({
      sql: '"t0"."user_id" IN (?, ?)',
      params: ['a', 'b'],
    });
  });

  it('compiles an empty in filter to a false predicate', () => {
    expect(compileConstraint({ userId: { in: [] } }, options)).toMatchObject({
      sql: '1 = 0',
      params: [],
    });
  });

  it('compiles null equality as IS NULL without a parameter', () => {
    expect(compileConstraint({ trackId: null }, options)).toMatchObject({
      sql: '"t0"."track_id" IS NULL',
      params: [],
    });
  });

  it('compiles an AND composition', () => {
    const compiled = compileConstraint(
      { AND: [{ userId: 'u' }, { trackId: 't' }] },
      options,
    );
    expect(compiled.sql).toBe(
      '("t0"."user_id" = ? AND "t0"."track_id" = ?)',
    );
    expect(compiled.params).toEqual(['u', 't']);
  });

  it('quotes identifiers per dialect', () => {
    const compiled = compileConstraint(
      { userId: 'u' },
      { model: play, dialect: 'mysql' },
    );
    expect(compiled.sql).toBe('`t0`.`user_id` = ?');
  });

  it('never inlines a value into the sql', () => {
    const compiled = compileConstraint(
      { userId: "'; DROP TABLE plays; --" },
      options,
    );
    expect(compiled.sql).toBe('"t0"."user_id" = ?');
    expect(compiled.params).toEqual(["'; DROP TABLE plays; --"]);
  });

  it('compiles a one-hop relation-scoped constraint as a join', () => {
    const compiled = compileConstraint(
      { track: { is: { artistId: 'artist-1' } } },
      options,
    );
    expect(compiled.sql).toBe('"t1"."artist_id" = ?');
    expect(compiled.params).toEqual(['artist-1']);
    expect(compiled.joins).toEqual([
      {
        table: '"tracks"',
        alias: '"t1"',
        on: '"t0"."track_id" = "t1"."id"',
      },
    ]);
  });

  it('compiles own scalars and a hop together', () => {
    const compiled = compileConstraint(
      { userId: 'u', track: { is: { artistId: 'a' } } },
      options,
    );
    expect(compiled.sql).toBe('("t0"."user_id" = ? AND "t1"."artist_id" = ?)');
    expect(compiled.params).toEqual(['u', 'a']);
    expect(compiled.joins).toHaveLength(1);
  });

  it('gives each hop its own alias', () => {
    const compiled = compileConstraint(
      {
        AND: [
          { track: { is: { artistId: 'a' } } },
          { track: { is: { id: 't' } } },
        ],
      },
      options,
    );
    expect(compiled.joins.map((join) => join.alias)).toEqual(['"t1"', '"t2"']);
    expect(compiled.sql).toBe('("t1"."artist_id" = ? AND "t2"."id" = ?)');
  });

  describe('refuses rather than approximates', () => {
    it.each([
      ['OR', { OR: [{ userId: 'a' }, { userId: 'b' }] }],
      ['NOT', { NOT: { userId: 'a' } }],
        ['an unknown field', { nope: 'x' }],
      ['an unsupported operator', { userId: { contains: 'x' } }],
      ['a non-scalar equals', { userId: { equals: { nested: true } } }],
      ['a non-scalar in', { userId: { in: [{ nested: true }] } }],
      ['a to-many hop', { tags: { some: { id: 'a' } } }],
      ['a non-is relation filter', { track: { isNot: { id: 'a' } } }],
      ['a hop to an unknown model', { track: { is: { nope: 'a' } } }],
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

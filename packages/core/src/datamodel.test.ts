import { DatamodelField, DatamodelModel, isEqualityIndexed } from './datamodel';

function field(name: string, overrides: Partial<DatamodelField> = {}): DatamodelField {
  return {
    name,
    dbName: name,
    kind: 'scalar',
    type: 'String',
    isList: false,
    isRequired: true,
    isUnique: false,
    isId: false,
    hasDefaultValue: false,
    isReadOnly: false,
    isUpdatedAt: false,
    ...overrides,
  };
}

function model(overrides: Partial<DatamodelModel> = {}): DatamodelModel {
  return {
    name: 'Article',
    dbName: 'Article',
    fields: [field('id', { isId: true }), field('a'), field('b'), field('c')],
    ...overrides,
  };
}

describe('isEqualityIndexed', () => {
  it('accepts the single column of a single-column index', () => {
    const subject = model({ indexes: [{ kind: 'normal', fields: ['a'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(true);
  });

  it('accepts the leading column of a composite index and rejects the trailing ones', () => {
    const subject = model({ indexes: [{ kind: 'normal', fields: ['a', 'b'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(true);
    expect(isEqualityIndexed(subject, 'b')).toBe(false);
  });

  it('accepts a column that leads a unique index', () => {
    const subject = model({ indexes: [{ kind: 'unique', fields: ['a', 'b'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(true);
  });

  it('accepts the leading column of the primary key', () => {
    const subject = model({ indexes: [{ kind: 'id', fields: ['a', 'b'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(true);
    expect(isEqualityIndexed(subject, 'b')).toBe(false);
  });

  it('rejects a fulltext index, which cannot serve an equality lookup', () => {
    const subject = model({ indexes: [{ kind: 'fulltext', fields: ['a'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(false);
  });

  it('rejects a column no index leads', () => {
    const subject = model({ indexes: [{ kind: 'id', fields: ['id'] }] });
    expect(isEqualityIndexed(subject, 'a')).toBe(false);
  });

  it('rejects every column of a model that declares no indexes at all', () => {
    const subject = model({ fields: [field('a'), field('b')] });
    expect(isEqualityIndexed(subject, 'a')).toBe(false);
    expect(isEqualityIndexed(subject, 'b')).toBe(false);
  });

  it('falls back to isId and isUnique when a datamodel carries no index list', () => {
    const subject = model({
      fields: [field('id', { isId: true }), field('slug', { isUnique: true }), field('a')],
    });
    expect(isEqualityIndexed(subject, 'id')).toBe(true);
    expect(isEqualityIndexed(subject, 'slug')).toBe(true);
    expect(isEqualityIndexed(subject, 'a')).toBe(false);
  });

  it('falls back to the declared primary key and compound uniques when there is no index list', () => {
    const subject = model({
      primaryKey: { fields: ['userId', 'orgId'] },
      uniqueIndexes: [{ fields: ['a', 'b'] }],
      fields: [field('userId'), field('orgId'), field('a'), field('b')],
    });
    expect(isEqualityIndexed(subject, 'userId')).toBe(true);
    expect(isEqualityIndexed(subject, 'orgId')).toBe(false);
    expect(isEqualityIndexed(subject, 'a')).toBe(true);
    expect(isEqualityIndexed(subject, 'b')).toBe(false);
  });
});

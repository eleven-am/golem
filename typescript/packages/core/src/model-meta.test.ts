import { buildModelMetadata } from './model-meta';
import { field } from './testing';

describe('model metadata', () => {
  it('indexes fields once and exposes immutable lookup maps and arrays', () => {
    const model = {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'email', type: 'String' }),
        field({ name: 'posts', type: 'Post', kind: 'object', isList: true }),
      ],
    };
    const index = buildModelMetadata([model]);
    const metadata = index.get('User')!;

    expect(metadata.primaryKey?.name).toBe('id');
    expect(metadata.fieldsByName.get('email')?.type).toBe('String');
    expect(metadata.scalarFields.map((entry) => entry.name)).toEqual(['id', 'email']);
    expect(metadata.relations.map((entry) => entry.name)).toEqual(['posts']);
    expect((index as unknown as { set?: unknown }).set).toBeUndefined();
    expect((metadata.fieldsByName as unknown as { set?: unknown }).set).toBeUndefined();
    expect(Object.isFrozen(metadata.scalarFields)).toBe(true);
    expect(Object.isFrozen(metadata)).toBe(true);
  });

  it('reports a single-field primary key through primaryKeys without a compound key name', () => {
    const index = buildModelMetadata([
      {
        name: 'User',
        fields: [field({ name: 'id', type: 'String', isId: true }), field({ name: 'email', type: 'String' })],
      },
    ]);
    const metadata = index.get('User')!;
    expect(metadata.primaryKey?.name).toBe('id');
    expect(metadata.primaryKeys.map((entry) => entry.name)).toEqual(['id']);
    expect(metadata.compoundKeyName).toBeUndefined();
  });

  it('resolves composite primary keys in declared order and derives the compound key name', () => {
    const index = buildModelMetadata([
      {
        name: 'PostTag',
        fields: [
          field({ name: 'postId', type: 'String' }),
          field({ name: 'tagId', type: 'String' }),
          field({ name: 'addedAt', type: 'DateTime', hasDefaultValue: true }),
        ],
        primaryKey: { fields: ['postId', 'tagId'] },
      },
    ]);
    const metadata = index.get('PostTag')!;
    expect(metadata.primaryKey).toBeUndefined();
    expect(metadata.primaryKeys.map((entry) => entry.name)).toEqual(['postId', 'tagId']);
    expect(metadata.compoundKeyName).toBe('postId_tagId');
  });

  it('honours a named composite primary key', () => {
    const index = buildModelMetadata([
      {
        name: 'Membership',
        fields: [field({ name: 'userId', type: 'String' }), field({ name: 'orgId', type: 'String' })],
        primaryKey: { name: 'membership', fields: ['userId', 'orgId'] },
      },
    ]);
    expect(index.get('Membership')!.compoundKeyName).toBe('membership');
  });

  it('derives compound unique selectors from unnamed and named indexes', () => {
    const index = buildModelMetadata([
      {
        name: 'Branch',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'authorId', type: 'String' }),
          field({ name: 'name', type: 'String' }),
        ],
        uniqueIndexes: [
          { fields: ['authorId', 'name'] },
          { name: 'authorNameKey', fields: ['authorId', 'name'] },
        ],
      },
    ]);
    const selectors = index.get('Branch')!.compoundUniqueSelectors;
    expect([...selectors.get('authorId_name')!]).toEqual(['authorId', 'name']);
    expect([...selectors.get('authorNameKey')!]).toEqual(['authorId', 'name']);
  });

  it('leaves compound unique selectors empty when a model declares none', () => {
    const index = buildModelMetadata([
      {
        name: 'User',
        fields: [field({ name: 'id', type: 'String', isId: true })],
      },
    ]);
    expect(index.get('User')!.compoundUniqueSelectors.size).toBe(0);
  });
});


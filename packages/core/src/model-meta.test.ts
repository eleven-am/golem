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
});


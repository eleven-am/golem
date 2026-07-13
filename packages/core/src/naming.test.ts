import { deleteManyFieldName, findManyFieldName, updateManyFieldName } from './naming';

describe('naming policy', () => {
  it('pluralizes regular model names', () => {
    expect(findManyFieldName('User')).toBe('users');
    expect(findManyFieldName('Category')).toBe('categories');
    expect(findManyFieldName('Person')).toBe('people');
  });

  it('appends List when the plural equals the singular', () => {
    expect(findManyFieldName('Equipment')).toBe('equipmentList');
    expect(findManyFieldName('Series')).toBe('seriesList');
  });

  it('applies the same policy to batch mutation names', () => {
    expect(updateManyFieldName('Category')).toBe('updateManyCategories');
    expect(deleteManyFieldName('Equipment')).toBe('deleteManyEquipmentList');
  });
});

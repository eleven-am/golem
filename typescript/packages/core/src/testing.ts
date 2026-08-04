import { DatamodelField } from './datamodel';

export function field(overrides: Partial<DatamodelField> & Pick<DatamodelField, 'name' | 'type'>): DatamodelField {
  return {
    kind: 'scalar',
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

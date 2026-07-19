import { ComputedField } from '../src/generated/golem';

class ValidComputedFields {
  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  displayName(): string {
    return '';
  }
}

class InvalidComputedFields {
  // @ts-expect-error generated decorators reject unknown model names
  @ComputedField('Ghost', { type: 'String!' })
  unknownModel(): string {
    return '';
  }

  // @ts-expect-error generated decorators reject fields that do not belong to the model
  @ComputedField('User', { type: 'String!', requires: ['title'] })
  unknownRequirement(): string {
    return '';
  }
}

void ValidComputedFields;
void InvalidComputedFields;

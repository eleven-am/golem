import type { DMMF } from '@prisma/generator-helper';
import { emitDatamodelModule } from './emit';

function scalar(name: string): DMMF.Field {
  return {
    name,
    kind: 'scalar',
    type: 'String',
    isList: false,
    isRequired: true,
    isUnique: false,
    isId: false,
    hasDefaultValue: false,
    isReadOnly: false,
    isGenerated: false,
    isUpdatedAt: false,
  } as DMMF.Field;
}

function datamodel(models: DMMF.Model[]): DMMF.Datamodel {
  return { models, enums: [], types: [], indexes: [] } as unknown as DMMF.Datamodel;
}

function model(name: string, fields: DMMF.Field[], primaryKey: DMMF.Model['primaryKey']): DMMF.Model {
  return {
    name,
    dbName: null,
    fields,
    primaryKey,
    uniqueFields: [],
    uniqueIndexes: [],
  } as unknown as DMMF.Model;
}

function parseEmitted(output: string): { models: Array<{ name: string; primaryKey?: { name?: string; fields: string[] } }> } {
  const marker = 'export const datamodel = ';
  const start = output.indexOf(marker) + marker.length;
  const end = output.indexOf(' as const;', start);
  return JSON.parse(output.slice(start, end));
}

describe('emitDatamodelModule composite primary keys', () => {
  it('emits an unnamed composite primary key as its field list', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('PostTag', [scalar('postId'), scalar('tagId')], { name: null, fields: ['postId', 'tagId'] }),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toEqual({ fields: ['postId', 'tagId'] });
  });

  it('preserves a named composite primary key', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('Membership', [scalar('userId'), scalar('orgId')], { name: 'membership', fields: ['userId', 'orgId'] }),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toEqual({ name: 'membership', fields: ['userId', 'orgId'] });
  });

  it('omits primaryKey for single-field id models', () => {
    const idField = { ...scalar('id'), isId: true } as DMMF.Field;
    const output = emitDatamodelModule(datamodel([model('User', [idField, scalar('email')], null)]));
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toBeUndefined();
  });
});

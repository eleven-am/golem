import { PolicyDatamodel, PolicyDatamodelModel } from '../../src/index';

function scalar(name: string, dbName: string, type: string) {
  return { name, dbName, kind: 'scalar' as const, type, isList: false };
}

function enumeration(name: string, dbName: string, type: string) {
  return { name, dbName, kind: 'enum' as const, type, isList: false };
}

function toOne(name: string, type: string, from: string, to: string, relationName: string) {
  return {
    name,
    kind: 'object' as const,
    type,
    isList: false,
    relationName,
    relationFromFields: [from],
    relationToFields: [to],
  };
}

function toMany(name: string, type: string, relationName: string) {
  return { name, kind: 'object' as const, type, isList: true, relationName };
}

export const DEMO_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Related',
      dbName: 'Related',
      fields: [
        scalar('id', 'id', 'Int'),
        scalar('tag', 'tag', 'String'),
        scalar('tagNull', 'tagNull', 'String'),
        scalar('num', 'num', 'Int'),
        scalar('numNull', 'numNull', 'Int'),
        scalar('big', 'big', 'BigInt'),
        scalar('at', 'at', 'DateTime'),
        scalar('nextId', 'nextId', 'Int'),
        toOne('next', 'Related', 'nextId', 'id', 'RelatedChain'),
        toMany('previous', 'Related', 'RelatedChain'),
        toMany('parents', 'Parent', 'ParentToRelated'),
      ],
    },
    {
      name: 'Parent',
      dbName: 'Parent',
      fields: [
        scalar('id', 'id', 'Int'),
        scalar('name', 'name', 'String'),
        scalar('nameNull', 'nameNull', 'String'),
        scalar('count', 'count', 'Int'),
        scalar('countNull', 'countNull', 'Int'),
        scalar('big', 'big', 'BigInt'),
        scalar('bigNull', 'bigNull', 'BigInt'),
        scalar('at', 'at', 'DateTime'),
        scalar('atNull', 'atNull', 'DateTime'),
        scalar('relatedId', 'relatedId', 'Int'),
        toOne('related', 'Related', 'relatedId', 'id', 'ParentToRelated'),
      ],
    },
  ],
};

export const MAPPED_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Region',
      dbName: 'region_rows',
      fields: [
        scalar('code', 'region_code', 'String'),
        scalar('name', 'region_name', 'String'),
        toMany('owners', 'Owner', 'OwnerToRegion'),
      ],
    },
    {
      name: 'Owner',
      dbName: 'owner_rows',
      fields: [
        scalar('id', 'owner_pk', 'Int'),
        scalar('kind', 'kind_text', 'String'),
        scalar('rank', 'rank_value', 'Int'),
        scalar('rankNull', 'rank_null', 'Int'),
        scalar('regionCode', 'region_ref', 'String'),
        toOne('region', 'Region', 'regionCode', 'code', 'OwnerToRegion'),
        toMany('tagged', 'Tagged', 'OwnerToTagged'),
      ],
    },
    {
      name: 'Tagged',
      dbName: 'tagged_rows',
      fields: [
        scalar('id', 'row_pk', 'Int'),
        scalar('label', 'label_text', 'String'),
        scalar('labelNull', 'label_null', 'String'),
        scalar('amount', 'amount_value', 'Int'),
        scalar('amountNull', 'amount_null', 'Int'),
        scalar('big', 'big_value', 'BigInt'),
        scalar('at', 'at_value', 'DateTime'),
        scalar('flag', 'flag_value', 'Boolean'),
        scalar('flagNull', 'flag_null', 'Boolean'),
        enumeration('state', 'state_value', 'State'),
        enumeration('stateNull', 'state_null', 'State'),
        scalar('ownerId', 'owner_ref', 'Int'),
        toOne('owner', 'Owner', 'ownerId', 'id', 'OwnerToTagged'),
        scalar('parentId', 'parent_ref', 'Int'),
        toOne('parent', 'Tagged', 'parentId', 'id', 'TaggedSelf'),
        toMany('children', 'Tagged', 'TaggedSelf'),
      ],
    },
  ],
};

export function modelOf(datamodel: PolicyDatamodel, name: string): PolicyDatamodelModel {
  const found = datamodel.models.find((model) => model.name === name);
  if (found === undefined) {
    throw new Error(`model ${name} is missing from the test datamodel`);
  }
  return found;
}

export function idColumnOf(datamodel: PolicyDatamodel, name: string): string {
  return modelOf(datamodel, name).fields.find((field) => field.name === 'id')!.dbName!;
}

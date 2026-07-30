import { GolemOperation } from './hooks';

export type DatamodelFieldKind = 'scalar' | 'object' | 'enum';

export interface DatamodelField {
  name: string;
  dbName?: string;
  kind: DatamodelFieldKind;
  type: string;
  isList: boolean;
  isRequired: boolean;
  isUnique: boolean;
  isId: boolean;
  hasDefaultValue: boolean;
  isReadOnly: boolean;
  isUpdatedAt: boolean;
  relationName?: string;
  relationFromFields?: readonly string[];
  relationToFields?: readonly string[];
}

export interface DatamodelPrimaryKey {
  name?: string;
  fields: readonly string[];
}

export interface DatamodelUniqueIndex {
  name?: string;
  fields: readonly string[];
}

export interface DatamodelModel {
  name: string;
  dbName?: string;
  fields: readonly DatamodelField[];
  primaryKey?: DatamodelPrimaryKey;
  uniqueIndexes?: readonly DatamodelUniqueIndex[];
}

export interface DatamodelEnum {
  name: string;
  values: readonly string[];
}

export interface DatamodelDocument<TModels = Record<string, string>> {
  models: readonly DatamodelModel[];
  enums: readonly DatamodelEnum[];
  provider?: string;
  __models?: TModels;
}

export interface AggregationsConfig<TField extends string = string> {
  dimensions?: readonly TField[];
  measures?: readonly TField[];
  maxGroups?: number;
}

export interface ModelConfig<TField extends string = string> {
  subscriptions?: boolean;
  aggregations?: boolean | AggregationsConfig<TField>;
  operations?: readonly GolemOperation[];
  hidden?: readonly TField[];
  immutable?: readonly TField[];
  readOnly?: readonly TField[];
  writeOnly?: readonly TField[];
  maxTake?: number;
}

export type ModelsConfig<TModels> = {
  [K in keyof TModels]?: false | ModelConfig<Extract<TModels[K], string>>;
};

export interface GolemDefaults {
  subscriptions?: boolean;
  aggregations?: boolean;
  operations?: readonly GolemOperation[];
  maxTake?: number;
  maxGroups?: number;
  maxDepth?: number;
  checkWriteResults?: boolean;
  checkReadFields?: boolean;
}

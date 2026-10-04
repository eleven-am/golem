import { GolemOperation } from './hooks';

export type DatamodelFieldKind = 'scalar' | 'object' | 'enum';

export type DatamodelReferentialAction = 'Cascade' | 'Restrict' | 'NoAction' | 'SetNull' | 'SetDefault';

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
  /** Prisma native database type and arguments, when the schema declares one. */
  nativeType?: readonly [string, readonly string[]];
  relationName?: string;
  relationFromFields?: readonly string[];
  relationToFields?: readonly string[];
  /** What the database does to this row when the row its foreign key references is deleted. */
  relationOnDelete?: DatamodelReferentialAction;
}

export interface DatamodelPrimaryKey {
  name?: string;
  fields: readonly string[];
}

export interface DatamodelUniqueIndex {
  name?: string;
  fields: readonly string[];
}

export type DatamodelIndexKind = 'id' | 'unique' | 'normal' | 'fulltext';

export interface DatamodelIndex {
  kind: DatamodelIndexKind;
  name?: string;
  dbName?: string;
  fields: readonly string[];
}

export interface DatamodelModel {
  name: string;
  dbName?: string;
  schema?: string;
  fields: readonly DatamodelField[];
  primaryKey?: DatamodelPrimaryKey;
  uniqueIndexes?: readonly DatamodelUniqueIndex[];
  indexes?: readonly DatamodelIndex[];
}

export interface RowIdentity {
  readonly fields: readonly string[];
  readonly selector: string;
}

export function rowIdentity(model: DatamodelModel): RowIdentity | undefined {
  if (model.primaryKey?.fields.length) {
    const fields = model.primaryKey.fields;
    return { fields, selector: fields.length === 1 ? fields[0] : model.primaryKey.name ?? fields.join('_') };
  }
  const scalars = model.fields.filter((field) => field.kind !== 'object' && !field.isList);
  const id = scalars.find((field) => field.isId) ?? scalars.find((field) => field.isUnique && field.isRequired);
  if (id) {
    return { fields: [id.name], selector: id.name };
  }
  const compound = (model.uniqueIndexes ?? []).find((index) =>
    index.fields.every((name) => scalars.some((field) => field.name === name && field.isRequired)));
  return compound && { fields: compound.fields, selector: compound.name ?? compound.fields.join('_') };
}

export function rowIdentityFields(model: DatamodelModel): readonly string[] | undefined {
  return rowIdentity(model)?.fields;
}

export function isEqualityIndexed(model: DatamodelModel, fieldName: string): boolean {
  const leadsAnIndex = (model.indexes ?? []).some(
    (index) => index.kind !== 'fulltext' && index.fields[0] === fieldName,
  );
  if (leadsAnIndex) {
    return true;
  }
  if (model.primaryKey && model.primaryKey.fields[0] === fieldName) {
    return true;
  }
  if ((model.uniqueIndexes ?? []).some((index) => index.fields[0] === fieldName)) {
    return true;
  }
  return model.fields.some((field) => field.name === fieldName && (field.isId || field.isUnique));
}

export interface DatamodelEnum {
  name: string;
  values: readonly string[];
}

export type GolemProvider = 'postgresql' | 'sqlite';

export function supportedProvider(provider: unknown): GolemProvider {
  if (provider === 'postgresql' || provider === 'sqlite') {
    return provider;
  }
  throw new Error(
    `Golem supports the postgresql and sqlite datasource providers, not ${provider === undefined ? 'an unspecified provider' : `"${String(provider)}"`}`,
  );
}

export interface DatamodelDocument<TModels = Record<string, string>> {
  models: readonly DatamodelModel[];
  enums: readonly DatamodelEnum[];
  provider?: GolemProvider;
  __models?: TModels;
}

export interface RelationDimensionConfig {
  path: readonly string[];
  field: string;
}

export interface AggregationsConfig<TField extends string = string> {
  dimensions?: readonly TField[];
  relationDimensions?: Readonly<Record<string, RelationDimensionConfig>>;
  measures?: readonly TField[];
  maxIntermediateGroups?: number;
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
  maxIntermediateGroups?: number;
  maxDepth?: number;
  checkWriteResults?: boolean;
  checkReadFields?: boolean;
  upsertGuardStripes?: number;
}

export type DatamodelFieldKind = 'scalar' | 'object' | 'enum';

export interface PolicyDatamodelField {
  readonly name: string;
  readonly dbName?: string;
  readonly kind: DatamodelFieldKind;
  readonly type: string;
  readonly isList: boolean;
  readonly relationName?: string;
  readonly relationFromFields?: readonly string[];
  readonly relationToFields?: readonly string[];
}

export interface PolicyDatamodelModel {
  readonly name: string;
  readonly dbName?: string;
  readonly fields: readonly PolicyDatamodelField[];
}

export interface PolicyDatamodel {
  readonly models: readonly PolicyDatamodelModel[];
}

export interface ManifestRelation {
  readonly target: string
  readonly list: boolean
  readonly nullable: boolean
  readonly inverse: string | null
}

export interface ManifestModel {
  readonly primaryKey: ReadonlyArray<string>
  readonly cursorSelector: string
  readonly uniqueInput: string | null
  readonly events: string | null
  readonly relations: Readonly<Record<string, ManifestRelation>>
  readonly computed: Readonly<Record<string, ReadonlyArray<string>>>
}

export type RootShape = 'rows' | 'row' | 'deletedRow' | 'aggregate' | 'groupBy' | 'updateMany' | 'deleteMany' | 'value'

export interface ManifestRoot {
  readonly kind: 'generated' | 'opaque'
  readonly shape: RootShape
  readonly nullable: boolean
  readonly models: ReadonlyArray<string>
}

export interface ClientManifest {
  readonly contract: string
  readonly models: Readonly<Record<string, ManifestModel>>
  readonly roots: Readonly<Record<string, ManifestRoot>>
}

import { GolemConfigurationError } from './errors.js'

export const OPERATION_INVALIDATION_CAPABILITY = 'operation-invalidation-v1'
export const DATA_INCARNATION_CAPABILITY = 'data-incarnation-v1'

export function requiredCapabilities(manifest: { readonly formatVersion?: number; readonly requiredCapabilities?: ReadonlyArray<string> }): ReadonlyArray<string> {
  const required = manifest.requiredCapabilities
  if (required === undefined) return []
  if (manifestVersion(manifest) !== 3 || !Array.isArray(required) || required.length < 1 || required.length > 2 || required.some((value, index) => (value !== OPERATION_INVALIDATION_CAPABILITY && value !== DATA_INCARNATION_CAPABILITY) || index > 0 && value <= required[index - 1]!)) {
    throw new GolemConfigurationError('the manifest requires unsupported or malformed client capabilities; regenerate with a matching Golem client')
  }
  return required
}

export type ManifestType =
  | { readonly kind: 'list'; readonly nullable: boolean; readonly element: ManifestType; readonly maxItems?: number }
  | { readonly kind: 'scalar' | 'enum' | 'model' | 'object' | 'inputObject'; readonly name: string; readonly nullable: boolean }

export interface ManifestTypeDefinition {
  readonly kind: 'object' | 'inputObject'
  readonly fields: Readonly<Record<string, ManifestType>>
  readonly oneOf?: boolean
}

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
  readonly computedResults?: Readonly<Record<string, ManifestType>>
}

export type RootShape = 'rows' | 'row' | 'deletedRow' | 'aggregate' | 'groupBy' | 'updateMany' | 'deleteMany' | 'value' | 'object'

export interface ManifestPagination {
  readonly kind: 'continuation'
  readonly takeArgument: 'take'
  readonly afterArgument: 'after'
  readonly itemsField: 'items'
  readonly nextField: 'next'
}

export type ManifestMutationScope =
  | { readonly kind: 'models'; readonly models: ReadonlyArray<string> }
  | { readonly kind: 'unscoped' }

export interface ManifestRoot {
  // Write reach is emitted by the server compiler, independently of response models.
  readonly mutationScope?: ManifestMutationScope
  readonly invalidation?: { readonly kind: 'operation'; readonly subscriptionField: string }
  readonly pagination?: ManifestPagination
  readonly kind: 'generated' | 'opaque'
  readonly shape: RootShape
  readonly nullable: boolean
  readonly models: ReadonlyArray<string>
  readonly result?: ManifestType
}

export interface ClientManifest {
  readonly requiredCapabilities?: ReadonlyArray<string>
  readonly formatVersion?: 1 | 2 | 3
  readonly contract: string
  readonly models: Readonly<Record<string, ManifestModel>>
  readonly roots: Readonly<Record<string, ManifestRoot>>
  readonly types?: Readonly<Record<string, ManifestTypeDefinition>>
}

// Format 1 is the existing row manifest. Registered transport types require 2;
// continuation pages require 3. These are independent of event fact codecs.
export function manifestVersion(manifest: { readonly formatVersion?: number }): 1 | 2 | 3 {
  const version = manifest.formatVersion === undefined ? 1 : manifest.formatVersion
  if (version !== 1 && version !== 2 && version !== 3) {
    throw new GolemConfigurationError(`client manifest format ${version} is unsupported; regenerate with a matching Golem client`)
  }
  return version
}

export function assertClientManifest(manifest: ClientManifest): void {
  const version = manifestVersion(manifest)
  const capabilities = requiredCapabilities(manifest)
  const subscriptionFields = new Set<string>()
  for (const [name, root] of Object.entries(manifest.roots)) {
    if (root.invalidation === undefined) continue
    const invalidation = root.invalidation
    if (!capabilities.includes(OPERATION_INVALIDATION_CAPABILITY) || root.kind !== 'opaque' || invalidation?.kind !== 'operation' || typeof invalidation.subscriptionField !== 'string' || !/^[_A-Za-z][_0-9A-Za-z]*$/.test(invalidation.subscriptionField) || invalidation.subscriptionField.startsWith('__') || subscriptionFields.has(invalidation.subscriptionField)) {
      throw new GolemConfigurationError(`the manifest root ${name} has invalid operation invalidation metadata`)
    }
    subscriptionFields.add(invalidation.subscriptionField)
  }
  if (version === 1 && (manifest.types !== undefined || Object.values(manifest.roots).some((root) => root.shape === 'object') || Object.values(manifest.models).some((model) => model.computedResults !== undefined))) {
    throw new GolemConfigurationError('registered object types require client manifest format 2')
  }
  for (const [name, root] of Object.entries(manifest.roots)) {
    if (root.mutationScope !== undefined) {
      const scope = root.mutationScope
      if (scope === null || typeof scope !== 'object' || Array.isArray(scope)) {
        throw new GolemConfigurationError(`the manifest root ${name} has invalid mutation scope metadata`)
      }
      const keys = Object.keys(scope)
      const valid = scope.kind === 'unscoped'
        ? keys.length === 1 && keys[0] === 'kind'
        : scope.kind === 'models' && keys.length === 2 && keys.includes('kind') && keys.includes('models')
          && Array.isArray(scope.models) && scope.models.length > 0
          && Array.from(scope.models).every((model) => typeof model === 'string' && Object.prototype.hasOwnProperty.call(manifest.models, model))
          && new Set(scope.models).size === scope.models.length
      if (!valid) {
        throw new GolemConfigurationError(`the manifest root ${name} has invalid mutation scope metadata`)
      }
    }
    if (root.shape === 'object' && root.result === undefined) {
      throw new GolemConfigurationError(`the manifest object root ${name} has no result type`)
    }
    if (root.pagination !== undefined) {
      if (version !== 3) throw new GolemConfigurationError('continuation pages require client manifest format 3')
      const page = root.pagination
      if (page === null || page.kind !== 'continuation' || page.takeArgument !== 'take' || page.afterArgument !== 'after' || page.itemsField !== 'items' || page.nextField !== 'next') {
        throw new GolemConfigurationError(`the manifest page ${name} has invalid continuation metadata`)
      }
      const result = root.result
      const definition = result?.kind === 'object' ? manifest.types?.[result.name] : undefined
      const items = definition?.fields[page.itemsField]
      const next = definition?.fields[page.nextField]
      if (root.kind !== 'opaque' || root.shape !== 'object' || root.nullable || result?.kind !== 'object' || result.nullable || definition?.kind !== 'object' || Object.keys(definition.fields).length !== 2 || items?.kind !== 'list' || items.nullable || items.element.nullable || !Number.isInteger(items.maxItems) || (items.maxItems ?? 0) < 1 || (items.maxItems ?? 0) > 1000 || next?.kind !== 'scalar' || next.name !== 'String' || !next.nullable) {
        throw new GolemConfigurationError(`the manifest page ${name} must return a bounded items list and nullable String next`)
      }
    }
  }
}

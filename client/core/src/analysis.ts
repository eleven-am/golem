import {
  Kind,
  OperationTypeNode,
  parse,
  print,
  visit,
  type ArgumentNode,
  type DocumentNode,
  type FieldNode,
  type FragmentDefinitionNode,
  type InlineFragmentNode,
  type ObjectFieldNode,
  type ObjectValueNode,
  type OperationDefinitionNode,
  type SelectionNode,
  type SelectionSetNode,
  type ValueNode,
  type VariableNode,
} from 'graphql'
import { GolemDocumentError, GolemMultipleOperationsError, GolemNotLiveError } from './errors.js'
import { assertClientManifest, type ClientManifest, type ManifestModel, type ManifestMutationScope, type ManifestRoot, type ManifestType } from './manifest.js'
import type { SubscriptionRequest } from './session.js'

const VERSION_FIELD = '_version'
const TYPENAME_FIELD = '__typename'

export interface StreamPlan {
  readonly kind?: 'rows'
  readonly key: string
  readonly model: string
  readonly eventsRoot: string
  readonly patchable: boolean
  readonly derived: boolean
  readonly scope: 'exact' | 'policy-only'
  readonly paths: ReadonlyArray<string>
  readonly query: string
  readonly variableNames: ReadonlyArray<string>
}

export interface OperationInvalidationPlan {
  readonly kind: 'invalidation'
  readonly key: string
  readonly query: string
  readonly variableNames: ReadonlyArray<string>
  // Deliberately no row identity, patch scope or returned-model dependency.
  readonly model?: never
  readonly eventsRoot?: never
  readonly patchable?: never
  readonly derived?: never
  readonly scope?: never
  readonly paths?: never
}
export type LivePlan = StreamPlan | OperationInvalidationPlan

export interface ReadDependencies {
  readonly models: ReadonlyArray<string>
  // Opaque resolvers may depend on facts absent from the public manifest.
  readonly opaque: boolean
}

export interface DocumentAnalysis {
  readonly dependencies: ReadDependencies
  readonly document: DocumentNode
  readonly query: string
  readonly streams: ReadonlyArray<LivePlan>
  readonly nullableRoots: ReadonlySet<string>
  readonly notLive: ReadonlyArray<GolemNotLiveError>
  readonly live: boolean
}

// Static requests validate the same document vocabulary without broadening
// model projections for live row identity/version bookkeeping.
export interface StaticDocumentAnalysis {
  readonly dependencies: ReadDependencies
  readonly mode: 'static'
  readonly document: DocumentNode
  readonly query: string
}

export function assertLive(analysis: DocumentAnalysis): void {
  const first = analysis.notLive[0]
  if (first !== undefined) {
    throw first
  }
}

export function streamRequest(stream: LivePlan, variables: Readonly<Record<string, unknown>> = {}): SubscriptionRequest {
  const picked: Record<string, unknown> = {}
  for (const name of stream.variableNames) {
    if (name in variables) {
      picked[name] = variables[name]
    }
  }
  return { query: stream.query, variables: picked }
}

interface StreamDraft {
  readonly key: string
  readonly model: string
  readonly eventsRoot: string
  patchable: boolean
  derived: boolean
  readonly scope: Scope | null
  readonly order: number
  readonly fields: Map<string, FieldNode>
  readonly paths: Set<string>
}

interface Sink {
  readonly streams: Map<string, StreamDraft>
  readonly operations: Map<string, { readonly order: number; readonly plan: OperationInvalidationPlan }>
  readonly notLive: Map<string, GolemNotLiveError>
  readonly nullable: Set<string>
  sequence: number
}

interface Walk {
  readonly manifest: ClientManifest
  readonly fragments: ReadonlyMap<string, FragmentDefinitionNode>
  readonly sink: Sink | null
  readonly enrichRows: boolean
  readonly fragmentAliases: ReadonlyMap<string, string>
  readonly usedEnrichedFragments: Set<string>
  readonly dependencies?: { readonly models: Set<string>; opaque: boolean }
}

interface RowState {
  readonly fields: ReadonlyMap<string, Map<string, FieldNode>>
  nested: boolean
}

type Scope = ObjectValueNode | VariableNode

const nameNode = (value: string) => ({ kind: Kind.NAME as const, value })

const bareField = (name: string): FieldNode => ({ kind: Kind.FIELD, name: nameNode(name) })

const responseKey = (field: FieldNode): string => (field.alias ?? field.name).value

const childPath = (path: string, field: FieldNode): string => (path === '' ? responseKey(field) : `${path}.${responseKey(field)}`)

const objectValue = (fields: Record<string, ValueNode>): ObjectValueNode => ({
  kind: Kind.OBJECT,
  fields: Object.entries(fields).map(
    ([name, value]): ObjectFieldNode => ({ kind: Kind.OBJECT_FIELD, name: nameNode(name), value }),
  ),
})

function conjunction(left: Scope | null, right: Scope | null): Scope | null {
  if (left === null) {
    return right
  }
  if (right === null) {
    return left
  }
  return objectValue({ AND: { kind: Kind.LIST, values: [left, right] } })
}

function argument(field: FieldNode, name: string): ArgumentNode | undefined {
  return field.arguments?.find((candidate) => candidate.name.value === name)
}

function filterScope(value: ValueNode | undefined): Scope | null {
  if (value === undefined || value.kind === Kind.NULL) {
    return null
  }
  if (value.kind === Kind.OBJECT) {
    return value.fields.length === 0 ? null : value
  }
  return value.kind === Kind.VARIABLE ? value : null
}

function uniqueScope(model: ManifestModel, value: ValueNode | undefined): Scope | null {
  if (value === undefined || value.kind !== Kind.OBJECT || value.fields.length === 0) {
    return null
  }
  const first = value.fields[0]
  if (first === undefined || value.fields.length !== 1 || first.name.value !== model.cursorSelector) {
    return null
  }
  const key = first.value
  if (model.primaryKey.length === 1) {
    const field = model.primaryKey[0]
    if (field === undefined || key.kind === Kind.OBJECT) {
      return null
    }
    return objectValue({ [field]: objectValue({ equals: key }) })
  }
  if (key.kind !== Kind.OBJECT) {
    return null
  }
  const parts: Record<string, ValueNode> = {}
  for (const part of key.fields) {
    parts[part.name.value] = objectValue({ equals: part.value })
  }
  return objectValue(parts)
}

function applicableTo(fragment: InlineFragmentNode | FragmentDefinitionNode, models: ReadonlyArray<string>, manifest: ClientManifest): ReadonlyArray<string> {
  const condition = fragment.typeCondition?.name.value
  if (condition === undefined || manifest.models[condition] === undefined) {
    return models
  }
  return models.includes(condition) ? [condition] : []
}

function record(sink: Sink | null, error: GolemNotLiveError, key: string): void {
  if (sink !== null && !sink.notLive.has(key)) {
    sink.notLive.set(key, error)
  }
}

function injections(model: ManifestModel): string[] {
  const names = [...model.primaryKey]
  if (model.events !== null) {
    names.push(VERSION_FIELD)
  }
  names.push(TYPENAME_FIELD)
  return names
}

function ensure(selections: ReadonlyArray<SelectionNode>, names: ReadonlyArray<string>): SelectionNode[] {
  const result = [...selections]
  for (const name of names) {
    const keyed = result.filter((selection): selection is FieldNode => selection.kind === Kind.FIELD && responseKey(selection) === name)
    const taken = keyed.find((selection) => selection.name.value !== name)
    if (taken !== undefined) {
      throw new GolemDocumentError(`the response key ${name} is taken by ${taken.name.value}; golem needs ${name} unaliased on this selection`)
    }
    if (!keyed.some((selection) => (selection.directives ?? []).length === 0)) {
      result.push(bareField(name))
    }
  }
  return result
}

function fragmentReference(name: string, walk: Walk): string {
  const alias = walk.enrichRows ? walk.fragmentAliases.get(name) : undefined
  if (alias === undefined) return name
  walk.usedEnrichedFragments.add(name)
  return alias
}

function visitSet(set: SelectionSetNode, models: ReadonlyArray<string>, scope: Scope | null, path: string, state: RowState, walk: Walk): SelectionNode[] {
  const out: SelectionNode[] = []
  for (const selection of set.selections) {
    if (selection.kind === Kind.FIELD) {
      out.push(visitField(selection, models, scope, path, state, walk))
    } else if (selection.kind === Kind.INLINE_FRAGMENT) {
      const narrowed = applicableTo(selection, models, walk.manifest)
      let inner = visitSet(selection.selectionSet, narrowed, scope, path, state, walk)
      const only = narrowed[0]
      if (walk.enrichRows && models.length > 1 && narrowed.length === 1 && only !== undefined) {
        const model = walk.manifest.models[only]
        if (model !== undefined) {
          inner = ensure(inner, injections(model).filter((name) => name !== TYPENAME_FIELD))
        }
      }
      out.push({ ...selection, selectionSet: { ...selection.selectionSet, selections: inner } })
    } else {
      const definition = walk.fragments.get(selection.name.value)
      if (definition === undefined) {
        throw new GolemDocumentError(`fragment ${selection.name.value} is not defined in the document`)
      }
      if (walk.sink !== null || walk.dependencies !== undefined) {
        visitSet(definition.selectionSet, applicableTo(definition, models, walk.manifest), scope, path, state, walk)
      }
      const reference = fragmentReference(selection.name.value, walk)
      out.push(reference === selection.name.value ? selection : { ...selection, name: nameNode(reference) })
    }
  }
  return out
}

function visitField(field: FieldNode, models: ReadonlyArray<string>, scope: Scope | null, path: string, state: RowState, walk: Walk): FieldNode {
  const name = field.name.value
  if (name.startsWith('__')) {
    return field
  }
  if (models.length === 0) {
    return field
  }
  const only = models[0]
  if (models.length > 1 || only === undefined) {
    throw new GolemDocumentError(`field ${name} is selected on a result that holds ${models.join(' or ')}; select it under a type condition`)
  }
  const model = walk.manifest.models[only]
  const fields = state.fields.get(only)
  if (model === undefined || fields === undefined) {
    throw new GolemDocumentError(`the manifest has no model ${only}`)
  }
  if (name === '_count' && field.selectionSet !== undefined && walk.dependencies !== undefined) {
    const counts = (set: SelectionSetNode): void => {
      for (const selection of set.selections) {
        if (selection.kind === Kind.FIELD) {
          const counted = model.relations[selection.name.value]
          if (counted !== undefined) {
            walk.dependencies?.models.add(counted.target)
            argumentDependencies(selection, [counted.target], walk)
          }
        } else if (selection.kind === Kind.INLINE_FRAGMENT) counts(selection.selectionSet)
        else {
          const fragment = walk.fragments.get(selection.name.value)
          if (fragment !== undefined) counts(fragment.selectionSet)
        }
      }
    }
    counts(field.selectionSet)
  }
  const relation = model.relations[name]
  if (relation !== undefined) {
    state.nested = true
    return visitRelation(field, only, relation, scope, path, walk)
  }
  if (Object.hasOwn(model.computed, name) || Object.hasOwn(model.computedResults ?? {}, name)) {
    if (walk.dependencies !== undefined) walk.dependencies.opaque = true
  }
  const computedResult = model.computedResults !== undefined && Object.hasOwn(model.computedResults, name) ? model.computedResults[name] : undefined
  if (computedResult !== undefined) {
    if (field.selectionSet === undefined) throw new GolemDocumentError(`the computed object ${only}.${name} requires a selection`)
    state.nested = true
    record(walk.sink, new GolemNotLiveError('non-row-root', `${only}.${name}`, `${only}.${name} is not live: computed object results require declared invalidation dependencies`), `non-row-root|${only}.${name}`)
    return { ...field, selectionSet: objectSelection(field.selectionSet, computedResult, childPath(path, field), walk, new Set()) }
  }
  const computed = model.computed[name]
  if (computed !== undefined && computed.length > 0) {
    if (field.selectionSet === undefined) {
      return field
    }
    state.nested = true
    return { ...field, selectionSet: rows(field.selectionSet, computed, null, false, false, childPath(path, field), walk) }
  }
  fields.set(responseKey(field), field)
  return field
}

function visitRelation(field: FieldNode, owner: string, relation: { readonly target: string; readonly inverse: string | null }, scope: Scope | null, path: string, walk: Walk): FieldNode {
  const target = walk.manifest.models[relation.target]
  if (target === undefined) {
    throw new GolemDocumentError(`the manifest has no model ${relation.target}`)
  }
  if (field.selectionSet === undefined) {
    return field
  }
  const subject = `${owner}.${field.name.value}`
  if (relation.inverse === null) {
    record(
      walk.sink,
      new GolemNotLiveError(
        'hidden-inverse',
        subject,
        `${subject} is not live: the inverse relation of ${relation.target} is hidden, so rows of ${relation.target} cannot be tied to ${owner}`,
      ),
      `hidden-inverse|${subject}`,
    )
  }
  const inverse = relation.inverse === null ? undefined : target.relations[relation.inverse]
  let inverseFilter: Scope | null = null
  if (relation.inverse !== null && inverse !== undefined && scope !== null) {
    inverseFilter = objectValue({ [relation.inverse]: objectValue({ [inverse.list ? 'some' : 'is']: scope }) })
  }
  argumentDependencies(field, [relation.target], walk)
  const childScope = conjunction(filterScope(argument(field, 'where')?.value), inverseFilter)
  return { ...field, selectionSet: rows(field.selectionSet, [relation.target], childScope, false, false, childPath(path, field), walk) }
}

function rows(set: SelectionSetNode, models: ReadonlyArray<string>, scope: Scope | null, plain: boolean, derived: boolean, path: string, walk: Walk): SelectionSetNode {
  for (const model of models) walk.dependencies?.models.add(model)
  const order = walk.sink === null ? 0 : walk.sink.sequence++
  const state: RowState = { fields: new Map(models.map((model) => [model, new Map<string, FieldNode>()])), nested: false }
  let selections = visitSet(set, models, scope, path, state, walk)
  const single = models.length === 1 ? walk.manifest.models[models[0] ?? ''] : undefined
  if (walk.enrichRows) {
    if (single !== undefined) {
      selections = ensure(selections, injections(single))
    } else {
      selections = ensure(selections, [TYPENAME_FIELD])
    }
  }
  register(models, scope, plain && !state.nested, derived, order, path, state, walk)
  return { ...set, selections }
}

function disagrees(draft: StreamDraft, fields: ReadonlyMap<string, FieldNode>): boolean {
  for (const [responseName, field] of fields) {
    const known = draft.fields.get(responseName)
    if (known !== undefined && print(known) !== print(field)) {
      return true
    }
  }
  return false
}

function register(models: ReadonlyArray<string>, scope: Scope | null, patchable: boolean, derived: boolean, order: number, path: string, state: RowState, walk: Walk): void {
  const sink = walk.sink
  if (sink === null) {
    return
  }
  for (const name of models) {
    const model = walk.manifest.models[name]
    const fields = state.fields.get(name)
    if (model === undefined || fields === undefined) {
      throw new GolemDocumentError(`the manifest has no model ${name}`)
    }
    if (model.events === null) {
      record(
        sink,
        new GolemNotLiveError('model-without-events', name, `${name} is not live: the model has no events; declare Subscriptions on ${name} or use a static query`),
        `model-without-events|${name}`,
      )
      continue
    }
    const base = `${model.events}|${scope === null ? '' : print(scope)}`
    let key = base
    let draft = sink.streams.get(key)
    for (let copy = 1; draft !== undefined && disagrees(draft, fields); copy++) {
      key = `${base}#${copy}`
      draft = sink.streams.get(key)
    }
    if (draft === undefined) {
      draft = { key, model: name, eventsRoot: model.events, patchable, derived, scope, order, fields: new Map(), paths: new Set() }
      sink.streams.set(key, draft)
    } else {
      draft.patchable &&= patchable
      draft.derived ||= derived
    }
    draft.paths.add(path)
    for (const [responseName, field] of fields) {
      draft.fields.set(responseName, field)
    }
  }
}

function collectVariables(node: SelectionSetNode | ValueNode): Set<string> {
  const names = new Set<string>()
  visit(node, {
    Variable(variable) {
      names.add(variable.name.value)
    },
  })
  return names
}

function finish(draft: StreamDraft, manifest: ClientManifest, operation: OperationDefinitionNode): StreamPlan {
  const model = manifest.models[draft.model]
  if (model === undefined) {
    throw new GolemDocumentError(`the manifest has no model ${draft.model}`)
  }
  const entity = ensure([...draft.fields.values()], injections(model)) as FieldNode[]
  const entitySet: SelectionSetNode = { kind: Kind.SELECTION_SET, selections: entity }
  const identity: FieldNode =
    model.primaryKey.length > 1
      ? { ...bareField('id'), selectionSet: { kind: Kind.SELECTION_SET, selections: model.primaryKey.map(bareField) } }
      : bareField('id')
  const eventFields: SelectionNode[] = [...['eventID', 'version', 'type'].map(bareField), identity]
  eventFields.push({ ...bareField('entity'), selectionSet: entitySet })
  const root: FieldNode = {
    ...bareField(draft.eventsRoot),
    arguments:
      draft.scope === null
        ? []
        : [{ kind: Kind.ARGUMENT, name: nameNode('where'), value: draft.scope }],
    selectionSet: { kind: Kind.SELECTION_SET, selections: eventFields },
  }
  const body: SelectionSetNode = { kind: Kind.SELECTION_SET, selections: [root] }
  const used = collectVariables(body)
  const definitions = (operation.variableDefinitions ?? []).filter((definition) => used.has(definition.variable.name.value))
  const document: DocumentNode = {
    kind: Kind.DOCUMENT,
    definitions: [
      {
        kind: Kind.OPERATION_DEFINITION,
        operation: 'subscription' as OperationDefinitionNode['operation'],
        variableDefinitions: definitions,
        selectionSet: body,
      },
    ],
  }
  return {
    key: draft.key,
    model: draft.model,
    eventsRoot: draft.eventsRoot,
    patchable: draft.patchable,
    derived: draft.derived,
    scope: draft.scope === null ? 'policy-only' : 'exact',
    paths: [...draft.paths].sort(),
    query: print(document),
    variableNames: [...used].sort(),
  }
}

function visitRootSet(set: SelectionSetNode, walk: Walk): SelectionNode[] {
  const out: SelectionNode[] = []
  for (const selection of set.selections) {
    if (selection.kind === Kind.FIELD) {
      out.push(visitRoot(selection, walk))
    } else if (selection.kind === Kind.INLINE_FRAGMENT) {
      out.push({ ...selection, selectionSet: { ...selection.selectionSet, selections: visitRootSet(selection.selectionSet, walk) } })
    } else {
      const definition = walk.fragments.get(selection.name.value)
      if (definition === undefined) {
        throw new GolemDocumentError(`fragment ${selection.name.value} is not defined in the document`)
      }
      if (walk.sink !== null || walk.dependencies !== undefined) {
        visitRootSet(definition.selectionSet, walk)
      }
      const reference = fragmentReference(selection.name.value, walk)
      out.push(reference === selection.name.value ? selection : { ...selection, name: nameNode(reference) })
    }
  }
  return out
}

// Generated filters/orderings may mention public relations absent from the
// projection. Variables are deliberately conservative across the public graph;
// no policy-only/hidden dependency is reconstructed in the browser.
function argumentDependencies(field: FieldNode, models: ReadonlyArray<string>, walk: Walk): void {
  const dependencies = walk.dependencies
  if (dependencies === undefined) return
  const graph = (model: string, seen = new Set<string>()): void => {
    if (seen.has(model)) return
    seen.add(model); dependencies.models.add(model)
    for (const relation of Object.values(walk.manifest.models[model]?.relations ?? {})) graph(relation.target, seen)
  }
  const inspect = (value: ValueNode, model: string): void => {
    if (value.kind === Kind.VARIABLE) { graph(model); return }
    if (value.kind === Kind.LIST) { for (const item of value.values) inspect(item, model); return }
    if (value.kind !== Kind.OBJECT) return
    for (const item of value.fields) {
      const relation = walk.manifest.models[model]?.relations[item.name.value]
      if (relation !== undefined) {
        dependencies.models.add(relation.target)
        inspect(item.value, relation.target)
      } else inspect(item.value, model)
    }
  }
  for (const argument of field.arguments ?? []) {
    if (argument.name.value === 'where' || argument.name.value === 'orderBy') for (const model of models) inspect(argument.value, model)
  }
}

function visitRoot(field: FieldNode, walk: Walk): FieldNode {
  const name = field.name.value
  if (name.startsWith('__')) {
    return field
  }
  const root = walk.manifest.roots[name]
  if (root === undefined) {
    throw new GolemDocumentError(`${name} is not a root in the manifest`)
  }
  if (walk.dependencies !== undefined) {
    for (const model of root.models) walk.dependencies.models.add(model)
    if (root.kind === 'opaque' || root.invalidation !== undefined) walk.dependencies.opaque = true
  }
  argumentDependencies(field, root.models, walk)
  if (root.invalidation !== undefined) return operationInvalidationRoot(field, root, walk)
  if (root.shape === 'object') {
    record(walk.sink, new GolemNotLiveError('non-row-root', name, `${name} is not live: custom object results require declared invalidation dependencies`), `non-row-root|${name}`)
    if (root.result === undefined || field.selectionSet === undefined) {
      throw new GolemDocumentError(`the object root ${name} requires a declared result type and a selection`)
    }
    return { ...field, selectionSet: objectSelection(field.selectionSet, root.result, responseKey(field), walk, new Set()) }
  }
  if (root.kind === 'generated') {
    const modelName = root.models[0]
    const model = modelName === undefined ? undefined : walk.manifest.models[modelName]
    if (modelName === undefined || model === undefined) {
      throw new GolemDocumentError(`the manifest root ${name} names no model`)
    }
    if (root.shape === 'aggregate' || root.shape === 'groupBy') {
      return derivedRoot(field, root, walk)
    }
    if (root.shape !== 'rows' && root.shape !== 'row' && root.shape !== 'deletedRow') {
      record(
        walk.sink,
        new GolemNotLiveError('non-row-root', name, `${name} is not live: its result shape is ${root.shape}, not model rows`),
        `non-row-root|${name}`,
      )
      return field
    }
    if (field.selectionSet === undefined) {
      return field
    }
    const where = argument(field, 'where')?.value
    const scope = root.shape === 'rows' ? filterScope(where) : uniqueScope(model, where)
    markNullable(field, root, walk)
    const plain = (field.arguments ?? []).every((candidate) => candidate.name.value === 'where')
    return { ...field, selectionSet: rows(field.selectionSet, [modelName], scope, plain, false, responseKey(field), walk) }
  }
  if (root.models.length === 0 || field.selectionSet === undefined) {
    record(
      walk.sink,
      new GolemNotLiveError('opaque-root-without-models', name, `${name} is not live: the custom root's result holds no model rows`),
      `opaque-root-without-models|${name}`,
    )
    return field
  }
  markNullable(field, root, walk)
  return { ...field, selectionSet: rows(field.selectionSet, root.models, null, false, true, responseKey(field), walk) }
}

function operationInvalidationRoot(field: FieldNode, root: ManifestRoot, walk: Walk): FieldNode {
  const staticWalk: Walk = { ...walk, sink: null, enrichRows: false }
  let selected = field
  if (root.shape === 'object') {
    if (root.result === undefined || field.selectionSet === undefined) throw new GolemDocumentError(`the object root ${field.name.value} requires a declared result type and selection`)
    selected = { ...field, selectionSet: objectSelection(field.selectionSet, root.result, responseKey(field), staticWalk, new Set()) }
  } else if (field.selectionSet !== undefined && root.models.length > 0) {
    selected = { ...field, selectionSet: rows(field.selectionSet, root.models, null, false, true, responseKey(field), staticWalk) }
  }
  if (walk.sink !== null) {
    const subscription: FieldNode = {
      kind: Kind.FIELD, name: nameNode(root.invalidation!.subscriptionField),
      arguments: (field.arguments ?? []).filter((argument) => root.pagination === undefined || argument.name.value !== root.pagination.takeArgument && argument.name.value !== root.pagination.afterArgument),
    }
    const body: SelectionSetNode = { kind: Kind.SELECTION_SET, selections: [subscription] }
    const used = collectVariables(body)
    const key = `operation|${print(subscription)}`
    if (!walk.sink.operations.has(key)) {
      // Variable definitions/defaults are filled once by the same analyser that
      // owns row plans. The subscription carries no output selection/data.
      walk.sink.operations.set(key, { order: walk.sink.sequence++, plan: { kind: 'invalidation', key, query: print(body), variableNames: [...used].sort() } })
    }
  }
  return selected
}

function objectSelection(set: SelectionSetNode, type: ManifestType, path: string, walk: Walk, stack: ReadonlySet<string>): SelectionSetNode {
  while (type.kind === 'list') type = type.element
  if (type.kind === 'model') {
    return rows(set, [type.name], null, false, true, path, { ...walk, sink: null })
  }
  if (type.kind !== 'object') {
    throw new GolemDocumentError(`a selection at ${path} requires an output object`)
  }
  const name = type.name
  const definition = walk.manifest.types?.[name]
  if (definition?.kind !== 'object') throw new GolemDocumentError(`the manifest has no output object ${name}`)
  return {
    ...set,
    selections: set.selections.map((selection): SelectionNode => {
      if (selection.kind === Kind.FIELD) {
        if (selection.name.value === TYPENAME_FIELD) return selection
        const fieldType = Object.hasOwn(definition.fields, selection.name.value) ? definition.fields[selection.name.value] : undefined
        if (fieldType === undefined) throw new GolemDocumentError(`the object ${name} has no field ${selection.name.value}`)
        let leaf = fieldType
        while (leaf.kind === 'list') leaf = leaf.element
        const composite = leaf.kind === 'object' || leaf.kind === 'model'
        if (composite && selection.selectionSet === undefined || !composite && selection.selectionSet !== undefined) {
          throw new GolemDocumentError(`the object field ${name}.${selection.name.value} has an invalid selection shape`)
        }
        return selection.selectionSet === undefined ? selection : { ...selection, selectionSet: objectSelection(selection.selectionSet, fieldType, childPath(path, selection), walk, stack) }
      }
      if (selection.kind === Kind.INLINE_FRAGMENT) {
        if (selection.typeCondition !== undefined && selection.typeCondition.name.value !== name) throw new GolemDocumentError(`a fragment on ${selection.typeCondition.name.value} cannot select ${name}`)
        return { ...selection, selectionSet: objectSelection(selection.selectionSet, type, path, walk, stack) }
      }
      const fragment = walk.fragments.get(selection.name.value)
      if (fragment === undefined) throw new GolemDocumentError(`fragment ${selection.name.value} is not defined in the document`)
      if (stack.has(selection.name.value)) throw new GolemDocumentError(`fragment ${selection.name.value} is recursive`)
      if (fragment.typeCondition.name.value !== name) throw new GolemDocumentError(`a fragment on ${fragment.typeCondition.name.value} cannot select ${name}`)
      objectSelection(fragment.selectionSet, type, path, walk, new Set([...stack, selection.name.value]))
      const reference = fragmentReference(selection.name.value, walk)
      return reference === selection.name.value ? selection : { ...selection, name: nameNode(reference) }
    }),
  }
}

function derivedRoot(field: FieldNode, root: ManifestRoot, walk: Walk): FieldNode {
  const order = walk.sink === null ? 0 : walk.sink.sequence++
  const scope = filterScope(argument(field, 'where')?.value)
  root.models.forEach((model, index) => {
    const state: RowState = { fields: new Map([[model, new Map<string, FieldNode>()]]), nested: false }
    register([model], index === 0 ? scope : null, false, true, order, responseKey(field), state, walk)
  })
  return field
}

function markNullable(field: FieldNode, root: ManifestRoot, walk: Walk): void {
  if ((root.shape === 'row' || root.shape === 'deletedRow') && root.nullable && walk.sink !== null) {
    walk.sink.nullable.add(responseKey(field))
  }
}

function rewriteFragment(definition: FragmentDefinitionNode, walk: Walk): FragmentDefinitionNode {
  const condition = definition.typeCondition.name.value
  if (condition === 'Query') {
    return { ...definition, selectionSet: { ...definition.selectionSet, selections: visitRootSet(definition.selectionSet, walk) } }
  }
  if (walk.manifest.types?.[condition]?.kind === 'object') {
    return { ...definition, selectionSet: objectSelection(definition.selectionSet, { kind: 'object', name: condition, nullable: false }, '', walk, new Set([definition.name.value])) }
  }
  if (walk.manifest.models[condition] === undefined) {
    return definition
  }
  return { ...definition, selectionSet: rows(definition.selectionSet, [condition], null, false, false, '', walk) }
}

export function operationOf(document: DocumentNode, kind: OperationTypeNode): OperationDefinitionNode {
  const operations = document.definitions.filter((definition): definition is OperationDefinitionNode => definition.kind === Kind.OPERATION_DEFINITION)
  if (operations.length > 1) {
    throw new GolemMultipleOperationsError(operations.length)
  }
  const operation = operations[0]
  if (operation === undefined) {
    throw new GolemDocumentError('the document holds no operation')
  }
  if (operation.operation !== kind) {
    throw new GolemDocumentError(`only ${kind} operations are analysed here; got ${operation.operation}`)
  }
  return operation
}

function fragmentsOf(document: DocumentNode): Map<string, FragmentDefinitionNode> {
  const fragments = new Map<string, FragmentDefinitionNode>()
  for (const definition of document.definitions) {
    if (definition.kind === Kind.FRAGMENT_DEFINITION) {
      fragments.set(definition.name.value, definition)
    }
  }
  return fragments
}

function operationFragments(operation: OperationDefinitionNode, fragments: ReadonlyMap<string, FragmentDefinitionNode>, manifest: ClientManifest): Set<string> {
  const result = new Set<string>()
  const body = (set: SelectionSetNode): void => {
    for (const selection of set.selections) {
      if (selection.kind === Kind.FRAGMENT_SPREAD) {
        if (result.has(selection.name.value)) continue
        result.add(selection.name.value)
        const fragment = fragments.get(selection.name.value)
        if (fragment !== undefined) body(fragment.selectionSet)
      } else if (selection.selectionSet !== undefined) body(selection.selectionSet)
    }
  }
  const roots = (set: SelectionSetNode, visiting: ReadonlySet<string>): void => {
    for (const selection of set.selections) {
      if (selection.kind === Kind.FIELD) {
        if (manifest.roots[selection.name.value]?.invalidation !== undefined && selection.selectionSet !== undefined) body(selection.selectionSet)
      } else if (selection.kind === Kind.INLINE_FRAGMENT) roots(selection.selectionSet, visiting)
      else {
        if (visiting.has(selection.name.value)) throw new GolemDocumentError(`fragment ${selection.name.value} is recursive`)
        const fragment = fragments.get(selection.name.value)
        if (fragment !== undefined) roots(fragment.selectionSet, new Set([...visiting, selection.name.value]))
      }
    }
  }
  roots(operation.selectionSet, new Set())
  return result
}

function finishOperation(plan: OperationInvalidationPlan, operation: OperationDefinitionNode): OperationInvalidationPlan {
  const body = parse(plan.query).definitions[0]
  if (body?.kind !== Kind.OPERATION_DEFINITION) throw new GolemDocumentError('operation invalidation plan is invalid')
  const used = new Set(plan.variableNames)
  return { ...plan, query: print({ kind: Kind.DOCUMENT, definitions: [{ ...body, operation: OperationTypeNode.SUBSCRIPTION, variableDefinitions: (operation.variableDefinitions ?? []).filter((definition) => used.has(definition.variable.name.value)) }] }) }
}

function analyse(document: DocumentNode, manifest: ClientManifest, mode: 'live'): DocumentAnalysis
function analyse(document: DocumentNode, manifest: ClientManifest, mode: 'static'): StaticDocumentAnalysis
function analyse(document: DocumentNode, manifest: ClientManifest, mode: 'live' | 'static'): DocumentAnalysis | StaticDocumentAnalysis {
  const operation = operationOf(document, OperationTypeNode.QUERY)
  const fragments = fragmentsOf(document)
  const nonEnriched = operationFragments(operation, fragments, manifest)
  const fragmentAliases = new Map<string, string>()
  let aliasIndex = 0
  for (const name of nonEnriched) {
    let alias: string
    do { alias = `golemLiveFragment${aliasIndex++}` } while (fragments.has(alias))
    fragmentAliases.set(name, alias)
  }
  const usedEnrichedFragments = new Set<string>()
  const sink: Sink = { streams: new Map(), operations: new Map(), notLive: new Map(), nullable: new Set(), sequence: 0 }
  const dependencies = { models: new Set<string>(), opaque: false }
  const live: Walk = { manifest, fragments, sink, enrichRows: mode === 'live', fragmentAliases, usedEnrichedFragments, dependencies }
  const silent: Walk = { ...live, sink: null, dependencies: undefined }
  const rewrittenOperation: OperationDefinitionNode = {
    ...operation,
    selectionSet: { ...operation.selectionSet, selections: visitRootSet(operation.selectionSet, live) },
  }
  const definitions = document.definitions.map((definition) => {
    if (definition === operation) {
      return rewrittenOperation
    }
    if (definition.kind === Kind.FRAGMENT_DEFINITION) {
      return rewriteFragment(definition, nonEnriched.has(definition.name.value) ? { ...silent, enrichRows: false } : silent)
    }
    return definition
  })
  for (const name of usedEnrichedFragments) {
    const source = fragments.get(name)
    if (source !== undefined) definitions.push(rewriteFragment({ ...source, name: nameNode(fragmentAliases.get(name)!) }, silent))
  }
  const rewritten: DocumentNode = { ...document, definitions }
  const readDependencies = { models: [...dependencies.models].sort(), opaque: dependencies.opaque }
  if (mode === 'static') return { mode, dependencies: readDependencies, document: rewritten, query: print(rewritten) }
  const notLive = [...sink.notLive.values()]
  return {
    dependencies: readDependencies,
    document: rewritten,
    query: print(rewritten),
    nullableRoots: sink.nullable,
    streams: notLive.length > 0 ? [] : [...[...sink.streams.values()].map((draft) => ({ order: draft.order, plan: finish(draft, manifest, operation) as LivePlan })), ...[...sink.operations.values()].map((entry) => ({ order: entry.order, plan: finishOperation(entry.plan, operation) as LivePlan }))].sort((left, right) => left.order - right.order).map((entry) => entry.plan),
    notLive,
    live: notLive.length === 0,
  }
}

export class DocumentAnalyzer {
  private readonly manifest: ClientManifest
  private readonly byText = new Map<string, DocumentAnalysis>()
  private readonly byNode = new WeakMap<DocumentNode, DocumentAnalysis>()
  private readonly staticByText = new Map<string, StaticDocumentAnalysis>()
  private readonly staticByNode = new WeakMap<DocumentNode, StaticDocumentAnalysis>()

  constructor(manifest: ClientManifest) {
    assertClientManifest(manifest)
    this.manifest = manifest
  }

  analyze(document: DocumentNode | string): DocumentAnalysis {
    if (typeof document === 'string') {
      let analysis = this.byText.get(document)
      if (analysis === undefined) {
        analysis = analyse(parse(document), this.manifest, 'live')
        this.byText.set(document, analysis)
      }
      return analysis
    }
    let analysis = this.byNode.get(document)
    if (analysis === undefined) {
      analysis = analyse(document, this.manifest, 'live')
      this.byNode.set(document, analysis)
    }
    return analysis
  }

  analyzeStatic(document: DocumentNode | string): StaticDocumentAnalysis {
    if (typeof document === 'string') {
      let analysis = this.staticByText.get(document)
      if (analysis === undefined) {
        analysis = analyse(parse(document), this.manifest, 'static')
        this.staticByText.set(document, analysis)
      }
      return analysis
    }
    let analysis = this.staticByNode.get(document)
    if (analysis === undefined) {
      analysis = analyse(document, this.manifest, 'static')
      this.staticByNode.set(document, analysis)
    }
    return analysis
  }
}

export interface MutationAnalysis {
  readonly query: string
  readonly roots: ReadonlyArray<{ readonly name: string; readonly mutationScope: ManifestMutationScope }>
}

export function analyzeMutation(document: DocumentNode | string, manifest: ClientManifest): MutationAnalysis {
  assertClientManifest(manifest)
  const parsed = typeof document === 'string' ? parse(document) : document
  const operation = operationOf(parsed, OperationTypeNode.MUTATION)
  const roots: Array<{ readonly name: string; readonly mutationScope: ManifestMutationScope }> = []
  for (const selection of operation.selectionSet.selections) {
    if (selection.kind !== Kind.FIELD) {
      throw new GolemDocumentError('a mutation document selects its roots directly; fragments at the mutation root are not supported')
    }
    const name = selection.name.value
    if (name.startsWith('__')) {
      continue
    }
    const described = manifest.roots[name]
    if (described === undefined) {
      throw new GolemDocumentError(`${name} is not a root in the manifest`)
    }
    // Older manifests describe response models only. They cannot prove write
    // reach, so retaining their support requires conservative invalidation.
    roots.push({ name, mutationScope: described.mutationScope ?? { kind: 'unscoped' } })
  }
  return { query: print(parsed), roots }
}

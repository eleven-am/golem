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
import type { ClientManifest, ManifestModel, ManifestRoot } from './manifest.js'
import type { SubscriptionRequest } from './session.js'

const VERSION_FIELD = '_version'
const TYPENAME_FIELD = '__typename'

export type Ranking = ReadonlyArray<string> | 'any'

export interface StreamPlan {
  readonly key: string
  readonly model: string
  readonly eventsRoot: string
  readonly slot: boolean
  readonly rankedBy: Ranking
  readonly scope: 'exact' | 'policy-only'
  readonly paths: ReadonlyArray<string>
  readonly query: string
  readonly variableNames: ReadonlyArray<string>
}

export interface DocumentAnalysis {
  readonly document: DocumentNode
  readonly query: string
  readonly streams: ReadonlyArray<StreamPlan>
  readonly nullableRoots: ReadonlySet<string>
  readonly notLive: ReadonlyArray<GolemNotLiveError>
  readonly live: boolean
}

export function assertLive(analysis: DocumentAnalysis): void {
  const first = analysis.notLive[0]
  if (first !== undefined) {
    throw first
  }
}

export function streamRequest(stream: StreamPlan, variables: Readonly<Record<string, unknown>> = {}): SubscriptionRequest {
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
  slot: boolean
  ranking: Ranking
  readonly scope: Scope | null
  readonly order: number
  readonly fields: Map<string, FieldNode>
  readonly paths: Set<string>
}

interface Sink {
  readonly streams: Map<string, StreamDraft>
  readonly notLive: Map<string, GolemNotLiveError>
  readonly nullable: Set<string>
  sequence: number
}

interface Walk {
  readonly manifest: ClientManifest
  readonly fragments: ReadonlyMap<string, FragmentDefinitionNode>
  readonly sink: Sink | null
}

type RowState = ReadonlyMap<string, Map<string, FieldNode>>

type Scope = ObjectValueNode | VariableNode

const NO_RANKING: Ranking = []

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
    const existing = result.find((selection): selection is FieldNode => selection.kind === Kind.FIELD && responseKey(selection) === name)
    if (existing === undefined) {
      result.push(bareField(name))
    } else if (existing.name.value !== name) {
      throw new GolemDocumentError(`the response key ${name} is taken by ${existing.name.value}; golem needs ${name} unaliased on this selection`)
    }
  }
  return result
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
      if (models.length > 1 && narrowed.length === 1 && only !== undefined) {
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
      if (walk.sink !== null) {
        visitSet(definition.selectionSet, applicableTo(definition, models, walk.manifest), scope, path, state, walk)
      }
      out.push(selection)
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
  const fields = state.get(only)
  if (model === undefined || fields === undefined) {
    throw new GolemDocumentError(`the manifest has no model ${only}`)
  }
  const relation = model.relations[name]
  if (relation !== undefined) {
    return visitRelation(field, only, relation, scope, path, walk)
  }
  const computed = model.computed[name]
  if (computed !== undefined && computed.length > 0) {
    if (field.selectionSet === undefined) {
      return field
    }
    return { ...field, selectionSet: rows(field.selectionSet, computed, null, true, NO_RANKING, childPath(path, field), walk) }
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
  const childScope = conjunction(filterScope(argument(field, 'where')?.value), inverseFilter)
  return { ...field, selectionSet: rows(field.selectionSet, [relation.target], childScope, false, rankingOf(field, target), childPath(path, field), walk) }
}

function rankingOf(field: FieldNode, model: ManifestModel): Ranking {
  const value = argument(field, 'orderBy')?.value
  if (value === undefined || value.kind === Kind.NULL) {
    return NO_RANKING
  }
  const names = new Set<string>()
  const read = (node: ValueNode): boolean => {
    if (node.kind === Kind.VARIABLE) {
      return false
    }
    if (node.kind === Kind.OBJECT) {
      for (const entry of node.fields) {
        if (!(entry.name.value in model.relations) && !(entry.name.value in model.computed)) {
          names.add(entry.name.value)
        }
      }
      return true
    }
    return node.kind === Kind.LIST ? node.values.every(read) : true
  }
  return read(value) ? [...names] : 'any'
}

function mergeRanking(left: Ranking, right: Ranking): Ranking {
  if (left === 'any' || right === 'any') {
    return 'any'
  }
  return [...new Set([...left, ...right])]
}

function rows(set: SelectionSetNode, models: ReadonlyArray<string>, scope: Scope | null, slot: boolean, ranking: Ranking, path: string, walk: Walk): SelectionSetNode {
  const order = walk.sink === null ? 0 : walk.sink.sequence++
  const state: RowState = new Map(models.map((model) => [model, new Map<string, FieldNode>()]))
  let selections = visitSet(set, models, scope, path, state, walk)
  const single = models.length === 1 ? walk.manifest.models[models[0] ?? ''] : undefined
  if (single !== undefined) {
    const ranked = ranking === 'any' ? [] : ranking
    selections = ensure(selections, [...injections(single), ...ranked])
    const known = state.get(models[0] ?? '')
    for (const name of ranked) {
      if (known !== undefined && !known.has(name)) {
        known.set(name, bareField(name))
      }
    }
  } else {
    selections = ensure(selections, [TYPENAME_FIELD])
  }
  register(models, scope, slot, ranking, order, path, state, walk)
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

function register(models: ReadonlyArray<string>, scope: Scope | null, slot: boolean, ranking: Ranking, order: number, path: string, state: RowState, walk: Walk): void {
  const sink = walk.sink
  if (sink === null) {
    return
  }
  for (const name of models) {
    const model = walk.manifest.models[name]
    const fields = state.get(name)
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
      draft = { key, model: name, eventsRoot: model.events, slot, ranking, scope, order, fields: new Map(), paths: new Set() }
      sink.streams.set(key, draft)
    } else {
      draft.slot ||= slot
      draft.ranking = mergeRanking(draft.ranking, ranking)
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
    slot: draft.slot,
    rankedBy: draft.ranking,
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
      if (walk.sink !== null) {
        visitRootSet(definition.selectionSet, walk)
      }
      out.push(selection)
    }
  }
  return out
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
  if (root.kind === 'generated') {
    const modelName = root.models[0]
    const model = modelName === undefined ? undefined : walk.manifest.models[modelName]
    if (modelName === undefined || model === undefined) {
      throw new GolemDocumentError(`the manifest root ${name} names no model`)
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
    return { ...field, selectionSet: rows(field.selectionSet, [modelName], scope, false, rankingOf(field, model), responseKey(field), walk) }
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
  return { ...field, selectionSet: rows(field.selectionSet, root.models, null, true, NO_RANKING, responseKey(field), walk) }
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
  if (walk.manifest.models[condition] === undefined) {
    return definition
  }
  return { ...definition, selectionSet: rows(definition.selectionSet, [condition], null, false, NO_RANKING, '', walk) }
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

function analyse(document: DocumentNode, manifest: ClientManifest): DocumentAnalysis {
  const operation = operationOf(document, OperationTypeNode.QUERY)
  const fragments = fragmentsOf(document)
  const sink: Sink = { streams: new Map(), notLive: new Map(), nullable: new Set(), sequence: 0 }
  const live: Walk = { manifest, fragments, sink }
  const silent: Walk = { manifest, fragments, sink: null }
  const rewrittenOperation: OperationDefinitionNode = {
    ...operation,
    selectionSet: { ...operation.selectionSet, selections: visitRootSet(operation.selectionSet, live) },
  }
  const definitions = document.definitions.map((definition) => {
    if (definition === operation) {
      return rewrittenOperation
    }
    if (definition.kind === Kind.FRAGMENT_DEFINITION) {
      return rewriteFragment(definition, silent)
    }
    return definition
  })
  const rewritten: DocumentNode = { ...document, definitions }
  const notLive = [...sink.notLive.values()]
  return {
    document: rewritten,
    query: print(rewritten),
    nullableRoots: sink.nullable,
    streams: notLive.length > 0 ? [] : [...sink.streams.values()].sort((left, right) => left.order - right.order).map((draft) => finish(draft, manifest, operation)),
    notLive,
    live: notLive.length === 0,
  }
}

export class DocumentAnalyzer {
  private readonly manifest: ClientManifest
  private readonly byText = new Map<string, DocumentAnalysis>()
  private readonly byNode = new WeakMap<DocumentNode, DocumentAnalysis>()

  constructor(manifest: ClientManifest) {
    this.manifest = manifest
  }

  analyze(document: DocumentNode | string): DocumentAnalysis {
    if (typeof document === 'string') {
      let analysis = this.byText.get(document)
      if (analysis === undefined) {
        analysis = analyse(parse(document), this.manifest)
        this.byText.set(document, analysis)
      }
      return analysis
    }
    let analysis = this.byNode.get(document)
    if (analysis === undefined) {
      analysis = analyse(document, this.manifest)
      this.byNode.set(document, analysis)
    }
    return analysis
  }
}

export interface MutationAnalysis {
  readonly query: string
  readonly roots: ReadonlyArray<{ readonly name: string; readonly models: ReadonlyArray<string> }>
}

export function analyzeMutation(document: DocumentNode | string, manifest: ClientManifest): MutationAnalysis {
  const parsed = typeof document === 'string' ? parse(document) : document
  const operation = operationOf(parsed, OperationTypeNode.MUTATION)
  const roots: Array<{ readonly name: string; readonly models: ReadonlyArray<string> }> = []
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
    roots.push({ name, models: described.models })
  }
  return { query: print(parsed), roots }
}

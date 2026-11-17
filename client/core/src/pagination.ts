import { Kind, OperationTypeNode, print, type DocumentNode, type FieldNode, type OperationDefinitionNode, type SelectionSetNode, type ValueNode } from 'graphql'
import { assertLive, operationOf, type DocumentAnalysis, type StaticDocumentAnalysis } from './analysis.js'
import { GolemConfigurationError, GolemDocumentError, GolemProtocolError } from './errors.js'
import { assertClientManifest, type ClientManifest } from './manifest.js'

export type Cursor = Readonly<Record<string, unknown>>

export type TakeSource = { readonly variable: string; readonly fallback?: number } | { readonly literal: number }

export interface PageSpec {
  readonly root: string
  readonly model: string
  readonly selector: string
  readonly primaryKey: ReadonlyArray<string>
  readonly take: TakeSource
  readonly continuation: string
}

export interface InfiniteQueryPlan {
  readonly spec: PageSpec
  readonly take: number
  readonly initialPageParam: Cursor | null
  getNextPageParam(lastPage: unknown): Cursor | undefined
  requestFor(pageParam: Cursor | null): { readonly query: string; readonly variables: Readonly<Record<string, unknown>> }
}

const SKIP_PAST_CURSOR = 1

function variableOf(field: FieldNode, name: string): string | undefined {
  const value = field.arguments?.find((candidate) => candidate.name.value === name)?.value
  return value?.kind === Kind.VARIABLE ? value.name.value : undefined
}

function takeOf(field: FieldNode, operation: OperationDefinitionNode): TakeSource | undefined {
  const value: ValueNode | undefined = field.arguments?.find((candidate) => candidate.name.value === 'take')?.value
  if (value?.kind === Kind.VARIABLE) {
    const name = value.name.value
    const fallback = operation.variableDefinitions?.find((definition) => definition.variable.name.value === name)?.defaultValue
    return fallback?.kind === Kind.INT ? { variable: name, fallback: Number(fallback.value) } : { variable: name }
  }
  if (value?.kind === Kind.INT) {
    return { literal: Number(value.value) }
  }
  return undefined
}

export const CURSOR_VARIABLE = 'golemCursor'

export const SKIP_VARIABLE = 'golemSkip'

const nameNode = (value: string) => ({ kind: Kind.NAME as const, value })

function continuationOf(document: DocumentNode, operation: OperationDefinitionNode, field: FieldNode, uniqueInput: string): string {
  const taken = operation.variableDefinitions?.find((definition) => definition.variable.name.value === CURSOR_VARIABLE || definition.variable.name.value === SKIP_VARIABLE)
  if (taken !== undefined) {
    throw new GolemDocumentError(`the variable ${taken.variable.name.value} belongs to the client; an infinite query adds it to every page after the first`)
  }
  const paged: FieldNode = {
    ...field,
    arguments: [
      ...(field.arguments ?? []),
      { kind: Kind.ARGUMENT, name: nameNode('cursor'), value: { kind: Kind.VARIABLE, name: nameNode(CURSOR_VARIABLE) } },
      { kind: Kind.ARGUMENT, name: nameNode('skip'), value: { kind: Kind.VARIABLE, name: nameNode(SKIP_VARIABLE) } },
    ],
  }
  const widened: OperationDefinitionNode = {
    ...operation,
    variableDefinitions: [
      ...(operation.variableDefinitions ?? []),
      { kind: Kind.VARIABLE_DEFINITION, variable: { kind: Kind.VARIABLE, name: nameNode(CURSOR_VARIABLE) }, type: { kind: Kind.NAMED_TYPE, name: nameNode(uniqueInput) } },
      { kind: Kind.VARIABLE_DEFINITION, variable: { kind: Kind.VARIABLE, name: nameNode(SKIP_VARIABLE) }, type: { kind: Kind.NAMED_TYPE, name: nameNode('Int') } },
    ],
    selectionSet: { ...operation.selectionSet, selections: operation.selectionSet.selections.map((selection) => (selection === field ? paged : selection)) },
  }
  return print({ ...document, definitions: document.definitions.map((definition) => (definition === operation ? widened : definition)) })
}

export function pageSpec(analysis: DocumentAnalysis, manifest: ClientManifest): PageSpec {
  const operation = operationOf(analysis.document, OperationTypeNode.QUERY)
  const named = (field: FieldNode, name: string) => field.arguments?.some((candidate) => candidate.name.value === name) === true
  for (const selection of operation.selectionSet.selections) {
    if (selection.kind === Kind.FIELD && (named(selection, 'cursor') || named(selection, 'skip'))) {
      throw new GolemDocumentError(`${selection.name.value} sets cursor or skip itself; an infinite query builds both from the last row of the previous page`)
    }
  }
  const roots = operation.selectionSet.selections.filter((selection): selection is FieldNode => selection.kind === Kind.FIELD && named(selection, 'take'))
  const [field] = roots
  if (field === undefined || roots.length !== 1) {
    throw new GolemDocumentError(`an infinite query needs exactly one root field with a take argument; found ${roots.length}`)
  }
  const name = field.name.value
  const root = manifest.roots[name]
  const modelName = root?.models[0]
  const model = modelName === undefined ? undefined : manifest.models[modelName]
  if (root === undefined || root.kind !== 'generated' || root.shape !== 'rows' || modelName === undefined || model === undefined) {
    throw new GolemDocumentError(`${name} cannot be paged: only a generated list root has a cursor`)
  }
  if (model.uniqueInput === null) {
    throw new GolemDocumentError(`${name} cannot be paged: the manifest names no unique input for ${modelName}`)
  }
  const take = takeOf(field, operation)
  if (take === undefined) {
    throw new GolemDocumentError(`${name} must give take a variable or an integer`)
  }
  return {
    root: (field.alias ?? field.name).value,
    model: modelName,
    selector: model.cursorSelector,
    primaryKey: model.primaryKey,
    take,
    continuation: continuationOf(analysis.document, operation, field, model.uniqueInput),
  }
}

export function cursorOf(spec: PageSpec, row: Readonly<Record<string, unknown>>): Cursor {
  const parts: Record<string, unknown> = {}
  for (const field of spec.primaryKey) {
    const value = row[field]
    if (value === undefined || value === null) {
      throw new GolemProtocolError(`a ${spec.model} row on the page lacks ${field}, so no cursor can be built from it`)
    }
    parts[field] = value
  }
  const [only] = spec.primaryKey
  return { [spec.selector]: spec.primaryKey.length === 1 && only !== undefined ? parts[only] : parts }
}

function pageRows(spec: PageSpec, page: unknown): ReadonlyArray<Readonly<Record<string, unknown>>> {
  const rows = typeof page === 'object' && page !== null ? (page as Record<string, unknown>)[spec.root] : undefined
  if (!Array.isArray(rows)) {
    throw new GolemProtocolError(`a page carried no ${spec.root} list`)
  }
  return rows as ReadonlyArray<Readonly<Record<string, unknown>>>
}

function takeValue(spec: Pick<PageSpec, 'take'>, variables: Readonly<Record<string, unknown>>): number {
  const value = 'literal' in spec.take ? spec.take.literal : (variables[spec.take.variable] ?? spec.take.fallback)
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) {
    throw new GolemConfigurationError(`an infinite query needs a positive integer take; got ${JSON.stringify(value)}`)
  }
  return value
}

export function golemInfiniteQuery(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>>, manifest: ClientManifest): InfiniteQueryPlan {
  const spec = pageSpec(analysis, manifest)
  for (const owned of [CURSOR_VARIABLE, SKIP_VARIABLE]) {
    if (owned in variables) {
      throw new GolemConfigurationError(`the variable ${owned} belongs to the client; an infinite query builds it from the last row of the previous page`)
    }
  }
  const take = takeValue(spec, variables)
  return {
    spec,
    take,
    initialPageParam: null,
    getNextPageParam(lastPage) {
      const rows = pageRows(spec, lastPage)
      const last = rows.at(-1)
      return rows.length < take || last === undefined ? undefined : cursorOf(spec, last)
    },
    requestFor(pageParam) {
      return pageParam === null
        ? { query: analysis.query, variables }
        : { query: spec.continuation, variables: { ...variables, [CURSOR_VARIABLE]: pageParam, [SKIP_VARIABLE]: SKIP_PAST_CURSOR } }
    },
  }
}

export type ContinuationPageParam = string | null

export const CONTINUATION_VARIABLE = 'golemAfter'
export const CONTINUATION_RESPONSE_KEY = '_golemNext'
export const CONTINUATION_TOKEN_MAX_BYTES = 8192

export interface ContinuationQueryPlan {
  readonly root: string
  readonly initialPageParam: ContinuationPageParam
  getNextPageParam(lastPage: unknown, allPages: ReadonlyArray<unknown>, lastPageParam: ContinuationPageParam, allPageParams: ReadonlyArray<ContinuationPageParam>): string | undefined
  requestFor(pageParam: ContinuationPageParam): { readonly query: string; readonly variables: Readonly<Record<string, unknown>> }
}

function continuationToken(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0 && value.length <= CONTINUATION_TOKEN_MAX_BYTES && new TextEncoder().encode(value).length <= CONTINUATION_TOKEN_MAX_BYTES
}

function validateContinuationTake(field: FieldNode, operation: OperationDefinitionNode, variables: Readonly<Record<string, unknown>>): void {
  let value = field.arguments?.find((argument) => argument.name.value === 'take')?.value
  if (value?.kind === Kind.VARIABLE) {
    const name = value.name.value
    const supplied = variables[name]
    if (supplied !== undefined) {
      if (supplied !== null && (typeof supplied !== 'number' || !Number.isInteger(supplied) || supplied < 1)) {
        throw new GolemConfigurationError(`a continuation query needs a positive integer take or null; got ${JSON.stringify(supplied)}`)
      }
      return
    }
    value = operation.variableDefinitions?.find((definition) => definition.variable.name.value === name)?.defaultValue
  }
  // Null and omission both select the declaration's default. Explicit null
  // must not fall back to a GraphQL variable's default value.
  if (value === undefined || value.kind === Kind.NULL) return
  if (value.kind !== Kind.INT) throw new GolemDocumentError(`${field.name.value} must give take an integer, null, or a variable`)
  const take = Number(value.value)
  if (!Number.isInteger(take) || take < 1) throw new GolemConfigurationError(`a continuation query needs a positive integer take or null; got ${value.value}`)
}

function assertPageAliasAvailable(set: SelectionSetNode, document: DocumentNode, visiting = new Set<string>()): void {
  for (const selection of set.selections) {
    if (selection.kind === Kind.FIELD) {
      if ((selection.alias ?? selection.name).value === CONTINUATION_RESPONSE_KEY) {
        throw new GolemDocumentError(`the response key ${CONTINUATION_RESPONSE_KEY} belongs to the continuation client`)
      }
    } else if (selection.kind === Kind.INLINE_FRAGMENT) {
      assertPageAliasAvailable(selection.selectionSet, document, visiting)
    } else {
      const name = selection.name.value
      if (visiting.has(name)) throw new GolemDocumentError(`fragment ${name} is recursive`)
      const fragment = document.definitions.find((definition) => definition.kind === Kind.FRAGMENT_DEFINITION && definition.name.value === name)
      if (fragment?.kind !== Kind.FRAGMENT_DEFINITION) throw new GolemDocumentError(`fragment ${name} is not defined`)
      assertPageAliasAvailable(fragment.selectionSet, document, new Set([...visiting, name]))
    }
  }
}

// Continuation pages use server-authenticated traversal state. The count of
// visible items never determines whether another candidate batch exists.
export function golemContinuationQuery(analysis: StaticDocumentAnalysis, variables: Readonly<Record<string, unknown>>, manifest: ClientManifest): ContinuationQueryPlan {
  if (analysis?.mode !== 'static') throw new GolemConfigurationError('a continuation query requires DocumentAnalyzer.analyzeStatic so model projections remain unchanged')
  return continuationQuery(analysis.document, variables, manifest)
}

export function golemLiveContinuationQuery(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>>, manifest: ClientManifest): ContinuationQueryPlan {
  assertLive(analysis)
  const operation = operationOf(analysis.document, OperationTypeNode.QUERY)
  const pageable = operation.selectionSet.selections.filter((selection): selection is FieldNode => selection.kind === Kind.FIELD && manifest.roots[selection.name.value]?.pagination?.kind === 'continuation')
  if (pageable.length !== 1 || manifest.roots[pageable[0]!.name.value]?.invalidation?.kind !== 'operation') {
    throw new GolemConfigurationError('live continuation pages require a declared operation invalidation subscription')
  }
  return continuationQuery(analysis.document, variables, manifest)
}

function continuationQuery(document: DocumentNode, variables: Readonly<Record<string, unknown>>, manifest: ClientManifest): ContinuationQueryPlan {
  assertClientManifest(manifest)
  const operation = operationOf(document, OperationTypeNode.QUERY)
  if (operation.selectionSet.selections.some((selection) => selection.kind !== Kind.FIELD)) {
    throw new GolemDocumentError('a continuation query selects its root directly; root fragments are not supported')
  }
  const roots = operation.selectionSet.selections.filter((selection): selection is FieldNode => selection.kind === Kind.FIELD && manifest.roots[selection.name.value]?.pagination?.kind === 'continuation')
  const field = roots[0]
  if (roots.length !== 1 || field === undefined) throw new GolemDocumentError(`a continuation query needs exactly one direct pageable root; found ${roots.length}`)
  const metadata = manifest.roots[field.name.value]?.pagination
  if (metadata === undefined || field.selectionSet === undefined) throw new GolemDocumentError('the continuation root requires page metadata and a selection')
  if ((field.directives?.length ?? 0) > 0) throw new GolemDocumentError('a continuation root must be unconditional')
  if (field.arguments?.some((argument) => argument.name.value === metadata.afterArgument)) {
    throw new GolemDocumentError(`${field.name.value} sets after itself; continuation queries own that argument`)
  }
  if (operation.variableDefinitions?.some((definition) => definition.variable.name.value === CONTINUATION_VARIABLE)) {
    throw new GolemDocumentError(`the variable ${CONTINUATION_VARIABLE} belongs to the continuation client`)
  }
  if (CONTINUATION_VARIABLE in variables) throw new GolemConfigurationError(`the variable ${CONTINUATION_VARIABLE} belongs to the continuation client`)
  validateContinuationTake(field, operation, variables)
  assertPageAliasAvailable(field.selectionSet, document)
  const selected: FieldNode = {
    ...field,
    selectionSet: {
      ...field.selectionSet,
      selections: [...field.selectionSet.selections, { kind: Kind.FIELD, name: nameNode(metadata.nextField), alias: nameNode(CONTINUATION_RESPONSE_KEY) }],
    },
  }
  const replace = (replacement: FieldNode, paged: boolean): string => {
    const nextOperation: OperationDefinitionNode = {
      ...operation,
      variableDefinitions: [
        ...(operation.variableDefinitions ?? []),
        ...(paged ? [{ kind: Kind.VARIABLE_DEFINITION as const, variable: { kind: Kind.VARIABLE as const, name: nameNode(CONTINUATION_VARIABLE) }, type: { kind: Kind.NAMED_TYPE as const, name: nameNode('String') } }] : []),
      ],
      selectionSet: { ...operation.selectionSet, selections: operation.selectionSet.selections.map((selection) => selection === field ? replacement : selection) },
    }
    return print({ ...document, definitions: document.definitions.map((definition) => definition === operation ? nextOperation : definition) })
  }
  const firstQuery = replace(selected, false)
  const nextQuery = replace({ ...selected, arguments: [...(selected.arguments ?? []), { kind: Kind.ARGUMENT, name: nameNode(metadata.afterArgument), value: { kind: Kind.VARIABLE, name: nameNode(CONTINUATION_VARIABLE) } }] }, true)
  const root = (field.alias ?? field.name).value
  return {
    root,
    initialPageParam: null,
    getNextPageParam(lastPage, _allPages, lastPageParam, allPageParams) {
      const page = typeof lastPage === 'object' && lastPage !== null && !Array.isArray(lastPage) ? (lastPage as Record<string, unknown>)[root] : undefined
      if (typeof page !== 'object' || page === null || Array.isArray(page) || !Object.hasOwn(page, CONTINUATION_RESPONSE_KEY)) throw new GolemProtocolError(`the page carried no ${root}.${CONTINUATION_RESPONSE_KEY} continuation`)
      const token = (page as Record<string, unknown>)[CONTINUATION_RESPONSE_KEY]
      if (token === null) return undefined
      if (!continuationToken(token)) throw new GolemProtocolError('the page continuation must be a nonempty string of at most 8192 bytes, or null')
      if (token === lastPageParam || allPageParams.includes(token)) throw new GolemProtocolError('the page repeated a continuation already used in this chain')
      return token
    },
    requestFor(pageParam) {
      if (pageParam === null) return { query: firstQuery, variables }
      if (!continuationToken(pageParam)) throw new GolemConfigurationError('a continuation page parameter must be a nonempty string of at most 8192 bytes, or null')
      return { query: nextQuery, variables: { ...variables, [CONTINUATION_VARIABLE]: pageParam } }
    },
  }
}

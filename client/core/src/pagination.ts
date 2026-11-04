import { Kind, OperationTypeNode, print, type DocumentNode, type FieldNode, type OperationDefinitionNode, type ValueNode } from 'graphql'
import { operationOf, type DocumentAnalysis } from './analysis.js'
import { GolemConfigurationError, GolemDocumentError, GolemProtocolError } from './errors.js'
import type { ClientManifest } from './manifest.js'

export type Cursor = Readonly<Record<string, unknown>>

export type TakeSource = { readonly variable: string } | { readonly literal: number }

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

function takeOf(field: FieldNode): TakeSource | undefined {
  const value: ValueNode | undefined = field.arguments?.find((candidate) => candidate.name.value === 'take')?.value
  if (value?.kind === Kind.VARIABLE) {
    return { variable: value.name.value }
  }
  if (value?.kind === Kind.INT) {
    return { literal: Number(value.value) }
  }
  return undefined
}

export const CURSOR_VARIABLE = 'golemCursor'

export const SKIP_VARIABLE = 'golemSkip'

const nameNode = (value: string) => ({ kind: Kind.NAME as const, value })

function continuationOf(document: DocumentNode, operation: OperationDefinitionNode, field: FieldNode, model: string): string {
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
      { kind: Kind.VARIABLE_DEFINITION, variable: { kind: Kind.VARIABLE, name: nameNode(CURSOR_VARIABLE) }, type: { kind: Kind.NAMED_TYPE, name: nameNode(`${model}WhereUniqueInput`) } },
      { kind: Kind.VARIABLE_DEFINITION, variable: { kind: Kind.VARIABLE, name: nameNode(SKIP_VARIABLE) }, type: { kind: Kind.NAMED_TYPE, name: nameNode('Int') } },
    ],
    selectionSet: { ...operation.selectionSet, selections: operation.selectionSet.selections.map((selection) => (selection === field ? paged : selection)) },
  }
  return print({ ...document, definitions: document.definitions.map((definition) => (definition === operation ? widened : definition)) })
}

export function pageSpec(analysis: DocumentAnalysis, manifest: ClientManifest): PageSpec {
  const operation = operationOf(analysis.document, analysis.operationName, OperationTypeNode.QUERY)
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
  const take = takeOf(field)
  if (take === undefined) {
    throw new GolemDocumentError(`${name} must give take a variable or an integer`)
  }
  return {
    root: (field.alias ?? field.name).value,
    model: modelName,
    selector: model.cursorSelector,
    primaryKey: model.primaryKey,
    take,
    continuation: continuationOf(analysis.document, operation, field, modelName),
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

function takeValue(spec: PageSpec, variables: Readonly<Record<string, unknown>>): number {
  const value = 'literal' in spec.take ? spec.take.literal : variables[spec.take.variable]
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) {
    throw new GolemConfigurationError(`an infinite query needs a positive integer take; got ${JSON.stringify(value)}`)
  }
  return value
}

export function lastPageOf(data: unknown): unknown {
  const pages = typeof data === 'object' && data !== null ? (data as { pages?: unknown }).pages : undefined
  return Array.isArray(pages) ? pages.at(-1) : undefined
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

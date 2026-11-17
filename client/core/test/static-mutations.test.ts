import { InfiniteQueryObserver, QueryClient, QueryObserver } from '@tanstack/query-core'
import { getOperationAST, Kind, parse } from 'graphql'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CONTINUATION_RESPONSE_KEY, DocumentAnalyzer, GolemClient, GolemCommitUnknownError, GolemPartialCommitError, Mutations, type ClientManifest } from '../src/index.js'
import { continuationManifest } from './fixtures/continuation.js'

const model = { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: 'UserWhereUniqueInput', events: null, relations: {}, computed: {} } as const
const manifest = {
  ...continuationManifest,
  models: {
    User: { ...model, relations: { profile: { target: 'Profile', inverse: null, list: false, nullable: false } } },
    Profile: { ...model, uniqueInput: 'ProfileWhereUniqueInput' },
    Other: { ...model, uniqueInput: 'OtherWhereUniqueInput' },
  },
  roots: {
    ...continuationManifest.roots,
    users: { kind: 'generated', shape: 'rows', nullable: false, models: ['User'] },
    others: { kind: 'generated', shape: 'rows', nullable: false, models: ['Other'] },
    updateUser: { kind: 'generated', shape: 'row', nullable: false, models: ['User'], mutationScope: { kind: 'models', models: ['User'] } },
    updateProfile: { kind: 'generated', shape: 'row', nullable: false, models: ['Profile'], mutationScope: { kind: 'models', models: ['Profile'] } },
    updateOther: { kind: 'generated', shape: 'row', nullable: false, models: ['Other'], mutationScope: { kind: 'models', models: ['Other'] } },
    state: { kind: 'opaque', shape: 'value', nullable: false, models: [] },
  },
} as const satisfies ClientManifest
const USERS = '{users{name}}'
const PAGE = '{homeFeed(seed:7,take:1){items{kind index}}}'
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
const response = (data: unknown) => new Response(JSON.stringify({ data }))
const cleanups: Array<() => void> = []
afterEach(() => { for (const close of cleanups.splice(0).reverse()) close(); vi.restoreAllMocks() })
function world() {
  let revision = 0
  let commit: 'PARTIAL' | 'UNKNOWN' | undefined
  let hold = false
  const held: Array<{ request: Record<string, any>; init: RequestInit | undefined; body: Response; gate: ReturnType<typeof deferred<Response>> }> = []
  const calls: Array<Record<string, any>> = []
  const inits: Array<RequestInit | undefined> = []
  const socket = vi.fn(() => { throw new Error('a static read opened a socket') })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
  const client = new GolemClient({ queryClient, manifest, url: 'http://static-mutations.test/graphql', createSocket: socket,
    fetch: async (_input, init) => {
      const request = JSON.parse(String(init?.body)); calls.push(request); inits.push(init)
      const operation = getOperationAST(parse(request.query))!
      const selected = operation.selectionSet.selections[0]!
      if (selected.kind !== Kind.FIELD) throw new Error('fixture query must select a root')
      const name = selected.name.value
      if (operation.operation === 'mutation') {
        revision++
        if (commit !== undefined) return new Response(JSON.stringify({ data: { [name]: { id: 'changed' } }, errors: [{ message: 'commit evidence', extensions: { code: 'EVIDENCE' } }], extensions: { golem: { commit } } }))
        return response({ [name]: { id: 'changed' } })
      }
      let data: unknown
      if (name === 'users') data = { users: request.variables?.golemCursor === undefined ? [{ id: 'u', name: `user-${revision}`, profile: { id: 'p', label: `profile-${revision}` } }] : [] }
      else if (name === 'others') data = { others: [{ id: 'o', name: 'other' }] }
      else if (name === 'state') data = { state: revision }
      else {
        const after = request.variables?.golemAfter
        data = { homeFeed: { items: [{ kind: `revision-${revision}`, index: after === undefined ? 1 : 2 }], [CONTINUATION_RESPONSE_KEY]: after === undefined ? `${revision}:next` : null } }
      }
      const body = response(data)
      if (!hold) return body
      const gate = deferred<Response>(); held.push({ request, init, body, gate }); return gate.promise
    },
  })
  cleanups.push(() => { for (const item of held) item.gate.resolve(item.body); client.dispose(); queryClient.clear() })
  return { client, queryClient, calls, inits, socket, held, revision: () => revision, hold: (value: boolean) => { hold = value }, commit: (value: typeof commit) => { commit = value } }
}

function observe(view: ReturnType<typeof world>, document: string) {
  const options = view.client.staticQueryOptions(document)
  const observer = new QueryObserver(view.queryClient, options)
  cleanups.push(observer.subscribe(() => undefined))
  return { options, observer }
}

function pageObserver(view: ReturnType<typeof world>) {
  const options = view.client.staticContinuationQueryOptions(PAGE)
  const observer = new InfiniteQueryObserver(view.queryClient, options)
  cleanups.push(observer.subscribe(() => undefined))
  return { options, observer }
}

describe('own-write invalidation of SDK static reads', () => {
  it.each(['success', 'PARTIAL', 'UNKNOWN'] as const)('refreshes eventless model reads and opaque reads after %s, marks inactive reads stale and preserves unrelated data', async (outcome) => {
    const view = world()
    const active = observe(view, USERS)
    const opaque = observe(view, '{state}')
    const inactive = view.client.staticQueryOptions('query Inactive{users{name}}')
    const unrelated = view.client.staticQueryOptions('{others{name}}')
    await active.observer.refetch(); await opaque.observer.refetch()
    await view.queryClient.fetchQuery(inactive); await view.queryClient.fetchQuery(unrelated)
    view.queryClient.setQueryData(['ordinary'], { kept: true })
    view.queryClient.setQueryData(['golem', 'manual-only'], { kept: true })
    const unrelatedBefore = view.queryClient.getQueryData(unrelated.queryKey)
    view.commit(outcome === 'success' ? undefined : outcome)
    const mutations = new Mutations({ queryClient: view.queryClient, transport: view.client.transport, manifest })
    const pending = mutations.execute('mutation{updateUser{id}}')
    if (outcome === 'success') await pending
    else await expect(pending).rejects.toBeInstanceOf(outcome === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)
    await vi.waitFor(() => {
      expect(active.observer.getCurrentResult().data).toMatchObject({ users: [{ name: 'user-1' }] })
      expect(opaque.observer.getCurrentResult().data).toEqual({ state: 1 })
    })
    expect(view.queryClient.getQueryState(inactive.queryKey)?.isInvalidated).toBe(true)
    expect(view.queryClient.getQueryState(unrelated.queryKey)?.isInvalidated).toBe(false)
    expect(view.queryClient.getQueryData(unrelated.queryKey)).toBe(unrelatedBefore)
    expect(view.queryClient.getQueryState(['ordinary'])?.isInvalidated).toBe(false)
    expect(view.queryClient.getQueryState(['golem', 'manual-only'])?.isInvalidated).toBe(false)
    expect(view.client.live.started).toBe(false)
    expect(view.socket).not.toHaveBeenCalled()
  })

  it('collects nested public model dependencies without requiring event streams', async () => {
    const view = world()
    const active = observe(view, '{users{name profile{label}}}')
    await active.observer.refetch()
    expect(new DocumentAnalyzer(manifest).analyzeStatic('{users{name profile{...Fields}}} fragment Fields on Profile{label}').dependencies).toEqual({ models: ['Profile', 'User'], opaque: false })
    const analyzer = new DocumentAnalyzer(manifest)
    expect(analyzer.analyzeStatic('{users(where:{profile:{is:{id:{equals:"p"}}}}){name}}').dependencies.models).toEqual(['Profile', 'User'])
    expect(analyzer.analyzeStatic('query($where:UserWhereInput){users(where:$where){name}}').dependencies.models).toEqual(['Profile', 'User'])
    await view.client.mutate('mutation{updateProfile{id}}')
    await vi.waitFor(() => expect(active.observer.getCurrentResult().data).toMatchObject({ users: [{ profile: { label: 'profile-1' } }] }))
    expect(view.socket).not.toHaveBeenCalled()
  })

  it('refreshes a generated cursor API for a model without events while preserving its ordinary refetch traversal', async () => {
    const view = world()
    const options = view.client.staticInfiniteQueryOptions('{users(take:1){name}}')
    const observer = new InfiniteQueryObserver(view.queryClient, options)
    cleanups.push(observer.subscribe(() => undefined))
    await observer.refetch(); await observer.fetchNextPage()
    expect(observer.getCurrentResult().data?.pages).toHaveLength(2)
    await view.client.mutate('mutation{updateUser{id}}')
    await vi.waitFor(() => expect(observer.getCurrentResult().data?.pages[0]).toMatchObject({ users: [{ name: 'user-1' }] }))
    expect(observer.getCurrentResult().data?.pages).toHaveLength(2)
    expect(view.socket).not.toHaveBeenCalled()
  })

  it.each(['success', 'PARTIAL', 'UNKNOWN'] as const)('discards an opaque static continuation chain after a scoped %s write and restarts at null', async (outcome) => {
    const view = world(); const page = pageObserver(view)
    await page.observer.refetch(); await page.observer.fetchNextPage()
    expect(page.observer.getCurrentResult().data?.pageParams).toEqual([null, '0:next'])
    view.commit(outcome === 'success' ? undefined : outcome)
    const pending = view.client.mutate('mutation{updateOther{id}}')
    if (outcome === 'success') await pending
    else await expect(pending).rejects.toBeInstanceOf(outcome === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)
    await vi.waitFor(() => {
      expect(page.observer.getCurrentResult().data?.pageParams).toEqual([null])
      expect(page.observer.getCurrentResult().data?.pages).toEqual([{ homeFeed: { items: [{ kind: 'revision-1', index: 1 }], [CONTINUATION_RESPONSE_KEY]: '1:next' } }])
    })
    expect(view.socket).not.toHaveBeenCalled()
  })

  it('fences a held obsolete page despite ignored abort, including repeated commits while a replacement is pending', async () => {
    const view = world(); const page = pageObserver(view)
    await page.observer.refetch()
    view.hold(true)
    const old = page.observer.fetchNextPage().catch(() => undefined)
    await vi.waitFor(() => expect(view.held).toHaveLength(1))
    await view.client.mutate('mutation{updateUser{id}}')
    await vi.waitFor(() => expect(view.held).toHaveLength(2))
    expect(view.held[0]!.init?.signal?.aborted).toBe(true)
    await view.client.mutate('mutation{updateUser{id}}')
    await vi.waitFor(() => expect(view.held).toHaveLength(3))
    expect(view.held[1]!.init?.signal?.aborted).toBe(true)
    view.hold(false)
    view.held[2]!.gate.resolve(view.held[2]!.body)
    await vi.waitFor(() => expect(page.observer.getCurrentResult().data?.pageParams).toEqual([null]))
    view.held[0]!.gate.resolve(view.held[0]!.body)
    view.held[1]!.gate.resolve(view.held[1]!.body)
    await old
    await vi.waitFor(() => expect(page.observer.getCurrentResult().data?.pages).toEqual([{ homeFeed: { items: [{ kind: 'revision-2', index: 1 }], [CONTINUATION_RESPONSE_KEY]: '2:next' } }]))
    expect(view.socket).not.toHaveBeenCalled()
  })

  it('fences an already-settled inactive static page before cache dispatch when cancellation is too late', async () => {
    const view = world()
    const options = view.client.staticContinuationQueryOptions(PAGE)
    const observer = new InfiniteQueryObserver(view.queryClient, options)
    await observer.refetch(); await observer.fetchNextPage()
    const query = view.queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
    let invalidating: Promise<void> | undefined
    let tooLate = false
    query.setOptions({ ...query.options, structuralSharing: (previous, next) => {
      if (invalidating === undefined) {
        invalidating = view.queryClient.invalidateQueries({ queryKey: options.queryKey, exact: true, refetchType: 'none' })
        tooLate = view.inits.at(-1)?.signal?.aborted === false
      }
      return options.structuralSharing(previous, next)
    } })
    await view.queryClient.refetchQueries({ queryKey: options.queryKey, exact: true, type: 'all' })
    await invalidating
    expect(tooLate).toBe(true)
    expect(query.state.data).toBeUndefined()
    expect(query.state.fetchStatus).toBe('idle')
    expect(view.socket).not.toHaveBeenCalled()
  })
})

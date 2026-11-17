import { InfiniteQueryObserver, QueryClient, QueryObserver, type InfiniteData } from '@tanstack/query-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DocumentAnalyzer, GolemClient, GolemError, LiveSync, RECOVERY_COOLDOWN_MS,
  type ClientManifest, type ContinuationPageParam, type Cursor, type ExecuteRequest, type LiveTransport, type StreamHandlers, type SubscriptionRequest,
} from '../src/index.js'

// No server fixture: this fake implements only the production LiveTransport
// contract and deliberately ignores HTTP aborts to exercise the commit fences.
const manifest = {
  formatVersion: 3, contract: 'recovery',
  models: {
    Post: { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: 'PostWhereUniqueInput', events: 'postEvents', relations: {}, computed: {} },
    User: { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: 'UserWhereUniqueInput', events: 'userEvents', relations: {}, computed: {} },
  },
  roots: {
    posts: { kind: 'generated', shape: 'rows', nullable: false, models: ['Post'] },
    searchPosts: { kind: 'opaque', shape: 'rows', nullable: false, models: ['Post'] },
    users: { kind: 'generated', shape: 'rows', nullable: false, models: ['User'] },
    feed: { kind: 'opaque', shape: 'object', nullable: false, models: [], result: { kind: 'object', name: 'Feed', nullable: false }, pagination: { kind: 'continuation', takeArgument: 'take', afterArgument: 'after', itemsField: 'items', nextField: 'next' } },
  },
  types: { Feed: { kind: 'object', fields: {
    items: { kind: 'list', nullable: false, maxItems: 25, element: { kind: 'model', name: 'Post', nullable: false } },
    next: { kind: 'scalar', name: 'String', nullable: true },
  } } },
} as const satisfies ClientManifest
const analyzer = new DocumentAnalyzer(manifest)
const LIST = '{ posts { title } }'
const data = (title: string) => ({ posts: [{ id: 'p', title, __typename: 'Post', _version: '1' }], users: [] })
const ended = (code = 'GOLEM_SUBSCRIPTION_RESYNC') => ({ kind: 'error' as const, errors: [new GolemError({ message: code, extensions: { code, reason: 'DEPENDENCY_CHANGED' } })] })
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

class RecoveryTransport implements LiveTransport {
  readonly streams: Array<{ handlers: StreamHandlers; stopped: boolean; request: SubscriptionRequest }> = []
  readonly calls: ExecuteRequest[] = []
  readonly pending: Array<ReturnType<typeof deferred<unknown>>> = []
  value: unknown = data('initial')
  respond: ((request: ExecuteRequest) => unknown) | undefined
  hold = false
  autoAck = false
  renews = 0

  async execute<T>(request: ExecuteRequest): Promise<T> {
    this.calls.push(request)
    if (!this.hold) return (this.respond?.(request) ?? this.value) as T
    const held = deferred<unknown>()
    this.pending.push(held)
    return await held.promise as T
  }

  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    const stream = { handlers, stopped: false, request }
    this.streams.push(stream)
    if (this.autoAck) queueMicrotask(() => { if (!stream.stopped) handlers.onAck() })
    return () => { stream.stopped = true }
  }

  renew(): void { this.renews++ }
}

const cleanups: Array<() => void> = []
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => {
  for (const cleanup of cleanups.splice(0).reverse()) cleanup()
  vi.useRealTimers()
})
const flush = () => vi.advanceTimersByTimeAsync(0)

function mount(document = LIST, scheduling: { windowMs?: number; maxWaitMs?: number } = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
  const transport = new RecoveryTransport()
  const live = new LiveSync({ queryClient, transport, manifest, clock: () => Date.now(), random: () => 0, ...scheduling })
  live.start()
  const options = live.options(analyzer.analyze(document))
  const observer = new QueryObserver(queryClient, options)
  const unobserve = observer.subscribe(() => undefined)
  const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
  cleanups.push(() => queryClient.clear(), () => live.stop(), unobserve)
  return { queryClient, transport, live, query, observer, unobserve }
}
async function ready(view: ReturnType<typeof mount>) {
  for (const stream of view.transport.streams) stream.handlers.onAck()
  await flush()
  expect(view.query.state.data).toEqual(data('initial'))
  expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
}

describe('recovery coordinated across replacement streams', () => {
  it('bounds 1000 relevant writes, reopens during perpetual writes, and eventually refreshes', async () => {
    const view = mount()
    await ready(view)
    const { transport, live, query } = view
    transport.autoAck = true
    const began = Date.now()
    for (let write = 0; write < 1000; write++) {
      const current = transport.streams.at(-1)!
      if (!current.stopped) current.handlers.onEnd(ended())
      transport.value = data(`write-${write}`)
      await vi.advanceTimersByTimeAsync(1)
    }
    // A trailing debounce would never reopen within this continuous workload.
    expect(transport.streams.length).toBeGreaterThan(3)
    expect(transport.streams.length).toBeLessThanOrEqual(5)
    expect(transport.calls.length).toBeLessThanOrEqual(5)
    // At the last fixed deadline the replacement can acknowledge and commit
    // immediately, even though ordinary event coalescing has a trailing edge.
    expect(query.state.data).toEqual(data('write-999'))
    expect(live.statusOf(query)?.state).toBe('LIVE')
    await vi.advanceTimersByTimeAsync(800)
    expect(query.state.data).toEqual(data('write-999'))
    expect(live.statusOf(query)?.state).toBe('LIVE')
    expect(transport.streams.length).toBeLessThanOrEqual(6)
    expect(transport.calls.length).toBeLessThanOrEqual(6)
    expect(Date.now() - began).toBe(1800)
  })

  it('shares the deadline across simultaneous streams and waits for all replacement acknowledgements', async () => {
    const view = mount('{ posts { title } users { name } }')
    await ready(view)
    const { transport, query, live } = view
    transport.streams[0]!.handlers.onEnd(ended())
    transport.streams[1]!.handlers.onEnd(ended())
    expect(query.state.data).toBeUndefined()
    await vi.advanceTimersByTimeAsync(RECOVERY_COOLDOWN_MS - 1)
    expect(transport.streams).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(transport.streams).toHaveLength(4)
    transport.streams[2]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(500)
    expect(transport.calls).toHaveLength(1)
    transport.hold = true
    transport.streams[3]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(transport.calls).toHaveLength(2)
    expect(live.statusOf(query)?.state).toBe('RECOVERING')
    transport.pending[0]!.resolve(data('fresh'))
    await flush()
    expect(query.state.data).toEqual(data('fresh'))
    expect(live.statusOf(query)?.state).toBe('LIVE')
  })

  it('refetches after cooldown and every ack without waiting for a 60-second event coalescer', async () => {
    const view = mount('{ posts { title } users { name } }', { windowMs: 60_000, maxWaitMs: 60_000 })
    await ready(view)
    view.transport.streams[0]!.handlers.onEnd(ended())
    view.transport.streams[1]!.handlers.onEnd(ended())
    await vi.advanceTimersByTimeAsync(RECOVERY_COOLDOWN_MS)
    view.transport.streams[2]!.handlers.onAck()
    await flush()
    expect(view.transport.calls).toHaveLength(1)
    view.transport.hold = true
    view.transport.streams[3]!.handlers.onAck()
    await flush()
    expect(view.transport.calls).toHaveLength(2)
    expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
    view.transport.pending[0]!.resolve(data('recovered'))
    await flush()
    expect(view.query.state.data).toEqual(data('recovered'))
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
    // An ordinary CREATED still uses exactly the configured event coalescer.
    view.transport.value = data('recovered')
    view.transport.hold = false
    view.transport.streams[2]!.handlers.onEvent({ data: { postEvents: { id: 'new', version: '1', type: 'CREATED', entity: null } } })
    await vi.advanceTimersByTimeAsync(59_999)
    expect(view.transport.calls).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(view.transport.calls).toHaveLength(3)
  })

  it('keeps the first recovery deadline when a second stream ends later', async () => {
    const view = mount('{ posts { title } users { name } }')
    await ready(view)
    view.transport.streams[0]!.handlers.onEnd(ended())
    await vi.advanceTimersByTimeAsync(200)
    view.transport.streams[1]!.handlers.onEnd(ended())
    await vi.advanceTimersByTimeAsync(RECOVERY_COOLDOWN_MS - 200)
    // The second end joins the original deadline, not a new trailing delay.
    expect(view.transport.streams).toHaveLength(4)
    view.transport.value = data('after-staggered-ends')
    view.transport.streams[2]!.handlers.onAck()
    view.transport.streams[3]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(view.transport.calls).toHaveLength(2)
    expect(view.query.state.data).toEqual(data('after-staggered-ends'))
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('clears immediately and fences an abort-ignoring response from an old query generation', async () => {
    const view = mount()
    await ready(view)
    const { queryClient, transport, query, live } = view
    transport.hold = true
    void queryClient.refetchQueries({ queryKey: query.queryKey, exact: true })
    await flush()
    expect(transport.pending).toHaveLength(1)
    transport.streams[0]!.handlers.onEnd(ended())
    expect(query.state.data).toBeUndefined()
    expect(transport.calls[1]!.signal?.aborted).toBe(true)
    transport.pending[0]!.resolve(data('revoked'))
    await flush()
    expect(query.state.data).toBeUndefined()
    await vi.advanceTimersByTimeAsync(RECOVERY_COOLDOWN_MS)
    transport.streams[1]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(live.statusOf(query)?.state).toBe('RECOVERING')
    transport.pending[1]!.resolve(data('authorized'))
    await flush()
    expect(query.state.data).toEqual(data('authorized'))
    expect(live.statusOf(query)?.state).toBe('LIVE')
    // Even a late callback from the stopped stream cannot invalidate/patch it.
    transport.streams[0]!.handlers.onEnd(ended())
    expect(query.state.data).toEqual(data('authorized'))
  })

  it('fences the TanStack settlement-to-commit gap before any observer receives stale data', async () => {
    const view = mount()
    await ready(view)
    const seen: unknown[] = []
    const stop = view.observer.subscribe((result) => { seen.push(result.data) })
    cleanups.push(stop)
    let intercept = true
    let cancellationWasTooLate = false
    view.query.setOptions({ ...view.query.options, structuralSharing: (previous, next) => {
      if (intercept) {
        intercept = false
        // This is Query.setData's real pre-dispatch callback. Its retryer has
        // already resolved, so cancelling alone cannot suppress the commit.
        view.transport.streams[0]!.handlers.onEnd(ended())
        cancellationWasTooLate = view.transport.calls.at(-1)!.signal?.aborted === false
      }
      return view.live.structuralSharing(previous, next)
    } })
    view.transport.value = data('must-not-commit')
    await view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    expect(cancellationWasTooLate).toBe(true)
    expect(view.query.state.data).toBeUndefined()
    expect(view.query.state.fetchStatus).toBe('idle')
    expect(JSON.stringify(seen)).not.toContain('must-not-commit')
    expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
  })

  it('fences an ordinary EXITED during settlement while preserving other rows and normal refill', async () => {
    const view = mount()
    view.transport.value = { posts: [...data('initial').posts, { id: 'other', title: 'unaffected', _version: '1', __typename: 'Post' }], users: [] }
    view.transport.streams[0]!.handlers.onAck()
    await flush()
    let intercept = true
    const seen: unknown[] = []
    cleanups.push(view.observer.subscribe((result) => { seen.push(result.data) }))
    view.query.setOptions({ ...view.query.options, structuralSharing: (previous, next) => {
      if (intercept) {
        intercept = false
        view.transport.streams[0]!.handlers.onEvent({ data: { postEvents: { id: 'p', version: '2', type: 'EXITED', entity: null } } })
      }
      return view.live.structuralSharing(previous, next)
    } })
    view.transport.value = data('must-not-reinstate')
    await view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    const remaining = { posts: [{ id: 'other', title: 'unaffected', _version: '1', __typename: 'Post' }], users: [] }
    expect(view.query.state.data).toEqual(remaining)
    expect(view.query.state.fetchStatus).toBe('idle')
    expect(JSON.stringify(seen)).not.toContain('must-not-reinstate')
    expect(view.transport.streams).toHaveLength(1)
    view.transport.value = remaining
    await vi.advanceTimersByTimeAsync(50)
    expect(view.query.state.data).toEqual(remaining)
    expect(view.query.state.fetchStatus).toBe('idle')
    expect(view.transport.calls).toHaveLength(3)
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it.each(['reset', 'dispose'] as const)('does not write old neighbours when a landing removal synchronously triggers %s', async (boundary) => {
    const view = mount()
    view.transport.value = { posts: [...data('initial').posts, { id: 'other', title: 'old-neighbour', _version: '1', __typename: 'Post' }], users: [] }
    view.transport.streams[0]!.handlers.onAck()
    await flush()
    let intercept = true
    let triggered = false
    let reset: Promise<void> | undefined
    const afterBoundary: unknown[] = []
    const stop = view.queryClient.getQueryCache().subscribe((event) => {
      if (triggered && event.type === 'updated') afterBoundary.push(event.query.state.data)
      if (triggered || intercept || event.type !== 'updated' || event.action.type !== 'setState' || event.query.state.fetchStatus !== 'idle') return
      triggered = true
      if (boundary === 'reset') reset = view.live.resetSession()
      else view.live.dispose()
    })
    cleanups.push(stop)
    view.query.setOptions({ ...view.query.options, structuralSharing: (previous, next) => {
      if (intercept) {
        intercept = false
        view.transport.streams[0]!.handlers.onEvent({ data: { postEvents: { id: 'p', version: '2', type: 'DELETED', entity: null } } })
      }
      return view.live.structuralSharing(previous, next)
    } })
    await view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await reset
    expect(triggered).toBe(true)
    expect(view.query.state.data).toBeUndefined()
    expect(afterBoundary.every((value) => value === undefined)).toBe(true)
  })

  it('immediately purges forbidden data without waiting for cooldown or reopening', async () => {
    const view = mount()
    await ready(view)
    view.transport.hold = true
    void view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await flush()
    view.transport.streams[0]!.handlers.onEnd(ended('GOLEM_SUBSCRIPTION_FORBIDDEN'))
    expect(view.query.state.data).toBeUndefined()
    expect(view.live.statusOf(view.query)?.state).toBe('FORBIDDEN')
    view.transport.pending[0]!.resolve(data('revoked'))
    await vi.advanceTimersByTimeAsync(10_000)
    expect(view.query.state.data).toBeUndefined()
    expect(view.transport.streams).toHaveLength(1)
    expect(view.transport.calls).toHaveLength(2)
  })

  it('invalidates immediately on network interruption and retains the ack-before-refetch gate', async () => {
    const view = mount()
    await ready(view)
    view.transport.streams[0]!.handlers.onInterrupted()
    expect(view.query.state.data).toBeUndefined()
    expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
    await vi.advanceTimersByTimeAsync(5000)
    expect(view.transport.calls).toHaveLength(1)
    view.transport.value = data('after-network')
    view.transport.streams[0]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(view.query.state.data).toEqual(data('after-network'))
    expect(view.transport.calls).toHaveLength(2)
  })

  it('hides uncertain derived rows immediately on EXITED and stays recovering until a fresh commit', async () => {
    const view = mount('{ searchPosts { title } }')
    view.transport.value = { searchPosts: data('old-derived').posts }
    view.transport.streams[0]!.handlers.onAck()
    await flush()
    view.transport.hold = true
    view.transport.streams[0]!.handlers.onEvent({ data: { postEvents: { id: 'p', version: '2', type: 'EXITED', entity: null } } })
    expect(view.query.state.data).toBeUndefined()
    expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
    await vi.advanceTimersByTimeAsync(50)
    expect(view.transport.streams).toHaveLength(1)
    view.transport.pending[0]!.resolve({ searchPosts: [] })
    await flush()
    expect(view.query.state.data).toEqual({ searchPosts: [] })
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('keeps ordinary generated row patching and coalescing outside recovery', async () => {
    const view = mount()
    await ready(view)
    const stream = view.transport.streams[0]!
    stream.handlers.onEvent({ data: { postEvents: { id: 'p', version: '2', type: 'UPDATED', entity: { id: 'p', title: 'patched', _version: '2', __typename: 'Post' } } } })
    expect(view.query.state.data).toEqual({ posts: [{ id: 'p', title: 'patched', _version: '2', __typename: 'Post' }], users: [] })
    view.transport.value = { posts: [{ id: 'p', title: 'after-coalesced-creates', _version: '3', __typename: 'Post' }], users: [] }
    for (let write = 0; write < 1000; write++) {
      stream.handlers.onEvent({ data: { postEvents: { id: `p${write}`, version: '1', type: 'CREATED', entity: null } } })
      await vi.advanceTimersByTimeAsync(1)
    }
    await vi.advanceTimersByTimeAsync(50)
    expect(view.transport.calls.length).toBeLessThanOrEqual(4)
    expect(view.query.state.data).toEqual(view.transport.value)
  })

  it('closes pending recovery on unmount', async () => {
    const view = mount()
    await ready(view)
    view.transport.streams[0]!.handlers.onEnd(ended())
    view.unobserve()
    await vi.advanceTimersByTimeAsync(1000)
    expect(view.live.statusOf(view.query)).toBeUndefined()
    const opened = view.transport.streams.length
    await vi.advanceTimersByTimeAsync(10_000)
    expect(view.transport.streams).toHaveLength(opened)
    view.queryClient.removeQueries({ queryKey: view.query.queryKey, exact: true })
    expect(view.queryClient.getQueryData(view.query.queryKey)).toBeUndefined()
  })

  it('fences held reads when a query is removed and cancels all scheduled work on stop', async () => {
    const view = mount()
    await ready(view)
    view.transport.hold = true
    void view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await flush()
    view.queryClient.removeQueries({ queryKey: view.query.queryKey, exact: true })
    expect(view.live.statusOf(view.query)).toBeUndefined()
    expect(view.transport.calls[1]!.signal?.aborted).toBe(true)
    view.transport.pending[0]!.resolve(data('late-removed'))
    await flush()
    view.live.stop()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(view.queryClient.getQueryData(view.query.queryKey)).toBeUndefined()
    expect(view.transport.streams).toHaveLength(1)
  })

  it('fences the whole session immediately at credential expiry and awaits fresh stream acknowledgements', async () => {
    const view = mount()
    await ready(view)
    view.queryClient.setQueryData(['golem', 'static', 'inactive'], data('expired-static'))
    view.transport.hold = true
    void view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await flush()
    view.transport.streams[0]!.handlers.onEnd({ kind: 'reauthenticate' })
    expect(view.query.state.data).toBeUndefined()
    expect(view.queryClient.getQueryData(['golem', 'static', 'inactive'])).toBeUndefined()
    expect(view.transport.renews).toBe(1)
    expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
    await vi.advanceTimersByTimeAsync(RECOVERY_COOLDOWN_MS)
    view.transport.streams[1]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(view.transport.pending).toHaveLength(2)
    view.transport.pending[0]!.resolve(data('expired-owner'))
    await flush()
    expect(view.query.state.data).toBeUndefined()
    view.transport.pending[1]!.resolve(data('fresh-credentials'))
    await flush()
    expect(view.query.state.data).toEqual(data('fresh-credentials'))
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('preserves accepted neighbouring pages while fencing a removal during infinite-query settlement', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
    const transport = new RecoveryTransport()
    let removed = false
    transport.respond = (request) => {
      const cursor = (request.variables?.golemCursor as { ID?: string } | undefined)?.ID
      const id = cursor === undefined ? 'a' : cursor === 'a' && !removed ? 'b' : 'c'
      return { posts: [{ id, title: id, _version: '1', __typename: 'Post' }] }
    }
    const live = new LiveSync({ queryClient, transport, manifest, clock: () => Date.now(), random: () => 0 })
    live.start()
    const options = live.infiniteOptions(analyzer.analyze('{ posts(take: 1) { title } }'))
    const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, Cursor | null>, readonly unknown[], Cursor | null>(queryClient, options)
    const stop = observer.subscribe(() => undefined)
    cleanups.push(() => queryClient.clear(), () => live.dispose(), stop)
    const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
    transport.streams[0]!.handlers.onAck()
    await flush()
    await observer.fetchNextPage()
    const cached = query.state.data as InfiniteData<unknown>
    expect(cached.pages).toHaveLength(2)
    let intercept = true
    observer.setOptions({ ...options, structuralSharing: (previous, next) => {
      if (intercept) {
        intercept = false
        removed = true
        transport.streams[0]!.handlers.onEvent({ data: { postEvents: { id: 'b', version: '2', type: 'DELETED', entity: null } } })
      }
      return live.structuralSharing(previous, next)
    } })
    await observer.fetchNextPage()
    expect(intercept).toBe(false)
    const remaining = query.state.data as InfiniteData<{ posts: Array<{ id: string }> }>
    expect(remaining.pages.map((page) => page.posts.map((row) => row.id))).toEqual([['a'], []])
    expect(remaining.pages[0]).toBe(cached.pages[0])
    expect(query.state.fetchStatus).toBe('idle')
    await vi.advanceTimersByTimeAsync(50)
    expect((query.state.data as typeof remaining).pages.map((page) => page.posts.map((row) => row.id))).toEqual([['a'], ['c']])
    expect(live.statusOf(query)?.state).toBe('LIVE')
  })

  it('refreshes shared page provenance after closing and reopening a live entry', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
    const transport = new RecoveryTransport()
    transport.respond = (request) => {
      const cursor = (request.variables?.golemCursor as { ID?: string } | undefined)?.ID
      const id = cursor === undefined ? 'a' : cursor === 'a' ? 'b' : 'c'
      return { posts: [{ id, title: id, _version: '1', __typename: 'Post' }] }
    }
    const live = new LiveSync({ queryClient, transport, manifest, clock: () => Date.now(), random: () => 0 })
    live.start()
    const options = live.infiniteOptions(analyzer.analyze('{ posts(take: 1) { title } }'))
    const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, Cursor | null>, readonly unknown[], Cursor | null>(queryClient, options)
    const stop = observer.subscribe(() => undefined)
    cleanups.push(() => queryClient.clear(), () => live.dispose(), stop)
    const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
    transport.streams[0]!.handlers.onAck()
    await flush()
    await observer.fetchNextPage()
    const cached = query.state.data as InfiniteData<unknown, Cursor | null>
    expect(cached.pages).toHaveLength(2)
    live.stop()
    live.start()
    transport.streams[1]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    // Identical authenticated data keeps ordinary structural sharing intact.
    expect(query.state.data).toBe(cached)
    expect((query.state.data as InfiniteData<unknown>).pages[0]).toBe(cached.pages[0])
    expect(live.statusOf(query)?.state).toBe('LIVE')
    await observer.fetchNextPage()
    expect((query.state.data as InfiniteData<unknown>).pages).toHaveLength(3)
    expect((query.state.data as InfiniteData<unknown, Cursor | null>).pageParams).toEqual([null, { ID: 'a' }, { ID: 'b' }])
    expect(query.state.fetchStatus).toBe('idle')
    expect(live.statusOf(query)?.state).toBe('LIVE')
  })

  it('resets live principal snapshots and clears inactive cached Golem queries without touching unrelated data', async () => {
    const view = mount()
    await ready(view)
    view.queryClient.setQueryData(['golem', 'static', 'inactive'], data('old-inactive'))
    view.queryClient.setQueryData(['application', 'unrelated'], { keep: true })
    const reset = view.live.resetSession()
    expect(view.query.state.data).toBeUndefined()
    expect(view.queryClient.getQueryData(['golem', 'static', 'inactive'])).toBeUndefined()
    expect(view.queryClient.getQueryData(['application', 'unrelated'])).toEqual({ keep: true })
    await reset
    expect(view.transport.renews).toBe(1)
    expect(view.transport.streams[0]!.stopped).toBe(true)
    expect(view.transport.streams).toHaveLength(2)
    expect(view.transport.calls).toHaveLength(1)
    view.transport.value = data('new-principal')
    view.transport.streams[1]!.handlers.onAck()
    await vi.advanceTimersByTimeAsync(50)
    expect(view.query.state.data).toEqual(data('new-principal'))
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })
})

describe('public session reset for static and continuation reads', () => {
  it('purges Golem data on disposal before another client reuses the QueryClient', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
    let owned = new GolemClient({ queryClient, manifest, url: 'http://owner.test/graphql', fetch: async () => new Response(JSON.stringify({ data: data('owner-secret') })) })
    cleanups.push(() => queryClient.clear(), () => owned.dispose())
    const first = owned.staticQueryOptions(LIST)
    await queryClient.fetchQuery(first)
    queryClient.setQueryData(['application', 'keep'], { keep: true })
    owned.dispose()
    expect(queryClient.getQueryData(first.queryKey)).toBeUndefined()
    expect(queryClient.getQueryData(['application', 'keep'])).toEqual({ keep: true })
    owned = new GolemClient({ queryClient, manifest, url: 'http://member.test/graphql', fetch: async () => new Response(JSON.stringify({ data: data('member') })) })
    const second = owned.staticQueryOptions(LIST)
    expect(second.queryKey).toEqual(first.queryKey)
    expect(queryClient.getQueryData(second.queryKey)).toBeUndefined()
    await queryClient.fetchQuery(second)
    expect(queryClient.getQueryData(second.queryKey)).toEqual(data('member'))
  })

  it('preserves an independent static page chain across live attach/detach', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
    const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
      const variables = JSON.parse(String(init?.body)).variables
      const id = variables.golemAfter === undefined ? 'a' : variables.golemAfter === 'a-next' ? 'b' : 'c'
      return new Response(JSON.stringify({ data: { feed: { items: [{ title: id }], _golemNext: id === 'c' ? null : `${id}-next` } } }))
    })
    const client = new GolemClient({ queryClient, manifest, url: 'http://detach.test/graphql', fetch })
    const options = client.staticContinuationQueryOptions('{ feed(take: 1) { items { title } } }')
    const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>(queryClient, options)
    const stop = observer.subscribe(() => undefined)
    cleanups.push(() => queryClient.clear(), () => client.dispose(), stop)
    await flush()
    await observer.fetchNextPage()
    const detach = client.attach()
    detach()
    expect(client.live.started).toBe(false)
    const reattach = client.attach()
    cleanups.push(reattach)
    await observer.fetchNextPage()
    const result = observer.getCurrentResult()
    expect(result.data?.pages).toHaveLength(3)
    expect(result.data?.pageParams).toEqual([null, 'a-next', 'b-next'])
    expect(result.hasNextPage).toBe(false)
    expect(result.fetchStatus).toBe('idle')
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it('also fences a static result already resolved by TanStack before reset', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
    let title = 'owner'
    const client = new GolemClient({ queryClient, manifest, url: 'http://reset.test/graphql', fetch: async () => new Response(JSON.stringify({ data: data(title) })) })
    cleanups.push(() => queryClient.clear(), () => client.dispose())
    const options = client.staticQueryOptions(LIST)
    await queryClient.fetchQuery(options)
    const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
    let resetting: Promise<void> | undefined
    query.setOptions({ ...query.options, structuralSharing: (previous, next) => {
      if (resetting === undefined) resetting = client.resetSession()
      return options.structuralSharing(previous, next)
    } })
    title = 'old-owner-settlement'
    await queryClient.refetchQueries({ queryKey: query.queryKey, exact: true })
    await resetting
    expect(query.state.data).toBeUndefined()
  })

  it('starts a new page chain and fences old HTTP responses, including direct query functions', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
    const held: Array<{ finish: ReturnType<typeof deferred<Response>>; init: RequestInit | undefined }> = []
    const requests: Array<{ principal: string; variables: Record<string, unknown> }> = []
    let principal = 'owner'
    let hold = false
    const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
      const payload = JSON.parse(String(init?.body))
      requests.push({ principal, variables: payload.variables })
      if (hold) {
        const finish = deferred<Response>()
        held.push({ finish, init })
        return finish.promise
      }
      return new Response(JSON.stringify({ data: { feed: { items: [{ title: principal }], _golemNext: payload.variables.golemAfter === undefined ? `${principal}-next` : null } } }))
    })
    const socket = vi.fn(() => { throw new Error('static reset opened a socket') })
    const client = new GolemClient({ queryClient, manifest, url: 'http://recovery.test/graphql', credentials: () => ({ headers: { authorization: principal } }), fetch, createSocket: socket })
    const options = client.staticContinuationQueryOptions('{ feed(take: 1) { items { title } } }')
    const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>(queryClient, options)
    const stop = observer.subscribe(() => undefined)
    cleanups.push(() => queryClient.clear(), () => client.dispose(), stop)
    await flush()
    await observer.fetchNextPage()
    const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })!
    expect((query.state.data as { pageParams: unknown[] }).pageParams).toEqual([null, 'owner-next'])
    queryClient.setQueryData(['golem', 'static', 'inactive'], { secret: true })
    queryClient.setQueryData(['other'], { preserve: true })
    hold = true
    const oldDirect = options.queryFn({ pageParam: 'owner-next', signal: new AbortController().signal })
    const oldRejected = expect(oldDirect).rejects.toBeDefined()
    void observer.refetch()
    await flush()
    expect(held).toHaveLength(2)
    principal = 'member'
    hold = false
    const resetting = client.resetSession()
    expect(query.state.data).toBeUndefined()
    expect(queryClient.getQueryData(['golem', 'static', 'inactive'])).toBeUndefined()
    expect(queryClient.getQueryData(['other'])).toEqual({ preserve: true })
    await resetting
    await flush()
    expect((query.state.data as { pageParams: unknown[] }).pageParams).toEqual([null])
    expect(requests.at(-1)).toEqual({ principal: 'member', variables: {} })
    expect(held[1]!.init?.signal?.aborted).toBe(true)
    for (const item of held) item.finish.resolve(new Response(JSON.stringify({ data: { feed: { items: [{ title: 'revoked-owner' }], _golemNext: 'owner-next' } } })))
    await oldRejected
    await flush()
    expect(JSON.stringify(query.state.data)).not.toContain('revoked-owner')
    expect((query.state.data as { pages: unknown[] }).pages).toHaveLength(1)
    expect(socket).not.toHaveBeenCalled()
    // Logout uses the same explicit lifecycle. Already cached owner/member
    // rows disappear synchronously even if the next credential request fails.
    principal = 'logged-out'
    hold = true
    const logout = client.resetSession()
    expect(query.state.data).toBeUndefined()
    await logout
  })
})

import { CancelledError, replaceEqualDeep, type Query, type QueryCacheNotifyEvent, type QueryClient, type QueryFunctionContext } from '@tanstack/query-core'
import { assertLive, streamRequest, type DocumentAnalysis, type StaticDocumentAnalysis, type LivePlan, type StreamPlan } from './analysis.js'
import { Coalescer, COALESCE_MAX_WAIT_MS, COALESCE_WINDOW_MS } from './coalescer.js'
import { GolemNotFoundError, GolemProtocolError, GolemError } from './errors.js'
import type { ClientManifest } from './manifest.js'
import { applyRemoval, decideUpdate, EMPTY_MERGE_STATE, noteChange, parseVersion, resolveResponse, type MergeState } from './merge.js'
import { backoffDelay } from './protocol.js'
import { golemContinuationQuery, golemInfiniteQuery, golemLiveContinuationQuery, type ContinuationPageParam, type Cursor, type PageSpec } from './pagination.js'
import { Patcher, type EventIdentity, type RowKey } from './patcher.js'
import type { StreamEnd, StreamHandlers, SubscriptionRequest } from './session.js'
import { sessionState, type SessionState } from './session-state.js'
import type { ExecuteRequest } from './transport.js'

export const CLOSE_GRACE_MS = 1000
export const STABLE_STREAM_MS = 10_000
// A fixed leading deadline survives replacement streams. Unlike a trailing
// debounce, sustained writes cannot postpone reopening forever. These values
// are provisional pending real workload measurement; normal network backoff
// remains separate. Once replacement acknowledgements arrive, recovery fetches
// immediately: the ordinary event coalescer cannot extend this 300 ms bound
// (excluding acknowledgement/network time).
export const RECOVERY_COOLDOWN_MS = 250
export const RECOVERY_JITTER_MS = 50

export const SubscriptionCode = {
  Cancelled: 'GOLEM_SUBSCRIPTION_CANCELLED',
  Resync: 'GOLEM_SUBSCRIPTION_RESYNC',
  Overflow: 'GOLEM_SUBSCRIPTION_OVERFLOW',
  Revalidation: 'GOLEM_SUBSCRIPTION_REVALIDATION',
  Forbidden: 'GOLEM_SUBSCRIPTION_FORBIDDEN',
  Limit: 'GOLEM_SUBSCRIPTION_LIMIT',
  SourceClosed: 'GOLEM_SUBSCRIPTION_SOURCE_CLOSED',
} as const

export const REASON_DEPENDENCY_CHANGED = 'DEPENDENCY_CHANGED'

export type LiveState = 'CONNECTING' | 'LIVE' | 'RECOVERING' | 'LIMIT' | 'FORBIDDEN' | 'NOT_FOUND' | 'FAILED'

export interface LiveStatus {
  readonly state: LiveState
  readonly error: Error | undefined
}

export interface GolemQueryMeta {
  readonly mode?: never
  readonly invalidate: (query: Query) => void
  readonly analysis: DocumentAnalysis
  readonly variables: Readonly<Record<string, unknown>>
  readonly pagination?: PageSpec
  readonly continuation?: true
}

export interface GolemStaticQueryMeta {
  readonly mode: 'static'
  readonly invalidate: (query: Query) => void
  readonly analysis: DocumentAnalysis | StaticDocumentAnalysis
  readonly variables: Readonly<Record<string, unknown>>
  readonly continuation?: true
}

export interface LiveTransport {
  execute<T = unknown>(request: ExecuteRequest): Promise<T>
  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void
  renew(): void
}

export interface LiveSyncOptions {
  readonly queryClient: QueryClient
  readonly transport: LiveTransport
  readonly manifest: ClientManifest
  readonly clock?: () => number
  readonly closeGraceMs?: number
  readonly windowMs?: number
  readonly maxWaitMs?: number
  readonly random?: () => number
}

type StreamPhase = 'opening' | 'live' | 'limit' | 'forbidden' | 'failed' | 'closed'

interface StreamState {
  readonly plan: LivePlan
  phase: StreamPhase
  acked: boolean
  ackedAt: number
  attempt: number
  generation: number
  stop: (() => void) | undefined
  timer: ReturnType<typeof setTimeout> | undefined
}

interface QueryRecovery { generation: number }

interface Entry {
  readonly query: Query
  readonly meta: GolemQueryMeta
  readonly streams: StreamState[]
  readonly coalescer: Coalescer
  readonly waiters: Set<() => void>
  merge: MergeState
  inFlight: number
  derivedStale: boolean
  outage: boolean
  readonly recovery: QueryRecovery
  acceptedGeneration: number | undefined
  recoveryAt: number
  everLive: boolean
  failure: Error | undefined
  notFound: GolemNotFoundError | undefined
  closeTimer: ReturnType<typeof setTimeout> | undefined
  closed: boolean
}

interface ReadStamp {
  readonly auth: object
  readonly recovery?: QueryRecovery
  readonly generation?: number
}

interface EventFrame {
  readonly version: string
  readonly type: string
  readonly id: EventIdentity
  readonly entity: Record<string, unknown> | null
}

export function readGolemMeta(query: Query): GolemQueryMeta | GolemStaticQueryMeta | undefined {
  const meta = query.meta?.golem
  if (typeof meta !== 'object' || meta === null || !('invalidate' in meta) || typeof meta.invalidate !== 'function') return undefined
  return meta as GolemQueryMeta | GolemStaticQueryMeta
}

export function readMeta(query: Query): GolemQueryMeta | undefined {
  const meta = readGolemMeta(query)
  return meta?.mode === 'static' ? undefined : meta
}

function frameOf(plan: StreamPlan, data: unknown): EventFrame {
  const frame = typeof data === 'object' && data !== null ? (data as Record<string, unknown>)[plan.eventsRoot] : undefined
  if (typeof frame !== 'object' || frame === null) {
    throw new GolemProtocolError(`an event frame for ${plan.eventsRoot} carried no ${plan.eventsRoot} object`)
  }
  const { version, type, id, entity } = frame as Record<string, unknown>
  if (typeof version !== 'string' || typeof type !== 'string' || !(typeof id === 'string' || typeof id === 'number' || (typeof id === 'object' && id !== null))) {
    throw new GolemProtocolError(`an event frame for ${plan.eventsRoot} lacks version, type or id`)
  }
  return { version, type, id: id as EventIdentity, entity: typeof entity === 'object' && entity !== null ? (entity as Record<string, unknown>) : null }
}

const SAME_STATUS = (left: LiveStatus, right: LiveStatus): boolean => left.state === right.state && left.error === right.error

export class LiveSync {
  private readonly client: QueryClient
  private readonly transport: LiveTransport
  private readonly patcher: Patcher
  private readonly manifest: ClientManifest
  private readonly clock: () => number
  private readonly closeGraceMs: number
  private readonly windowMs: number
  private readonly maxWaitMs: number
  private readonly random: () => number
  private readonly entries = new Map<string, Entry>()
  private statuses = new Map<string, LiveStatus>()
  private readonly listeners = new Set<() => void>()
  private unsubscribe: (() => void) | undefined
  private readonly stopInvalidations: () => void
  private readonly state: SessionState
  private get authGeneration(): object { return this.state.generation }
  private resetWork: Promise<void> | undefined
  private readonly reads = new WeakMap<object, ReadStamp>()
  private readonly queryRecoveries = new WeakMap<Query, QueryRecovery>()

  private queryRecovery(query: Query): QueryRecovery {
    let recovery = this.queryRecoveries.get(query)
    if (recovery === undefined) { recovery = { generation: 0 }; this.queryRecoveries.set(query, recovery) }
    return recovery
  }

  // TanStack invokes structuralSharing immediately before its success dispatch.
  // This also fences the gap after retryer resolution, when cancel() can no
  // longer prevent a queued success. Untagged manual cache writes remain valid.
  readonly structuralSharing = (previous: unknown, next: unknown): unknown => {
    this.assertCurrentRead(next, previous)
    const shared = replaceEqualDeep(previous, next)
    this.transferReadStamp(next, shared)
    return shared
  }

  private assertCurrentRead(value: unknown, previous: unknown): void {
    const accepted = new Set<unknown>([previous])
    if (typeof previous === 'object' && previous !== null && 'pages' in previous && Array.isArray(previous.pages) && 'pageParams' in previous && Array.isArray(previous.pageParams)) {
      for (const page of previous.pages) accepted.add(page)
    }
    const check = (candidate: unknown): boolean => {
      if (typeof candidate !== 'object' || candidate === null) return false
      const stamp = this.reads.get(candidate)
      if (stamp === undefined) return false
      // Row removal can fence a pending response while retaining accepted
      // neighbours. Only exact objects still in the cache may retain their
      // older recovery stamp; authentication is always checked.
      if (stamp.auth !== this.authGeneration || stamp.recovery !== undefined && stamp.generation !== stamp.recovery.generation && !accepted.has(candidate)) {
        throw new CancelledError({ silent: true })
      }
      return true
    }
    if (check(value) || typeof value !== 'object' || value === null) return
    // InfiniteQueryBehavior owns the outer container; our query function owns
    // each page. Inspect that single layer, never arbitrary DTO fields/cycles.
    if ('pages' in value && Array.isArray(value.pages) && 'pageParams' in value && Array.isArray(value.pageParams)) {
      for (const page of value.pages) check(page)
    }
  }

  private transferReadStamp(next: unknown, shared: unknown): void {
    const transfer = (source: unknown, target: unknown): boolean => {
      if (typeof source !== 'object' || source === null || typeof target !== 'object' || target === null) return false
      const stamp = this.reads.get(source)
      if (stamp === undefined) {
        // An equal manual cache write has no coordinator provenance; it must
        // not inherit a stale stamp solely through structural sharing either.
        this.reads.delete(target)
        return false
      }
      // replaceEqualDeep proves the returned value equals this validated input.
      // Reusing the old container must not reuse its old authorization stamp.
      this.reads.set(target, stamp)
      return true
    }
    if (transfer(next, shared) || typeof next !== 'object' || next === null || typeof shared !== 'object' || shared === null) return
    if ('pages' in next && Array.isArray(next.pages) && 'pageParams' in next && Array.isArray(next.pageParams) && 'pages' in shared && Array.isArray(shared.pages)) {
      for (let index = 0; index < next.pages.length; index++) transfer(next.pages[index], shared.pages[index])
    }
  }

  private stampRead(value: unknown, recovery?: QueryRecovery): void {
    if (typeof value === 'object' && value !== null) {
      this.reads.set(value, { auth: this.authGeneration, recovery, generation: recovery?.generation })
    }
  }

  constructor(options: LiveSyncOptions) {
    this.client = options.queryClient
    this.transport = options.transport
    this.state = sessionState(options.transport, options.manifest)
    this.manifest = options.manifest
    this.patcher = new Patcher(options.manifest)
    this.clock = options.clock ?? (() => performance.now())
    this.closeGraceMs = options.closeGraceMs ?? CLOSE_GRACE_MS
    this.windowMs = options.windowMs ?? COALESCE_WINDOW_MS
    this.maxWaitMs = options.maxWaitMs ?? COALESCE_MAX_WAIT_MS
    this.random = options.random ?? Math.random
    // Read invalidation exists without live observation: static APIs and direct
    // Mutations must use the same recovery/commit fence even while detached.
    this.stopInvalidations = this.client.getQueryCache().subscribe((event) => {
      if (event.type === 'updated' && event.action.type === 'invalidate' && readGolemMeta(event.query) !== undefined) this.invalidateRead(event.query)
    })
    this.state.onReset((adopting) => {
      this.resetWork = Promise.resolve()
      // Recovery is observable immediately, including the interval before
      // discovery completes and replacement subscriptions can be created.
      this.statuses = new Map(this.state.disposed ? [] : this.client.getQueryCache().getAll()
        .filter((query) => this.isGolemRead(query) && query.isActive() && readMeta(query)?.analysis.live)
        .map((query) => [query.queryHash, { state: 'RECOVERING' as const, error: undefined }]))
      const cancellations = this.clearReads(!adopting)
      for (const entry of [...this.entries.values()]) this.closeEntry(entry, true)
      this.transport.renew()
      this.resetWork = Promise.all(cancellations).then(() => undefined)
      this.notify()
    })
    this.state.onReady(() => { void this.resumeSession().catch(() => undefined) })
    this.state.onFailure((error) => {
      const generation = this.authGeneration
      this.resetWork = undefined
      for (const query of this.client.getQueryCache().getAll()) {
        if (generation !== this.authGeneration || this.state.disposed) return
        if (!this.isGolemRead(query) || !query.isActive()) continue
        query.setState({ error, errorUpdatedAt: Date.now(), status: 'error', fetchStatus: 'idle' })
        if (generation !== this.authGeneration || this.state.disposed) return
        if (readMeta(query)?.analysis.live) this.statuses.set(query.queryHash, { state: 'FAILED', error })
      }
      this.notify()
    })
  }

  get started(): boolean {
    return this.unsubscribe !== undefined
  }

  start(): () => void {
    this.state.capture()
    if (this.unsubscribe === undefined) {
      const cache = this.client.getQueryCache()
      this.unsubscribe = cache.subscribe((event) => this.onCacheEvent(event))
      for (const query of cache.getAll()) {
        this.reconcileHold(query)
      }
    }
    return () => this.stop()
  }

  stop(): void {
    this.unsubscribe?.()
    this.unsubscribe = undefined
    for (const entry of [...this.entries.values()]) {
      this.closeEntry(entry)
    }
  }

  dispose(): void {
    this.state.close()
    this.stop()
    this.stopInvalidations()
    this.clearReads()
  }

  // Every Golem read, including static pages, shares this session fence.
  async execute<T = unknown>(request: ExecuteRequest): Promise<T> {
    const entry = this.state.capture()
    const stamp = await this.state.read(entry)
    await this.resetWork
    if (!this.state.current(stamp) || request.signal?.aborted) throw new CancelledError({ silent: true })
    const data = await this.transport.execute<T>(request)
    if (typeof data !== 'object' || data === null || Array.isArray(data)) throw new GolemProtocolError('a Golem query must return a GraphQL result object')
    if (!this.state.current(stamp) || request.signal?.aborted) throw new CancelledError({ silent: true })
    this.stampRead(data)
    return data
  }

  resetSession(): Promise<void> {
    this.state.capture()
    this.state.invalidate()
    return this.state.ready().then(() => this.resetWork)
  }

  private async resumeSession(): Promise<void> {
    const generation = this.authGeneration
    const work = this.resetWork
    await work
    if (generation !== this.authGeneration || this.state.disposed) return
    if (this.resetWork === work) this.resetWork = undefined
    if (this.started) {
      for (const query of this.client.getQueryCache().getAll()) {
        const meta = readMeta(query)
        if (query.isActive() && meta?.analysis.live) this.openEntry(query, meta, true)
      }
    }
    void this.client.refetchQueries({ type: 'active', predicate: (query) => this.isGolemRead(query) && readMeta(query) === undefined && query.state.fetchStatus !== 'fetching' }, { cancelRefetch: true, throwOnError: false })
  }

  private isGolemRead(query: Query): boolean {
    return query.queryKey[0] === 'golem'
  }

  private clearQuery(query: Query, cancel = true): Promise<void> {
    const generation = this.authGeneration
    const cancelled = cancel ? query.cancel({ silent: true, revert: false }) : Promise.resolve()
    if (generation !== this.authGeneration) return cancelled
    query.setState({ data: undefined, dataUpdatedAt: 0, dataUpdateCount: 0, error: null, errorUpdatedAt: 0, errorUpdateCount: 0, fetchFailureCount: 0, fetchFailureReason: null, isInvalidated: true, status: 'pending', ...(cancel ? { fetchStatus: 'idle' as const } : {}) })
    return cancelled
  }

  private clearReads(cancel = true): Promise<void>[] {
    const generation = this.authGeneration
    const cancellations: Promise<void>[] = []
    for (const query of this.client.getQueryCache().getAll()) {
      if (generation !== this.authGeneration) break
      if (this.isGolemRead(query)) cancellations.push(this.clearQuery(query, cancel))
    }
    return cancellations
  }

  private expireCredentials(): void {
    this.state.invalidate()
    void this.state.ready().catch(() => undefined)
  }

  private readonly invalidateRead = (query: Query): void => {
    if (this.state.disposed) return
    const generation = this.authGeneration
    const meta = readGolemMeta(query)
    if (meta === undefined) return
    const entry = this.entries.get(query.queryHash)
    if (meta.continuation && entry !== undefined) { this.invalidate(entry); return }
    if (entry !== undefined) {
      this.advanceGeneration(entry)
      entry.outage = true
      entry.coalescer.cancel()
      this.publish(entry)
    } else this.queryRecovery(query).generation++
    if (generation !== this.authGeneration) return
    if (meta.continuation) { void this.clearQuery(query); return }
    void query.cancel({ silent: true, revert: false })
    if (generation !== this.authGeneration) return
    query.setState({ isInvalidated: true, fetchStatus: 'idle' })
  }

  private async fetchStatic(queryKey: readonly unknown[], request: ExecuteRequest): Promise<unknown> {
    const query = this.client.getQueryCache().find({ queryKey, exact: true })
    const recovery = query === undefined ? undefined : this.queryRecovery(query)
    const generation = recovery?.generation
    const data = await this.execute(request)
    if (generation !== recovery?.generation || request.signal?.aborted) throw new CancelledError({ silent: true })
    this.stampRead(data, recovery)
    return data
  }

  staticOptions(analysis: StaticDocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    const queryKey = ['golem', 'static', analysis.query, variables] as const
    const meta: GolemStaticQueryMeta = { mode: 'static', analysis, variables, invalidate: this.invalidateRead }
    return { queryKey, meta: { golem: meta }, structuralSharing: this.structuralSharing,
      queryFn: (context: Pick<QueryFunctionContext, 'signal'>) => this.fetchStatic(queryKey, { query: analysis.query, variables, signal: context.signal }) }
  }

  staticInfiniteOptions(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    const plan = golemInfiniteQuery(analysis, variables, this.manifest)
    const queryKey = ['golem', 'static', analysis.query, variables, 'infinite'] as const
    const meta: GolemStaticQueryMeta = { mode: 'static', analysis, variables, invalidate: this.invalidateRead }
    return { queryKey, meta: { golem: meta }, structuralSharing: this.structuralSharing,
      queryFn: (context: Pick<QueryFunctionContext<readonly unknown[], Cursor | null>, 'signal' | 'pageParam'>) => this.fetchStatic(queryKey, { ...plan.requestFor(context.pageParam), signal: context.signal }),
      initialPageParam: plan.initialPageParam, getNextPageParam: (lastPage: unknown) => plan.getNextPageParam(lastPage) }
  }

  staticContinuationOptions(analysis: StaticDocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    const plan = golemContinuationQuery(analysis, variables, this.manifest)
    const queryKey = ['golem', 'static', analysis.query, variables, 'continuation'] as const
    const meta: GolemStaticQueryMeta = { mode: 'static', analysis, variables, continuation: true, invalidate: this.invalidateRead }
    return { queryKey, meta: { golem: meta }, structuralSharing: this.structuralSharing,
      queryFn: (context: Pick<QueryFunctionContext<readonly unknown[], ContinuationPageParam>, 'signal' | 'pageParam'>) => this.fetchStatic(queryKey, { ...plan.requestFor(context.pageParam), signal: context.signal }),
      initialPageParam: plan.initialPageParam,
      getNextPageParam: (lastPage: unknown, allPages: unknown[], lastPageParam: ContinuationPageParam, allPageParams: ContinuationPageParam[]) => plan.getNextPageParam(lastPage, allPages, lastPageParam, allPageParams) }
  }

  options(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    assertLive(analysis)
    const meta: GolemQueryMeta = { analysis, variables, invalidate: this.invalidateRead }
    return {
      queryKey: ['golem', analysis.query, variables] as const,
      queryFn: (context: QueryFunctionContext) => this.fetch(context, meta),
      meta: { golem: meta },
      structuralSharing: this.structuralSharing,
    }
  }

  infiniteOptions(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    assertLive(analysis)
    const plan = golemInfiniteQuery(analysis, variables, this.manifest)
    const meta: GolemQueryMeta = { analysis, variables, invalidate: this.invalidateRead, pagination: plan.spec }
    return {
      queryKey: ['golem', analysis.query, variables, 'infinite'] as const,
      queryFn: (context: QueryFunctionContext<readonly unknown[], Cursor | null>) => this.fetch(context, meta, plan.requestFor(context.pageParam)),
      initialPageParam: plan.initialPageParam,
      getNextPageParam: (lastPage: unknown) => plan.getNextPageParam(lastPage),
      meta: { golem: meta },
      structuralSharing: this.structuralSharing,
    }
  }

  continuationOptions(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    const plan = golemLiveContinuationQuery(analysis, variables, this.manifest)
    const meta: GolemQueryMeta = { analysis, variables, invalidate: this.invalidateRead, continuation: true }
    return {
      queryKey: ['golem', analysis.query, variables, 'continuation'] as const,
      queryFn: (context: QueryFunctionContext<readonly unknown[], ContinuationPageParam>) => this.fetch(context, meta, plan.requestFor(context.pageParam)),
      initialPageParam: plan.initialPageParam,
      getNextPageParam: (lastPage: unknown, allPages: unknown[], lastPageParam: ContinuationPageParam, allPageParams: ContinuationPageParam[]) => plan.getNextPageParam(lastPage, allPages, lastPageParam, allPageParams),
      meta: { golem: meta },
      structuralSharing: this.structuralSharing,
    }
  }

  statusOf(query: Query): LiveStatus | undefined {
    return this.statuses.get(query.queryHash)
  }

  private hasQueuedRefetch(query: Query): boolean {
    const entry = this.entries.get(query.queryHash)
    return entry !== undefined && (entry.coalescer.pending || entry.derivedStale)
  }

  subscribeStatus(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  private onCacheEvent(event: QueryCacheNotifyEvent): void {
    switch (event.type) {
      case 'observerAdded':
      case 'observerRemoved':
      case 'observerOptionsUpdated':
        this.reconcileHold(event.query)
        return
      case 'removed': {
        const entry = this.entries.get(event.query.queryHash)
        if (entry !== undefined) {
          this.closeEntry(entry)
        }
        return
      }
      case 'updated': {
        const entry = this.entries.get(event.query.queryHash)
        if (entry !== undefined && event.action.type === 'success' && !event.action.manual && entry.acceptedGeneration === entry.recovery.generation) {
          entry.acceptedGeneration = undefined
          if (entry.streams.every((stream) => stream.acked)) entry.outage = false
          this.publish(entry)
        }
        return
      }
      default:
    }
  }

  private reconcileHold(query: Query): void {
    const meta = readMeta(query)
    if (this.resetWork !== undefined || meta === undefined || !meta.analysis.live) {
      return
    }
    const entry = this.entries.get(query.queryHash)
    if (query.isActive()) {
      if (entry === undefined) {
        this.openEntry(query, meta)
      } else if (entry.closeTimer !== undefined) {
        clearTimeout(entry.closeTimer)
        entry.closeTimer = undefined
      }
      return
    }
    if (entry !== undefined && entry.closeTimer === undefined) {
      entry.closeTimer = setTimeout(() => this.closeEntry(entry), this.closeGraceMs)
    }
  }

  private openEntry(query: Query, meta: GolemQueryMeta, recovering = false): void {
    if (this.state.disposed) return
    if (!this.state.initialized) { void this.state.ready().catch(() => undefined); return }
    // A new live page subscription cannot certify an old rank snapshot across
    // an unobserved gap. Clear before InfiniteQueryBehavior captures its pages.
    if (meta.continuation && query.state.data !== undefined) {
      this.queryRecovery(query).generation++
      void this.clearQuery(query)
      recovering = true
    }
    const entry: Entry = {
      query,
      meta,
      streams: meta.analysis.streams.map((plan) => ({ plan, phase: 'opening', acked: false, ackedAt: 0, attempt: 0, generation: 0, stop: undefined, timer: undefined })),
      coalescer: new Coalescer(() => this.refetch(entry), this.windowMs, this.maxWaitMs),
      waiters: new Set(),
      merge: EMPTY_MERGE_STATE,
      inFlight: 0,
      derivedStale: false,
      outage: recovering || query.state.isInvalidated || query.state.dataUpdatedAt !== 0,
      recovery: this.queryRecovery(query),
      acceptedGeneration: undefined,
      recoveryAt: 0,
      everLive: false,
      failure: undefined,
      notFound: undefined,
      closeTimer: undefined,
      closed: false,
    }
    this.entries.set(query.queryHash, entry)
    this.publish(entry)
    for (const stream of entry.streams) {
      this.openStream(entry, stream)
    }
  }

  private closeEntry(entry: Entry, preserveStatus = false): void {
    if (entry.closed) {
      return
    }
    entry.closed = true
    entry.recovery.generation++
    void entry.query.cancel({ silent: true, revert: false })
    entry.query.setState({ fetchStatus: 'idle' })
    if (entry.closeTimer !== undefined) {
      clearTimeout(entry.closeTimer)
    }
    entry.coalescer.cancel()
    this.entries.delete(entry.query.queryHash)
    if (!preserveStatus) this.statuses.delete(entry.query.queryHash)
    for (const stream of entry.streams) {
      this.stopStream(stream)
    }
    this.releaseWaiters(entry)
    this.notify()
    if (this.resetWork === undefined) this.retryLimited()
  }

  private stopStream(stream: StreamState): void {
    if (stream.timer !== undefined) {
      clearTimeout(stream.timer)
      stream.timer = undefined
    }
    stream.generation++
    stream.stop?.()
    stream.stop = undefined
    stream.phase = 'closed'
    stream.acked = false
  }

  private openStream(entry: Entry, stream: StreamState): void {
    if (entry.closed || stream.phase === 'closed') {
      return
    }
    stream.timer = undefined
    stream.phase = 'opening'
    stream.acked = false
    const generation = ++stream.generation
    const current = () => !entry.closed && stream.generation === generation
    stream.stop = this.transport.subscribe(streamRequest(stream.plan, entry.meta.variables), {
      onAck: () => {
        if (current()) {
          this.onAck(entry, stream)
        }
      },
      onEvent: (body) => {
        if (current()) {
          this.guarded(entry, () => {
            if (stream.plan.kind === 'invalidation') throw new GolemProtocolError('an operation invalidation stream emitted an unexpected data packet')
            this.onEvent(entry, stream, frameOf(stream.plan, body.data))
          })
        }
      },
      onInterrupted: () => {
        if (current()) {
          stream.acked = false
          this.invalidate(entry)
        }
      },
      onEnd: (end) => {
        if (current()) {
          stream.stop = undefined
          this.onEnd(entry, stream, end)
        }
      },
    })
  }

  private guarded(entry: Entry, run: () => void): void {
    try {
      run()
    } catch (error) {
      this.fail(entry, error instanceof Error ? error : new GolemProtocolError(String(error)))
    }
  }

  private fail(entry: Entry, error: Error): void {
    this.invalidate(entry)
    entry.failure = error
    for (const stream of entry.streams) {
      this.stopStream(stream)
      stream.phase = 'failed'
    }
    this.publish(entry)
    this.releaseWaiters(entry)
    this.retryLimited()
  }

  private settled(entry: Entry): boolean {
    return entry.streams.every((stream) => stream.acked || stream.phase === 'limit' || stream.phase === 'forbidden' || stream.phase === 'failed')
  }

  private onAck(entry: Entry, stream: StreamState): void {
    stream.acked = true
    stream.ackedAt = this.clock()
    stream.phase = 'live'
    this.afterSettle(entry)
  }

  private afterSettle(entry: Entry): void {
    if (!this.settled(entry)) {
      this.publish(entry)
      return
    }
    if (entry.streams.every((stream) => stream.acked)) {
      entry.everLive = true
    }
    this.publish(entry)
    const covered = entry.waiters.size > 0
    this.releaseWaiters(entry)
    if (entry.outage && entry.streams.every((stream) => stream.acked)) {
      if (!covered) this.refetchNow(entry)
    }
  }

  private releaseWaiters(entry: Entry): void {
    for (const release of [...entry.waiters]) {
      release()
    }
  }

  private onEnd(entry: Entry, stream: StreamState, end: StreamEnd): void {
    switch (end.kind) {
      case 'reauthenticate':
        this.expireCredentials()
        return
      case 'fatal':
        this.endFatal(entry, stream, end.error)
        return
      case 'complete':
        this.reopen(entry, stream, this.backoff(stream))
        return
      case 'error':
        this.onStreamError(entry, stream, end.errors)
    }
  }

  private onStreamError(entry: Entry, stream: StreamState, errors: ReadonlyArray<GolemError>): void {
    const first = errors[0]
    switch (first?.code) {
      case SubscriptionCode.Resync:
        this.reopen(entry, stream, 0)
        return
      case SubscriptionCode.Overflow:
      case SubscriptionCode.SourceClosed:
      case SubscriptionCode.Cancelled:
        this.reopen(entry, stream, this.backoff(stream))
        return
      case SubscriptionCode.Revalidation:
        this.expireCredentials()
        return
      case SubscriptionCode.Forbidden:
        this.invalidate(entry)
        this.endStream(entry, stream, 'forbidden')
        return
      case SubscriptionCode.Limit:
        this.endStream(entry, stream, 'limit')
        return
      default:
        this.endFatal(entry, stream, first ?? new GolemProtocolError('a stream ended with an empty error list'))
    }
  }

  private endStream(entry: Entry, stream: StreamState, phase: 'limit' | 'forbidden'): void {
    this.stopStream(stream)
    stream.phase = phase
    this.afterSettle(entry)
    if (phase === 'forbidden') {
      for (const other of entry.streams) {
        if (other !== stream) {
          this.stopStream(other)
          other.phase = 'forbidden'
        }
      }
      this.releaseWaiters(entry)
      this.retryLimited()
    }
  }

  private endFatal(entry: Entry, stream: StreamState, error: Error): void {
    this.invalidate(entry)
    this.stopStream(stream)
    stream.phase = 'failed'
    entry.failure = error
    this.publish(entry)
    this.releaseWaiters(entry)
    this.retryLimited()
  }

  private backoff(stream: StreamState): number {
    if (this.clock() - stream.ackedAt > STABLE_STREAM_MS) {
      stream.attempt = 0
    }
    return backoffDelay(stream.attempt++, this.random)
  }

  private advanceGeneration(entry: Entry): void {
    entry.recovery.generation++
    entry.acceptedGeneration = undefined
    entry.inFlight = 0
    entry.merge = EMPTY_MERGE_STATE
    entry.derivedStale = false
  }

  private invalidate(entry: Entry): void {
    this.advanceGeneration(entry)
    entry.outage = true
    entry.notFound = undefined
    entry.coalescer.cancel()
    void this.clearQuery(entry.query)
    this.publish(entry)
  }

  private reopen(entry: Entry, stream: StreamState, delay: number): void {
    this.stopStream(stream)
    stream.phase = 'opening'
    this.invalidate(entry)
    const now = this.clock()
    if (entry.recoveryAt <= now) {
      entry.recoveryAt = now + RECOVERY_COOLDOWN_MS + Math.floor(this.random() * RECOVERY_JITTER_MS)
    }
    stream.timer = setTimeout(() => this.openStream(entry, stream), Math.max(delay, entry.recoveryAt - now))
  }

  private retryLimited(): void {
    if (!this.started || this.resetWork !== undefined) return
    for (const entry of this.entries.values()) {
      for (const stream of entry.streams) {
        if (stream.phase === 'limit') {
          entry.outage = true
          this.reopen(entry, stream, 0)
        }
      }
    }
  }

  private statusFor(entry: Entry): LiveStatus {
    if (entry.failure !== undefined || entry.streams.some((stream) => stream.phase === 'failed')) {
      return { state: 'FAILED', error: entry.failure }
    }
    if (entry.streams.some((stream) => stream.phase === 'forbidden')) {
      return { state: 'FORBIDDEN', error: undefined }
    }
    if (entry.streams.some((stream) => stream.phase === 'limit')) {
      return { state: 'LIMIT', error: undefined }
    }
    if (entry.notFound !== undefined) {
      return { state: 'NOT_FOUND', error: entry.notFound }
    }
    if (!entry.outage && entry.streams.every((stream) => stream.acked)) {
      return { state: 'LIVE', error: undefined }
    }
    return { state: entry.everLive || entry.outage ? 'RECOVERING' : 'CONNECTING', error: undefined }
  }

  private publish(entry: Entry): void {
    if (entry.closed) {
      return
    }
    const next = this.statusFor(entry)
    const known = this.statuses.get(entry.query.queryHash)
    if (known !== undefined && SAME_STATUS(known, next)) {
      return
    }
    this.statuses.set(entry.query.queryHash, next)
    this.notify()
  }

  private notify(): void {
    for (const listener of [...this.listeners]) {
      listener()
    }
  }

  private onEvent(entry: Entry, stream: StreamState, frame: EventFrame): void {
    const plan = stream.plan
    if (plan.kind === 'invalidation') throw new GolemProtocolError('operation invalidations do not carry row events')
    if (plan.derived) {
      if (frame.type === 'EXITED' || frame.type === 'DELETED') {
        // A derived result cannot prove which slots remain authorized. Hide
        // uncertain data immediately; these still-live streams need no reopen.
        this.invalidate(entry)
      } else {
        entry.derivedStale = entry.inFlight > 0
      }
      entry.coalescer.request()
      return
    }
    const key = this.patcher.eventKey(plan.model, frame.id, frame.entity)
    const version = parseVersion(frame.version)
    const receivedAt = this.clock()
    const data = entry.query.state.data
    const within = entry.meta.pagination === undefined && !entry.meta.continuation ? plan.paths : plan.paths.map((path) => `pages.${path}`)
    const held = this.patcher.versions(data, frame.type === 'DELETED' ? undefined : within).get(key)
    const recording = entry.inFlight > 0
    switch (frame.type) {
      case 'CREATED':
        if (recording) {
          entry.merge = noteChange(entry.merge, key, receivedAt)
        }
        entry.coalescer.request()
        return
      case 'UPDATED': {
        if (recording) {
          entry.merge = noteChange(entry.merge, key, receivedAt)
        }
        const decision = decideUpdate(held, version)
        if (decision === 'invalidate' || (decision === 'patch' && !plan.patchable)) {
          entry.coalescer.request()
        } else if (decision === 'patch') {
          if (frame.entity === null) {
            throw new GolemProtocolError(`an UPDATED event for the held row ${key} carried no entity`)
          }
          this.write(entry, this.patcher.patch(data, key, frame.entity, within))
        }
        return
      }
      case 'DELETED':
      case 'EXITED': {
        const outcome = applyRemoval(entry.merge, { key, version, receivedAt }, held)
        if (recording) {
          entry.merge = outcome.state
        }
        const landing = !recording && entry.acceptedGeneration === entry.recovery.generation && entry.query.state.fetchStatus === 'fetching'
        if (landing && (outcome.remove || held === undefined)) {
          // A removal can land after reconcile() but before TanStack's success
          // dispatch. Fence that response without discarding unaffected rows or
          // putting an ordinary list event through stream recovery cooldown.
          this.advanceGeneration(entry)
          void entry.query.cancel({ silent: true, revert: false })
          entry.query.setState({ fetchStatus: 'idle' })
          entry.coalescer.request()
        }
        if (outcome.remove) {
          const removal = this.patcher.remove(data, new Set<RowKey>([key]), entry.meta.analysis.nullableRoots, frame.type === 'EXITED' ? within : undefined)
          this.write(entry, removal.data)
          if (removal.held.size > 0) {
            this.markNotFound(entry, removal.held)
            this.refetchNow(entry)
          } else if (removal.evicted) {
            entry.coalescer.request()
          }
        }
        return
      }
      default:
        throw new GolemProtocolError(`an event frame carried the unknown type ${JSON.stringify(frame.type)}`)
    }
  }

  private markNotFound(entry: Entry, keys: ReadonlySet<RowKey>): void {
    entry.notFound = new GolemNotFoundError([...keys])
    this.publish(entry)
  }

  private clearNotFound(entry: Entry): void {
    if (entry.notFound !== undefined) {
      entry.notFound = undefined
      this.publish(entry)
    }
  }

  private write(entry: Entry, next: unknown): void {
    if (entry.closed) return
    if (next !== entry.query.state.data) {
      this.client.setQueryData(entry.query.queryKey, next)
    }
  }

  private refetch(entry: Entry): void {
    if (entry.closed || entry.waiters.size > 0 || this.resetWork !== undefined || !this.settled(entry) || entry.failure !== undefined || entry.streams.some((stream) => stream.phase === 'forbidden')) {
      return
    }
    void this.client.refetchQueries({ queryKey: entry.query.queryKey, exact: true }, { cancelRefetch: true, throwOnError: false })
  }

  private refetchNow(entry: Entry): void {
    entry.coalescer.cancel()
    this.refetch(entry)
  }

  private async gate(entry: Entry, signal: AbortSignal): Promise<void> {
    if (signal.aborted) throw new CancelledError({ silent: true })
    if (this.settled(entry) || entry.closed) {
      return
    }
    await new Promise<void>((resolve, reject) => {
      const release = () => {
        entry.waiters.delete(release)
        signal.removeEventListener('abort', abort)
        resolve()
      }
      const abort = () => {
        entry.waiters.delete(release)
        reject(signal.reason)
      }
      entry.waiters.add(release)
      signal.addEventListener('abort', abort, { once: true })
    })
  }

  private async fetch(context: Pick<QueryFunctionContext, 'queryKey' | 'signal'>, meta: GolemQueryMeta, request: Pick<ExecuteRequest, 'query' | 'variables'> = { query: meta.analysis.query, variables: meta.variables }): Promise<unknown> {
    const stamp = await this.state.read(this.state.capture())
    await this.resetWork
    this.state.assert(stamp)
    const query = this.client.getQueryCache().find({ queryKey: context.queryKey, exact: true })
    if (query !== undefined && this.started) this.reconcileHold(query)
    const entry = query === undefined ? undefined : this.entries.get(query.queryHash)
    if (entry !== undefined) {
      await this.gate(entry, context.signal)
    }
    this.state.assert(stamp)
    if (entry?.closed || context.signal.aborted) throw new CancelledError({ silent: true })
    if (entry?.streams.some((stream) => stream.phase === 'forbidden')) throw new GolemError({ message: 'the live query is forbidden', extensions: { code: SubscriptionCode.Forbidden } }, false)
    if (entry?.failure !== undefined) throw entry.failure
    const live = entry
    const recovery = query === undefined ? undefined : this.queryRecovery(query)
    const generation = recovery?.generation
    const issuedAt = this.clock()
    if (live !== undefined) {
      live.inFlight++
      live.derivedStale = false
    }
    try {
      const data = await this.execute({ ...request, signal: context.signal })
      if (generation !== recovery?.generation || live?.closed || context.signal.aborted) throw new CancelledError({ silent: true })
      const reconciled = live === undefined ? data : this.reconcile(live, issuedAt, data)
      this.stampRead(reconciled, recovery)
      if (live === undefined) return reconciled
      live.acceptedGeneration = generation
      return reconciled
    } catch (error) {
      if (live !== undefined && generation === live.recovery.generation && !context.signal.aborted) {
        this.clearNotFound(live)
      }
      throw error
    } finally {
      if (live !== undefined && generation === live.recovery.generation) {
        live.inFlight--
        if (live.inFlight === 0) {
          live.merge = EMPTY_MERGE_STATE
          if (live.derivedStale) {
            live.derivedStale = false
            live.coalescer.request()
          }
        }
      }
    }
  }

  private reconcile(entry: Entry, issuedAt: number, fresh: unknown): unknown {
    this.clearNotFound(entry)
    const cachedData = entry.query.state.data
    const resolution = resolveResponse(entry.merge, {
      issuedAt,
      rows: this.patcher.versions(fresh),
      cached: this.patcher.versions(cachedData),
    })
    entry.merge = resolution.state
    const dropped = new Set<RowKey>()
    let stale = false
    for (const [key, decision] of resolution.rows) {
      if (decision === 'drop') {
        dropped.add(key)
      } else if (decision === 'keep-cached') {
        stale = true
      }
    }
    if (stale) {
      entry.coalescer.request()
      if (entry.meta.pagination === undefined && !entry.meta.continuation) {
        return cachedData
      }
    }
    let next = fresh
    if (dropped.size > 0) {
      const removal = this.patcher.remove(next, dropped, entry.meta.analysis.nullableRoots)
      next = removal.data
      if (removal.held.size > 0) {
        this.markNotFound(entry, removal.held)
      }
      if (removal.evicted) {
        entry.coalescer.request()
      }
    }
    if (resolution.refetch) {
      entry.coalescer.request()
    }
    return next
  }
}

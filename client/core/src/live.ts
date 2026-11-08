import type { Query, QueryCacheNotifyEvent, QueryClient, QueryFunctionContext } from '@tanstack/query-core'
import { assertLive, streamRequest, type DocumentAnalysis, type StreamPlan } from './analysis.js'
import { Coalescer, COALESCE_MAX_WAIT_MS, COALESCE_WINDOW_MS } from './coalescer.js'
import { GolemNotFoundError, GolemProtocolError, type GolemError } from './errors.js'
import type { ClientManifest } from './manifest.js'
import { applyRemoval, decideUpdate, EMPTY_MERGE_STATE, noteChange, parseVersion, resolveResponse, type MergeState } from './merge.js'
import { backoffDelay } from './protocol.js'
import { golemInfiniteQuery, type Cursor, type PageSpec } from './pagination.js'
import { Patcher, type EventIdentity, type RowKey } from './patcher.js'
import type { StreamEnd, StreamHandlers, SubscriptionRequest } from './session.js'
import type { ExecuteRequest } from './transport.js'

export const CLOSE_GRACE_MS = 1000
export const STABLE_STREAM_MS = 10_000

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
  readonly analysis: DocumentAnalysis
  readonly variables: Readonly<Record<string, unknown>>
  readonly pagination?: PageSpec
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
  readonly plan: StreamPlan
  phase: StreamPhase
  acked: boolean
  ackedAt: number
  attempt: number
  generation: number
  stop: (() => void) | undefined
  timer: ReturnType<typeof setTimeout> | undefined
}

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
  bypass: boolean
  everLive: boolean
  failure: Error | undefined
  notFound: GolemNotFoundError | undefined
  closeTimer: ReturnType<typeof setTimeout> | undefined
  closed: boolean
}

interface EventFrame {
  readonly version: string
  readonly type: string
  readonly id: EventIdentity
  readonly entity: Record<string, unknown> | null
}

export function readMeta(query: Query): GolemQueryMeta | undefined {
  const meta = query.meta?.golem
  if (typeof meta !== 'object' || meta === null) {
    return undefined
  }
  return meta as GolemQueryMeta
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
  private readonly statuses = new Map<string, LiveStatus>()
  private readonly listeners = new Set<() => void>()
  private unsubscribe: (() => void) | undefined

  constructor(options: LiveSyncOptions) {
    this.client = options.queryClient
    this.transport = options.transport
    this.manifest = options.manifest
    this.patcher = new Patcher(options.manifest)
    this.clock = options.clock ?? (() => performance.now())
    this.closeGraceMs = options.closeGraceMs ?? CLOSE_GRACE_MS
    this.windowMs = options.windowMs ?? COALESCE_WINDOW_MS
    this.maxWaitMs = options.maxWaitMs ?? COALESCE_MAX_WAIT_MS
    this.random = options.random ?? Math.random
  }

  get started(): boolean {
    return this.unsubscribe !== undefined
  }

  start(): () => void {
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

  options(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    assertLive(analysis)
    const meta: GolemQueryMeta = { analysis, variables }
    return {
      queryKey: ['golem', analysis.query, variables] as const,
      queryFn: (context: QueryFunctionContext) => this.fetch(context, meta),
      meta: { golem: meta },
    }
  }

  infiniteOptions(analysis: DocumentAnalysis, variables: Readonly<Record<string, unknown>> = {}) {
    assertLive(analysis)
    const plan = golemInfiniteQuery(analysis, variables, this.manifest)
    const meta: GolemQueryMeta = { analysis, variables, pagination: plan.spec }
    return {
      queryKey: ['golem', analysis.query, variables, 'infinite'] as const,
      queryFn: (context: QueryFunctionContext<readonly unknown[], Cursor | null>) => this.fetch(context, meta, plan.requestFor(context.pageParam)),
      initialPageParam: plan.initialPageParam,
      getNextPageParam: (lastPage: unknown) => plan.getNextPageParam(lastPage),
      meta: { golem: meta },
    }
  }

  statusOf(query: Query): LiveStatus | undefined {
    return this.statuses.get(query.queryHash)
  }

  hasQueuedRefetch(query: Query): boolean {
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
      default:
    }
  }

  private reconcileHold(query: Query): void {
    const meta = readMeta(query)
    if (meta === undefined || !meta.analysis.live) {
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

  private openEntry(query: Query, meta: GolemQueryMeta): void {
    const entry: Entry = {
      query,
      meta,
      streams: meta.analysis.streams.map((plan) => ({ plan, phase: 'opening', acked: false, ackedAt: 0, attempt: 0, generation: 0, stop: undefined, timer: undefined })),
      coalescer: new Coalescer(() => this.refetch(entry), this.windowMs, this.maxWaitMs),
      waiters: new Set(),
      merge: EMPTY_MERGE_STATE,
      inFlight: 0,
      derivedStale: false,
      outage: query.state.dataUpdatedAt !== 0,
      bypass: false,
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

  private closeEntry(entry: Entry): void {
    if (entry.closed) {
      return
    }
    entry.closed = true
    if (entry.closeTimer !== undefined) {
      clearTimeout(entry.closeTimer)
    }
    entry.coalescer.cancel()
    this.entries.delete(entry.query.queryHash)
    this.statuses.delete(entry.query.queryHash)
    for (const stream of entry.streams) {
      this.stopStream(stream)
    }
    this.releaseWaiters(entry)
    this.notify()
    this.retryLimited()
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
          this.guarded(entry, () => this.onEvent(entry, stream, frameOf(stream.plan, body.data)))
        }
      },
      onInterrupted: () => {
        if (current()) {
          stream.acked = false
          entry.outage = true
          this.publish(entry)
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
      entry.outage = false
      const bypass = entry.bypass
      entry.bypass = false
      if (covered) {
        return
      }
      if (bypass) {
        this.refetchNow(entry)
      } else {
        entry.coalescer.request()
      }
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
        this.reopen(entry, stream, this.backoff(stream), false)
        return
      case 'fatal':
        this.endFatal(entry, stream, end.error)
        return
      case 'complete':
        this.reopen(entry, stream, this.backoff(stream), false)
        return
      case 'error':
        this.onStreamError(entry, stream, end.errors)
    }
  }

  private onStreamError(entry: Entry, stream: StreamState, errors: ReadonlyArray<GolemError>): void {
    const first = errors[0]
    switch (first?.code) {
      case SubscriptionCode.Resync:
        this.reopen(entry, stream, 0, first.extensions.reason === REASON_DEPENDENCY_CHANGED)
        return
      case SubscriptionCode.Overflow:
      case SubscriptionCode.SourceClosed:
      case SubscriptionCode.Cancelled:
        this.reopen(entry, stream, this.backoff(stream), false)
        return
      case SubscriptionCode.Revalidation:
        this.transport.renew()
        this.reopen(entry, stream, this.backoff(stream), false)
        return
      case SubscriptionCode.Forbidden:
        this.endStream(entry, stream, 'forbidden')
        entry.coalescer.request()
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
      this.retryLimited()
    }
  }

  private endFatal(entry: Entry, stream: StreamState, error: Error): void {
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

  private reopen(entry: Entry, stream: StreamState, delay: number, bypass: boolean): void {
    this.stopStream(stream)
    stream.phase = 'opening'
    entry.outage = true
    entry.bypass ||= bypass
    this.publish(entry)
    stream.timer = setTimeout(() => this.openStream(entry, stream), delay)
  }

  private retryLimited(): void {
    for (const entry of this.entries.values()) {
      for (const stream of entry.streams) {
        if (stream.phase === 'limit') {
          entry.outage = true
          this.reopen(entry, stream, 0, false)
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
    if (entry.streams.every((stream) => stream.acked)) {
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
    if (stream.plan.derived) {
      entry.derivedStale = entry.inFlight > 0
      entry.coalescer.request()
      return
    }
    const key = this.patcher.eventKey(stream.plan.model, frame.id, frame.entity)
    const version = parseVersion(frame.version)
    const receivedAt = this.clock()
    const data = entry.query.state.data
    const within = entry.meta.pagination === undefined ? stream.plan.paths : stream.plan.paths.map((path) => `pages.${path}`)
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
        if (decision === 'invalidate' || (decision === 'patch' && !stream.plan.patchable)) {
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
    if (next !== entry.query.state.data) {
      this.client.setQueryData(entry.query.queryKey, next)
    }
  }

  private refetch(entry: Entry): void {
    if (entry.closed || entry.waiters.size > 0) {
      return
    }
    void this.client.refetchQueries({ queryKey: entry.query.queryKey, exact: true }, { cancelRefetch: true, throwOnError: false })
  }

  private refetchNow(entry: Entry): void {
    entry.coalescer.cancel()
    this.refetch(entry)
  }

  private async gate(entry: Entry, signal: AbortSignal): Promise<void> {
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
    const query = this.client.getQueryCache().find({ queryKey: context.queryKey, exact: true })
    const entry = query === undefined ? undefined : this.entries.get(query.queryHash)
    if (entry !== undefined) {
      await this.gate(entry, context.signal)
    }
    const live = entry !== undefined && !entry.closed ? entry : undefined
    const issuedAt = this.clock()
    if (live !== undefined) {
      live.inFlight++
      live.derivedStale = false
    }
    try {
      const data = await this.transport.execute({ ...request, signal: context.signal })
      if (live === undefined || live.closed || context.signal.aborted) {
        return data
      }
      return this.reconcile(live, issuedAt, data)
    } catch (error) {
      if (live !== undefined && !context.signal.aborted) {
        this.clearNotFound(live)
      }
      throw error
    } finally {
      if (live !== undefined) {
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
      if (entry.meta.pagination === undefined) {
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

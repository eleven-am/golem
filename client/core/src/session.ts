import { createClient, type Client } from 'graphql-ws'
import { GolemError, GolemProtocolError, GolemSessionError, type GraphQLResponseBody } from './errors.js'
import { sessionState, type SessionState, type SessionStamp } from './session-state.js'
import { AckTracker, backoffDelay, classifyClose, golemBlock, isFatalHandshakeStatus, type ContractManifest, type SessionStatus } from './protocol.js'

export interface Credentials {
  readonly headers?: Readonly<Record<string, string>>
  readonly connectionParams?: Readonly<Record<string, unknown>>
}

export type CredentialsGetter = () => Credentials | Promise<Credentials>

export interface SocketLike {
  readonly readyState: number
}

export type SocketFactory = (url: string, protocols: string | string[], credentials: Credentials) => SocketLike

export interface SubscriptionRequest {
  readonly query: string
  readonly variables?: Readonly<Record<string, unknown>>
}

export type StreamEnd =
  | { readonly kind: 'complete' }
  | { readonly kind: 'error'; readonly errors: ReadonlyArray<GolemError> }
  | { readonly kind: 'reauthenticate' }
  | { readonly kind: 'fatal'; readonly status: SessionStatus; readonly error: Error }

export interface StreamHandlers {
  onAck(): void
  onEvent(body: GraphQLResponseBody): void
  onInterrupted(): void
  onEnd(end: StreamEnd): void
}

export interface SessionOptions {
  readonly url: string
  readonly manifest: ContractManifest
  readonly credentials?: CredentialsGetter
  readonly createSocket?: SocketFactory
  readonly random?: () => number
  readonly sleep?: (milliseconds: number) => Promise<void>
}

interface CloseEventLike {
  readonly code: number
  readonly reason: string
}

function isCloseEventLike(value: unknown): value is CloseEventLike {
  return typeof value === 'object' && value !== null && 'code' in value && typeof value.code === 'number'
}

function reasonOf(event: CloseEventLike): string {
  return typeof event.reason === 'string' ? event.reason : ''
}

function defaultSleep(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

function socketUrl(url: string): string {
  return url.replace(/^http/, 'ws')
}

// graphql-ws does not observe rejection of its asynchronous URL initializer.
// Preparation failures therefore enter its normal socket-error path after the
// handlers are installed, without constructing a native/application socket.
class SocketPreparationError extends Error {
  constructor(readonly reason: unknown) { super('socket preparation failed') }
}

function failedSocket(reason: unknown): SocketLike {
  const socket = {
    readyState: 3,
    onerror: undefined as ((reason: unknown) => void) | undefined,
    close() {},
  }
  queueMicrotask(() => socket.onerror?.(new SocketPreparationError(reason)))
  return socket
}

function socketClass(factory: SocketFactory, credentials: () => Credentials) {
  return class {
    static readonly CONNECTING = 0
    static readonly OPEN = 1
    static readonly CLOSING = 2
    static readonly CLOSED = 3
    constructor(url: string, protocols: string | string[]) {
      try {
        return factory(url, protocols, credentials()) as unknown as object
      } catch (reason) {
        return failedSocket(reason) as unknown as object
      }
    }
  }
}

export class Session {
  private readonly options: SessionOptions
  private readonly state: SessionState
  private client: Client | undefined
  private currentStatus: SessionStatus = 'idle'
  private readonly listeners = new Set<(status: SessionStatus) => void>()
  private readonly active = new Set<{ readonly tracker: AckTracker; readonly handlers: StreamHandlers; stop?: () => void }>()
  private acknowledged = false
  private latched: { readonly status: SessionStatus; readonly error: Error } | undefined
  private lastCloseAcknowledged = false

  constructor(options: SessionOptions) {
    golemBlock(options.manifest)
    this.options = options
    this.state = sessionState(this, options.manifest)
    this.state.onReset((_adopting, refusal) => this.resetSocket(refusal))
  }

  get status(): SessionStatus {
    return this.currentStatus
  }

  onStatus(listener: (status: SessionStatus) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    const entry = this.state.capture()
    const owned = JSON.parse(JSON.stringify(request)) as SubscriptionRequest
    if (this.state.initialized) return this.subscribeReady(owned, handlers, entry)
    let stopped = false
    let pendingStamp = entry
    let stop: (() => void) | undefined
    void this.state.read(entry).then((stamp) => {
      pendingStamp = stamp
      if (!stopped && this.state.current(stamp)) stop = this.subscribeReady(owned, handlers, stamp)
    }).catch((error: unknown) => {
      if (!stopped && this.state.current(pendingStamp)) handlers.onEnd({ kind: 'fatal', status: 'invalid', error: error instanceof Error ? error : new GolemProtocolError('discovery failed') })
    })
    return () => { stopped = true; stop?.() }
  }

  private subscribeReady(request: SubscriptionRequest, handlers: StreamHandlers, stamp: SessionStamp): () => void {
    this.state.assert(stamp)
    if (this.latched !== undefined) {
      const { status, error } = this.latched
      let stopped = false
      queueMicrotask(() => { if (!stopped && this.state.current(stamp)) handlers.onEnd({ kind: 'fatal', status, error }) })
      return () => { stopped = true }
    }
    const stream: { tracker: AckTracker; handlers: StreamHandlers; stop?: () => void } = { tracker: new AckTracker(), handlers }
    this.active.add(stream)
    let ended = false
    const finish = (end: StreamEnd) => {
      if (ended) {
        return
      }
      ended = true
      this.active.delete(stream)
      // graphql-ws leaves disposal to us after connection/preparation errors.
      // Release before notifying a handler that may start another stream.
      dispose()
      handlers.onEnd(end)
    }
    const dispose = this.connection(stamp).subscribe(
      { query: request.query, variables: request.variables as Record<string, unknown> | undefined, ...(stamp.incarnation === undefined ? {} : { extensions: { golem: { ...golemBlock(this.options.manifest), dataIncarnation: stamp.incarnation } } }) },
      {
        next: (value) => {
          if (ended || !this.state.current(stamp)) {
            return
          }
          if (this.state.opted && (value as GraphQLResponseBody).extensions?.golem?.dataIncarnation !== stamp.incarnation) {
            this.state.refuse(stamp)
            return
          }
          const outcome = stream.tracker.accept(value as GraphQLResponseBody)
          switch (outcome.kind) {
            case 'ack':
              handlers.onAck()
              return
            case 'event':
              handlers.onEvent(outcome.body)
              return
            case 'error':
              finish({ kind: 'error', errors: outcome.errors })
              return
            case 'violation':
              finish({ kind: 'fatal', status: 'invalid', error: outcome.error })
          }
        },
        error: (reason) => {
          if (ended || !this.state.current(stamp)) return
          if (reason instanceof SocketPreparationError) reason = reason.reason
          if (isCloseEventLike(reason) && reason.reason === 'golem:incarnation') { this.state.refuse(stamp); return }
          if (isCloseEventLike(reason)) {
            const disposition = classifyClose(reason.code, reasonOf(reason), this.lastCloseAcknowledged)
            if (disposition.action === 'fatal') {
              this.fail(disposition.status, disposition.error)
              finish({ kind: 'fatal', status: disposition.status, error: disposition.error })
              return
            }
            if (disposition.action === 'reauthenticate') {
              finish({ kind: 'reauthenticate' })
              return
            }
            finish({ kind: 'fatal', status: 'invalid', error: new GolemProtocolError(`the socket closed with ${reason.code} and was not retried`) })
            return
          }
          if (Array.isArray(reason)) {
            if (this.state.opted && reason.some((shape) => shape?.extensions?.code === 'GOLEM_DATA_INCARNATION_MISMATCH')) { this.state.refuse(stamp); return }
            finish({ kind: 'error', errors: reason.map((shape) => new GolemError(shape)) })
            return
          }
          const failure = reason instanceof Error ? reason : new GolemProtocolError(String(reason))
          this.fail('invalid', failure)
          finish({ kind: 'fatal', status: 'invalid', error: failure })
        },
        complete: () => { if (this.state.current(stamp)) finish({ kind: 'complete' }) },
      },
    )
    const stop = () => {
      if (!ended) {
        ended = true
        this.active.delete(stream)
        dispose()
      }
    }
    stream.stop = stop
    return stop
  }

  renew(): void {
    this.client?.terminate()
  }

  dispose(): void { this.state.close() }

  private resetSocket(refusal?: GolemSessionError): void {
    const refused = refusal === undefined ? [] : [...this.active]
    const fenced = refusal === undefined || this.state.disposed ? undefined : this.state.capture()
    for (const stream of [...this.active]) stream.stop?.()
    // graphql-ws disposal awaits an in-flight handshake. Its rejection belongs
    // to the invalidated session; callbacks are fenced below and at dispatch.
    void Promise.resolve(this.client?.dispose()).catch(() => undefined)
    this.client = undefined
    this.active.clear()
    this.acknowledged = false
    this.lastCloseAcknowledged = false
    this.latched = undefined
    this.setStatus('idle')
    if (refusal !== undefined && fenced !== undefined) {
      queueMicrotask(() => {
        for (const stream of refused) {
          if (!this.state.current(fenced)) return
          stream.handlers.onEnd({ kind: 'fatal', status: 'invalid', error: refusal })
        }
      })
    }
  }

  private fail(status: SessionStatus, error: Error): void {
    if (isFatalHandshakeStatus(status)) {
      this.latched = { status, error }
    }
    this.setStatus(status)
  }

  private setStatus(status: SessionStatus): void {
    if (status === this.currentStatus) {
      return
    }
    this.currentStatus = status
    for (const listener of [...this.listeners]) {
      listener(status)
    }
  }

  private connection(stamp: SessionStamp): Client {
    this.state.assert(stamp)
    if (this.client !== undefined) {
      return this.client
    }
    const { options } = this
    const sleep = options.sleep ?? defaultSleep
    const random = options.random ?? Math.random
    let credentials: Credentials = {}
    let preparationFailure: { readonly reason: unknown } | undefined
    const factory: SocketFactory = options.createSocket ?? ((url, protocols) => new globalThis.WebSocket(url, protocols))
    const webSocketImpl = socketClass(factory, () => {
      this.state.assert(stamp)
      if (preparationFailure !== undefined) throw preparationFailure.reason
      return credentials
    })
    const client = createClient({
      url: async () => {
        preparationFailure = undefined
        try {
          this.state.assert(stamp)
          const resolved = (await options.credentials?.()) ?? {}
          this.state.assert(stamp)
          credentials = resolved
        } catch (reason) {
          preparationFailure = { reason }
        }
        // Always settle URL preparation. The adapter fences again before any
        // real factory call and routes failure through graphql-ws's sink.
        return socketUrl(options.url)
      },
      connectionParams: () => {
        this.state.assert(stamp)
        return { ...credentials.connectionParams, golem: { ...golemBlock(options.manifest), ...(stamp.incarnation === undefined ? {} : { dataIncarnation: stamp.incarnation }) } }
      },
      lazy: true,
      retryAttempts: Number.POSITIVE_INFINITY,
      retryWait: (retries) => sleep(backoffDelay(retries, random)),
      shouldRetry: (event) => !(event instanceof SocketPreparationError) && (!isCloseEventLike(event) || classifyClose(event.code, reasonOf(event), this.lastCloseAcknowledged).action === 'retry'),
      webSocketImpl,
      on: {
        connecting: (retrying) => { if (this.state.current(stamp)) this.setStatus(retrying ? 'reconnecting' : 'connecting') },
        connected: (_socket, payload) => {
          if (!this.state.current(stamp)) return
          if (this.state.opted && (payload as { golem?: { dataIncarnation?: unknown } } | undefined)?.golem?.dataIncarnation !== stamp.incarnation) { this.state.refuse(stamp); return }
          this.acknowledged = true
          this.setStatus('open')
        },
        closed: (event) => {
          if (!this.state.current(stamp)) return
          if (isCloseEventLike(event) && event.reason === 'golem:incarnation') { this.state.refuse(stamp); return }
          this.lastCloseAcknowledged = this.acknowledged
          this.acknowledged = false
          if (this.currentStatus === 'open') {
            this.setStatus('reconnecting')
          }
          if (isCloseEventLike(event) && classifyClose(event.code, reasonOf(event), this.lastCloseAcknowledged).action === 'retry') {
            for (const stream of this.active) {
              if (stream.tracker.live) {
                stream.tracker.reset()
                stream.handlers.onInterrupted()
              }
            }
          }
        },
      },
    })
    this.client = client
    return client
  }
}

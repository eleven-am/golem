import { createClient, type Client } from 'graphql-ws'
import { GolemError, GolemProtocolError, type GraphQLResponseBody } from './errors.js'
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
  readonly operationName?: string
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

function socketClass(factory: SocketFactory, credentials: () => Credentials) {
  return class {
    static readonly CONNECTING = 0
    static readonly OPEN = 1
    static readonly CLOSING = 2
    static readonly CLOSED = 3
    constructor(url: string, protocols: string | string[]) {
      return factory(url, protocols, credentials()) as unknown as object
    }
  }
}

export class Session {
  private readonly options: SessionOptions
  private client: Client | undefined
  private currentStatus: SessionStatus = 'idle'
  private readonly listeners = new Set<(status: SessionStatus) => void>()
  private readonly active = new Set<{ readonly tracker: AckTracker; readonly handlers: StreamHandlers }>()
  private connectionCredentials: Credentials = {}
  private acknowledged = false
  private latched: { readonly status: SessionStatus; readonly error: Error } | undefined
  private lastCloseAcknowledged = false

  constructor(options: SessionOptions) {
    this.options = options
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
    if (this.latched !== undefined) {
      const { status, error } = this.latched
      queueMicrotask(() => handlers.onEnd({ kind: 'fatal', status, error }))
      return () => undefined
    }
    const stream = { tracker: new AckTracker(), handlers }
    this.active.add(stream)
    let ended = false
    const finish = (end: StreamEnd) => {
      if (ended) {
        return
      }
      ended = true
      this.active.delete(stream)
      handlers.onEnd(end)
    }
    const dispose = this.connection().subscribe(
      { query: request.query, variables: request.variables as Record<string, unknown> | undefined, operationName: request.operationName },
      {
        next: (value) => {
          if (ended) {
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
              dispose()
          }
        },
        error: (reason) => {
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
            finish({ kind: 'error', errors: reason.map((shape) => new GolemError(shape)) })
            return
          }
          finish({ kind: 'fatal', status: 'invalid', error: reason instanceof Error ? reason : new GolemProtocolError(String(reason)) })
        },
        complete: () => finish({ kind: 'complete' }),
      },
    )
    return () => {
      if (!ended) {
        ended = true
        this.active.delete(stream)
        dispose()
      }
    }
  }

  renew(): void {
    this.client?.terminate()
  }

  dispose(): void {
    this.client?.dispose()
    this.client = undefined
    this.active.clear()
    this.setStatus('idle')
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

  private connection(): Client {
    if (this.client !== undefined) {
      return this.client
    }
    const { options } = this
    const sleep = options.sleep ?? defaultSleep
    const random = options.random ?? Math.random
    const webSocketImpl = options.createSocket === undefined ? undefined : socketClass(options.createSocket, () => this.connectionCredentials)
    const client = createClient({
      url: async () => {
        this.connectionCredentials = (await options.credentials?.()) ?? {}
        return socketUrl(options.url)
      },
      connectionParams: () => ({ ...this.connectionCredentials.connectionParams, golem: golemBlock(options.manifest) }),
      lazy: true,
      retryAttempts: Number.POSITIVE_INFINITY,
      retryWait: (retries) => sleep(backoffDelay(retries, random)),
      shouldRetry: (event) => !isCloseEventLike(event) || classifyClose(event.code, reasonOf(event), this.acknowledged).action === 'retry',
      ...(webSocketImpl === undefined ? {} : { webSocketImpl }),
      on: {
        connecting: (retrying) => this.setStatus(retrying ? 'reconnecting' : 'connecting'),
        connected: () => {
          this.acknowledged = true
          this.setStatus('open')
        },
        closed: (event) => {
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

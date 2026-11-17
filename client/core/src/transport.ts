import { GolemConfigurationError, GolemHttpError, GolemKeepaliveTooLargeError, KEEPALIVE_BODY_LIMIT, GolemProtocolError, GolemSessionError, GolemCommitError, failureOf, type GraphQLResponseBody } from './errors.js'
import { golemBlock, type ContractManifest, type SessionStatus } from './protocol.js'
import { canonicalIncarnation, sessionState, shareSession, type SessionStamp, type SessionState } from './session-state.js'
import { getOperationAST, parse } from 'graphql'
import { Session, type CredentialsGetter, type SocketFactory, type StreamHandlers, type SubscriptionRequest } from './session.js'

export interface TransportOptions {
  readonly url: string
  readonly manifest: ContractManifest
  readonly credentials?: CredentialsGetter
  readonly fetch?: typeof globalThis.fetch
  readonly createSocket?: SocketFactory
  readonly random?: () => number
  readonly sleep?: (milliseconds: number) => Promise<void>
}

export interface ExecuteRequest {
  readonly query: string
  readonly variables?: Readonly<Record<string, unknown>>
  readonly signal?: AbortSignal
  readonly keepalive?: boolean
}

export class Transport {
  private readonly options: TransportOptions
  private readonly session: Session
  private readonly state: SessionState

  constructor(options: TransportOptions) {
    if (options.credentials !== undefined && typeof options.credentials !== 'function') {
      throw new GolemConfigurationError('credentials must be a function returning headers and connection params')
    }
    this.options = options
    this.session = new Session(options)
    this.state = sessionState(this.session, options.manifest)
    shareSession(this, this.state)
    this.state.configure((nonce, entry) => this.discover(nonce, entry))
  }

  get status(): SessionStatus {
    return this.session.status
  }

  onStatus(listener: (status: SessionStatus) => void): () => void {
    return this.session.onStatus(listener)
  }

  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    return this.session.subscribe(request, handlers)
  }

  renew(): void {
    this.session.renew()
  }

  dispose(): void {
    this.session.dispose()
  }

  ready(): Promise<void> { return this.state.ready() }

  private async discover(nonce: string, entry: SessionStamp): Promise<string> {
    const block = golemBlock(this.options.manifest)
    const credentials = (await this.options.credentials?.()) ?? {}
    this.state.assert(entry)
    const response = await (this.options.fetch ?? globalThis.fetch)(this.options.url, {
      method: 'POST', cache: 'no-store',
      headers: { 'content-type': 'application/json', accept: 'application/json', ...credentials.headers },
      body: JSON.stringify({ extensions: { golem: { ...block, discover: { nonce } } } }),
    })
    const text = await response.text()
    this.state.assert(entry)
    let body: GraphQLResponseBody
    try { body = JSON.parse(text) as GraphQLResponseBody } catch { throw new GolemHttpError(response.status, 'incarnation discovery returned a non-JSON body') }
    const failure = failureOf(body)
    if (failure !== undefined) throw failure
    const found = body.extensions?.golem
    const discovery = found?.discover
    if (!response.ok || body.data !== null || found?.protocol !== block.protocol || found.contract !== block.contract || found.manifestVersion !== 3 || JSON.stringify(found.capabilities) !== JSON.stringify(block.capabilities) || typeof discovery !== 'object' || discovery === null || !('nonce' in discovery) || discovery.nonce !== nonce || !canonicalIncarnation(found.dataIncarnation)) {
      throw new GolemProtocolError('invalid authenticated incarnation discovery response')
    }
    return found.dataIncarnation
  }

  async execute<T = unknown>(request: ExecuteRequest): Promise<T> {
    // Capture before credential/discovery awaits. Snapshot the caller's JSON
    // inputs at entry, so queued requests cannot acquire different arguments.
    const entry = this.state.capture()
    const document = JSON.stringify({ query: request.query, variables: request.variables })
    let mutation = false
    try { mutation = getOperationAST(parse(request.query))?.operation === 'mutation' } catch { /* The ordinary server remains the document diagnostic owner. */ }
    this.state.assert(entry)
    const stamp = mutation ? this.state.mutation() : await this.state.read(entry)
    this.state.assert(stamp)
    const payload = JSON.stringify({ ...JSON.parse(document) as object, extensions: { golem: { ...golemBlock(this.options.manifest), ...(stamp.incarnation === undefined ? {} : { dataIncarnation: stamp.incarnation }) } } })
    if (request.keepalive === true) {
      const bytes = new TextEncoder().encode(payload).length
      if (bytes > KEEPALIVE_BODY_LIMIT) throw new GolemKeepaliveTooLargeError(bytes)
    }
    let dispatched = false
    try {
      const credentials = (await this.options.credentials?.()) ?? {}
      this.state.assert(stamp)
      const send = this.options.fetch ?? globalThis.fetch
      dispatched = true
      const response = await send(this.options.url, {
        method: 'POST',
        headers: { 'content-type': 'application/json', accept: 'application/json', ...credentials.headers },
        body: payload, signal: request.signal,
        ...(request.keepalive === true ? { keepalive: true } : {}),
      })
      const text = await response.text()
      let body: GraphQLResponseBody
      try { body = JSON.parse(text) as GraphQLResponseBody } catch { throw new GolemHttpError(response.status, `HTTP ${response.status} with a non-JSON body`) }
      const failure = failureOf(body)
      if (this.state.opted && this.state.current(stamp) && body.extensions?.golem?.dataIncarnation !== stamp.incarnation) this.state.refuse(stamp)
      // Preserve explicit server commit evidence even across a boundary. It
      // remains an error, never cacheable success under the new principal.
      if (failure instanceof GolemCommitError) throw failure
      this.state.assert(stamp, true)
      if (failure !== undefined) throw failure
      if (!response.ok) throw new GolemHttpError(response.status, `HTTP ${response.status} without GraphQL errors`)
      if (body.data === undefined) throw new GolemProtocolError('the response carried neither data nor errors')
      return body.data as T
    } catch (error) {
      if (error instanceof GolemCommitError) throw error
      if (!this.state.current(stamp)) throw new GolemSessionError(this.state.disposed ? 'disposed' : 'changed', dispatched)
      throw error
    }
  }
}

import { GolemConfigurationError, GolemHttpError, GolemKeepaliveTooLargeError, KEEPALIVE_BODY_LIMIT, GolemProtocolError, failureOf, type GraphQLResponseBody } from './errors.js'
import { golemBlock, type ContractManifest, type SessionStatus } from './protocol.js'
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

  constructor(options: TransportOptions) {
    if (options.credentials !== undefined && typeof options.credentials !== 'function') {
      throw new GolemConfigurationError('credentials must be a function returning headers and connection params')
    }
    this.options = options
    this.session = new Session(options)
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

  async execute<T = unknown>(request: ExecuteRequest): Promise<T> {
    const credentials = (await this.options.credentials?.()) ?? {}
    const send = this.options.fetch ?? globalThis.fetch
    const payload = JSON.stringify({
      query: request.query,
      variables: request.variables,
      extensions: { golem: golemBlock(this.options.manifest) },
    })
    if (request.keepalive === true) {
      const bytes = new TextEncoder().encode(payload).length
      if (bytes > KEEPALIVE_BODY_LIMIT) {
        throw new GolemKeepaliveTooLargeError(bytes)
      }
    }
    const response = await send(this.options.url, {
      method: 'POST',
      headers: { 'content-type': 'application/json', accept: 'application/json', ...credentials.headers },
      body: payload,
      signal: request.signal,
      ...(request.keepalive === true ? { keepalive: true } : {}),
    })
    const text = await response.text()
    let body: GraphQLResponseBody
    try {
      body = JSON.parse(text) as GraphQLResponseBody
    } catch {
      throw new GolemHttpError(response.status, `HTTP ${response.status} with a non-JSON body`)
    }
    const failure = failureOf(body)
    if (failure !== undefined) {
      throw failure
    }
    if (!response.ok) {
      throw new GolemHttpError(response.status, `HTTP ${response.status} without GraphQL errors`)
    }
    if (body.data === undefined) {
      throw new GolemProtocolError('the response carried neither data nor errors')
    }
    return body.data as T
  }
}

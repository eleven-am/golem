import type { DocumentNode } from 'graphql'
import type { QueryClient, QueryFunctionContext } from '@tanstack/query-core'
import { DocumentAnalyzer } from './analysis.js'
import { GolemConfigurationError } from './errors.js'
import { LiveSync } from './live.js'
import type { ClientManifest } from './manifest.js'
import { Mutations } from './mutations.js'
import type { CredentialsGetter, SocketFactory } from './session.js'
import { Transport } from './transport.js'

export interface GolemClientOptions {
  readonly queryClient: QueryClient
  readonly url: string
  readonly manifest: ClientManifest
  readonly credentials?: CredentialsGetter
  readonly createSocket?: SocketFactory
  readonly fetch?: typeof globalThis.fetch
}

const claimed = new WeakMap<QueryClient, GolemClient>()

export class GolemClient {
  readonly queryClient: QueryClient
  readonly transport: Transport
  readonly live: LiveSync
  readonly mutations: Mutations
  readonly analyzer: DocumentAnalyzer
  private holds = 0
  private startedByHolds = false

  constructor(options: GolemClientOptions) {
    if (options.queryClient === undefined || options.queryClient === null) {
      throw new GolemConfigurationError('GolemClient needs the application QueryClient: pass queryClient')
    }
    if (claimed.has(options.queryClient)) {
      throw new GolemConfigurationError('this QueryClient already belongs to a GolemClient; one QueryClient serves one GolemClient')
    }
    this.queryClient = options.queryClient
    this.transport = new Transport({
      url: options.url,
      manifest: options.manifest,
      credentials: options.credentials,
      createSocket: options.createSocket,
      fetch: options.fetch,
    })
    this.analyzer = new DocumentAnalyzer(options.manifest)
    this.live = new LiveSync({ queryClient: options.queryClient, transport: this.transport, manifest: options.manifest })
    this.mutations = new Mutations({ queryClient: options.queryClient, transport: this.transport, manifest: options.manifest })
    claimed.set(options.queryClient, this)
  }

  queryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}, operationName?: string) {
    return this.live.options(this.analyzer.analyze(document, operationName), variables)
  }

  infiniteQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}, operationName?: string) {
    return this.live.infiniteOptions(this.analyzer.analyze(document, operationName), variables)
  }

  staticQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}, operationName?: string) {
    const analysis = this.analyzer.analyze(document, operationName)
    return {
      queryKey: ['golem', 'static', analysis.query, variables] as const,
      queryFn: (context: Pick<QueryFunctionContext, 'signal'>) => this.transport.execute({ query: analysis.query, variables, signal: context.signal }),
    }
  }

  mutate<T = unknown>(document: DocumentNode | string, variables?: Readonly<Record<string, unknown>>, operationName?: string, signal?: AbortSignal): Promise<T> {
    return this.mutations.execute<T>(document, variables, operationName, signal)
  }

  attach(): () => void {
    this.holds++
    if (this.holds === 1 && !this.live.started) {
      this.live.start()
      this.startedByHolds = true
    }
    let released = false
    return () => {
      if (released) {
        return
      }
      released = true
      this.holds--
      if (this.holds === 0 && this.startedByHolds) {
        this.startedByHolds = false
        this.live.stop()
      }
    }
  }

  dispose(): void {
    this.live.stop()
    this.transport.dispose()
    if (claimed.get(this.queryClient) === this) {
      claimed.delete(this.queryClient)
    }
  }
}

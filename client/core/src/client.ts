import type { DocumentNode } from 'graphql'
import type { QueryClient } from '@tanstack/query-core'
import { DocumentAnalyzer } from './analysis.js'
import { GolemConfigurationError } from './errors.js'
import { LiveSync } from './live.js'
import { assertClientManifest, type ClientManifest } from './manifest.js'
import { Mutations, type MutateOptions } from './mutations.js'
import type { CredentialsGetter, SocketFactory } from './session.js'
import { Transport } from './transport.js'
import { sessionState } from './session-state.js'

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
  private readonly manifest: ClientManifest
  private holds = 0
  private startedByHolds = false

  constructor(options: GolemClientOptions) {
    assertClientManifest(options.manifest)
    if (options.queryClient === undefined || options.queryClient === null) {
      throw new GolemConfigurationError('GolemClient needs the application QueryClient: pass queryClient')
    }
    if (claimed.has(options.queryClient)) {
      throw new GolemConfigurationError('this QueryClient already belongs to a GolemClient; one QueryClient serves one GolemClient')
    }
    this.queryClient = options.queryClient
    this.manifest = options.manifest
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

  queryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.options(this.analyzer.analyze(document), variables)
  }

  infiniteQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.infiniteOptions(this.analyzer.analyze(document), variables)
  }

  staticQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.staticOptions(this.analyzer.analyzeStatic(document), variables)
  }

  staticInfiniteQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.staticInfiniteOptions(this.analyzer.analyze(document), variables)
  }

  continuationQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.continuationOptions(this.analyzer.analyze(document), variables)
  }

  staticContinuationQueryOptions(document: DocumentNode | string, variables: Readonly<Record<string, unknown>> = {}) {
    return this.live.staticContinuationOptions(this.analyzer.analyzeStatic(document), variables)
  }

  // Update the credentials callback's source first, then reset on logout or
  // principal/profile changes. No cached Golem data crosses that boundary.
  ready(): Promise<void> { return this.transport.ready() }

  // Read-only lifecycle notification for adapters retaining operation results.
  // The session owner alone changes identity; no epoch or reset controls leak.
  onSessionReset(listener: () => void): () => void {
    return sessionState(this.transport).onReset(listener)
  }

  resetSession(): Promise<void> {
    return this.live.resetSession()
  }

  mutate<T = unknown>(document: DocumentNode | string, variables?: Readonly<Record<string, unknown>>, options?: AbortSignal | MutateOptions): Promise<T> {
    return this.mutations.execute<T>(document, variables, options)
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
    this.live.dispose()
    this.transport.dispose()
    if (claimed.get(this.queryClient) === this) {
      claimed.delete(this.queryClient)
    }
  }
}

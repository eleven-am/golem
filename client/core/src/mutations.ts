import type { DocumentNode } from 'graphql'
import type { QueryClient } from '@tanstack/query-core'
import { analyzeMutation, type MutationAnalysis } from './analysis.js'
import { GolemCommitUnknownError, GolemPartialCommitError, GolemSessionError, GolemError } from './errors.js'
import { sessionState, type SessionStamp, type SessionState } from './session-state.js'
import { readGolemMeta } from './live.js'
import type { ClientManifest } from './manifest.js'
import type { ExecuteRequest } from './transport.js'

export interface MutateOptions {
  readonly signal?: AbortSignal
  readonly keepalive?: boolean
}

function isAbortSignal(value: AbortSignal | MutateOptions | undefined): value is AbortSignal {
  return value !== undefined && typeof (value as AbortSignal).aborted === 'boolean' && typeof (value as AbortSignal).addEventListener === 'function'
}

export interface MutationTransport {
  execute<T = unknown>(request: ExecuteRequest): Promise<T>
}

export interface MutationsOptions {
  readonly queryClient: QueryClient
  readonly transport: MutationTransport
  readonly manifest: ClientManifest
}

// A prepared operation captures its session and owned JSON arguments at the
// caller's invocation, before an external scheduler can pause it. Single use.
export type PreparedMutation<T> = () => Promise<T>

export class Mutations {
  private readonly client: QueryClient
  private readonly transport: MutationTransport
  private readonly manifest: ClientManifest
  private readonly state: SessionState

  constructor(options: MutationsOptions) {
    this.client = options.queryClient
    this.transport = options.transport
    this.manifest = options.manifest
    this.state = sessionState(options.transport, options.manifest)
    this.observeRetainedResults()
  }

  private observeRetainedResults(): void {
    const cache = this.client.getMutationCache()
    const stamps = new WeakMap<object, SessionStamp>()
    const owned = (record: { readonly meta?: Readonly<Record<string, unknown>> }): boolean => {
      const marker = record.meta?.golem
      return typeof marker === 'object' && marker !== null && 'kind' in marker && marker.kind === 'mutation'
    }
    for (const record of cache.getAll()) if (owned(record)) stamps.set(record, this.state.capture())
    let stop: (() => void) | undefined
    const releaseWhenSettled = () => {
      if (this.state.disposed && !cache.getAll().some((record) => stamps.has(record) && record.state.status === 'pending')) {
        stop?.()
        stop = undefined
      }
    }
    stop = cache.subscribe((event) => {
      const record = event.mutation
      if (record === undefined) return
      if (event.type === 'added' && !this.state.disposed && owned(record)) stamps.set(record, this.state.capture())
      const stamp = stamps.get(record)
      if (event.type === 'removed') stamps.delete(record)
      else if (stamp !== undefined && record.state.status !== 'pending' && !this.state.current(stamp)) cache.remove(record)
      releaseWhenSettled()
    })
    this.state.onReset(() => {
      // Pending records remain in TanStack until settlement: removing one now
      // would let a same-scope successor dispatch before its predecessor ends.
      for (const record of cache.getAll()) {
        if (stamps.has(record) && record.state.status !== 'pending') cache.remove(record)
      }
      releaseWhenSettled()
    })
  }

  async execute<T = unknown>(document: DocumentNode | string, variables?: Readonly<Record<string, unknown>>, options?: AbortSignal | MutateOptions): Promise<T> {
    return this.prepare<T>(document, variables, options)()
  }

  prepare<T = unknown>(document: DocumentNode | string, variables?: Readonly<Record<string, unknown>>, options?: AbortSignal | MutateOptions): PreparedMutation<T> {
    const stamp = this.state.mutation()
    const { signal, keepalive } = isAbortSignal(options) ? { signal: options, keepalive: undefined } : (options ?? {})
    const analysis = analyzeMutation(document, this.manifest)
    const owned = variables === undefined ? undefined : JSON.parse(JSON.stringify(variables)) as Readonly<Record<string, unknown>>
    this.state.assert(stamp)
    let used = false
    return async () => {
      if (used) throw new GolemError({ message: 'a prepared mutation can execute only once', extensions: { code: 'GOLEM_MUTATION_REPLAY_REFUSED' } }, false)
      used = true
      this.state.assert(stamp)
      let data: T
      try {
        data = await this.transport.execute<T>({ query: analysis.query, variables: owned, signal, keepalive })
      } catch (error) {
        if (!this.state.current(stamp)) {
          if (error instanceof GolemPartialCommitError || error instanceof GolemCommitUnknownError) throw error
          if (error instanceof GolemSessionError && !error.dispatched) throw error
          throw this.unknownSession()
        }
        if (error instanceof GolemPartialCommitError || error instanceof GolemCommitUnknownError) this.invalidateCommitted(analysis, stamp)
        throw error
      }
      // A reset can run after Transport resolves but before this continuation.
      // No old success or invalidation may cross into the replacement session.
      if (!this.state.current(stamp)) throw this.unknownSession()
      this.invalidateCommitted(analysis, stamp)
      if (!this.state.current(stamp)) throw this.unknownSession()
      return data
    }
  }

  private unknownSession(): GolemCommitUnknownError {
    const failure = new GolemError({ message: 'the session changed after dispatch; the mutation may have committed and must not be replayed automatically', extensions: { code: 'GOLEM_SESSION_CHANGED' } }, false)
    return new GolemCommitUnknownError(failure, [failure], undefined)
  }

  private invalidateCommitted(analysis: MutationAnalysis, stamp: SessionStamp): void {
    const unscoped = analysis.roots.some((root) => root.mutationScope.kind === 'unscoped')
    const models = new Set(analysis.roots.flatMap((root) => root.mutationScope.kind === 'models' ? root.mutationScope.models : []))
    const selected = new Set(this.client.getQueryCache().getAll().filter((query) => {
      const dependencies = readGolemMeta(query)?.analysis.dependencies
      return dependencies !== undefined && (unscoped || dependencies.opaque || dependencies.models.some((model) => models.has(model)))
    }))
    // Route each commit through its existing read owner before TanStack marks
    // stale/refetches. This also fences repeated writes while already stale;
    // Query.invalidate() alone emits no second event in that interval.
    for (const query of selected) {
      if (!this.state.current(stamp)) return
      readGolemMeta(query)?.invalidate(query)
      // Cache notifications and refetch admission can synchronously reset the
      // session. Finish one query before admitting the next old invalidation.
      if (!this.state.current(stamp)) return
      void this.client.invalidateQueries({ queryKey: query.queryKey, exact: true, predicate: (candidate) => candidate === query && this.state.current(stamp) })
    }
  }
}

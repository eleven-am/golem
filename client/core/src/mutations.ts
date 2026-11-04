import type { DocumentNode } from 'graphql'
import type { Query, QueryClient } from '@tanstack/query-core'
import { analyzeMutation, type MutationAnalysis } from './analysis.js'
import { GolemCommitUnknownError, GolemPartialCommitError } from './errors.js'
import { readMeta } from './live.js'
import type { ClientManifest } from './manifest.js'
import { parseVersion } from './merge.js'
import { Patcher, type RowKey } from './patcher.js'
import type { ExecuteRequest } from './transport.js'

export interface MutationTransport {
  execute<T = unknown>(request: ExecuteRequest): Promise<T>
}

export interface MutationsOptions {
  readonly queryClient: QueryClient
  readonly transport: MutationTransport
  readonly manifest: ClientManifest
}

interface Held {
  readonly query: Query
  readonly data: unknown
}

export class Mutations {
  private readonly client: QueryClient
  private readonly transport: MutationTransport
  private readonly manifest: ClientManifest
  private readonly patcher: Patcher

  constructor(options: MutationsOptions) {
    this.client = options.queryClient
    this.transport = options.transport
    this.manifest = options.manifest
    this.patcher = new Patcher(options.manifest)
  }

  async execute<T = unknown>(document: DocumentNode | string, variables?: Readonly<Record<string, unknown>>, signal?: AbortSignal): Promise<T> {
    const analysis = analyzeMutation(document, this.manifest)
    let data: T
    try {
      data = await this.transport.execute<T>({ query: analysis.query, variables, signal })
    } catch (error) {
      if (error instanceof GolemPartialCommitError || error instanceof GolemCommitUnknownError) {
        this.invalidateCommitted(analysis)
      }
      throw error
    }
    this.absorb(analysis, data)
    return data
  }

  private absorb(analysis: MutationAnalysis, data: unknown): void {
    if (typeof data !== 'object' || data === null) {
      return
    }
    const result = data as Record<string, unknown>
    for (const root of analysis.roots) {
      const value = result[root.key]
      if (value === null || value === undefined) {
        continue
      }
      switch (root.effect) {
        case 'merge':
          this.merge(value)
          break
        case 'delete':
          this.drop(value)
          break
        case 'invalidate':
          this.invalidate(root.models)
          break
        default:
      }
    }
  }

  private held(): Held[] {
    const found: Held[] = []
    for (const query of this.client.getQueryCache().getAll()) {
      if (readMeta(query) !== undefined && query.state.data !== undefined) {
        found.push({ query, data: query.state.data })
      }
    }
    return found
  }

  private merge(value: unknown): void {
    const returned = this.versioned(this.patcher.rows(value))
    for (const { query, data } of this.held()) {
      const cached = this.patcher.versions(data)
      let next = data
      for (const [key, row] of returned.rows) {
        const version = returned.versions.get(key)
        const have = cached.get(key)
        if (version !== undefined && have !== undefined && version > have) {
          next = this.patcher.patch(next, key, row)
        }
      }
      if (next !== data) {
        this.client.setQueryData(query.queryKey, next)
      }
      for (const [key, version] of returned.versions) {
        if (this.patcher.lags(next, key, version) && cached.has(key)) {
          void this.client.invalidateQueries({ queryKey: query.queryKey, exact: true })
          break
        }
      }
    }
  }

  private drop(value: unknown): void {
    const row = value as Record<string, unknown>
    const key = typeof row.__typename === 'string' ? this.patcher.keyOf(row.__typename, row) : undefined
    const version = typeof row._version === 'string' ? parseVersion(row._version) : undefined
    if (key === undefined || version === undefined) {
      return
    }
    for (const { query, data } of this.held()) {
      const have = this.patcher.versions(data).get(key)
      const nullable = readMeta(query)?.analysis.nullableRoots
      if (have === undefined || have > version || nullable === undefined) {
        continue
      }
      const removal = this.patcher.remove(data, new Set<RowKey>([key]), nullable)
      if (removal.data !== data) {
        this.client.setQueryData(query.queryKey, removal.data)
      }
      if (removal.held.size > 0) {
        void this.client.invalidateQueries({ queryKey: query.queryKey, exact: true })
      }
    }
  }

  private invalidateCommitted(analysis: MutationAnalysis): void {
    const unscoped = analysis.roots.some((root) => root.models.length === 0 && !root.name.startsWith('__'))
    if (unscoped) {
      void this.client.invalidateQueries({ predicate: (query) => readMeta(query) !== undefined })
      return
    }
    this.invalidate(analysis.roots.flatMap((root) => root.models))
  }

  private invalidate(models: ReadonlyArray<string>): void {
    if (models.length === 0) {
      return
    }
    void this.client.invalidateQueries({
      predicate: (query) => readMeta(query)?.analysis.streams.some((stream) => models.includes(stream.model)) === true,
    })
  }

  private versioned(rows: ReadonlyMap<RowKey, Record<string, unknown>>) {
    const versions = new Map<RowKey, bigint>()
    for (const [key, row] of rows) {
      if (typeof row._version === 'string') {
        versions.set(key, parseVersion(row._version))
      }
    }
    return { rows, versions }
  }
}

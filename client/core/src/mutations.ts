import type { DocumentNode } from 'graphql'
import type { QueryClient } from '@tanstack/query-core'
import { analyzeMutation, type MutationAnalysis } from './analysis.js'
import { GolemCommitUnknownError, GolemPartialCommitError } from './errors.js'
import { readMeta } from './live.js'
import type { ClientManifest } from './manifest.js'
import type { ExecuteRequest } from './transport.js'

export interface MutationTransport {
  execute<T = unknown>(request: ExecuteRequest): Promise<T>
}

export interface MutationsOptions {
  readonly queryClient: QueryClient
  readonly transport: MutationTransport
  readonly manifest: ClientManifest
}

export class Mutations {
  private readonly client: QueryClient
  private readonly transport: MutationTransport
  private readonly manifest: ClientManifest

  constructor(options: MutationsOptions) {
    this.client = options.queryClient
    this.transport = options.transport
    this.manifest = options.manifest
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
    this.invalidateCommitted(analysis)
    return data
  }

  private invalidateCommitted(analysis: MutationAnalysis): void {
    const unscoped = analysis.roots.some((root) => root.models.length === 0)
    if (unscoped) {
      void this.client.invalidateQueries({ predicate: (query) => readMeta(query) !== undefined })
      return
    }
    const models = analysis.roots.flatMap((root) => root.models)
    if (models.length === 0) {
      return
    }
    void this.client.invalidateQueries({
      predicate: (query) => readMeta(query)?.analysis.streams.some((stream) => models.includes(stream.model)) === true,
    })
  }
}

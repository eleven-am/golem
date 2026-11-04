import { QueryClient, QueryObserver, type Query } from '@tanstack/query-core'
import { DocumentAnalyzer, LiveSync, Transport, type Credentials, type DocumentAnalysis, type LiveTransport } from '../../src/index.js'
import { startProxy, type TcpProxy } from './proxy.js'
import { actor, CREATE_POST, headerSocket, manifest, postInput, signUp, socialUrl } from './social.js'

export const analyzer = new DocumentAnalyzer(manifest)

export const LIST = 'query($t: String!) { posts(where: { title: { contains: $t } }) { title } }'

export const POST_WITH_COMMENTS = 'query($id: UUID!) { post(where: { ID: $id }) { title comments { body } } }'

export const UPDATE_POST = 'mutation($id: UUID!, $data: PostUpdateInput!) { updatePost(where: { ID: $id }, data: $data) { id } }'

export const DELETE_POST = 'mutation($id: UUID!) { deletePost(where: { ID: $id }) { id } }'

export const DELETE_COMMENT = 'mutation($id: UUID!) { deleteComment(where: { ID: $id }) { id } }'

export const CREATE_COMMENT =
  'mutation($id: UUID!, $post: UUID!, $author: UUID!, $body: String!) { createComment(data: {id: $id, body: $body, post: {connect: {ID: $post}}, author: {connect: {ID: $author}}}) { id } }'

export const sleep = (milliseconds: number) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds))

export async function until(predicate: () => boolean, milliseconds = 10_000, label = 'condition'): Promise<void> {
  const deadline = Date.now() + milliseconds
  while (!predicate()) {
    if (Date.now() > deadline) {
      throw new Error(`${label} not reached in ${milliseconds} ms`)
    }
    await sleep(15)
  }
}

export class HttpGate {
  calls: Array<{ query: string; variables: Record<string, unknown> | undefined }> = []
  private holding = false
  private waiting: Array<() => void> = []

  private readonly target: URL

  constructor(target: string) {
    this.target = new URL(target)
  }

  readonly fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input))
    url.protocol = this.target.protocol
    url.host = this.target.host
    if (typeof init?.body === 'string') {
      const body = JSON.parse(init.body) as { query: string; variables?: Record<string, unknown> }
      this.calls.push({ query: body.query, variables: body.variables })
    }
    const response = await globalThis.fetch(url, init)
    if (this.holding) {
      await new Promise<void>((resolve, reject) => {
        this.waiting.push(resolve)
        init?.signal?.addEventListener('abort', () => reject(init.signal?.reason), { once: true })
      })
    }
    return response
  }

  hold(): void {
    this.holding = true
  }

  release(): void {
    this.holding = false
    const waiting = this.waiting
    this.waiting = []
    for (const resolve of waiting) {
      resolve()
    }
  }

  count(analysis: DocumentAnalysis, variables: Record<string, unknown>): number {
    return this.calls.filter((call) => call.query === analysis.query && JSON.stringify(call.variables) === JSON.stringify(variables)).length
  }
}

export interface World {
  readonly who: { readonly id: string; readonly credentials: () => Credentials }
  readonly gate: HttpGate
  readonly proxy: TcpProxy
  readonly queryClient: QueryClient
  readonly transport: Transport
  readonly live: LiveSync
  readonly direct: Transport
  run<T>(query: string, variables?: Record<string, unknown>): Promise<T>
  createPost(title: string, published?: boolean): Promise<string>
  updatePost(id: string, data: Record<string, unknown>): Promise<void>
  close(): Promise<void>
}

export async function world(overrides: { credentials?: () => Credentials | Promise<Credentials>; credentialsFor?: (who: { id: string }) => () => Credentials | Promise<Credentials>; url?: string; clock?: () => number; windowMs?: number; maxWaitMs?: number; closeGraceMs?: number; wrap?: (transport: Transport) => LiveTransport } = {}): Promise<World> {
  const who = actor()
  const target = new URL(overrides.url ?? socialUrl())
  const gate = new HttpGate(target.origin)
  const proxy = await startProxy(Number(target.port))
  const credentials = overrides.credentials ?? overrides.credentialsFor?.(who) ?? who.credentials
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const transport = new Transport({
    url: `http://127.0.0.1:${proxy.port}/graphql`,
    manifest,
    credentials,
    createSocket: headerSocket,
    fetch: gate.fetch,
  })
  const direct = new Transport({ url: `${target.origin}/graphql`, manifest, credentials, createSocket: headerSocket })
  const live = new LiveSync({
    queryClient,
    transport: overrides.wrap?.(transport) ?? transport,
    manifest,
    clock: overrides.clock,
    windowMs: overrides.windowMs,
    maxWaitMs: overrides.maxWaitMs,
    closeGraceMs: overrides.closeGraceMs,
  })
  live.start()
  await signUp(direct, who)
  const run = <T>(query: string, variables?: Record<string, unknown>) => direct.execute<T>({ query, variables })
  return {
    who,
    gate,
    proxy,
    queryClient,
    transport,
    live,
    direct,
    run,
    async createPost(title, published = true) {
      const data = await run<{ createPost: { id: string } }>(CREATE_POST, { data: { ...postInput(title), published } })
      return data.createPost.id
    },
    async updatePost(id, data) {
      await run(UPDATE_POST, { id, data })
    },
    async close() {
      live.stop()
      queryClient.clear()
      transport.dispose()
      direct.dispose()
      await proxy.close()
    },
  }
}

export interface Watch {
  readonly analysis: DocumentAnalysis
  readonly variables: Record<string, unknown>
  fetches(): number
  readonly query: Query
  readonly frames: unknown[]
  data(): unknown
  stop(): void
}

export function watch(w: World, document: string, variables: Record<string, unknown>): Watch {
  const analysis = analyzer.analyze(document)
  const options = w.live.options(analysis, variables)
  const observer = new QueryObserver(w.queryClient, options)
  const frames: unknown[] = []
  const stop = observer.subscribe((result) => {
    if (result.data !== undefined && frames.at(-1) !== result.data) {
      frames.push(result.data)
    }
  })
  const query = w.queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })
  if (query === undefined) {
    throw new Error('the query was not created')
  }
  return { analysis, variables, fetches: () => w.gate.count(analysis, variables), query, frames, data: () => observer.getCurrentResult().data, stop }
}

export function titles(data: unknown): string[] {
  const posts = (data as { posts?: Array<{ title: string }> } | undefined)?.posts ?? []
  return posts.map((post) => post.title).sort()
}

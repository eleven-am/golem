import { StrictMode, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Transport, GolemClient } from '@eleven-am/golem-client'
import { actor, headerSocket, manifest, signUp, socialUrl } from '../../core/test/harness/social.js'
import { CREATE_POST, postInput } from '../../core/test/harness/social.js'

export { manifest }

export const sleep = (milliseconds: number) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds))

export async function until(predicate: () => boolean, milliseconds = 20_000, label = 'condition'): Promise<void> {
  const deadline = Date.now() + milliseconds
  while (!predicate()) {
    if (Date.now() > deadline) {
      throw new Error(`${label} not reached in ${milliseconds} ms`)
    }
    await sleep(15)
  }
}

export async function settled(one: Screen): Promise<void> {
  await until(
    () =>
      one.queryClient.isFetching() === 0 &&
      one.queryClient
        .getQueryCache()
        .getAll()
        .every((query) => !one.client.live['hasQueuedRefetch'](query)),
    20_000,
    'screen settled',
  )
}

export const UPDATE_POST = 'mutation($id: UUID!, $data: PostUpdateInput!) { updatePost(where: { ID: $id }, data: $data) { id } }'

export const DELETE_POST = 'mutation($id: UUID!) { deletePost(where: { ID: $id }) { id } }'

export interface Screen {
  readonly container: HTMLElement
  readonly root: Root
  readonly client: GolemClient
  readonly queryClient: QueryClient
  readonly direct: Transport
  readonly who: ReturnType<typeof actor>
  render(node: ReactNode, options?: { strict?: boolean }): void
  text(): string
  createPost(title: string, published?: boolean): Promise<string>
  run<T>(query: string, variables?: Record<string, unknown>): Promise<T>
  close(): Promise<void>
}

export async function screen(): Promise<Screen> {
  const who = actor()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const credentials = who.credentials
  const client = new GolemClient({ queryClient, url: `${socialUrl()}/graphql`, manifest, credentials, createSocket: headerSocket })
  const direct = new Transport({ url: `${socialUrl()}/graphql`, manifest, credentials, createSocket: headerSocket })
  await signUp(direct, who)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const run = <T,>(query: string, variables?: Record<string, unknown>) => direct.execute<T>({ query, variables })
  return {
    container,
    root,
    client,
    queryClient,
    direct,
    who,
    render(node, options = {}) {
      const tree = <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
      root.render(options.strict === true ? <StrictMode>{tree}</StrictMode> : tree)
    },
    text: () => container.textContent ?? '',
    run,
    async createPost(title, published = true) {
      const data = await run<{ createPost: { id: string } }>(CREATE_POST, { data: { ...postInput(title), published } })
      return data.createPost.id
    },
    async close() {
      root.unmount()
      client.dispose()
      direct.dispose()
      queryClient.clear()
      container.remove()
    },
  }
}

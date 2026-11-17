import { QueryClient, QueryClientProvider, type InfiniteData } from '@tanstack/react-query'
import { createRoot } from 'react-dom/client'
import { buildSchema, parse, validate } from 'graphql'
import { initGraphQLTada, type ResultOf, type VariablesOf } from 'gql.tada'
import { expect, expectTypeOf, it, vi } from 'vitest'
import { CONTINUATION_RESPONSE_KEY, GolemClient, GolemError, OPERATION_INVALIDATION_CAPABILITY, type StreamHandlers, type ContinuationPageParam } from '@eleven-am/golem-client'
import { GolemProvider, useGolemContinuationQuery, useGolemStaticContinuationQuery, type ResultFor, type VariablesFor } from '../src/index.js'
import { continuationManifest as manifest, continuationSDL } from '../../core/test/fixtures/continuation.js'
import { until } from './harness.js'

// Minimal type-only introspection for the isolated transport SDL. Actual Go
// generation, authorization and stable ordering are backend acceptance gates.
type Ref<K extends 'OBJECT' | 'SCALAR', N extends string> = { kind: K; name: N; ofType: null }
type Required<T> = { kind: 'NON_NULL'; name: never; ofType: T }
type List<T> = { kind: 'LIST'; name: never; ofType: T }
type Field<N extends string, T> = { name: N; type: T }
type PageIntrospection = {
  name: never; query: 'Query'; mutation: never; subscription: never
  types: {
    Int: unknown; String: unknown
    Query: { kind: 'OBJECT'; name: 'Query'; fields: { homeFeed: Field<'homeFeed', Required<Ref<'OBJECT', 'HomeFeedPage'>>> } }
    HomeFeedPage: { kind: 'OBJECT'; name: 'HomeFeedPage'; fields: {
      items: Field<'items', Required<List<Required<Ref<'OBJECT', 'HomeSegment'>>>>>
      next: Field<'next', Ref<'SCALAR', 'String'>>
    } }
    HomeSegment: { kind: 'OBJECT'; name: 'HomeSegment'; fields: {
      kind: Field<'kind', Required<Ref<'SCALAR', 'String'>>>
      index: Field<'index', Required<Ref<'SCALAR', 'Int'>>>
    } }
  }
}
const graphql = initGraphQLTada<{ introspection: PageIntrospection }>()
const HOME = graphql(`query Feed($seed: Int!, $take: Int!) {
  feed: homeFeed(seed: $seed, take: $take) { items { kind index } }
}`)
const schema = buildSchema(continuationSDL)

function TypeChecks() {
  // @ts-expect-error the live continuation API retains generated variable types
  useGolemContinuationQuery(HOME, { seed: '7', take: 25 })
  // @ts-expect-error the live continuation planner owns initialPageParam
  useGolemContinuationQuery(HOME, { seed: 7, take: 25 }, { initialPageParam: 'owned' })
  // @ts-expect-error the coordinator owns live continuation commit fencing
  useGolemContinuationQuery(HOME, { seed: 7, take: 25 }, { structuralSharing: false })
  // @ts-expect-error generated Int variables remain numbers
  useGolemStaticContinuationQuery(HOME, { seed: '7', take: 25 })
  // @ts-expect-error seed is required by the document
  useGolemStaticContinuationQuery(HOME, { take: 25 })
  // @ts-expect-error the continuation planner owns initialPageParam
  useGolemStaticContinuationQuery(HOME, { seed: 7, take: 25 }, { initialPageParam: 'owned' })
  // @ts-expect-error static continuation queries cannot acquire live metadata
  useGolemStaticContinuationQuery(HOME, { seed: 7, take: 25 }, { meta: { golem: {} } })
  // @ts-expect-error the read coordinator owns static page cache commits
  useGolemStaticContinuationQuery(HOME, { seed: 7, take: 25 }, { structuralSharing: false })
  return null
}
void TypeChecks

function Feed({ revision }: { revision: number }) {
  const query = useGolemStaticContinuationQuery(HOME, { seed: 7, take: 25 }, { staleTime: Infinity })
  expectTypeOf(query.data).toEqualTypeOf<InfiniteData<ResultOf<typeof HOME>, string | null> | undefined>()
  expectTypeOf(query.data?.pageParams).toEqualTypeOf<Array<ContinuationPageParam> | undefined>()
  // @ts-expect-error the injected metadata is not published in typed results
  void query.data?.pages[0]?.feed._golemNext
  return <main data-revision={revision}>
    {query.isError && <p data-error>{query.error.message}</p>}
    <p data-lengths>{query.data?.pages.map((page) => page.feed.items.length).join('/')}</p>
    <button disabled={!query.hasNextPage || query.isFetchingNextPage} onClick={() => { void query.fetchNextPage() }}>more</button>
  </main>
}

it('preserves gql.tada results and variables while exposing string continuation parameters', () => {
  expectTypeOf<ResultFor<typeof HOME>>().toEqualTypeOf<ResultOf<typeof HOME>>()
  expectTypeOf<VariablesFor<typeof HOME>>().toEqualTypeOf<VariablesOf<typeof HOME>>()
  expectTypeOf<VariablesFor<typeof HOME>>().toEqualTypeOf<{ seed: number; take: number }>()
  expectTypeOf<ResultFor<typeof HOME>['feed']['items'][number]>().toEqualTypeOf<{ kind: string; index: number }>()
  expectTypeOf<ReturnType<typeof useGolemStaticContinuationQuery<typeof HOME>>['data']>().toEqualTypeOf<InfiniteData<ResultOf<typeof HOME>, string | null> | undefined>()
  expectTypeOf<ReturnType<typeof useGolemContinuationQuery<typeof HOME>>['data']>().toEqualTypeOf<InfiniteData<ResultOf<typeof HOME>, string | null> | undefined>()
})

it('renders empty nonterminal pages and stops at null next without row streams', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const socket = vi.fn(() => { throw new Error('static continuation hook opened a stream') })
  const sent = vi.fn<typeof fetch>(async (_url, init) => {
    const payload = JSON.parse(String(init?.body))
    expect(validate(schema, parse(payload.query))).toEqual([])
    expect(payload.extensions.golem.manifestVersion).toBe(3)
    const token = payload.variables.golemAfter
    const items = token === 'second' ? [{ kind: 'visible', index: 27 }] : []
    const next = token === undefined ? 'second' : token === 'second' ? 'third' : null
    return new Response(JSON.stringify({ data: { feed: { items, [CONTINUATION_RESPONSE_KEY]: next } } }))
  })
  const client = new GolemClient({ queryClient, url: 'http://continuation-hook.test/graphql', manifest, fetch: sent, createSocket: socket })
  const options = vi.spyOn(client, 'staticContinuationQueryOptions')
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const render = (revision: number) => root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><Feed revision={revision} /></GolemProvider></QueryClientProvider>)
  try {
    render(0)
    await until(() => container.querySelector('[data-lengths]')?.textContent === '0', 5_000, 'empty continuation page')
    const next = () => container.querySelector<HTMLButtonElement>('button')
    expect(next()?.disabled).toBe(false)
    next()?.click()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '0/1', 5_000, 'short continuation page')
    next()?.click()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '0/1/0', 5_000, 'terminal continuation page')
    expect(next()?.disabled).toBe(true)
    expect(container.querySelector('[data-error]')).toBeNull()
    render(1)
    await until(() => container.querySelector('main')?.getAttribute('data-revision') === '1', 5_000, 'same variables rerender')
    expect(options).toHaveBeenCalledTimes(1)
    expect(sent).toHaveBeenCalledTimes(3)
    expect(socket).not.toHaveBeenCalled()
    for (const query of queryClient.getQueryCache().getAll()) {
      expect(query.meta?.golem).toMatchObject({ mode: 'static' })
      expect(client.live.statusOf(query)).toBeUndefined()
    }
  } finally {
    root.unmount(); options.mockRestore(); client.dispose(); queryClient.clear(); container.remove()
  }
})


function LiveFeed({ revision }: { revision: number }) {
  const query = useGolemContinuationQuery(HOME, { seed: 7, take: 25 }, { staleTime: Infinity })
  expectTypeOf(query.data).toEqualTypeOf<InfiniteData<ResultOf<typeof HOME>, string | null> | undefined>()
  return <main data-revision={revision}>
    {query.isError && <p data-error>{query.error.message}</p>}
    <p data-lengths>{query.data?.pages.map((page) => page.feed.items.length).join('/')}</p>
    <p data-kinds>{query.data?.pages.flatMap((page) => page.feed.items.map((item) => item.kind)).join(',')}</p>
    <button disabled={!query.hasNextPage || query.isFetchingNextPage} onClick={() => { void query.fetchNextPage() }}>more</button>
  </main>
}

it('restarts declared live pages after a terminal-only invalidation and new ack', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const opted = { ...manifest, requiredCapabilities: [OPERATION_INVALIDATION_CAPABILITY], roots: {
    homeFeed: { ...manifest.roots.homeFeed, invalidation: { kind: 'operation' as const, subscriptionField: 'homeFeedChanges' } },
  } }
  let revision = 0
  const sent = vi.fn<typeof fetch>(async (_url, init) => {
    const payload = JSON.parse(String(init?.body))
    expect(payload.extensions.golem.capabilities).toEqual([OPERATION_INVALIDATION_CAPABILITY])
    return new Response(JSON.stringify({ data: { feed: { items: [{ kind: `revision-${revision}`, index: 0 }], [CONTINUATION_RESPONSE_KEY]: payload.variables.golemAfter === undefined ? `next-${revision}` : null } } }))
  })
  const client = new GolemClient({ queryClient, url: 'http://live-page-hook.test/graphql', manifest: opted, fetch: sent })
  const streams: StreamHandlers[] = []
  const subscribe = vi.spyOn(client.transport, 'subscribe').mockImplementation((request, handlers) => {
    expect(request.query).toContain('homeFeedChanges(seed: $seed)')
    expect(request.query).not.toContain('take:')
    streams.push(handlers)
    return () => undefined
  })
  const options = vi.spyOn(client, 'continuationQueryOptions')
  const container = document.createElement('div'); document.body.appendChild(container)
  const root = createRoot(container)
  const render = (version: number) => root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><LiveFeed revision={version} /></GolemProvider></QueryClientProvider>)
  try {
    render(0)
    await until(() => streams.length === 1, 5_000, 'invalidation subscribe')
    expect(sent).not.toHaveBeenCalled()
    streams[0]!.onAck()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '1', 5_000, 'first live page')
    container.querySelector<HTMLButtonElement>('button')!.click()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '1/1', 5_000, 'second live page')
    const query = queryClient.getQueryCache().getAll()[0]!
    revision = 1
    streams[0]!.onEnd({ kind: 'error', errors: [new GolemError({ message: 'changed', extensions: { code: 'GOLEM_SUBSCRIPTION_RESYNC', reason: 'DEPENDENCY_CHANGED' } })] })
    expect(query.state.data).toBeUndefined()
    expect(client.live.statusOf(query)?.state).toBe('RECOVERING')
    await until(() => streams.length === 2, 5_000, 'replacement invalidation subscribe')
    expect(sent).toHaveBeenCalledTimes(2)
    streams[1]!.onAck()
    await until(() => container.querySelector('[data-kinds]')?.textContent === 'revision-1', 5_000, 'fresh population')
    expect(container.querySelector('[data-lengths]')?.textContent).toBe('1')
    expect(JSON.parse(String(sent.mock.calls.at(-1)?.[1]?.body)).variables).toEqual({ seed: 7, take: 25 })
    render(1)
    await until(() => container.querySelector('main')?.getAttribute('data-revision') === '1', 5_000, 'equal variables rerender')
    expect(options).toHaveBeenCalledTimes(1)
    expect(container.querySelector('[data-error]')).toBeNull()
  } finally {
    root.unmount(); options.mockRestore(); client.dispose(); subscribe.mockRestore(); queryClient.clear(); container.remove()
  }
})

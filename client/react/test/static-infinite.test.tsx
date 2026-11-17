import { readFileSync } from 'node:fs'
import { QueryClient, QueryClientProvider, type InfiniteData } from '@tanstack/react-query'
import { createRoot } from 'react-dom/client'
import { buildSchema, parse, validate } from 'graphql'
import { initGraphQLTada, type ResultOf, type VariablesOf } from 'gql.tada'
import { expect, expectTypeOf, it, vi } from 'vitest'
import { GolemClient, type Cursor } from '@eleven-am/golem-client'
import { GolemProvider, useGolemStaticInfiniteQuery, type GolemQueryOptions, type GolemInfiniteQueryOptions, type GolemContinuationQueryOptions, type ResultFor, type VariablesFor } from '../src/index.js'
import { golemClientManifest as manifest } from '../../core/test/fixtures/objects.manifest.js'
import type { introspection } from './fixtures/objects.introspection.js'

const schema = buildSchema(readFileSync(new URL('../../core/test/fixtures/objects.schema.graphql', import.meta.url), 'utf8'))
const graphql = initGraphQLTada<{ introspection: introspection; scalars: { UUID: string } }>()
const USERS = graphql(`query StaticUsers($take: Int!, $prefix: String!) {
  users(where: {name: {startsWith: $prefix}}, orderBy: [{id: asc}], take: $take) { id name }
}`)
const records = Array.from({ length: 60 }, (_, index) => ({
  id: `00000000-0000-0000-0000-${String(index + 1).padStart(12, '0')}`,
  name: `static-${String(index + 1).padStart(2, '0')}`,
}))

function TypeChecks() {
  // @ts-expect-error the generated Int variable remains a number
  useGolemStaticInfiniteQuery(USERS, { take: '25', prefix: 'static-' })
  // @ts-expect-error the generated query requires its filter variable
  useGolemStaticInfiniteQuery(USERS, { take: 25 })
  // @ts-expect-error the cursor planner owns the initial page parameter
  useGolemStaticInfiniteQuery(USERS, { take: 25, prefix: 'static-' }, { initialPageParam: null })
  // @ts-expect-error callers cannot register static queries as live cache entries
  useGolemStaticInfiniteQuery(USERS, { take: 25, prefix: 'static-' }, { meta: { golem: {} } })
  // @ts-expect-error the read coordinator owns the cache commit fence
  useGolemStaticInfiniteQuery(USERS, { take: 25, prefix: 'static-' }, { structuralSharing: false })
  return null
}
void TypeChecks

function Users({ revision }: { revision: number }) {
  // A fresh but equal variables object on every render must not rebuild the
  // pagination options or reset accumulated pages.
  const query = useGolemStaticInfiniteQuery(USERS, { take: 25, prefix: 'static-' }, { staleTime: Infinity })
  expectTypeOf(query.data).toEqualTypeOf<InfiniteData<ResultOf<typeof USERS>, Cursor | null> | undefined>()
  expectTypeOf(query.data?.pageParams).toEqualTypeOf<Array<Cursor | null> | undefined>()
  const pages = query.data?.pages ?? []
  return <main data-revision={revision}>
    {query.isError && <p data-error>{query.error.message}</p>}
    <p data-lengths>{pages.map((page) => page.users.length).join('/')}</p>
    <p data-count>{pages.reduce((count, page) => count + page.users.length, 0)}</p>
    <button disabled={!query.hasNextPage || query.isFetchingNextPage} onClick={() => { void query.fetchNextPage() }}>more</button>
  </main>
}

async function until(ready: () => boolean): Promise<void> {
  const deadline = Date.now() + 5_000
  while (!ready()) {
    if (Date.now() >= deadline) throw new Error('static infinite query did not settle')
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
}

it('keeps generated document results and variables inferred for the static hook', () => {
  type OwnsSharing<T> = 'structuralSharing' extends keyof T ? true : false
  expectTypeOf<OwnsSharing<GolemQueryOptions<unknown>>>().toEqualTypeOf<false>()
  expectTypeOf<OwnsSharing<GolemInfiniteQueryOptions<unknown>>>().toEqualTypeOf<false>()
  expectTypeOf<OwnsSharing<GolemContinuationQueryOptions<unknown>>>().toEqualTypeOf<false>()
  expectTypeOf<ResultFor<typeof USERS>>().toEqualTypeOf<ResultOf<typeof USERS>>()
  expectTypeOf<VariablesFor<typeof USERS>>().toEqualTypeOf<VariablesOf<typeof USERS>>()
  expectTypeOf<VariablesFor<typeof USERS>>().toEqualTypeOf<{ take: number; prefix: string }>()
  expectTypeOf<ReturnType<typeof useGolemStaticInfiniteQuery<typeof USERS>>['data']>().toEqualTypeOf<InfiniteData<ResultOf<typeof USERS>, Cursor | null> | undefined>()
})

it('renders 25/25/10 static pages without row streams or live cache metadata', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const socket = vi.fn(() => { throw new Error('a static hook opened a row stream') })
  const signals: Array<AbortSignal | null | undefined> = []
  const sent = vi.fn<typeof fetch>(async (_url, init) => {
    signals.push(init?.signal)
    const payload = JSON.parse(String(init?.body))
    expect(payload.extensions.golem).toEqual({ protocol: 1, contract: manifest.contract, manifestVersion: 2 })
    expect(validate(schema, parse(payload.query))).toEqual([])
    expect(payload.query).not.toContain('_version')
    expect(payload.variables.take).toBe(25)
    expect(payload.variables.prefix).toBe('static-')
    const cursor = payload.variables.golemCursor?.ID
    const start = cursor === undefined ? 0 : records.findIndex((row) => row.id === cursor) + payload.variables.golemSkip
    return new Response(JSON.stringify({ data: { users: records.slice(start, start + payload.variables.take) } }), { status: 200 })
  })
  const client = new GolemClient({ queryClient, url: 'http://static-hook.test/graphql', manifest, fetch: sent, createSocket: socket })
  const options = vi.spyOn(client, 'staticInfiniteQueryOptions')
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const render = (revision: number) => root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><Users revision={revision} /></GolemProvider></QueryClientProvider>)
  try {
    render(0)
    await until(() => container.querySelector('[data-lengths]')?.textContent === '25')
    const next = () => container.querySelector<HTMLButtonElement>('button')
    next()?.click()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '25/25')
    next()?.click()
    await until(() => container.querySelector('[data-lengths]')?.textContent === '25/25/10')
    expect(container.querySelector('[data-count]')?.textContent).toBe('60')
    expect(next()?.disabled).toBe(true)
    expect(container.querySelector('[data-error]')).toBeNull()
    expect(options).toHaveBeenCalledTimes(1)
    render(1)
    await until(() => container.querySelector('main')?.getAttribute('data-revision') === '1')
    expect(options).toHaveBeenCalledTimes(1)
    expect(sent).toHaveBeenCalledTimes(3)
    expect(sent.mock.calls.map(([, init]) => JSON.parse(String(init?.body)).variables)).toEqual([
      { take: 25, prefix: 'static-' },
      { take: 25, prefix: 'static-', golemCursor: { ID: records[24]?.id }, golemSkip: 1 },
      { take: 25, prefix: 'static-', golemCursor: { ID: records[49]?.id }, golemSkip: 1 },
    ])
    expect(signals).toHaveLength(3)
    expect(signals.every((signal) => signal instanceof AbortSignal && !signal.aborted)).toBe(true)
    expect(socket).not.toHaveBeenCalled()
    expect(queryClient.getQueryCache().getAll()).toHaveLength(1)
    for (const query of queryClient.getQueryCache().getAll()) {
      expect(query.meta?.golem).toMatchObject({ mode: 'static' })
      expect(client.live.statusOf(query)).toBeUndefined()
    }
  } finally {
    root.unmount()
    options.mockRestore()
    client.dispose()
    queryClient.clear()
    container.remove()
  }
})

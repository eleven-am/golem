import { readFileSync } from 'node:fs'
import { InfiniteQueryObserver, QueryClient, type InfiniteData } from '@tanstack/query-core'
import { buildSchema, parse, validate, type DocumentNode } from 'graphql'
import { describe, expect, expectTypeOf, it, vi } from 'vitest'
import { GolemClient, GolemConfigurationError, GolemNotLiveError, type Cursor } from '../src/index.js'
import { golemClientManifest as manifest } from './fixtures/objects.manifest.js'

const schema = buildSchema(readFileSync(new URL('./fixtures/objects.schema.graphql', import.meta.url), 'utf8'))
const DOCUMENT = `query StaticUsers($take: Int!, $prefix: String!) {
  users(where: {name: {startsWith: $prefix}}, orderBy: [{id: asc}], take: $take) { name }
}`
const records = Array.from({ length: 60 }, (_, index) => ({
  id: `00000000-0000-0000-0000-${String(index + 1).padStart(12, '0')}`,
  name: `static-${String(index + 1).padStart(2, '0')}`,
}))
type Page = { users: typeof records }

describe('static generated infinite queries', () => {
  it('pages 25/25/10 through HTTP without subscriptions or live cache metadata', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    const socket = vi.fn(() => { throw new Error('static pagination opened a socket') })
    const sent = vi.fn<typeof fetch>(async (url, init) => {
      expect(url).toBe('http://static.test/graphql')
      expect(init?.method).toBe('POST')
      expect(init?.headers).toMatchObject({ authorization: 'Bearer static-principal' })
      const payload = JSON.parse(String(init?.body))
      expect(payload.extensions.golem).toEqual({ protocol: 1, contract: manifest.contract, manifestVersion: 2 })
      expect(validate(schema, parse(payload.query))).toEqual([])
      expect(payload.query).not.toContain('_version')
      expect(payload.variables.prefix).toBe('static-')
      const cursor = payload.variables.golemCursor?.ID
      const start = cursor === undefined ? 0 : records.findIndex((row) => row.id === cursor) + payload.variables.golemSkip
      return new Response(JSON.stringify({ data: { users: records.slice(start, start + payload.variables.take) } }), { status: 200 })
    })
    const client = new GolemClient({
      queryClient, url: 'http://static.test/graphql', manifest,
      credentials: () => ({ headers: { authorization: 'Bearer static-principal' } }), fetch: sent, createSocket: socket,
    })
    const variables = { take: 25, prefix: 'static-' }
    const options = client.staticInfiniteQueryOptions(DOCUMENT, variables)
    expect(manifest.models.User.events).toBeNull()
    expect(() => client.infiniteQueryOptions(DOCUMENT, variables)).toThrow(GolemNotLiveError)
    expect(options.meta.golem.mode).toBe('static')
    expect(options.queryKey).toEqual(['golem', 'static', client.analyzer.analyze(DOCUMENT).query, variables, 'infinite'])
    expect(options.queryKey).not.toEqual(client.staticQueryOptions(DOCUMENT, variables).queryKey)
    expectTypeOf(options.initialPageParam).toEqualTypeOf<Cursor | null>()
    expectTypeOf(options.getNextPageParam).returns.toEqualTypeOf<Cursor | undefined>()
    expectTypeOf(client.staticInfiniteQueryOptions).parameters.toEqualTypeOf<[DocumentNode | string, Readonly<Record<string, unknown>>?]>()
    const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, Cursor | null>, readonly unknown[], Cursor | null>(queryClient, options)
    let unsubscribe: () => void = () => undefined
    let release: () => void = () => undefined
    try {
      // The first page does not require LiveSync to be attached at all.
      await observer.refetch()
      expect(client.live.started).toBe(false)
      // Observing the remaining pages while the ordinary live owner is
      // attached must still leave this static cache entry entirely alone.
      release = client.attach()
      unsubscribe = observer.subscribe(() => undefined)
      await observer.fetchNextPage()
      await observer.fetchNextPage()
      const result = observer.getCurrentResult()
      expect(result.data?.pages.map((page) => (page as Page).users.length)).toEqual([25, 25, 10])
      expect(result.data?.pages.flatMap((page) => (page as Page).users)).toEqual(records)
      expect(result.data?.pageParams).toEqual([null, { ID: records[24]?.id }, { ID: records[49]?.id }])
      expect(result.hasNextPage).toBe(false)
      await observer.fetchNextPage()
      expect(sent).toHaveBeenCalledTimes(3)
      expect(sent.mock.calls.map(([, init]) => JSON.parse(String(init?.body)).variables)).toEqual([
        variables,
        { ...variables, golemCursor: { ID: records[24]?.id }, golemSkip: 1 },
        { ...variables, golemCursor: { ID: records[49]?.id }, golemSkip: 1 },
      ])
      expect(variables).toEqual({ take: 25, prefix: 'static-' })
      expect(socket).not.toHaveBeenCalled()
      const cached = queryClient.getQueryCache().getAll()
      expect(cached).toHaveLength(1)
      expect(cached[0]?.meta?.golem).toMatchObject({ mode: 'static' })
      for (const query of cached) expect(client.live.statusOf(query)).toBeUndefined()
    } finally {
      unsubscribe()
      release()
      client.dispose()
      queryClient.clear()
    }
  })

  it('forwards the continuation request and abort signal through the real transport', async () => {
    const queryClient = new QueryClient()
    const controller = new AbortController()
    let entered!: () => void
    const fetching = new Promise<void>((resolve) => { entered = resolve })
    const sent = vi.fn<typeof fetch>(async (_url, init) => {
      expect(init?.signal).toBe(controller.signal)
      const payload = JSON.parse(String(init?.body))
      expect(validate(schema, parse(payload.query))).toEqual([])
      expect(payload.variables).toEqual({ take: 25, prefix: 'static-', golemCursor: { ID: records[24]?.id }, golemSkip: 1 })
      entered()
      return new Promise<Response>((_resolve, reject) => {
        controller.signal.addEventListener('abort', () => reject(controller.signal.reason), { once: true })
      })
    })
    const client = new GolemClient({ queryClient, url: 'http://static.test/graphql', manifest, fetch: sent })
    try {
      const options = client.staticInfiniteQueryOptions(DOCUMENT, { take: 25, prefix: 'static-' })
      const request = options.queryFn({ signal: controller.signal, pageParam: { ID: records[24]?.id } })
      await fetching
      const reason = new DOMException('static page canceled', 'AbortError')
      controller.abort(reason)
      await expect(request).rejects.toBe(reason)
      expect(sent).toHaveBeenCalledTimes(1)
    } finally {
      client.dispose()
      queryClient.clear()
    }
  })

  it('keeps cursor validation in the existing planner before sending HTTP', () => {
    const queryClient = new QueryClient()
    const sent = vi.fn<typeof fetch>()
    const client = new GolemClient({ queryClient, url: 'http://static.test/graphql', manifest, fetch: sent })
    try {
      expect(() => client.staticInfiniteQueryOptions(DOCUMENT, { take: 0, prefix: 'static-' })).toThrow(GolemConfigurationError)
      expect(() => client.staticInfiniteQueryOptions(DOCUMENT, { take: 25, prefix: 'static-', golemCursor: { ID: 'caller-owned' } })).toThrow(GolemConfigurationError)
      expect(sent).not.toHaveBeenCalled()
    } finally {
      client.dispose()
      queryClient.clear()
    }
  })
})

import { InfiniteQueryObserver, QueryClient, type InfiniteData } from '@tanstack/query-core'
import { buildSchema, parse, print, validate, visit } from 'graphql'
import { describe, expect, expectTypeOf, it, vi } from 'vitest'
import {
  assertClientManifest, CONTINUATION_RESPONSE_KEY, CONTINUATION_TOKEN_MAX_BYTES, CONTINUATION_VARIABLE,
  DocumentAnalyzer, GolemClient, GolemConfigurationError, GolemDocumentError, GolemProtocolError,
  golemBlock, golemContinuationQuery, type ClientManifest, type ContinuationPageParam, type StaticDocumentAnalysis,
} from '../src/index.js'
import { continuationManifest as manifest, continuationSDL } from './fixtures/continuation.js'

const schema = buildSchema(continuationSDL)
const analyzer = new DocumentAnalyzer(manifest)
const document = `query Feed($seed: Int!, $take: Int = 25) {
  feed: homeFeed(seed: $seed, take: $take) { ...Page }
} fragment Page on HomeFeedPage { items { ...Segment } }
fragment Segment on HomeSegment { kind index }`
const response = (next: unknown, items: unknown[] = []) => ({ feed: { items, [CONTINUATION_RESPONSE_KEY]: next } })

describe('custom continuation planning', () => {
  it('requires static analysis rather than accepting a live-enriched document', () => {
    expectTypeOf(golemContinuationQuery).parameter(0).toEqualTypeOf<StaticDocumentAnalysis>()
    const live = analyzer.analyze(document)
    // @ts-expect-error a live document analysis may contain injected projections
    expect(() => golemContinuationQuery(live, { seed: 7 }, manifest)).toThrow(GolemConfigurationError)
  })
  it('injects unconditional metadata into first and following requests, preserving aliases and fragments', () => {
    const analysis = analyzer.analyzeStatic(document)
    const plan = golemContinuationQuery(analysis, { seed: 7 }, manifest)
    expect(analysis.mode).toBe('static')
    for (const parameter of [null, 'opaque-next']) {
      const request = plan.requestFor(parameter)
      expect(validate(schema, parse(request.query))).toEqual([])
      expect(request.query).toContain('_golemNext: next')
      expect(request.query).toContain('feed: homeFeed')
      expect(request.query).toContain('...Page')
      expect(request.query).not.toContain('_version')
    }
    expect(plan.requestFor(null).variables).toEqual({ seed: 7 })
    expect(plan.requestFor(null).query).not.toContain('$golemAfter')
    expect(plan.requestFor('opaque-next').variables).toEqual({ seed: 7, golemAfter: 'opaque-next' })
    expect(plan.requestFor('opaque-next').query).toContain('$golemAfter: String')
    expect(analysis.query).not.toContain('_golemNext')
  })

  it('continues empty and short pages, stopping only at null next', () => {
    const plan = golemContinuationQuery(analyzer.analyzeStatic(document), { seed: 7 }, manifest)
    expect(plan.getNextPageParam(response('second'), [], null, [null])).toBe('second')
    expect(plan.getNextPageParam(response('third', [{ index: 2 }]), [], 'second', [null, 'second'])).toBe('third')
    expect(plan.getNextPageParam(response(null), [], 'third', [null, 'second', 'third'])).toBeUndefined()
    // No mutable planner state: another independent chain can reuse the token.
    expect(plan.getNextPageParam(response('second'), [], null, [null])).toBe('second')
  })

  it('rejects missing, malformed, oversized and repeated next tokens', () => {
    const plan = golemContinuationQuery(analyzer.analyzeStatic(document), { seed: 7 }, manifest)
    for (const next of [undefined, '', ' ', 3, {}, 'a'.repeat(CONTINUATION_TOKEN_MAX_BYTES + 1), 'é'.repeat(CONTINUATION_TOKEN_MAX_BYTES)]) {
      expect(() => plan.getNextPageParam(response(next), [], null, [null])).toThrow(GolemProtocolError)
    }
    for (const page of [null, {}, { feed: null }, { feed: {} }]) {
      expect(() => plan.getNextPageParam(page, [], null, [null])).toThrow(GolemProtocolError)
    }
    expect(() => plan.getNextPageParam(response('used'), [], 'used', [null])).toThrow(GolemProtocolError)
    expect(() => plan.getNextPageParam(response('used'), [], 'newer', [null, 'used', 'newer'])).toThrow(GolemProtocolError)
    expect(() => plan.requestFor('')).toThrow(GolemConfigurationError)
    expect(() => plan.requestFor('x'.repeat(CONTINUATION_TOKEN_MAX_BYTES + 1))).toThrow(GolemConfigurationError)
  })

  it('refuses caller paging arguments, reserved aliases through fragments, conditional and indirect roots', () => {
    const invalid = [
      '{ homeFeed(seed: 7, after: null) { items { index } } }',
      'query($golemAfter: String) { homeFeed(seed: 7) { items { index } } }',
      '{ homeFeed(seed: 7) { _golemNext: next } }',
      '{ homeFeed(seed: 7) { ...Page } } fragment Page on HomeFeedPage { _golemNext: next @skip(if: true) }',
      '{ homeFeed(seed: 7) { ... on HomeFeedPage { _golemNext: items { index } } } }',
      '{ homeFeed(seed: 7) @skip(if: false) { items { index } } }',
      '{ ...Root } fragment Root on Query { homeFeed(seed: 7) { items { index } } }',
      '{ homeFeed(seed: 7) { items { index } } other: homeFeed(seed: 8) { items { index } } }',
    ]
    for (const query of invalid) expect(() => golemContinuationQuery(analyzer.analyzeStatic(query), {}, manifest)).toThrow(GolemDocumentError)
    expect(() => golemContinuationQuery(analyzer.analyzeStatic(document), { seed: 7, [CONTINUATION_VARIABLE]: 'owned' }, manifest)).toThrow(GolemConfigurationError)
  })

  it('accepts omitted server-default take, literals and variable defaults; rejects malformed explicit take', () => {
    expect(() => golemContinuationQuery(analyzer.analyzeStatic('{ homeFeed(seed: 7) { items { index } } }'), {}, manifest)).not.toThrow()
    expect(() => golemContinuationQuery(analyzer.analyzeStatic('{ homeFeed(seed: 7, take: 25) { items { index } } }'), {}, manifest)).not.toThrow()
    expect(() => golemContinuationQuery(analyzer.analyzeStatic(document), { seed: 7 }, manifest)).not.toThrow()
    for (const take of [0, -1, 2.5, '25']) expect(() => golemContinuationQuery(analyzer.analyzeStatic(document), { seed: 7, take }, manifest)).toThrow(GolemConfigurationError)
  })

  it('preserves server-default take for nullable variables and explicit null', () => {
    const nullable = analyzer.analyzeStatic('query($take: Int) { homeFeed(seed: 7, take: $take) { items { index } } }')
    for (const variables of [{}, { take: undefined }, { take: null }]) {
      const plan = golemContinuationQuery(nullable, variables, manifest)
      expect(plan.requestFor(null).variables).toEqual(variables)
      expect(plan.getNextPageParam({ homeFeed: { items: [], [CONTINUATION_RESPONSE_KEY]: 'next' } }, [], null, [null])).toBe('next')
    }
    expect(() => golemContinuationQuery(analyzer.analyzeStatic('{ homeFeed(seed: 7, take: null) { items { index } } }'), {}, manifest)).not.toThrow()
    expect(() => golemContinuationQuery(analyzer.analyzeStatic('query($take: Int = null) { homeFeed(seed: 7, take: $take) { items { index } } }'), {}, manifest)).not.toThrow()
    // Explicit null overrides the variable default, even an invalid size.
    const defaulted = analyzer.analyzeStatic('query($take: Int = 0) { homeFeed(seed: 7, take: $take) { items { index } } }')
    expect(() => golemContinuationQuery(defaulted, { take: null }, manifest)).not.toThrow()
    expect(() => golemContinuationQuery(defaulted, {}, manifest)).toThrow(GolemConfigurationError)
    for (const take of [0, -1, 2.5, '25', false, {}]) expect(() => golemContinuationQuery(nullable, { take }, manifest)).toThrow(GolemConfigurationError)
  })
})

const modelPageManifest: ClientManifest = {
  ...manifest,
  models: {
    Movie: { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: 'MovieWhereUniqueInput', events: 'movieEvents', relations: {}, computed: {} },
  },
  roots: { homeFeed: { ...manifest.roots.homeFeed, models: ['Movie'] } },
  types: {
    ...manifest.types,
    HomeFeedPage: { kind: 'object', fields: {
      items: { kind: 'list', nullable: false, maxItems: 100, element: { kind: 'model', name: 'Movie', nullable: false } },
      next: { kind: 'scalar', name: 'String', nullable: true },
    } },
  },
}
const modelPageSchema = buildSchema(`
  scalar BigInt
  type Query { homeFeed(seed: Int!, take: Int, after: String): HomeFeedPage! }
  type HomeFeedPage { items: [Movie!]!, next: String }
  type Movie { id: ID!, title: String!, _version: BigInt! }
`)
const modelPageDocument = `query MoviePage($show: Boolean!) {
  feed: homeFeed(seed: 7) { ...PageFields }
} fragment PageFields on HomeFeedPage { items { ...MovieFields } }
fragment MovieFields on Movie { title @include(if: $show) }`

describe('selection-preserving static analysis', () => {
  it('keeps separate text/node caches and preserves authored model metadata', () => {
    const modelAnalyzer = new DocumentAnalyzer(modelPageManifest)
    const node = parse(modelPageDocument)
    const original = print(node)
    const staticNode = modelAnalyzer.analyzeStatic(node)
    expect(staticNode).toBe(modelAnalyzer.analyzeStatic(node))
    expect(modelAnalyzer.analyzeStatic(modelPageDocument)).toBe(modelAnalyzer.analyzeStatic(modelPageDocument))
    expect(staticNode.query).toBe(original)
    const live = modelAnalyzer.analyze(node)
    expect(live.query).toContain('_version')
    expect(live.query).toContain('__typename')
    expect(live.query).toMatch(/\bid\b/)
    expect(print(node)).toBe(original)
    expect(modelAnalyzer.analyzeStatic(node)).toBe(staticNode)
    const authored = '{ homeFeed(seed: 7) { items { title id _version __typename } } }'
    const selected = modelAnalyzer.analyzeStatic(authored)
    expect(selected.query).toBe(print(parse(authored)))
    const request = golemContinuationQuery(selected, {}, modelPageManifest).requestFor(null)
    const names: string[] = []
    visit(parse(request.query), { Field(field) { names.push(field.name.value) } })
    expect(names).toEqual(['homeFeed', 'items', 'title', 'id', '_version', '__typename', 'next'])
  })

  it('runs the shared document validation without requiring row enrichment', () => {
    const modelAnalyzer = new DocumentAnalyzer(modelPageManifest)
    expect(() => modelAnalyzer.analyzeStatic('{ homeFeed(seed: 7) { missing } }')).toThrow(GolemDocumentError)
    expect(() => modelAnalyzer.analyzeStatic('{ missingRoot { items } }')).toThrow(GolemDocumentError)
    expect(() => modelAnalyzer.analyzeStatic('{ homeFeed(seed: 7) { items { ...Missing } } }')).toThrow(GolemDocumentError)
    const alias = '{ homeFeed(seed: 7) { items { id: title } } }'
    expect(modelAnalyzer.analyzeStatic(alias).query).toBe(print(parse(alias)))
    expect(() => modelAnalyzer.analyze(alias)).toThrow(GolemDocumentError)
  })

  it('sends only requested model fields plus private next through static HTTP', async () => {
    const queryClient = new QueryClient()
    const socket = vi.fn(() => { throw new Error('static model page opened a socket') })
    const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
      const payload = JSON.parse(String(init?.body))
      expect(validate(modelPageSchema, parse(payload.query))).toEqual([])
      expect(payload.query).toContain('...MovieFields')
      expect(payload.query).toContain('@include(if: $show)')
      const names: string[] = []
      visit(parse(payload.query), { Field(field) { names.push(field.name.value) } })
      // This transport fixture refuses public identity/version projections.
      // The backend's actual hidden-ID hydration contrast is a separate gate.
      expect(names).not.toContain('id')
      expect(names).not.toContain('_version')
      expect(names).not.toContain('__typename')
      expect(names.filter((name) => name === 'next')).toHaveLength(1)
      expect(payload.query).toContain('_golemNext: next')
      expect(payload.variables.show).toBe(true)
      return new Response(JSON.stringify({ data: { feed: { items: [{ title: 'readable-title' }], [CONTINUATION_RESPONSE_KEY]: payload.variables.golemAfter === undefined ? 'next-token' : null } } }))
    })
    const client = new GolemClient({ queryClient, url: 'http://model-pages.test/graphql', manifest: modelPageManifest, fetch, createSocket: socket })
    try {
      const options = client.staticContinuationQueryOptions(modelPageDocument, { show: true })
      const signal = new AbortController().signal
      const first = await options.queryFn({ signal, pageParam: null })
      const next = options.getNextPageParam(first, [first], null, [null])
      expect(next).toBe('next-token')
      const second = await options.queryFn({ signal, pageParam: next! })
      expect(options.getNextPageParam(second, [first, second], next!, [null, next!])).toBeUndefined()
      expect(fetch).toHaveBeenCalledTimes(2)
      expect(socket).not.toHaveBeenCalled()
      const plain = client.staticQueryOptions(modelPageDocument, { show: true })
      expect(plain.queryKey[2]).toBe(print(parse(modelPageDocument)))
      expect(plain.meta.golem.mode).toBe('static')
    } finally { client.dispose(); queryClient.clear() }
  })
})

describe('continuation transport and manifest', () => {
  it('negotiates version 3 and rejects unversioned or mismatched page metadata', () => {
    expect(golemBlock(manifest)).toEqual({ protocol: 1, contract: manifest.contract, manifestVersion: 3 })
    expect(golemBlock({ contract: 'old' })).toEqual({ protocol: 1, contract: 'old' })
    expect(golemBlock({ contract: 'objects', formatVersion: 2 }).manifestVersion).toBe(2)
    expect(() => golemBlock({ contract: 'future', formatVersion: 4 })).toThrow(GolemConfigurationError)
    for (const formatVersion of [1, 2]) expect(() => assertClientManifest({ ...manifest, formatVersion } as ClientManifest)).toThrow(GolemConfigurationError)
    const wrong = { ...manifest, roots: { homeFeed: { ...manifest.roots.homeFeed, pagination: { ...manifest.roots.homeFeed.pagination, nextField: 'total' } } } }
    expect(() => assertClientManifest(wrong as unknown as ClientManifest)).toThrow(GolemConfigurationError)
    const malformed = { ...manifest, types: { ...manifest.types, HomeFeedPage: { ...manifest.types.HomeFeedPage, fields: { ...manifest.types.HomeFeedPage.fields, next: { kind: 'scalar', name: 'String', nullable: false } } } } }
    expect(() => assertClientManifest(malformed as unknown as ClientManifest)).toThrow(GolemConfigurationError)
  })

  it('loads 25/25/10 through the real transport without live metadata or sockets', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    const records = Array.from({ length: 60 }, (_, index) => ({ kind: 'segment', index }))
    const socket = vi.fn(() => { throw new Error('continuation query opened a socket') })
    const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
      const payload = JSON.parse(String(init?.body))
      expect(validate(schema, parse(payload.query))).toEqual([])
      expect(payload.extensions.golem.manifestVersion).toBe(3)
      const start = payload.variables.golemAfter === 'second' ? 25 : payload.variables.golemAfter === 'third' ? 50 : 0
      return new Response(JSON.stringify({ data: response(start === 0 ? 'second' : start === 25 ? 'third' : null, records.slice(start, start + 25)) }))
    })
    const client = new GolemClient({ queryClient, url: 'http://pages.test/graphql', manifest, fetch, createSocket: socket })
    try {
      const variables = { seed: 7 }
      const options = client.staticContinuationQueryOptions(document, variables)
      expect(options.queryKey).toEqual(['golem', 'static', analyzer.analyzeStatic(document).query, variables, 'continuation'])
      expect(options.meta.golem.mode).toBe('static')
      expectTypeOf(options.initialPageParam).toEqualTypeOf<string | null>()
      const observer = new InfiniteQueryObserver<unknown, Error, InfiniteData<unknown, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>(queryClient, options)
      await observer.refetch()
      await observer.fetchNextPage()
      await observer.fetchNextPage()
      const result = observer.getCurrentResult()
      const pages = result.data?.pages as Array<{ feed: { items: typeof records } }> | undefined
      expect(pages?.map((page) => page.feed.items.length)).toEqual([25, 25, 10])
      expect(pages?.flatMap((page) => page.feed.items)).toEqual(records)
      expect(result.data?.pageParams).toEqual([null, 'second', 'third'])
      expect(result.hasNextPage).toBe(false)
      expect(fetch).toHaveBeenCalledTimes(3)
      expect(socket).not.toHaveBeenCalled()
      expect(client.live.started).toBe(false)
      expect(queryClient.getQueryCache().getAll()[0]?.meta?.golem).toMatchObject({ mode: 'static' })
    } finally { client.dispose(); queryClient.clear() }
  })

  it('forwards AbortSignal and opaque page parameter through direct HTTP', async () => {
    const queryClient = new QueryClient()
    const controller = new AbortController()
    const fetch = vi.fn<typeof globalThis.fetch>(async (_url, init) => {
      expect(init?.signal).toBe(controller.signal)
      expect(JSON.parse(String(init?.body)).variables.golemAfter).toBe('opaque-next')
      return new Response(JSON.stringify({ data: response(null) }))
    })
    const client = new GolemClient({ queryClient, url: 'http://pages.test/graphql', manifest, fetch })
    try { await client.staticContinuationQueryOptions(document, { seed: 7 }).queryFn({ signal: controller.signal, pageParam: 'opaque-next' }) }
    finally { client.dispose(); queryClient.clear() }
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})

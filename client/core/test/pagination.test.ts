import { randomUUID } from 'node:crypto'
import { InfiniteQueryObserver, type InfiniteData } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { cursorOf, DocumentAnalyzer, GolemConfigurationError, GolemDocumentError, GolemError, golemInfiniteQuery, pageSpec } from '../src/index.js'
import { DELETE_POST, sleep, until, world, type World } from './harness/live.js'
import { collector, manifest } from './harness/social.js'
import { socialWithoutPostUniqueInput, socialWithPostTagEvents, socialWithRenamedPostUniqueInput } from './fixtures/manifests.js'

const PAGED =
  'query($t: String!, $take: Int!) { posts(where: { title: { contains: $t }, published: { equals: true } }, orderBy: [{ title: asc }], take: $take) { title } }'

const CREATE_TAG = 'mutation($data: TagCreateInput!) { createTag(data: $data) { id name } }'
const CREATE_POST_TAG = 'mutation($data: PostTagCreateInput!) { createPostTag(data: $data) { postID tagName } }'
const POST_TAGS = 'query($p: String!, $take: Int!) { postTags(where: { tagName: { startsWith: $p } }, orderBy: [{ tagName: asc }], take: $take) { postID tagName } }'

type Page = { posts: Array<{ id: string; title: string }> }

const open: World[] = []

afterEach(async () => {
  for (const one of open.splice(0)) {
    await one.close()
  }
})

const analyzer = new DocumentAnalyzer(manifest)

const token = () => `pg${randomUUID().slice(0, 6)}`

async function fresh(): Promise<World> {
  const created = await world()
  open.push(created)
  return created
}

function paged(w: World, tag: string, take: number) {
  const analysis = analyzer.analyze(PAGED)
  const variables = { t: tag, take }
  const options = w.live.infiniteOptions(analysis, variables)
  const observer = new InfiniteQueryObserver<Page, Error, InfiniteData<Page, unknown>, readonly unknown[], unknown>(w.queryClient, options as never)
  const stop = observer.subscribe(() => undefined)
  return {
    analysis,
    variables,
    observer,
    stop,
    titles: () => (observer.getCurrentResult().data?.pages ?? []).flatMap((page) => page.posts.map((row) => row.title)),
    shape: () => (observer.getCurrentResult().data?.pages ?? []).map((page) => page.posts.map((row) => row.title)),
    calls: () => w.gate.calls.filter((call) => call.query === analysis.query || call.query === pageSpec(analysis, manifest).continuation),
  }
}

const SEED_EVENTS = 'subscription($t: String!) { postEvents(where: { title: { contains: $t } }) { type } }'

async function seed(w: World, tag: string, names: string[]): Promise<Map<string, string>> {
  const seen = collector()
  const stop = w.direct.subscribe({ query: SEED_EVENTS, variables: { t: tag } }, seen.handlers)
  await seen.until(() => seen.acks() === 1)
  const ids = new Map<string, string>()
  for (const name of names) {
    ids.set(name, await w.createPost(`${tag}-${name}`))
  }
  await seen.until(() => seen.events.length === names.length)
  stop()
  return ids
}

const named = (tag: string, ...names: string[]) => names.map((name) => `${tag}-${name}`)

describe('infinite query on the real social server', () => {
  it('pages forward with a cursor built from the last row and skip 1', async () => {
    const w = await fresh()
    const tag = token()
    const ids = await seed(w, tag, ['1', '2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    expect(view.observer.getCurrentResult().hasNextPage).toBe(true)
    await view.observer.fetchNextPage()
    await view.observer.fetchNextPage()
    expect(view.shape()).toEqual([named(tag, '1', '2', '3'), named(tag, '4', '5', '6'), named(tag, '7')])
    expect(view.observer.getCurrentResult().hasNextPage).toBe(false)
    const withCursor = view.calls().filter((call) => call.variables?.golemCursor !== undefined)
    expect(withCursor.map((call) => call.variables)).toEqual([
      { t: tag, take: 3, golemCursor: { ID: ids.get('3') }, golemSkip: 1 },
      { t: tag, take: 3, golemCursor: { ID: ids.get('6') }, golemSkip: 1 },
    ])
    view.stop()
  })

  it('keeps membership after a create that sorts into the first page', async () => {
    const w = await fresh()
    const tag = token()
    await seed(w, tag, ['2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    await view.observer.fetchNextPage()
    expect(view.shape()).toEqual([named(tag, '2', '3', '4'), named(tag, '5', '6', '7')])
    await w.createPost(`${tag}-1`)
    await until(() => view.titles().includes(`${tag}-1`))
    await until(() => view.observer.getCurrentResult().isFetching === false)
    expect(view.shape()).toEqual([named(tag, '1', '2', '3'), named(tag, '4', '5', '6')])
    expect(view.observer.getCurrentResult().hasNextPage).toBe(true)
    await view.observer.fetchNextPage()
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '4', '5', '6', '7'))
    view.stop()
  })

  it('keeps membership after a create that sorts into the second page', async () => {
    const w = await fresh()
    const tag = token()
    await seed(w, tag, ['1', '2', '3', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    await view.observer.fetchNextPage()
    await w.createPost(`${tag}-4`)
    await until(() => view.titles().includes(`${tag}-4`))
    await until(() => view.observer.getCurrentResult().isFetching === false)
    expect(view.shape()).toEqual([named(tag, '1', '2', '3'), named(tag, '4', '5', '6')])
    await view.observer.fetchNextPage()
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '4', '5', '6', '7'))
    view.stop()
  })

  it('removes a row that exits from the first page at once and still pages on correctly', async () => {
    const w = await fresh()
    const tag = token()
    const ids = await seed(w, tag, ['1', '2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    await view.observer.fetchNextPage()
    const before = view.calls().length
    w.gate.hold()
    await w.updatePost(ids.get('2') ?? '', { published: { set: false } })
    await until(() => !view.titles().includes(`${tag}-2`))
    expect(view.shape()).toEqual([named(tag, '1', '3'), named(tag, '4', '5', '6')])
    expect(view.calls().length).toBe(before)
    w.gate.release()
    await view.observer.fetchNextPage()
    await until(() => view.observer.getCurrentResult().isFetching === false)
    expect(view.titles()).toEqual(named(tag, '1', '3', '4', '5', '6', '7'))
    view.stop()
  })

  it('removes a deleted row from the second page and keeps its page neighbours', async () => {
    const w = await fresh()
    const tag = token()
    const ids = await seed(w, tag, ['1', '2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    await view.observer.fetchNextPage()
    await w.run(DELETE_POST, { id: ids.get('4') })
    await until(() => !view.titles().includes(`${tag}-4`))
    await until(() => view.observer.getCurrentResult().isFetching === false)
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '5', '6'))
    view.stop()
  })

  it('does not lose the next page when an exit shortens the last loaded page', async () => {
    const w = await fresh()
    const tag = token()
    const ids = await seed(w, tag, ['1', '2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    await view.observer.fetchNextPage()
    const before = view.calls().length
    w.gate.hold()
    await w.updatePost(ids.get('5') ?? '', { published: { set: false } })
    await until(() => !view.titles().includes(`${tag}-5`))
    await until(() => view.calls().length > before)
    w.gate.release()
    await until(() => view.titles().length === 6 && view.observer.getCurrentResult().isFetching === false)
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '4', '6', '7'))
    expect(view.observer.getCurrentResult().hasNextPage).toBe(true)
    await view.observer.fetchNextPage()
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '4', '6', '7'))
    expect(view.observer.getCurrentResult().hasNextPage).toBe(false)
    view.stop()
  })

  it('does not lose the next page when a landing page drops a row deleted while it was in flight', async () => {
    const w = await fresh()
    const tag = token()
    const ids = await seed(w, tag, ['1', '2', '3', '4', '5', '6', '7'])
    const view = paged(w, tag, 3)
    await until(() => view.titles().length === 3)
    const before = view.calls().length
    w.gate.hold()
    const loading = view.observer.fetchNextPage()
    await until(() => view.calls().length > before)
    await w.run(DELETE_POST, { id: ids.get('5') })
    await sleep(600)
    w.gate.release()
    await loading
    await until(() => view.observer.getCurrentResult().isFetching === false)
    await until(() => view.titles().length === 6)
    expect(view.titles()).toEqual(named(tag, '1', '2', '3', '4', '6', '7'))
    expect(view.observer.getCurrentResult().hasNextPage).toBe(true)
    view.stop()
  })

  it('returns the server typed error for a take past the page maximum', async () => {
    const w = await fresh()
    const view = paged(w, token(), 501)
    await until(() => view.observer.getCurrentResult().status === 'error')
    const error = view.observer.getCurrentResult().error
    expect(error).toBeInstanceOf(GolemError)
    const typed = error as GolemError
    expect(typed.code).toBe('BAD_USER_INPUT')
    expect(typed.extensions).toMatchObject({ reason: 'INVALID_VALUE', argument: 'take', min: -500, max: 500 })
    view.stop()
  })

  it('returns the server typed error for a take past the query cost limit', async () => {
    const w = await fresh()
    const view = paged(w, token(), 100_000)
    await until(() => view.observer.getCurrentResult().status === 'error')
    const typed = view.observer.getCurrentResult().error as GolemError
    expect(typed).toBeInstanceOf(GolemError)
    expect(typed.code).toBe('QUERY_LIMIT_EXCEEDED')
    expect(typed.extensions).toMatchObject({ limit: 'MaxComplexity' })
    view.stop()
  })

  it('accepts a composite cursor built from the unique-input shape', async () => {
    const w = await fresh()
    const tag = token()
    const post = await w.createPost(`${tag}-host`)
    const names = ['a', 'b', 'c', 'd', 'e'].map((suffix) => `pg-${tag}-${suffix}`)
    for (const name of names) {
      await w.run(CREATE_TAG, { data: { name } })
      await w.run(CREATE_POST_TAG, { data: { post: { connect: { ID: post } }, tag: { connect: { Name: name } } } })
    }
    const spec = pageSpec(analyzer.analyze(POST_TAGS), socialWithPostTagEvents)
    const prefix = `pg-${tag}`
    const first = await w.run<{ postTags: Array<{ postID: string; tagName: string }> }>(POST_TAGS, { p: prefix, take: 2 })
    const mine = first.postTags
    expect(mine.map((row) => row.tagName)).toEqual(names.slice(0, 2))
    const cursor = cursorOf(spec, mine[0] ?? {})
    expect(cursor).toEqual({ PostID_TagName: { postID: mine[0]?.postID, tagName: mine[0]?.tagName } })
    const next = await w.run<{ postTags: Array<{ postID: string; tagName: string }> }>(spec.continuation, { p: prefix, take: 2, golemCursor: cursor, golemSkip: 1 })
    expect(next.postTags[0]).toMatchObject({ postID: mine[1]?.postID, tagName: mine[1]?.tagName })
  })
})

describe('cursor shape from the manifest', () => {
  const tags = analyzer.analyze(POST_TAGS)

  it('builds a plain value for a one-field key', () => {
    const spec = pageSpec(analyzer.analyze(PAGED), manifest)
    expect(cursorOf(spec, { id: 'x', title: 't' })).toEqual({ ID: 'x' })
  })

  it('builds an object keyed by the primary key for a compound key', () => {
    const spec = pageSpec(tags, socialWithPostTagEvents)
    expect(cursorOf(spec, { postID: 'p', tagName: 'n', extra: 1 })).toEqual({ PostID_TagName: { postID: 'p', tagName: 'n' } })
  })

  it('refuses a row that lacks a key field', () => {
    const spec = pageSpec(tags, socialWithPostTagEvents)
    expect(() => cursorOf(spec, { postID: 'p' })).toThrow(/tagName/)
  })

  it('takes the cursor from the last row only when the page is full', () => {
    const plan = golemInfiniteQuery(analyzer.analyze(PAGED), { t: 'x', take: 2 }, manifest)
    expect(plan.getNextPageParam({ posts: [{ id: 'a' }, { id: 'b' }] })).toEqual({ ID: 'b' })
    expect(plan.getNextPageParam({ posts: [{ id: 'a' }] })).toBeUndefined()
    expect(plan.getNextPageParam({ posts: [] })).toBeUndefined()
  })

  it('sends no cursor and no skip for the first page and both for the rest', () => {
    const plan = golemInfiniteQuery(analyzer.analyze(PAGED), { t: 'x', take: 2 }, manifest)
    expect(plan.requestFor(plan.initialPageParam)).toEqual({ query: analyzer.analyze(PAGED).query, variables: { t: 'x', take: 2 } })
    const next = plan.requestFor({ ID: 'b' })
    expect(next.variables).toEqual({ t: 'x', take: 2, golemCursor: { ID: 'b' }, golemSkip: 1 })
    expect(next.query).toContain('$golemCursor: PostWhereUniqueInput')
    expect(next.query).toContain('$golemSkip: Int')
    expect(next.query).toMatch(/take: \$take\s+cursor: \$golemCursor\s+skip: \$golemSkip/)
  })

  it('declares the continuation variable with the unique input the manifest names', () => {
    const plan = golemInfiniteQuery(analyzer.analyze(PAGED), { t: 'x', take: 2 }, socialWithRenamedPostUniqueInput)
    const next = plan.requestFor({ ID: 'b' })
    expect(next.query).toContain('$golemCursor: PostKey')
    expect(next.query).not.toContain('PostWhereUniqueInput')
  })

  it('refuses to page a model whose manifest names no unique input', () => {
    expect(() => pageSpec(analyzer.analyze(PAGED), socialWithoutPostUniqueInput)).toThrow(GolemDocumentError)
  })

  it('reads a literal take and an aliased root', () => {
    const document = 'query { latest: posts(orderBy: [{ title: asc }], take: 5) { title } }'
    const plan = golemInfiniteQuery(analyzer.analyze(document), {}, manifest)
    expect(plan.take).toBe(5)
    expect(plan.spec.root).toBe('latest')
    expect(plan.getNextPageParam({ latest: [{ id: '1' }, { id: '2' }, { id: '3' }, { id: '4' }, { id: '5' }] })).toEqual({ ID: '5' })
  })

  it('refuses documents the client cannot page', () => {
    const noTake = analyzer.analyze('query { posts { title } }')
    expect(() => pageSpec(noTake, manifest)).toThrow(GolemDocumentError)
    const twoLists = analyzer.analyze('query { posts(take: 2) { title } comments(take: 2) { body } }')
    expect(() => pageSpec(twoLists, manifest)).toThrow(GolemDocumentError)
    const custom = analyzer.analyze('query { searchPosts(take: 2, where: {}) { title } }')
    expect(() => pageSpec(custom, manifest)).toThrow(GolemDocumentError)
    const ownCursor = analyzer.analyze('query($cursor: PostWhereUniqueInput) { posts(take: 2, cursor: $cursor) { title } }')
    expect(() => pageSpec(ownCursor, manifest)).toThrow(GolemDocumentError)
    const ownSkip = analyzer.analyze('query { posts(take: 2, skip: 1) { title } }')
    expect(() => pageSpec(ownSkip, manifest)).toThrow(GolemDocumentError)
    const taken = analyzer.analyze('query($golemSkip: Int) { posts(take: 2) { title } }')
    expect(() => pageSpec(taken, manifest)).toThrow(GolemDocumentError)
  })

  it('uses the variable default when the caller omits the take', () => {
    const withDefault = analyzer.analyze('query($take: Int = 20) { posts(take: $take) { title } }')
    expect(golemInfiniteQuery(withDefault, {}, manifest).take).toBe(20)
    expect(golemInfiniteQuery(withDefault, { take: 5 }, manifest).take).toBe(5)
    const bare = analyzer.analyze('query($take: Int) { posts(take: $take) { title } }')
    expect(() => golemInfiniteQuery(bare, {}, manifest)).toThrow(GolemConfigurationError)
    const zero = analyzer.analyze('query($take: Int = 0) { posts(take: $take) { title } }')
    expect(() => golemInfiniteQuery(zero, {}, manifest)).toThrow(GolemConfigurationError)
  })

  it('rejects client-owned variables and a bad take', () => {
    const analysis = analyzer.analyze(PAGED)
    expect(() => golemInfiniteQuery(analysis, { t: 'x', take: 2, golemCursor: { ID: 'a' } }, manifest)).toThrow(GolemConfigurationError)
    expect(() => golemInfiniteQuery(analysis, { t: 'x', take: 2, golemSkip: 3 }, manifest)).toThrow(GolemConfigurationError)
    expect(() => golemInfiniteQuery(analysis, { t: 'x', take: 0 }, manifest)).toThrow(GolemConfigurationError)
    expect(() => golemInfiniteQuery(analysis, { t: 'x', take: -2 }, manifest)).toThrow(GolemConfigurationError)
    expect(() => golemInfiniteQuery(analysis, { t: 'x' }, manifest)).toThrow(GolemConfigurationError)
  })
})

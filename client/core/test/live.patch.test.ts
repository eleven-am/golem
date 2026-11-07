import { QueryClient, QueryObserver, type Query } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemNotFoundError, LiveSync, Mutations, type ClientManifest, type LiveTransport, type StreamHandlers, type SubscriptionRequest } from '../src/index.js'
import { sleep, until } from './harness/live.js'
import { socialManifest, socialStrictCommentPost, socialWithPostTagEvents } from './fixtures/manifests.js'

class ScriptedTransport implements LiveTransport {
  readonly streams: StreamHandlers[] = []
  readonly requests: SubscriptionRequest[] = []
  executes = 0
  answer: () => unknown

  constructor(answer: () => unknown) {
    this.answer = answer
  }

  async execute<T>(): Promise<T> {
    this.executes++
    return structuredClone(this.answer()) as T
  }

  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    this.requests.push(request)
    this.streams.push(handlers)
    return () => undefined
  }

  renew(): void {}
}

const cleanup: Array<() => void> = []

afterEach(() => {
  for (const one of cleanup.splice(0)) {
    one()
  }
})

const withRoots = (roots: ClientManifest['roots']): ClientManifest => ({ ...socialManifest, roots: { ...socialManifest.roots, ...roots } })

async function mount(manifest: ClientManifest, document: string, answer: () => unknown, variables: Record<string, unknown> = {}) {
  const transport = new ScriptedTransport(answer)
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const live = new LiveSync({ queryClient, transport, manifest, random: () => 0, windowMs: 20, maxWaitMs: 100 })
  live.start()
  const analysis = new DocumentAnalyzer(manifest).analyze(document)
  const options = live.options(analysis, variables)
  const observer = new QueryObserver(queryClient, options)
  const stop = observer.subscribe(() => undefined)
  const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true }) as Query
  cleanup.push(stop, () => live.stop(), () => queryClient.clear())
  for (const stream of transport.streams) {
    stream.onAck()
  }
  await until(() => query.state.data !== undefined)
  return {
    transport,
    live,
    query,
    queryClient,
    emit(root: string, frame: Record<string, unknown>, index = 0) {
      transport.streams[index]?.onEvent({ data: { [root]: frame } })
    },
  }
}

const post = (id: string, version: string, extra: Record<string, unknown> = {}) => ({ __typename: 'Post', id, _version: version, title: `t-${id}`, ...extra })

describe('composite primary keys on events', () => {
  it('removes the row named by the identity object of a DELETED event with no entity', async () => {
    const tag = (name: string) => ({ __typename: 'PostTag', postID: 'p', tagName: name, _version: '1' })
    const view = await mount(socialWithPostTagEvents, '{ postTags { tagName } }', () => ({ postTags: [tag('a'), tag('b')] }))
    expect((view.transport.requests[0]?.query ?? '').replace(/\s+/g, ' ')).toContain('id { postID tagName }')
    view.emit('postTagEvents', { version: '2', type: 'DELETED', id: { tagName: 'a', postID: 'p' }, entity: null })
    expect((view.query.state.data as { postTags: Array<{ tagName: string }> }).postTags.map((row) => row.tagName)).toEqual(['b'])
    await sleep(150)
    expect(view.transport.executes).toBe(2)
  })
})

describe('numeric primary keys on events', () => {
  it('removes the row fetched with id 5 when a DELETED event names id 5 as a number', async () => {
    const numbered = (id: number) => ({ __typename: 'Post', id, _version: '1', title: `t-${id}` })
    const view = await mount(socialManifest, '{ posts { title } }', () => ({ posts: [numbered(5), numbered(6)] }))
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 5, entity: null })
    expect((view.query.state.data as { posts: Array<{ id: number }> }).posts.map((row) => row.id)).toEqual([6])
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })
})

describe('single slots', () => {
  const featured = withRoots({ featuredPost: { kind: 'opaque', models: ['Post'], shape: 'row', nullable: false } })
  const maybeFeatured = withRoots({ featuredPost: { kind: 'opaque', models: ['Post'], shape: 'row', nullable: true } })

  it('turns a generated single root into null and refetches', async () => {
    const view = await mount(socialManifest, 'query($id: UUID!) { post(where: { ID: $id }) { title } }', () => ({ post: post('a', '1') }), { id: 'a' })
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null })
    expect(view.query.state.data).toEqual({ post: null })
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
    await until(() => view.transport.executes === 2)
  })

  it('refetches a custom single root after its row is deleted and never patches it', async () => {
    let deleted = false
    const view = await mount(maybeFeatured, '{ featuredPost { title } }', () => ({ featuredPost: deleted ? post('b', '3') : post('a', '1') }))
    deleted = true
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null }, 0)
    expect(view.query.state.data).toEqual({ featuredPost: post('a', '1') })
    await until(() => view.transport.executes === 2)
    await until(() => (view.query.state.data as { featuredPost: unknown }).featuredPost !== null)
    expect(view.query.state.data).toEqual({ featuredPost: post('b', '3') })
  })

  it('keeps a custom single root LIVE and refetches it when its row exits', async () => {
    let held = true
    const view = await mount(featured, '{ featuredPost { title } }', () => (held ? { featuredPost: post('a', '1') } : { featuredPost: post('b', '3') }))
    held = false
    view.emit('postEvents', { version: '2', type: 'EXITED', id: 'a', entity: null }, 0)
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
    await until(() => view.transport.executes === 2)
    await until(() => (view.query.state.data as { featuredPost: { id: string } }).featuredPost.id === 'b')
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('shows null for a nullable custom single root once the refetch returns null', async () => {
    let gone = false
    const view = await mount(maybeFeatured, '{ featuredPost { title } }', () => ({ featuredPost: gone ? null : post('a', '1') }))
    gone = true
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null }, 0)
    await until(() => view.query.state.data !== undefined && (view.query.state.data as { featuredPost: unknown }).featuredPost === null)
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('turns a nested to-one relation the manifest marks nullable into null', async () => {
    const view = await mount(socialManifest, '{ comments { body post { title } } }', () => ({
      comments: [{ __typename: 'Comment', id: 'c', _version: '1', body: 'x', post: post('a', '1') }],
    }))
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null }, 1)
    expect((view.query.state.data as { comments: Array<{ post: unknown }> }).comments[0]?.post).toBeNull()
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('enters NOT_FOUND for a nested single slot the manifest marks non-nullable', async () => {
    const view = await mount(socialStrictCommentPost, '{ comments { body post { title } } }', () => ({
      comments: [{ __typename: 'Comment', id: 'c', _version: '1', body: 'x', post: post('a', '1') }],
    }))
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null }, 1)
    expect(view.live.statusOf(view.query)?.state).toBe('NOT_FOUND')
  })
})

describe('rows held only inside a nested relation', () => {
  const comment = (id: string, body: string, version: string) => ({ __typename: 'Comment', id, _version: version, body })

  it('refetches an UPDATED comment instead of patching it under its old post', async () => {
    let moved = false
    const view = await mount(socialManifest, '{ posts { comments { body } } }', () => ({
      posts: moved
        ? [post('p1', '1', { comments: [] }), post('p2', '1', { comments: [comment('c', 'edited', '2')] })]
        : [post('p1', '1', { comments: [comment('c', 'hello', '1')] }), post('p2', '1', { comments: [] })],
    }))
    moved = true
    view.emit('commentEvents', { version: '2', type: 'UPDATED', id: 'c', entity: comment('c', 'edited', '2') }, 1)
    await until(() => view.transport.executes === 2)
    const posts = (view.query.state.data as { posts: Array<{ id: string; comments: unknown[] }> }).posts
    expect(posts.map((row) => [row.id, row.comments.length])).toEqual([['p1', 0], ['p2', 1]])
  })
})

describe('selections that are not plain', () => {
  it('refetches an UPDATED comment that moved between posts instead of keeping its old post', async () => {
    let moved = false
    const view = await mount(socialManifest, '{ comments { body post { title } } }', () => ({
      comments: [{ __typename: 'Comment', id: 'c', _version: moved ? '2' : '1', body: 'x', post: post(moved ? 'b' : 'a', '1') }],
    }))
    moved = true
    view.emit('commentEvents', { version: '2', type: 'UPDATED', id: 'c', entity: { __typename: 'Comment', id: 'c', _version: '2', body: 'x' } }, 0)
    await until(() => view.transport.executes === 2)
    const comments = (view.query.state.data as { comments: Array<{ post: { id: string } }> }).comments
    expect(comments[0]?.post.id).toBe('b')
  })

  it('refetches an UPDATED row of a distinct selection instead of leaving two representatives', async () => {
    let flipped = false
    const view = await mount(socialManifest, '{ posts(distinct: [published]) { title published } }', () => ({
      posts: flipped
        ? [post('a', '2', { published: true })]
        : [post('a', '1', { published: true }), post('b', '1', { published: false })],
    }))
    flipped = true
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'b', entity: post('b', '2', { published: true }) })
    await until(() => view.transport.executes === 2)
    const posts = (view.query.state.data as { posts: Array<{ published: boolean }> }).posts
    expect(posts.map((row) => row.published)).toEqual([true])
  })
})

describe('events that also refetch', () => {
  const list = (priority: number) => () => ({ posts: [post('a', '1', { priority })] })

  it('an UPDATED on an ordered list refetches and does not patch', async () => {
    const view = await mount(socialManifest, '{ posts(orderBy: { priority: asc }) { title } }', list(1))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { priority: 9 }) })
    expect((view.query.state.data as { posts: Array<{ priority: number }> }).posts[0]?.priority).toBe(1)
    await until(() => view.transport.executes === 2)
  })

  it('an UPDATED on an unordered list never refetches', async () => {
    const view = await mount(socialManifest, '{ posts { title } }', list(1))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { priority: 7 }) })
    await sleep(250)
    expect(view.transport.executes).toBe(1)
  })

  it('an UPDATED of a row in a custom result slot refetches and does not patch', async () => {
    const view = await mount(socialManifest, '{ searchPosts(take: 5, where: {}) { title } }', () => ({ searchPosts: [post('a', '1')] }))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { title: 'changed' }) })
    expect((view.query.state.data as { searchPosts: Array<{ title: string }> }).searchPosts[0]?.title).toBe('t-a')
    await until(() => view.transport.executes === 2)
  })

  it('an older UPDATED of a row in a custom result slot still refetches and does not patch', async () => {
    const view = await mount(socialManifest, '{ searchPosts(take: 5, where: {}) { title } }', () => ({ searchPosts: [post('a', '5')] }))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { title: 'old' }) })
    expect((view.query.state.data as { searchPosts: Array<{ title: string }> }).searchPosts[0]?.title).toBe('t-a')
    await until(() => view.transport.executes === 2)
  })
})

describe('patches scoped to the emitting stream', () => {
  const row = (version: string, fields: Record<string, unknown>) => ({ __typename: 'Post', id: 'a', _version: version, ...fields })
  type Lists = Record<string, Array<Record<string, unknown>>>

  it('an EXITED on one stream evicts only that stream occurrences', async () => {
    const view = await mount(
      socialManifest,
      '{ all: posts { title } published: posts(where: { published: { equals: true } }) { title } }',
      () => ({ all: [row('1', { title: 't' })], published: [row('1', { title: 't' })] }),
    )
    view.emit('postEvents', { version: '2', type: 'EXITED', id: 'a', entity: null }, 1)
    const data = view.query.state.data as Lists
    expect(data.all).toHaveLength(1)
    expect(data.published).toHaveLength(0)
  })

  it('each stream advances only its own projection of a row', async () => {
    const view = await mount(
      socialManifest,
      '{ all: posts { title } published: posts(where: { published: { equals: true } }) { body } }',
      () => ({ all: [row('1', { title: 'old' })], published: [row('1', { body: 'old' })] }),
    )
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: row('2', { title: 'new' }) }, 0)
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: row('2', { body: 'new' }) }, 1)
    const data = view.query.state.data as Lists
    expect(data.all?.[0]).toMatchObject({ title: 'new', _version: '2' })
    expect(data.published?.[0]).toMatchObject({ body: 'new', _version: '2' })
  })
})

describe('mutation overlays and aliases', () => {
  it('does not advance the version of an occurrence whose aliased field the mutation did not return, and refetches', async () => {
    let title = 'old'
    const view = await mount(socialManifest, '{ posts { heading: title } }', () => ({ posts: [{ __typename: 'Post', id: 'a', _version: '1', heading: title }] }))
    const mutations = new Mutations({
      queryClient: view.queryClient,
      transport: { execute: <T>() => Promise.resolve({ updatePost: { __typename: 'Post', id: 'a', _version: '2', title: 'new' } } as T) },
      manifest: socialManifest,
    })
    title = 'new'
    await mutations.execute('mutation { updatePost(where: { ID: "a" }, data: { title: { set: "new" } }) { title } }')
    expect((view.query.state.data as { posts: Array<{ _version: string }> }).posts[0]?._version).not.toBe('2')
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: { __typename: 'Post', id: 'a', _version: '2', heading: 'new' } })
    await until(() => view.transport.executes === 2)
    await until(() => (view.query.state.data as { posts: Array<{ heading: string }> }).posts[0]?.heading === 'new')
  })
})

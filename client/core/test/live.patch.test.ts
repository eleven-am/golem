import { QueryClient, QueryObserver, type Query } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemNotFoundError, LiveSync, type ClientManifest, type LiveTransport, type StreamHandlers, type SubscriptionRequest } from '../src/index.js'
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
    expect(view.transport.executes).toBe(1)
  })
})

describe('single slots', () => {
  const featured = withRoots({ featuredPost: { kind: 'opaque', models: ['Post'], shape: 'row', nullable: false } })
  const maybeFeatured = withRoots({ featuredPost: { kind: 'opaque', models: ['Post'], shape: 'row', nullable: true } })

  it('turns a generated single root into null and does not refetch', async () => {
    const view = await mount(socialManifest, 'query($id: UUID!) { post(where: { ID: $id }) { title } }', () => ({ post: post('a', '1') }), { id: 'a' })
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null })
    expect(view.query.state.data).toEqual({ post: null })
    expect(view.live.statusOf(view.query)?.state).toBe('LIVE')
    await sleep(150)
    expect(view.transport.executes).toBe(1)
  })

  it('enters the NOT_FOUND state for a slot the manifest marks non-nullable, and refetches at once', async () => {
    let held = true
    const view = await mount(featured, '{ featuredPost { title } }', () => (held ? { featuredPost: post('a', '1') } : { featuredPost: post('b', '3') }))
    held = false
    view.emit('postEvents', { version: '2', type: 'EXITED', id: 'a', entity: null }, 0)
    const status = view.live.statusOf(view.query)
    expect(status?.state).toBe('NOT_FOUND')
    expect(status?.error).toBeInstanceOf(GolemNotFoundError)
    expect((status?.error as GolemNotFoundError).subjects).toEqual(['Post:a'])
    await until(() => view.transport.executes === 2)
    await until(() => view.live.statusOf(view.query)?.state === 'LIVE')
    expect(view.query.state.data).toEqual({ featuredPost: post('b', '3') })
  })

  it('turns a custom single root the manifest marks nullable into null', async () => {
    const view = await mount(maybeFeatured, '{ featuredPost { title } }', () => ({ featuredPost: post('a', '1') }))
    view.emit('postEvents', { version: '2', type: 'DELETED', id: 'a', entity: null }, 0)
    expect(view.query.state.data).toEqual({ featuredPost: null })
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

describe('events that also refetch', () => {
  const list = (priority: number) => () => ({ posts: [post('a', '1', { priority })] })

  it('an UPDATED that changes an orderBy field patches and refetches', async () => {
    const view = await mount(socialManifest, '{ posts(orderBy: { priority: asc }) { title } }', list(1))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { priority: 9 }) })
    expect((view.query.state.data as { posts: Array<{ priority: number }> }).posts[0]?.priority).toBe(9)
    await until(() => view.transport.executes === 2)
  })

  it('an UPDATED that leaves the orderBy field alone patches and does not refetch', async () => {
    const view = await mount(socialManifest, '{ posts(orderBy: { priority: asc }) { title } }', list(1))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { priority: 1, title: 'renamed' }) })
    expect((view.query.state.data as { posts: Array<{ title: string }> }).posts[0]?.title).toBe('renamed')
    await sleep(250)
    expect(view.transport.executes).toBe(1)
  })

  it('an UPDATED on an unordered list never refetches', async () => {
    const view = await mount(socialManifest, '{ posts { title } }', list(1))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { priority: 7 }) })
    await sleep(250)
    expect(view.transport.executes).toBe(1)
  })

  it('an UPDATED of a row in a custom result slot patches and refetches', async () => {
    const view = await mount(socialManifest, '{ searchPosts(take: 5, where: {}) { title } }', () => ({ searchPosts: [post('a', '1')] }))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { title: 'changed' }) })
    expect((view.query.state.data as { searchPosts: Array<{ title: string }> }).searchPosts[0]?.title).toBe('changed')
    await until(() => view.transport.executes === 2)
  })

  it('an older UPDATED neither patches nor refetches', async () => {
    const view = await mount(socialManifest, '{ searchPosts(take: 5, where: {}) { title } }', () => ({ searchPosts: [post('a', '5')] }))
    view.emit('postEvents', { version: '2', type: 'UPDATED', id: 'a', entity: post('a', '2', { title: 'old' }) })
    await sleep(250)
    expect(view.transport.executes).toBe(1)
  })
})

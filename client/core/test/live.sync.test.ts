import { randomUUID } from 'node:crypto'
import { afterEach, describe, expect, it } from 'vitest'
import { Transport, type LiveTransport, type StreamHandlers, type SubscriptionRequest } from '../src/index.js'
import { analyzer, CREATE_COMMENT, DELETE_COMMENT, DELETE_POST, idle, LIST, POST_WITH_COMMENTS, sleep, titles, until, watch, world, type Watch, type World } from './harness/live.js'
import { actor, CREATE_POST, headerSocket, manifest, postInput, signUp, socialUrl } from './harness/social.js'

const open: World[] = []

async function fresh(overrides: Parameters<typeof world>[0] = {}): Promise<World> {
  const created = await world(overrides)
  open.push(created)
  return created
}

afterEach(async () => {
  for (const one of open.splice(0)) {
    await one.close()
  }
})

async function settled(view: Watch): Promise<void> {
  await idle(view)
}

const token = () => `t${randomUUID().slice(0, 8)}`

describe('subscribe, ack, fetch', () => {
  it('does not fetch while the acknowledgement is withheld, then fetches once after it', async () => {
    const w = await fresh()
    const warm = watch(w, LIST, { t: token() })
    await until(() => w.live.statusOf(warm.query)?.state === 'LIVE')
    const mine = token()
    await w.createPost(`${mine}-one`)
    w.proxy.hold('downstream')
    const view = watch(w, LIST, { t: mine })
    await sleep(500)
    expect(view.fetches()).toBe(0)
    expect(w.live.statusOf(view.query)?.state).toBe('CONNECTING')
    expect(view.data()).toBeUndefined()
    w.proxy.release('downstream')
    await until(() => titles(view.data()).length === 1)
    expect(view.fetches()).toBe(1)
    expect(w.live.statusOf(view.query)?.state).toBe('LIVE')
  })

  it('shows a commit made between the subscribe frame and the fetch', async () => {
    const w = await fresh()
    const warm = watch(w, LIST, { t: token() })
    await until(() => w.live.statusOf(warm.query)?.state === 'LIVE')
    const mine = token()
    w.proxy.hold('upstream')
    const view = watch(w, LIST, { t: mine })
    await sleep(300)
    expect(view.fetches()).toBe(0)
    await w.createPost(`${mine}-gap`)
    w.proxy.release('upstream')
    await until(() => titles(view.data()).length === 1)
    expect(titles(view.data())).toEqual([`${mine}-gap`])
  })
})

describe('ordering between responses and events', () => {
  it('direction A: a slow response landing after a newer event never regresses the row', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-a`)
    const view = watch(w, LIST, { t: mine })
    await until(() => titles(view.data()).length === 1)
    await settled(view)
    w.gate.hold()
    const before = view.fetches()
    void w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => view.fetches() === before + 1)
    await w.updatePost(id, { title: { set: `${mine}-b` } })
    await until(() => titles(view.data())[0] === `${mine}-b`)
    w.gate.release()
    await idle(view)
    expect(titles(view.data())).toEqual([`${mine}-b`])
    const shown = view.frames.map((frame) => titles(frame)[0])
    expect(shown.lastIndexOf(`${mine}-a`)).toBeLessThan(shown.indexOf(`${mine}-b`))
    const database = await w.run<{ posts: Array<{ title: string }> }>(LIST, { t: mine })
    expect(titles(view.data())).toEqual(titles(database))
  })

  it('an older response that repeats a row across aliases never regresses a newer live title', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-a`)
    const document = 'query($t: String!) { titles: posts(where: { title: { contains: $t } }) { title } bodies: posts(where: { title: { contains: $t } }) { body } }'
    const view = watch(w, document, { t: mine })
    const shown = () => ((view.data() as { titles?: Array<{ title: string }> } | undefined)?.titles ?? []).map((row) => row.title)
    await until(() => shown().length === 1)
    await settled(view)
    w.gate.hold()
    const before = view.fetches()
    void w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => view.fetches() === before + 1)
    await w.updatePost(id, { title: { set: `${mine}-b` } })
    await until(() => shown()[0] === `${mine}-b`)
    w.gate.release()
    await idle(view)
    expect(shown()).toEqual([`${mine}-b`])
  })

  it('direction B: delayed events landing after a newer refetch are ignored', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-a`)
    const view = watch(w, LIST, { t: mine })
    await until(() => titles(view.data()).length === 1)
    await settled(view)
    w.proxy.hold('downstream')
    const delivered = w.deliveries()
    await w.updatePost(id, { title: { set: `${mine}-b` } })
    await w.updatePost(id, { title: { set: `${mine}-c` } })
    await w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => titles(view.data())[0] === `${mine}-c`)
    w.proxy.release('downstream')
    await until(() => w.deliveries() >= delivered + 2)
    await sleep(w.windowMs + 1)
    await idle(view)
    expect(titles(view.data())).toEqual([`${mine}-c`])
    const shown = view.frames.map((frame) => titles(frame)[0])
    expect(shown).not.toContain(`${mine}-b`)
    expect(shown.at(-1)).toBe(`${mine}-c`)
  })

  it('a delete that lands while an older response is held keeps the row gone', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-gone`)
    await w.createPost(`${mine}-stays`)
    const view = watch(w, LIST, { t: mine })
    await until(() => titles(view.data()).length === 2)
    await settled(view)
    w.gate.hold()
    const before = view.fetches()
    void w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => view.fetches() === before + 1)
    await w.run(DELETE_POST, { id })
    await until(() => titles(view.data()).length === 1)
    w.gate.release()
    await idle(view)
    expect(titles(view.data())).toEqual([`${mine}-stays`])
    expect(view.frames.slice(view.frames.findIndex((frame) => titles(frame).length === 1)).every((frame) => titles(frame).length === 1)).toBe(true)
  })

  it('a key deleted and recreated while a fetch is held ends with the recreated row', async () => {
    const w = await fresh()
    const mine = token()
    const post = await w.createPost(`${mine}-host`)
    const comment = randomUUID()
    await w.run(CREATE_COMMENT, { id: comment, post, author: w.who.id, body: 'first' })
    const view = watch(w, POST_WITH_COMMENTS, { id: post })
    await until(() => (view.data() as { post?: { comments: unknown[] } } | undefined)?.post?.comments.length === 1)
    await settled(view)
    w.gate.hold()
    const before = view.fetches()
    void w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => view.fetches() === before + 1)
    await w.run(DELETE_COMMENT, { id: comment })
    await w.run(CREATE_COMMENT, { id: comment, post, author: w.who.id, body: 'second' })
    w.gate.release()
    await until(() => (view.data() as { post: { comments: Array<{ body: string }> } }).post.comments[0]?.body === 'second')
    const database = await w.run<{ post: { comments: Array<{ body: string }> } }>('query($id: UUID!) { post(where: { ID: $id }) { comments { body } } }', { id: post })
    expect((view.data() as { post: { comments: Array<{ body: string }> } }).post.comments.map((one) => one.body)).toEqual(database.post.comments.map((one) => one.body))
  })
})

describe('reconnect', () => {
  it('refetches each live query exactly once after the socket drops', async () => {
    const w = await fresh()
    const mine = token()
    const post = await w.createPost(`${mine}-p`)
    const first = watch(w, LIST, { t: token() })
    const second = watch(w, POST_WITH_COMMENTS, { id: post })
    await until(() => w.live.statusOf(first.query)?.state === 'LIVE' && w.live.statusOf(second.query)?.state === 'LIVE')
    await until(() => first.fetches() >= 1 && second.fetches() >= 1)
    await idle(first)
    await idle(second)
    const baseFirst = first.fetches()
    const baseSecond = second.fetches()
    w.proxy.drop()
    await until(() => first.fetches() === baseFirst + 1 && second.fetches() === baseSecond + 1, 15_000)
    await sleep(700)
    expect(first.fetches()).toBe(baseFirst + 1)
    expect(second.fetches()).toBe(baseSecond + 1)
    expect(w.live.statusOf(first.query)?.state).toBe('LIVE')
    expect(w.live.statusOf(second.query)?.state).toBe('LIVE')
  })
})

describe('coalescing', () => {
  it('100 status updates cause at most 3 refetches of a status-filtered list', async () => {
    const w = await fresh()
    const mine = token()
    const other = actor()
    const otherTransport = new Transport({ url: `${socialUrl()}/graphql`, manifest, credentials: other.credentials, createSocket: headerSocket })
    await signUp(otherTransport, other)
    const ids: string[] = []
    for (let batch = 0; batch < 10; batch++) {
      const created = await Promise.all(
        Array.from({ length: 10 }, (_, index) =>
          otherTransport.execute<{ createPost: { id: string } }>({
            query: CREATE_POST,
            variables: { data: { ...postInput(`${mine}-${batch * 10 + index}`), published: false } },
          }),
        ),
      )
      ids.push(...created.map((one) => one.createPost.id))
    }
    const document = 'query($t: String!) { posts(where: { published: { equals: true }, title: { contains: $t } }, take: 100) { title } }'
    const view = watch(w, document, { t: mine })
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && view.data() !== undefined)
    expect(titles(view.data())).toHaveLength(0)
    await idle(view)
    const initial = view.fetches()
    const feed = await w.publication()
    w.proxy.hold('downstream')
    for (let batch = 0; batch < 10; batch++) {
      await Promise.all(
        ids.slice(batch * 10, batch * 10 + 10).map((id) =>
          otherTransport.execute({ query: 'mutation($id: UUID!) { updatePost(where: { ID: $id }, data: { published: { set: true } }) { id } }', variables: { id } }),
        ),
      )
    }
    await feed?.published(ids, 'UPDATED')
    feed?.close()
    w.proxy.release('downstream')
    await until(() => titles(view.data()).length === 100, 15_000)
    await idle(view)
    const refetches = view.fetches() - initial
    expect(refetches).toBeGreaterThanOrEqual(1)
    expect(refetches).toBeLessThanOrEqual(3)
    otherTransport.dispose()
  }, 60_000)
})

describe('holding streams', () => {
  it('keeps streams for one second after the last observer leaves, and reuses them on a quick remount', async () => {
    let subscribed = 0
    let stopped = 0
    const w = await fresh({
      wrap: (transport: Transport): LiveTransport => ({
        execute: (request) => transport.execute(request),
        renew: () => transport.renew(),
        subscribe: (request: SubscriptionRequest, handlers: StreamHandlers) => {
          subscribed++
          const stop = transport.subscribe(request, handlers)
          return () => {
            stopped++
            stop()
          }
        },
      }),
    })
    const mine = token()
    const view = watch(w, LIST, { t: mine })
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE')
    const streams = analyzer.analyze(LIST).streams.length
    expect(subscribed).toBe(streams)
    view.stop()
    await sleep(300)
    expect(stopped).toBe(0)
    const again = watch(w, LIST, { t: mine })
    await until(() => again.fetches() === 1)
    await idle(again)
    expect(subscribed).toBe(streams)
    expect(stopped).toBe(0)
    expect(again.fetches()).toBe(1)
    again.stop()
    await sleep(700)
    expect(stopped).toBe(0)
    await until(() => stopped === streams, 3000)
    expect(w.live.statusOf(again.query)).toBeUndefined()
  })
})

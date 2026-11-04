import { InfiniteQueryObserver, QueryClient, QueryObserver, type InfiniteData } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemCommitUnknownError, GolemError, GolemPartialCommitError, LiveSync, Mutations, Transport, type Credentials } from '../src/index.js'
import { HttpGate, sleep, until } from './harness/live.js'
import { actor, CREATE_POST, headerSocket, manifest, postInput, signUp, socialUrl } from './harness/social.js'

const analyzer = new DocumentAnalyzer(manifest)

const LIST = 'query($t: String!) { posts(where: { title: { contains: $t } }) { title } }'
const ORDERED = 'query($t: String!) { posts(where: { title: { contains: $t } }, orderBy: [{ title: asc }]) { title } }'
const PAGED = 'query($t: String!, $take: Int!) { posts(where: { title: { contains: $t } }, orderBy: [{ title: asc }], take: $take) { title } }'
const SINGLE = 'query($id: UUID!) { post(where: { ID: $id }) { title } }'
const RENAME = 'mutation($id: UUID!, $title: String!) { updatePost(where: { ID: $id }, data: { title: { set: $title } }) { title } }'
const REMOVE = 'mutation($id: UUID!) { deletePost(where: { ID: $id }) { title } }'
const RENAME_MANY = 'mutation($t: String!, $title: String!) { updateManyPosts(where: { title: { contains: $t } }, data: { title: { set: $title } }) { count } }'
const REMOVE_MANY = 'mutation($t: String!) { deleteManyPosts(where: { title: { contains: $t } }) { count } }'

interface Env {
  readonly queryClient: QueryClient
  readonly live: LiveSync
  readonly mutations: Mutations
  readonly gate: HttpGate
  readonly direct: Transport
  readonly gated: Transport
  readonly who: { readonly id: string; readonly credentials: () => Credentials }
}

type Page = { posts: Array<{ id: string; title: string }> }

const titles = (data: unknown): string[] => ((data as { posts?: Array<{ title: string }> } | undefined)?.posts ?? []).map((row) => row.title)

const open: Array<() => void> = []

afterEach(() => {
  for (const close of open.splice(0)) {
    close()
  }
})

async function env(): Promise<Env> {
  const who = actor()
  const gate = new HttpGate(new URL(socialUrl()).origin)
  const options = { url: `${socialUrl()}/graphql`, manifest, credentials: who.credentials, createSocket: headerSocket }
  const direct = new Transport(options)
  const gated = new Transport({ ...options, fetch: gate.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const live = new LiveSync({ queryClient, transport: direct, manifest })
  const mutations = new Mutations({ queryClient, transport: gated, manifest })
  await signUp(direct, who)
  open.push(() => {
    queryClient.clear()
    direct.dispose()
    gated.dispose()
  })
  return { queryClient, live, mutations, gate, direct, gated, who }
}

async function createPost(world: Env, title: string): Promise<string> {
  const data = await world.direct.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput(title) } })
  return data.createPost.id
}

function cached(world: Env, document: string, variables: Record<string, unknown>) {
  const options = world.live.options(analyzer.analyze(document), variables)
  return {
    options,
    load: () => world.queryClient.fetchQuery(options),
    data: () => world.queryClient.getQueryData(options.queryKey) as Record<string, unknown> | undefined,
    state: () => world.queryClient.getQueryState(options.queryKey),
  }
}

describe('mutations on the real social server', () => {
  it('marks every cached query over the model stale after a row mutation and refetches the active ones in server order', async () => {
    const world = await env()
    const tag = `m-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    await createPost(world, `${tag}-b`)
    const ordered = cached(world, ORDERED, { t: tag })
    const idle = cached(world, SINGLE, { id })
    const observer = new QueryObserver(world.queryClient, ordered.options)
    open.push(observer.subscribe(() => undefined))
    await until(() => ordered.data() !== undefined)
    await idle.load()
    await world.mutations.execute(RENAME, { id, title: `${tag}-z` })
    await until(() => titles(ordered.data()).join() === `${tag}-b,${tag}-z`, 3000)
    expect(idle.state()?.isInvalidated).toBe(true)
  })

  it('refetches the pages of an infinite query after a delete instead of shortening one', async () => {
    const world = await env()
    const tag = `i-${world.who.id}`
    const ids: string[] = []
    for (const name of ['1', '2', '3', '4', '5']) {
      ids.push(await createPost(world, `${tag}-${name}`))
    }
    const analysis = analyzer.analyze(PAGED)
    const options = world.live.infiniteOptions(analysis, { t: tag, take: 3 })
    const observer = new InfiniteQueryObserver<Page, Error, InfiniteData<Page, unknown>, readonly unknown[], unknown>(world.queryClient, options as never)
    open.push(observer.subscribe(() => undefined))
    const shape = () => (observer.getCurrentResult().data?.pages ?? []).map((page) => page.posts.map((row) => row.title.slice(-1)))
    await until(() => shape().length === 1)
    await observer.fetchNextPage()
    expect(shape()).toEqual([['1', '2', '3'], ['4', '5']])
    await world.mutations.execute(REMOVE, { id: ids[1] })
    await until(() => JSON.stringify(shape()) === JSON.stringify([['1', '3', '4'], ['5']]), 3000)
  })

  it('raises a partial commit and refetches the queries over the committed models', async () => {
    const world = await env()
    const tag = `p-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const active = cached(world, LIST, { t: tag })
    const idle = cached(world, SINGLE, { id })
    const observer = new QueryObserver(world.queryClient, active.options)
    open.push(observer.subscribe(() => undefined))
    await until(() => active.data() !== undefined)
    await idle.load()
    const failure = await world.mutations
      .execute(
        `mutation($id: UUID!, $title: String!, $handle: String!, $email: String!) {
          renamed: updatePost(where: { ID: $id }, data: { title: { set: $title } }) { title }
          duplicate: createUser(data: { handle: $handle, email: $email }) { id }
        }`,
        { id, title: `${tag}-b`, handle: `h-${world.who.id}`, email: `other-${world.who.id}@example.test` },
      )
      .catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(GolemPartialCommitError)
    expect(failure).toMatchObject({ retryable: false, path: ['duplicate'] })
    await until(() => (active.data() as { posts: Array<{ title: string }> }).posts.map((row) => row.title).join() === `${tag}-b`)
    expect(idle.state()?.isInvalidated).toBe(true)
  })

  it('does not touch the cache when a single root fails without committing', async () => {
    const world = await env()
    const tag = `f-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const list = cached(world, LIST, { t: tag })
    await list.load()
    const failure = await world.mutations.execute(RENAME, { id: crypto.randomUUID(), title: `${tag}-b` }).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(GolemError)
    expect(failure).not.toBeInstanceOf(GolemPartialCommitError)
    expect(list.state()?.isInvalidated).toBe(false)
    expect((list.data() as { posts: Array<{ id: string }> }).posts.map((row) => row.id)).toEqual([id])
  })

  it('invalidates the queries over the model after an updateMany and refetches the active ones', async () => {
    const world = await env()
    const tag = `u-${world.who.id}`
    await createPost(world, `${tag}-a`)
    const active = cached(world, LIST, { t: tag })
    const idle = cached(world, SINGLE, { id: await createPost(world, `${tag}-z`) })
    const observer = new QueryObserver(world.queryClient, active.options)
    open.push(observer.subscribe(() => undefined))
    await until(() => active.data() !== undefined)
    await idle.load()
    const result = await world.mutations.execute<{ updateManyPosts: { count: number } }>(RENAME_MANY, { t: tag, title: `${tag}-renamed` })
    expect(result.updateManyPosts.count).toBe(2)
    await until(() => (active.data() as { posts: Array<{ title: string }> }).posts.every((row) => row.title === `${tag}-renamed`))
    expect(idle.state()?.isInvalidated).toBe(true)
  })

  it('invalidates the queries over the model after a deleteMany', async () => {
    const world = await env()
    const tag = `x-${world.who.id}`
    await createPost(world, `${tag}-a`)
    await createPost(world, `${tag}-b`)
    const active = cached(world, LIST, { t: tag })
    const observer = new QueryObserver(world.queryClient, active.options)
    open.push(observer.subscribe(() => undefined))
    await until(() => active.data() !== undefined)
    const result = await world.mutations.execute<{ deleteManyPosts: { count: number } }>(REMOVE_MANY, { t: tag })
    expect(result.deleteManyPosts.count).toBe(2)
    await until(() => (active.data() as { posts: unknown[] }).posts.length === 0)
  })

  it('raises an unknown commit unchanged and invalidates the queries over the models it may have written', async () => {
    const world = await env()
    const tag = `n-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const list = cached(world, LIST, { t: tag })
    await list.load()
    const unknown = new GolemCommitUnknownError(new GolemError({ message: 'commit outcome unknown' }), [], undefined)
    const mutations = new Mutations({ queryClient: world.queryClient, transport: { execute: () => Promise.reject(unknown) }, manifest })
    const failure = await mutations.execute(RENAME, { id, title: `${tag}-b` }).catch((error: unknown) => error)
    expect(failure).toBe(unknown)
    expect(list.state()?.isInvalidated).toBe(true)
  })
})

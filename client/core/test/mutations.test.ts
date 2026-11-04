import { QueryClient, QueryObserver } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemCommitUnknownError, GolemError, GolemPartialCommitError, LiveSync, Mutations, Transport, type Credentials } from '../src/index.js'
import { HttpGate, sleep, until } from './harness/live.js'
import { actor, CREATE_POST, headerSocket, manifest, postInput, signUp, socialUrl } from './harness/social.js'

const analyzer = new DocumentAnalyzer(manifest)

const LIST = 'query($t: String!) { posts(where: { title: { contains: $t } }) { title } }'
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
  it('merges a returned row into every cached query holding the key', async () => {
    const world = await env()
    const tag = `m-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const list = cached(world, LIST, { t: tag })
    const single = cached(world, SINGLE, { id })
    await list.load()
    await single.load()
    const before = (single.data() as { post: { _version: string } }).post._version
    const result = await world.mutations.execute<{ updatePost: { title: string; _version: string } }>(RENAME, { id, title: `${tag}-b` })
    expect(BigInt(result.updatePost._version)).toBeGreaterThan(BigInt(before))
    expect((list.data() as { posts: Array<{ title: string; _version: string }> }).posts.map((row) => row.title)).toEqual([`${tag}-b`])
    expect((single.data() as { post: { title: string; _version: string } }).post).toMatchObject({ title: `${tag}-b`, _version: result.updatePost._version })
  })

  it('keeps a cached row that is newer than the returned one', async () => {
    const world = await env()
    const tag = `k-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const list = cached(world, LIST, { t: tag })
    await list.load()
    world.gate.hold()
    const pending = world.mutations.execute(RENAME, { id, title: `${tag}-old` })
    await until(() => world.gate.calls.some((call) => call.query.includes('updatePost')))
    await sleep(100)
    await world.direct.execute({ query: RENAME, variables: { id, title: `${tag}-new` } })
    await world.queryClient.fetchQuery({ ...list.options, staleTime: 0 })
    expect((list.data() as { posts: Array<{ title: string }> }).posts.map((row) => row.title)).toEqual([`${tag}-new`])
    world.gate.release()
    await pending
    expect((list.data() as { posts: Array<{ title: string }> }).posts.map((row) => row.title)).toEqual([`${tag}-new`])
  })

  it('removes the row a delete root returns from every cached query', async () => {
    const world = await env()
    const tag = `d-${world.who.id}`
    const id = await createPost(world, `${tag}-a`)
    const keep = await createPost(world, `${tag}-b`)
    const list = cached(world, LIST, { t: tag })
    await list.load()
    expect((list.data() as { posts: unknown[] }).posts).toHaveLength(2)
    await world.mutations.execute(REMOVE, { id })
    expect((list.data() as { posts: Array<{ id: string }> }).posts.map((row) => row.id)).toEqual([keep])
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

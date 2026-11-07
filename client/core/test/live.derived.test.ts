import { randomUUID } from 'node:crypto'
import { afterEach, describe, expect, it } from 'vitest'
import { Transport } from '../src/index.js'
import { DELETE_POST, sleep, until, watch, world, type World } from './harness/live.js'
import { actor, CREATE_POST, headerSocket, manifest, postInput, signUp, socialUrl } from './harness/social.js'

const open: World[] = []

async function fresh(): Promise<World> {
  const created = await world()
  open.push(created)
  return created
}

afterEach(async () => {
  for (const one of open.splice(0)) {
    await one.close()
  }
})

const token = () => `d${randomUUID().slice(0, 8)}`

const COUNT = 'query($t: String!) { aggregatePosts(where: { title: { contains: $t } }) { count } }'

const DELETE_MANY = 'mutation($t: String!) { deleteManyPosts(where: { title: { contains: $t } }) { count } }'

const count = (data: unknown): string | undefined => (data as { aggregatePosts?: { count: string } } | undefined)?.aggregatePosts?.count

async function settledCount(w: World, mine: string, expected: string) {
  const view = watch(w, COUNT, { t: mine })
  await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && count(view.data()) === expected)
  await sleep(1000)
  return view
}

describe('a live aggregate', () => {
  it('refetches on CREATED, UPDATED and DELETED and is never patched', async () => {
    const w = await fresh()
    const mine = token()
    const first = await w.createPost(`${mine}-one`)
    const view = await settledCount(w, mine, '1')
    const before = view.fetches()
    await w.createPost(`${mine}-two`)
    await until(() => count(view.data()) === '2')
    await until(() => view.fetches() === before + 1)
    await w.updatePost(first, { views: { set: 7 } })
    await until(() => view.fetches() === before + 2)
    await w.run(DELETE_POST, { id: first })
    await until(() => count(view.data()) === '1')
    await until(() => view.fetches() === before + 3)
  })

  it('refetches when a row leaves the where (EXITED)', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-one`)
    const view = await settledCount(w, mine, '1')
    const before = view.fetches()
    await w.updatePost(id, { title: { set: 'renamed' } })
    await until(() => count(view.data()) === '0')
    expect(view.fetches()).toBe(before + 1)
  })

  it('refetches once for a transaction that deletes many rows', async () => {
    const w = await fresh()
    const mine = token()
    for (let index = 0; index < 10; index++) {
      await w.createPost(`${mine}-${index}`)
    }
    const view = await settledCount(w, mine, '10')
    const before = view.fetches()
    await w.run(DELETE_MANY, { t: mine })
    await until(() => count(view.data()) === '0')
    await sleep(800)
    expect(view.fetches()).toBe(before + 1)
  })

  it('is correct after a disconnect and reconnect', async () => {
    const w = await fresh()
    const mine = token()
    await w.createPost(`${mine}-one`)
    const view = await settledCount(w, mine, '1')
    w.proxy.drop()
    await w.run(CREATE_POST, { data: postInput(`${mine}-two`) })
    await until(() => count(view.data()) === '2', 15_000)
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE')
  })

  it('does not refetch for a post the viewer cannot read', async () => {
    const w = await fresh()
    const mine = token()
    await w.createPost(`${mine}-one`)
    const view = await settledCount(w, mine, '1')
    const before = view.fetches()
    const other = actor()
    const stranger = new Transport({ url: `${socialUrl()}/graphql`, manifest, credentials: other.credentials, createSocket: headerSocket })
    await signUp(stranger, other)
    await stranger.execute({ query: CREATE_POST, variables: { data: { ...postInput(`${mine}-hidden`), published: false } } })
    await sleep(1000)
    expect(view.fetches()).toBe(before)
    expect(count(view.data())).toBe('1')
    stranger.dispose()
  })
})

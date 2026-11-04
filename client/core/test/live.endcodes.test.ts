import { randomUUID } from 'node:crypto'
import { afterEach, describe, expect, it } from 'vitest'
import { Transport, type Credentials } from '../src/index.js'
import { CREATE_COMMENT, LIST, POST_WITH_COMMENTS, sleep, titles, until, watch, world, type World } from './harness/live.js'
import { spy, type Spy } from './harness/spy.js'
import { actor, headerSocket, invalidatePolicies, manifest, setPrincipalMode, signUp, socialUrl, spawnSocial, type SpawnedSocial } from './harness/social.js'
import { CREATE_POST, postInput } from './harness/social.js'

const worlds: World[] = []
const servers: SpawnedSocial[] = []

afterEach(async () => {
  for (const one of worlds.splice(0)) {
    await one.close()
  }
  for (const one of servers.splice(0)) {
    await one.dispose()
  }
})

const token = () => `e${randomUUID().slice(0, 8)}`

async function live(overrides: Parameters<typeof world>[0] = {}): Promise<{ w: World; probe: Spy }> {
  let probe: Spy | undefined
  const w = await world({ ...overrides, wrap: (transport: Transport) => (probe = spy(transport)).transport })
  worlds.push(w)
  return { w, probe: probe as unknown as Spy }
}

async function server(env: Record<string, string>): Promise<SpawnedSocial> {
  const started = await spawnSocial(env)
  servers.push(started)
  return started
}

async function settledView(w: World, mine: string, count: number) {
  const view = watch(w, LIST, { t: mine })
  await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && titles(view.data()).length === count)
  await sleep(1000)
  return view
}

describe('RESYNC', () => {
  it('reopens the streams and refetches once when the policies are invalidated', async () => {
    const { w, probe } = await live()
    const mine = token()
    await w.createPost(`${mine}-one`)
    const view = await settledView(w, mine, 1)
    const before = view.fetches()
    await invalidatePolicies(socialUrl())
    await until(() => probe.ends.includes('GOLEM_SUBSCRIPTION_RESYNC'))
    await until(() => view.fetches() === before + 1)
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE')
    await sleep(600)
    expect(view.fetches()).toBe(before + 1)
    await w.createPost(`${mine}-two`)
    await until(() => titles(view.data()).length === 2)
  })

  it('DEPENDENCY_CHANGED refetches without waiting for the coalescing window', async () => {
    const { w, probe } = await live({ windowMs: 2000, maxWaitMs: 2000 })
    const carol = actor()
    const author = new Transport({ url: `${socialUrl()}/graphql`, manifest, credentials: carol.credentials, createSocket: headerSocket })
    await signUp(author, carol)
    const created = await author.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput(token()) } })
    const post = created.createPost.id
    await author.execute({ query: CREATE_COMMENT, variables: { id: randomUUID(), post, author: carol.id, body: 'hello' } })
    const view = watch(w, POST_WITH_COMMENTS, { id: post })
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && (view.data() as { post?: unknown } | undefined)?.post !== undefined)
    await sleep(2500)
    const before = view.fetches()
    const started = Date.now()
    await author.execute({ query: 'mutation($id: UUID!) { updatePost(where: { ID: $id }, data: { published: { set: false } }) { id } }', variables: { id: post } })
    await until(() => view.fetches() === before + 1, 1800)
    expect(Date.now() - started).toBeLessThan(1500)
    expect(probe.ends).toContain('GOLEM_SUBSCRIPTION_RESYNC')
    await until(() => (view.data() as { post: unknown }).post === null)
    author.dispose()
  }, 40_000)
})

describe('OVERFLOW', () => {
  it('reloads and resubscribes after the subscriber falls behind', async () => {
    const small = await server({ GOLEM_TEST_SUBSCRIBER_QUEUE: '1' })
    const { w, probe } = await live({ url: small.url })
    const mine = token()
    const document = 'query($t: String!) { posts(where: { title: { contains: $t } }, take: 100) { title body } }'
    const view = watch(w, document, { t: mine })
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && view.data() !== undefined)
    await sleep(500)
    const bulk = 'x'.repeat(200_000)
    w.proxy.stall('downstream')
    let created = 0
    while (!probe.ends.includes('GOLEM_SUBSCRIPTION_OVERFLOW') && created < 80) {
      await w.run(CREATE_POST, { data: { ...postInput(`${mine}-${created}`), body: bulk } })
      created++
      await sleep(40)
    }
    w.proxy.unstall('downstream')
    await until(() => probe.ends.includes('GOLEM_SUBSCRIPTION_OVERFLOW'), 20_000)
    await until(() => titles(view.data()).length === created && w.live.statusOf(view.query)?.state === 'LIVE', 30_000)
    expect(titles(view.data())).toHaveLength(created)
  }, 120_000)
})

describe('REVALIDATION', () => {
  it('asks the credentials again, resubscribes and refetches', async () => {
    let restore = false
    let calls = 0
    let who = ''
    const credentialsFor = (user: { id: string }) => {
      who = user.id
      return async (): Promise<Credentials> => {
        if (restore) {
          restore = false
          await setPrincipalMode(socialUrl(), who, '')
        }
        calls++
        return { headers: { 'x-golem-user': who } }
      }
    }
    const { w, probe } = await live({ credentialsFor })
    const other = actor()
    const author = new Transport({ url: `${socialUrl()}/graphql`, manifest, credentials: other.credentials, createSocket: headerSocket })
    await signUp(author, other)
    const mine = token()
    const view = await settledView(w, mine, 0)
    const before = view.fetches()
    const callsBefore = calls
    restore = true
    await setPrincipalMode(socialUrl(), who, 'unresolvable')
    await author.execute({ query: CREATE_POST, variables: { data: postInput(`${mine}-trigger`) } })
    await until(() => probe.ends.includes('GOLEM_SUBSCRIPTION_REVALIDATION'), 15_000)
    await until(() => calls > callsBefore, 10_000)
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && titles(view.data()).length === 1, 15_000)
    expect(probe.renews()).toBeGreaterThanOrEqual(1)
    expect(view.fetches()).toBeGreaterThan(before)
    author.dispose()
  }, 60_000)
})

describe('the server going away', () => {
  it('survives the server going away, resubscribes when it returns and refetches', async () => {
    const mortal = await server({})
    const { w } = await live({ url: mortal.url })
    const mine = token()
    await w.createPost(`${mine}-one`)
    const view = await settledView(w, mine, 1)
    const before = view.fetches()
    await mortal.stop(5000)
    await until(() => w.live.statusOf(view.query)?.state === 'RECOVERING', 10_000)
    await mortal.restart()
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE' && view.fetches() > before, 40_000)
    await w.createPost(`${mine}-two`)
    await until(() => titles(view.data()).length === 2, 10_000)
  }, 90_000)
})

describe('FORBIDDEN', () => {
  it('stops that stream and does not resubscribe with the same principal', async () => {
    const checked = await server({ GOLEM_TEST_RECHECK_MS: '1000' })
    const { w, probe } = await live({ url: checked.url })
    const mine = token()
    const view = await settledView(w, mine, 0)
    const subscribed = probe.subscribes()
    await setPrincipalMode(checked.url, w.who.id, 'forbidden')
    await until(() => probe.ends.includes('GOLEM_SUBSCRIPTION_FORBIDDEN'), 15_000)
    await until(() => w.live.statusOf(view.query)?.state === 'FORBIDDEN')
    await sleep(1500)
    expect(probe.subscribes()).toBe(subscribed)
    expect(w.live.statusOf(view.query)?.state).toBe('FORBIDDEN')
  }, 40_000)
})

describe('LIMIT', () => {
  it('reports LIMIT, still serves the data, and retries when one of its own streams closes', async () => {
    const { w, probe } = await live({ closeGraceMs: 200 })
    const mine = token()
    await w.createPost(`${mine}-visible`)
    const holders = Array.from({ length: 32 }, () => watch(w, LIST, { t: token() }))
    await until(() => holders.every((one) => w.live.statusOf(one.query)?.state === 'LIVE'), 20_000)
    const limited = watch(w, LIST, { t: mine })
    await until(() => w.live.statusOf(limited.query)?.state === 'LIMIT', 10_000)
    expect(probe.ends).toContain('GOLEM_SUBSCRIPTION_LIMIT')
    await until(() => titles(limited.data()).length === 1)
    const before = limited.fetches()
    holders[0]?.stop()
    await until(() => w.live.statusOf(limited.query)?.state === 'LIVE', 10_000)
    await until(() => limited.fetches() === before + 1)
    await w.createPost(`${mine}-live`)
    await until(() => titles(limited.data()).length === 2)
    for (const one of holders) {
      one.stop()
    }
  }, 60_000)
})

describe('4401 at the credential deadline', () => {
  it('reconnects with a fresh principal and refetches once, discarding a response held across the reconnect', async () => {
    const mine = token()
    const bob = actor()
    let current = ''
    let aliceId = ''
    const credentialsFor = (user: { id: string }) => {
      aliceId = user.id
      current = user.id
      return (): Credentials => ({
        headers: { 'x-golem-user': current, ...(current === aliceId ? { 'x-golem-test-deadline-ms': '8000' } : {}) },
      })
    }
    const { w, probe } = await live({ credentialsFor })
    await w.createPost(`${mine}-public`)
    await w.createPost(`${mine}-draft`, false)
    const view = await settledView(w, mine, 2)
    const before = view.fetches()
    w.gate.hold()
    void w.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true })
    await until(() => view.fetches() === before + 1)
    current = bob.id
    await until(() => probe.ends.includes('reauthenticate'), 15_000)
    await until(() => w.live.statusOf(view.query)?.state === 'LIVE', 15_000)
    await until(() => view.fetches() === before + 2, 5000)
    w.gate.release()
    await until(() => titles(view.data()).length === 1, 5000)
    await sleep(600)
    expect(titles(view.data())).toEqual([`${mine}-public`])
    expect(view.fetches()).toBe(before + 2)
  }, 60_000)
})

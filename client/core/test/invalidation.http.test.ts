import { InfiniteQueryObserver, QueryClient } from '@tanstack/query-core'
import { describe, expect, it } from 'vitest'
import { GolemClient, Transport, golemBlock, type StreamEnd } from '../src/index.js'
import { generatedFixture, generatedManifest, generatedSocket, generatedURL as url } from './harness/generated.js'
import { sleep, until } from './harness/live.js'

async function change(value: Record<string, unknown>) {
  const response = await fetch(`${url}/test/change`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(value) })
  if (response.status !== 204) throw new Error(`fixture change failed: ${await response.text()}`)
}

describe.skipIf(url === undefined || generatedFixture !== 'invalidation')('fresh generated invalidation HTTP and WebSocket', () => {
  it('isolates readable and hidden-fact authority and resets two independent live continuation chains', async () => {
    const manifest = generatedManifest()
    expect(manifest.requiredCapabilities).toEqual(['data-incarnation-v1', 'operation-invalidation-v1'])
    const stats = async () => (await (await fetch(`${url}/test/stats`)).json()) as { resolvers: number; binders: number; reports: number }
    const before = await stats()
    // An old client must fail before static execution or inferred row streams.
    for (const extensions of [undefined, { golem: { protocol: 1, contract: manifest.contract, manifestVersion: 3 } }]) {
      const response = await fetch(`${url}/graphql`, { method: 'POST', headers: { 'content-type': 'application/json', 'x-golem-profile': 'a' }, body: JSON.stringify({ query: '{staticState(category:"watch"){revision}}', extensions }) })
      const body = await response.json() as { errors?: Array<{ extensions?: { code?: string } }> }
      expect(body.errors?.[0]?.extensions?.code).toBe('GOLEM_PROTOCOL_MISMATCH')
    }
    expect((await stats()).resolvers).toBe(before.resolvers)
    const legacySocket = generatedSocket(`${url!.replace(/^http/, 'ws')}/graphql`, ['graphql-transport-ws'], { headers: { 'x-golem-profile': 'a' } })
    let legacyClose: CloseEvent | undefined
    let legacyAck = false
    legacySocket.onopen = () => legacySocket.send(JSON.stringify({ type: 'connection_init', payload: { golem: { protocol: 1, contract: manifest.contract, manifestVersion: 3 } } }))
    legacySocket.onmessage = (event) => { if (JSON.parse(String(event.data)).type === 'connection_ack') legacyAck = true }
    legacySocket.onclose = (event) => { legacyClose = event }
    await until(() => legacyClose !== undefined, 5000, 'old-client capability refusal')
    expect(legacyClose?.code).toBe(4426)
    expect(legacyAck).toBe(false)

    const transports: Transport[] = []
    const subscriptions: Array<() => void> = []
    const subscribe = async (field: string, profile = 'a') => {
      const transport = new Transport({ url: `${url}/graphql`, manifest, credentials: () => ({ headers: { 'x-golem-profile': profile } }), createSocket: generatedSocket })
      transports.push(transport)
      await transport.ready()
      const result = { acked: false, packets: [] as unknown[], end: undefined as StreamEnd | undefined }
      subscriptions.push(transport.subscribe({ query: `subscription{${field}(category:"watch")}` }, {
        onAck: () => { result.acked = true }, onEvent: (data) => { result.packets.push(data) },
        onInterrupted: () => undefined, onEnd: (end) => { result.end = end },
      }))
      await until(() => result.acked || result.end !== undefined, 5000, 'invalidation admission')
      return result
    }
    const assertChanged = async (stream: Awaited<ReturnType<typeof subscribe>>) => {
      await until(() => stream.end !== undefined, 5000, 'authorized invalidation')
      expect(stream.packets).toEqual([])
      expect(stream.end?.kind).toBe('error')
      if (stream.end?.kind === 'error') {
        expect(stream.end.errors[0]?.code).toBe('GOLEM_SUBSCRIPTION_RESYNC')
        expect(stream.end.errors[0]?.extensions.reason).toBe('DEPENDENCY_CHANGED')
      }
    }
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: 0 } } })
    let sockets = 0
    const client = new GolemClient({ queryClient, url: `${url}/graphql`, manifest, credentials: () => ({ headers: { 'x-golem-profile': 'a' } }), createSocket: (...args) => { sockets++; return generatedSocket(...args) } })
    const detach = client.attach()
    // Model a second tab with its own transport, recovery coordinator and
    // TanStack cache. Sharing a client or query options would hide this boundary.
    const secondQueryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: 0 } } })
    const secondClient = new GolemClient({ queryClient: secondQueryClient, url: `${url}/graphql`, manifest, credentials: () => ({ headers: { 'x-golem-profile': 'a' } }), createSocket: generatedSocket })
    const detachSecond = secondClient.attach()
    let unobserve: () => void = () => undefined
    let unobserveSecond: () => void = () => undefined
    try {
      const staticOptions = client.staticQueryOptions('{staticState(category:"watch"){revision}}')
      expect(await queryClient.fetchQuery(staticOptions)).toEqual({ staticState: { revision: 0 } })
      expect(sockets).toBe(0)
      const rows = await subscribe('progressStateChanges')
      expect(rows.acked).toBe(true) // Progress.ID is unreadable; no public ID is requested.
      for (const irrelevant of [
        { kind: 'noise', account: 'a', category: 'watch', revision: 0 },
        { kind: 'position', account: 'b', category: 'watch', position: 1, revision: 1 },
        { kind: 'position', account: 'a', category: 'other', position: 1, revision: 0 },
      ]) {
        await change(irrelevant)
        await sleep(250)
        expect(rows.end, `irrelevant change: ${JSON.stringify(irrelevant)}`).toBeUndefined()
      }
      await change({ kind: 'position', account: 'a', category: 'watch', position: 1, revision: 1 })
      await assertChanged(rows)
      const denied = await subscribe('progressStateChanges', 'deny')
      expect(denied.acked).toBe(false)
      expect(denied.end?.kind).toBe('error')
      const facts = await subscribe('factStateChanges')
      expect(facts.acked).toBe(true) // Owner rows themselves have no read grant.
      await change({ kind: 'owner', account: 'b', ready: true, revision: 2 })
      await sleep(250)
      expect(facts.end).toBeUndefined()
      await change({ kind: 'owner', account: 'a', ready: true, revision: 2 })
      await assertChanged(facts)
      const leaving = await subscribe('progressStateChanges')
      await change({ kind: 'reparent', account: 'a', revision: 3 })
      await assertChanged(leaving)

      const options = client.continuationQueryOptions('query($category:String!){feed(category:$category,take:25){items{index revision}}}', { category: 'watch' })
      const observer = new InfiniteQueryObserver(queryClient, options)
      const secondOptions = secondClient.continuationQueryOptions('query($category:String!){feed(category:$category,take:25){items{index revision}}}', { category: 'watch' })
      const secondObserver = new InfiniteQueryObserver(secondQueryClient, secondOptions)
      unobserve = observer.subscribe(() => undefined)
      unobserveSecond = secondObserver.subscribe(() => undefined)
      for (const tab of [observer, secondObserver]) {
        await tab.refetch({ throwOnError: true })
        await tab.fetchNextPage({ throwOnError: true })
        await tab.fetchNextPage({ throwOnError: true })
        expect(tab.getCurrentResult().data?.pages).toHaveLength(3)
      }
      expect(observer.getCurrentResult().data).not.toBe(secondObserver.getCurrentResult().data)
      await change({ kind: 'owner', account: 'a', ready: false, revision: 4 })
      await until(() => [observer, secondObserver].every((tab) => {
        const data = tab.getCurrentResult().data
        return data?.pages.length === 1 && (data.pages[0] as { feed: { items: Array<{ revision: number }> } }).feed.items[0]?.revision === 4
      }), 8000, 'both independent live chains restart after hidden owner change')
      for (const tab of [observer, secondObserver]) {
        expect(tab.getCurrentResult().data?.pages).toHaveLength(1)
        expect(tab.getCurrentResult().data?.pageParams).toEqual([null])
      }
      expect(observer.getCurrentResult().data).not.toBe(secondObserver.getCurrentResult().data)
      const scope = await subscribe('feedChanges')
      const response = await fetch(`${url}/test/profile?account=b`, { method: 'POST' })
      expect(response.status).toBe(204)
      await until(() => scope.end !== undefined, 5000, 'fresh authority revalidation')
      expect(scope.end?.kind).toBe('error')
      if (scope.end?.kind === 'error') {
        expect(scope.end.errors[0]?.code).toBe('GOLEM_SUBSCRIPTION_RESYNC')
        expect(scope.end.errors[0]?.extensions.reason).not.toBe('DEPENDENCY_CHANGED')
      }
      expect(scope.packets).toEqual([])
      expect((await stats()).reports).toBe(0)
      expect(golemBlock(manifest).capabilities).toEqual(['data-incarnation-v1', 'operation-invalidation-v1'])
    } finally {
      unobserve()
      unobserveSecond()
      detach()
      detachSecond()
      client.dispose()
      secondClient.dispose()
      queryClient.clear()
      secondQueryClient.clear()
      for (const stop of subscriptions) stop()
      for (const transport of transports) transport.dispose()
    }
  }, 60_000)
})

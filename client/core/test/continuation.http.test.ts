import { InfiniteQueryObserver, QueryClient } from '@tanstack/query-core'
import { describe, expect, it, vi } from 'vitest'
import { GolemClient, GolemCommitUnknownError } from '../src/index.js'
import { generatedFixture, generatedManifest, generatedURL as url } from './harness/generated.js'

// The fresh generated Go consumer supplies its own emitted manifest and HTTP
// handler. This test never starts or substitutes the social fixture server.
describe.skipIf(url === undefined || generatedFixture !== 'continuation')('fresh generated continuation HTTP', () => {
  it('traverses the real feed and empty pages without subscriptions or private field enrichment', async () => {
    const manifest = generatedManifest()
    for (const profile of ['full', 'none', 'privateID']) {
      let principal = profile
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
      let socketCalls = 0
      const client = new GolemClient({
        queryClient, url: `${url}/graphql`, manifest,
        credentials: () => ({ headers: { 'x-golem-profile': principal } }),
        createSocket: () => { socketCalls++; throw new Error('static page opened an event socket') },
      })
      const detach = client.attach()
      const query = profile === 'privateID'
        ? 'query($seed:Int!,$mode:String!){feed:identityRows(seed:$seed,mode:$mode,take:25){items{title}}}'
        : 'query($seed:Int!,$mode:String!){feed:rankedFeed(seed:$seed,mode:$mode,take:25){items{ordinal tie entry{title}}}}'
      const observer = new InfiniteQueryObserver(queryClient, client.staticContinuationQueryOptions(query, { seed: 7, mode: 'normal' }))
      const unsubscribe = observer.subscribe(() => undefined)
      try {
        await observer.refetch({ throwOnError: true })
        for (let count = 0; observer.getCurrentResult().hasNextPage; count++) {
          if (count >= 3) throw new Error('real feed failed to terminate')
          await observer.fetchNextPage({ throwOnError: true })
        }
        const result = observer.getCurrentResult()
        expect(result.error).toBeNull()
        const pages = result.data!.pages as Array<{ feed: { items: Array<Record<string, unknown>> } }>
        expect(pages).toHaveLength(3)
        expect(pages.map((page) => page.feed.items.length)).toEqual(profile === 'none' ? [0, 0, 0] : [25, 25, 10])
        const items = pages.flatMap((page) => page.feed.items)
        if (profile === 'full') {
          expect(items.map((item) => item.ordinal)).toEqual(Array.from({ length: 60 }, (_, index) => index + 1))
        }
        if (profile === 'privateID') {
          expect(items.map((item) => item.title)).toEqual(Array.from({ length: 60 }, (_, index) => `entry-${String(index + 1).padStart(2, '0')}`))
          expect(items.every((item) => Object.keys(item).join(',') === 'title')).toBe(true)
        }
        if (profile === 'full') {
          principal = 'none'
          await client.resetSession()
          await observer.refetch({ throwOnError: true })
          const restarted = observer.getCurrentResult().data!
          expect(restarted.pageParams).toEqual([null])
          expect(restarted.pages).toHaveLength(1)
          expect((restarted.pages[0] as { feed: { items: unknown[] } }).feed.items).toEqual([])
          expect(observer.getCurrentResult().hasNextPage).toBe(true)
        }
        expect(socketCalls).toBe(0)
      } finally {
        unsubscribe()
        detach()
        client.dispose()
        queryClient.clear()
      }
    }
  })

  it('reports an actually committed held write as UNKNOWN after reset without replay or stale effects', async () => {
    const manifest = generatedManifest()
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    let release!: () => void
    const held = new Promise<void>((resolve) => { release = resolve })
    let committed!: (body: unknown) => void
    const witnessed = new Promise<unknown>((resolve) => { committed = resolve })
    let writes = 0
    const client = new GolemClient({
      queryClient, url: `${url}/graphql`, manifest,
      credentials: () => ({ headers: { 'x-golem-profile': 'full' } }),
      fetch: async (input, init) => {
        const request = JSON.parse(String(init?.body)) as { query?: string }
        const response = await fetch(input, init)
        if (request.query?.includes('createEntry')) {
          writes++
          // Observe the real generated HTTP result before holding delivery to
          // the client. The write is already durable when reset happens.
          const body = await response.clone().json()
          committed(body)
          await held
        }
        return response
      },
      createSocket: () => { throw new Error('a mutation opened an event socket') },
    })
    const id = '00000000-0000-0000-0000-000000000071'
    const invalidations = vi.spyOn(queryClient, 'invalidateQueries')
    try {
      await client.ready()
      const pending = client.mutate('mutation($data:EntryCreateInput!){createEntry(data:$data){id title}}', {
        data: { id, ordinal: 71, rank: 14, even: false, visible: true, title: 'held-committed-write' },
      }).catch((error: unknown) => error)
      expect(await witnessed).toMatchObject({ data: { createEntry: { id, title: 'held-committed-write' } } })
      await client.resetSession()
      const fresh = ['golem', 'fresh-after-held-write']
      queryClient.setQueryData(fresh, { fresh: true })
      invalidations.mockClear()
      release()
      const failure = await pending
      expect(failure).toBeInstanceOf(GolemCommitUnknownError)
      expect(failure).toMatchObject({ code: 'GOLEM_SESSION_CHANGED', retryable: false })
      expect(writes).toBe(1)
      expect(invalidations).not.toHaveBeenCalled()
      expect(queryClient.getQueryData(fresh)).toEqual({ fresh: true })
      expect(await client.transport.execute({ query: 'query($id:UUID!){entry(where:{ID:$id}){id title}}', variables: { id } })).toEqual({ entry: { id, title: 'held-committed-write' } })
      expect(writes).toBe(1)
    } finally {
      release()
      invalidations.mockRestore()
      client.dispose()
      queryClient.clear()
    }
  })

})

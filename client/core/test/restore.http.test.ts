import { InfiniteQueryObserver, QueryClient, QueryObserver } from '@tanstack/query-core'
import { describe, expect, it, vi } from 'vitest'
import { GolemClient, GolemCommitUnknownError, GolemSessionError } from '../src/index.js'
import { generatedFixture, generatedManifest, generatedURL as url } from './harness/generated.js'

type Request = {
  query?: string
  variables?: Record<string, unknown>
  extensions: { golem: { dataIncarnation?: string; discover?: { nonce: string } } }
}
type Host = { PID: number; Identity: string; Contract: string; Generation: string }

// The Go owner supplies a persistent proxy over two actual child processes.
// Discovery, query responses, mutation commit and disk restore are all real.
describe.skipIf(url === undefined || generatedFixture !== 'restore')('generated process restore boundary', () => {
  it('adopts the restored identity, drops stale rows/tokens and fences a committed old write', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    const requests: Request[] = []
    const discoveries: string[] = []
    let writes = 0
    let switched = false
    let releaseWrite!: () => void
    const heldWrite = new Promise<void>((resolve) => { releaseWrite = resolve })
    let committed!: (body: unknown) => void
    const witnessedCommit = new Promise<unknown>((resolve) => { committed = resolve })
    let releaseDiscovery!: () => void
    const heldDiscovery = new Promise<void>((resolve) => { releaseDiscovery = resolve })
    let discovered!: () => void
    const witnessedDiscovery = new Promise<void>((resolve) => { discovered = resolve })
    const client = new GolemClient({
      queryClient, url: `${url}/graphql`, manifest: generatedManifest(),
      credentials: () => ({ headers: { 'x-golem-profile': 'full' } }),
      createSocket: () => { throw new Error('static restore reads opened an event socket') },
      fetch: async (input, init) => {
        const request = JSON.parse(String(init?.body)) as Request
        requests.push(request)
        const response = await fetch(input, init)
        if (request.extensions.golem.discover !== undefined) {
          const body = await response.clone().json() as { extensions: { golem: { dataIncarnation: string } } }
          discoveries.push(body.extensions.golem.dataIncarnation)
          if (switched) {
            discovered()
            await heldDiscovery
          }
        }
        if (request.query?.includes('createEntry')) {
          writes++
          // Fully consume the backend response: the mutation has committed
          // before the parent stops the old host, while SDK delivery is held.
          const body: unknown = await response.clone().json()
          committed(body)
          await heldWrite
        }
        return response
      },
    })
    const detach = client.attach()
    const id99 = '00000000-0000-0000-0000-000000000063'
    const id100 = '00000000-0000-0000-0000-000000000064'
    const rowQuery = 'query($id:UUID!){entry(where:{ID:$id}){id title}}'
    const row = new QueryObserver(queryClient, client.staticQueryOptions(rowQuery, { id: id99 }))
    const pages = new InfiniteQueryObserver(queryClient, client.staticContinuationQueryOptions(
      'query($seed:Int!,$mode:String!){feed:rankedFeed(seed:$seed,mode:$mode,take:25){items{ordinal tie entry{title}}}}',
      { seed: 7, mode: 'normal' },
    ))
    const stopRow = row.subscribe(() => undefined)
    const stopPages = pages.subscribe(() => undefined)
    const invalidations = vi.spyOn(queryClient, 'invalidateQueries')
    try {
      await client.ready()
      const identityA = discoveries[0]
      expect(identityA).toMatch(/^[0-9a-f]{32}$/)
      await row.refetch({ throwOnError: true })
      expect(row.getCurrentResult().data).toEqual({ entry: { id: id99, title: 'entry-99' } })
      await pages.refetch({ throwOnError: true })
      await pages.fetchNextPage({ throwOnError: true })
      expect(pages.getCurrentResult().data?.pages).toHaveLength(2)
      const oldToken = pages.getCurrentResult().data!.pageParams[1]
      expect(typeof oldToken).toBe('string')
      expect(requests.some((request) => request.variables?.golemAfter === oldToken)).toBe(true)

      const pendingWrite = client.mutate('mutation($data:EntryCreateInput!){createEntry(data:$data){id title}}', {
        data: { id: id100, ordinal: 100, rank: 20, even: true, visible: true, title: 'held-old-write' },
      }).catch((error: unknown) => error)
      expect(await witnessedCommit).toMatchObject({ data: { createEntry: { id: id100, title: 'held-old-write' } } })

      const control = await fetch(`${url}/test/restore`, { method: 'POST' })
      expect(control.status, await control.clone().text()).toBe(200)
      const replacement = await control.json() as { before: Host; after: Host; oldExited: boolean }
      expect(replacement.oldExited).toBe(true)
      expect(replacement.before.PID).not.toBe(replacement.after.PID)
      expect(replacement.before.Identity).toBe(identityA)
      expect(replacement.after.Identity).not.toBe(identityA)
      expect(replacement.after.Contract).toBe(replacement.before.Contract)
      expect(replacement.after.Generation).toBe(replacement.before.Generation)
      switched = true

      // No explicit resetSession: ordinary stale HTTP admission triggers the
      // authenticated discovery path and the single shared cache boundary.
      await expect(client.transport.execute({ query: rowQuery, variables: { id: id99 } })).rejects.toBeInstanceOf(GolemSessionError)
      await witnessedDiscovery
      expect(row.getCurrentResult().data).toBeUndefined()
      expect(pages.getCurrentResult().data).toBeUndefined()
      releaseDiscovery()
      await client.ready()
      await vi.waitFor(() => {
        expect(pages.getCurrentResult().data?.pageParams).toEqual([null])
        expect(pages.getCurrentResult().data?.pages).toHaveLength(1)
        expect(row.getCurrentResult().isFetching).toBe(false)
        expect(row.getCurrentResult().data).toEqual({ entry: null })
      })
      expect(discoveries).toEqual([identityA, replacement.after.Identity])
      const freshRequests = requests.filter((request) => request.extensions.golem.dataIncarnation === replacement.after.Identity)
      expect(freshRequests.length).toBeGreaterThan(0)
      expect(freshRequests.every((request) => request.variables?.golemAfter === undefined)).toBe(true)
      expect(freshRequests.some((request) => request.query?.includes('rankedFeed'))).toBe(true)

      const freshKey = ['golem', 'restore-fresh-cache']
      queryClient.setQueryData(freshKey, { fresh: true })
      invalidations.mockClear()
      releaseWrite()
      expect(await pendingWrite).toBeInstanceOf(GolemCommitUnknownError)
      expect(writes).toBe(1)
      expect(invalidations).not.toHaveBeenCalled()
      expect(queryClient.getQueryData(freshKey)).toEqual({ fresh: true })
      expect(row.getCurrentResult().data).toEqual({ entry: null })
      expect(pages.getCurrentResult().data?.pageParams).toEqual([null])
      expect(await client.transport.execute({ query: rowQuery, variables: { id: id100 } })).toEqual({ entry: null })
      expect(writes).toBe(1)
      // The generated Go parent independently reopens the restored database
      // after this test and proves both 99 and committed 100 absent, seed60 intact.
    } finally {
      releaseDiscovery()
      releaseWrite()
      stopRow()
      stopPages()
      detach()
      invalidations.mockRestore()
      client.dispose()
      queryClient.clear()
    }
  })
})

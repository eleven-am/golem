import { QueryClient, QueryObserver } from '@tanstack/query-core'
import { describe, expect, it } from 'vitest'
import { GolemClient, RECOVERY_COOLDOWN_MS } from '../src/index.js'
import { generatedFixture, generatedManifest, generatedSocket, generatedURL as url } from './harness/generated.js'
import { until } from './harness/live.js'
import { describeEnd } from './harness/spy.js'

type State = { done: boolean; revision: number; video: string }
type Data = { videos: Array<{ id: string; media: { id: string; title: string } }> }

describe.skipIf(url === undefined || generatedFixture !== 'mixed')('generated mixed workload SDK recovery', () => {
  it('recovers the authorized relation while scanner, jobs and semantic work contend', async () => {
    const state = async (): Promise<State> => {
      const response = await fetch(`${url}/test/state`)
      if (!response.ok) throw new Error(`fixture state failed: ${response.status}`)
      return await response.json() as State
    }
    const initial = await state()
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: 0 } } })
    let fetches = 0
    let subscriptions = 0
    let acknowledgements = 0
    const ends = new Map<string, number>()
    const client = new GolemClient({ queryClient, url: `${url}/graphql`, manifest: generatedManifest(),
      credentials: () => ({ headers: { 'x-golem-profile': 'member' } }), createSocket: generatedSocket,
      fetch: async (input, init) => {
        if (typeof init?.body === 'string') {
          const body = JSON.parse(init.body) as { query?: string }
          if (body.query?.includes('MixedPlayback')) fetches++
        }
        return fetch(input, init)
      },
    })
    const originalSubscribe = client.transport.subscribe
    const subscribe = originalSubscribe.bind(client.transport)
    let reportedTerminal = false
    // Keep counters only: a spy retains every request, callback and disposer
    // throughout the long workload even though this test never reads them.
    client.transport.subscribe = (request, handlers) => {
      subscriptions++
      return subscribe(request, { ...handlers,
        onAck: () => { acknowledgements++; handlers.onAck() },
        onEnd: (end) => {
          const code = describeEnd(end)
          ends.set(code, (ends.get(code) ?? 0) + 1)
          if (!reportedTerminal && (end.kind === 'fatal' || end.kind === 'error' && code !== 'GOLEM_SUBSCRIPTION_RESYNC')) {
            reportedTerminal = true
            console.info('mixed stream ended', JSON.stringify({ request, end }))
          }
          handlers.onEnd(end)
        },
      })
    }
    const detach = client.attach()
    // The relation filter makes Title an actual subscription dependency;
    // merely projecting Title could be satisfied by ordinary row patches.
    const options = client.queryOptions('query MixedPlayback($id:UUID!){videos(where:{id:{equals:$id},media:{is:{title:{startsWith:"mixed-title-"}}}},take:1){id media{id title}}}', { id: initial.video })
    const observer = new QueryObserver(queryClient, options)
    const stop = observer.subscribe(() => undefined)
    let stopStatus: () => void = () => undefined
    let memoryTimer: ReturnType<typeof setInterval> | undefined
    try {
      await observer.refetch({ throwOnError: true })
      const query = observer.getCurrentQuery()
      const data = () => observer.getCurrentResult().data as Data | undefined
      await until(() => client.live.statusOf(query)?.state === 'LIVE' && data()?.videos[0]?.media.title === 'mixed-title-0', 45_000, 'initial acknowledged authorized relation')
      expect(data()?.videos).toHaveLength(1)
      expect(data()?.videos[0]?.id).toBe(initial.video)
      const initialStreams = subscriptions
      const initialFetches = fetches
      const initialAcks = acknowledgements
      expect(initialStreams).toBeGreaterThanOrEqual(2)
      const began = performance.now()
      memoryTimer = setInterval(() => {
        const memory = process.memoryUsage()
        console.info('mixed SDK memory', { elapsedMs: Math.round(performance.now() - began),
          heapUsedMiB: Math.round(memory.heapUsed / 1024 / 1024), rssMiB: Math.round(memory.rss / 1024 / 1024), subscriptions, fetches })
      }, 60_000)
      memoryTimer.unref()
      let unavailableMs = 0
      let unavailableAt: number | undefined
      stopStatus = client.live.subscribeStatus(() => {
        const now = performance.now()
        if (client.live.statusOf(query)?.state === 'RECOVERING') unavailableAt ??= now
        else if (unavailableAt !== undefined) { unavailableMs += now - unavailableAt; unavailableAt = undefined }
      })
      const ready = await fetch(`${url}/test/ready`, { method: 'POST' })
      if (ready.status !== 204) throw new Error(`readiness refused: ${ready.status}`)
      let final: State
      for (;;) {
        final = await state()
        if (final.done) break
        if (observer.getCurrentResult().isError) throw observer.getCurrentResult().error
        await new Promise(resolve => setTimeout(resolve, 200))
      }
      expect(final.revision).toBeGreaterThan(0)
      await until(() => client.live.statusOf(query)?.state === 'LIVE' &&
        data()?.videos[0]?.media.title === `mixed-title-${final.revision}` && acknowledgements > initialAcks,
      45_000, 'final title after replacement acknowledgements')
      expect(data()?.videos).toHaveLength(1)
      expect(data()?.videos[0]?.id).toBe(initial.video)
      expect(data()?.videos[0]?.media.id).toBe('00000000-0000-0000-0000-0000000e7ef1')
      const elapsedMs = performance.now() - began
      const reopened = subscriptions - initialStreams
      const refetches = fetches - initialFetches
      const bound = initialStreams * Math.ceil(elapsedMs / RECOVERY_COOLDOWN_MS) + 4
      expect(ends.get('GOLEM_SUBSCRIPTION_RESYNC') ?? 0).toBeGreaterThan(0)
      expect(reopened).toBeGreaterThan(0)
      expect(refetches).toBeGreaterThan(0)
      expect(reopened).toBeLessThanOrEqual(bound)
      expect(refetches).toBeLessThanOrEqual(bound)
      console.info('mixed actual SDK recovery', { updates: final.revision, elapsedMs: Math.round(elapsedMs), subscriptions: reopened,
        refetches, replacementAcknowledgements: acknowledgements - initialAcks, unavailableMs: Math.round(unavailableMs), endcodes: Object.fromEntries(ends), bound })
    } finally {
      if (memoryTimer !== undefined) clearInterval(memoryTimer)
      stopStatus(); stop(); detach(); await client.dispose(); client.transport.subscribe = originalSubscribe; queryClient.clear()
    }
  })
})

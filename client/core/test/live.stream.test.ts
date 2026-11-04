import { QueryClient, QueryObserver } from '@tanstack/query-core'
import { afterEach, describe, expect, it } from 'vitest'
import { GolemError, LiveSync, type LiveTransport, type StreamHandlers, type SubscriptionRequest } from '../src/index.js'
import { analyzer, LIST, sleep, until } from './harness/live.js'
import { manifest } from './harness/social.js'

class FakeTransport implements LiveTransport {
  readonly streams: StreamHandlers[] = []
  readonly requests: SubscriptionRequest[] = []
  executes = 0
  renews = 0
  posts: unknown[] = []

  async execute<T>(): Promise<T> {
    this.executes++
    return { posts: this.posts } as T
  }

  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    this.requests.push(request)
    this.streams.push(handlers)
    return () => undefined
  }

  renew(): void {
    this.renews++
  }
}

const cleanup: Array<() => void> = []

afterEach(() => {
  for (const one of cleanup.splice(0)) {
    one()
  }
})

function mount() {
  const transport = new FakeTransport()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  const live = new LiveSync({ queryClient, transport, manifest, random: () => 0, windowMs: 20, maxWaitMs: 100 })
  live.start()
  const analysis = analyzer.analyze(LIST)
  const options = live.options(analysis, { t: 'x' })
  const observer = new QueryObserver(queryClient, options)
  const stop = observer.subscribe(() => undefined)
  const query = queryClient.getQueryCache().find({ queryKey: options.queryKey, exact: true })
  if (query === undefined) {
    throw new Error('no query')
  }
  cleanup.push(stop, () => live.stop(), () => queryClient.clear())
  return { transport, live, query }
}

const ended = (code: string) => ({ kind: 'error' as const, errors: [new GolemError({ message: code, extensions: { code } })] })

describe('stream ends the real server does not emit in a reachable scenario', () => {
  it('SOURCE_CLOSED resubscribes and refetches after the new acknowledgement', async () => {
    const { transport, live, query } = mount()
    expect(transport.executes).toBe(0)
    transport.streams[0]?.onAck()
    await until(() => transport.executes === 1)
    transport.streams[0]?.onEnd(ended('GOLEM_SUBSCRIPTION_SOURCE_CLOSED'))
    expect(live.statusOf(query)?.state).toBe('RECOVERING')
    await until(() => transport.streams.length === 2)
    await sleep(100)
    expect(transport.executes).toBe(1)
    transport.streams[1]?.onAck()
    await until(() => transport.executes === 2)
    await sleep(150)
    expect(transport.executes).toBe(2)
    expect(live.statusOf(query)?.state).toBe('LIVE')
  })

  it('CANCELLED resubscribes and refetches instead of leaving a dead stream reported as live', async () => {
    const { transport, live, query } = mount()
    transport.streams[0]?.onAck()
    await until(() => transport.executes === 1)
    transport.streams[0]?.onEnd(ended('GOLEM_SUBSCRIPTION_CANCELLED'))
    expect(live.statusOf(query)?.state).toBe('RECOVERING')
    await until(() => transport.streams.length === 2)
    transport.streams[1]?.onAck()
    await until(() => transport.executes === 2)
    await sleep(150)
    expect(live.statusOf(query)?.state).toBe('LIVE')
  })

  it('an unrecognised end code fails the query without resubscribing, and releases the fetch', async () => {
    const { transport, live, query } = mount()
    transport.streams[0]?.onAck()
    await until(() => transport.executes === 1)
    transport.streams[0]?.onEnd(ended('GOLEM_SUBSCRIPTION_INVALID'))
    expect(live.statusOf(query)?.state).toBe('FAILED')
    await sleep(100)
    expect(transport.streams).toHaveLength(1)
  })

  it('an UPDATED event without an entity for a held row fails the query instead of keeping the stale row', async () => {
    const { transport, live, query } = mount()
    transport.posts = [{ __typename: 'Post', id: 'a', _version: '1', title: 'held' }]
    transport.streams[0]?.onAck()
    await until(() => transport.executes === 1)
    transport.streams[0]?.onEvent({ data: { postEvents: { version: '2', type: 'UPDATED', id: 'a', entity: null } } })
    expect(live.statusOf(query)?.state).toBe('FAILED')
  })

  it('an event of an unknown type fails the query instead of being skipped', async () => {
    const { transport, live, query } = mount()
    transport.streams[0]?.onAck()
    await until(() => transport.executes === 1)
    transport.streams[0]?.onEvent({ data: { postEvents: { version: '1', type: 'MYSTERY', id: 'a', entity: null } } })
    expect(live.statusOf(query)?.state).toBe('FAILED')
  })
})

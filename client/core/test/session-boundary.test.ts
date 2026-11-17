import { InfiniteQueryObserver, QueryClient } from '@tanstack/query-core'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CONTINUATION_RESPONSE_KEY, OPERATION_INVALIDATION_CAPABILITY, DATA_INCARNATION_CAPABILITY, GolemClient, GolemCommitUnknownError, GolemPartialCommitError, GolemSessionError, Transport, golemBlock, type ClientManifest, type Credentials } from '../src/index.js'

import { continuationManifest } from './fixtures/continuation.js'

const A = 'a'.repeat(32)
const B = 'b'.repeat(32)
const legacy = { formatVersion: 3, contract: 'session-fixture', models: {}, roots: {
  value: { kind: 'opaque', shape: 'value', nullable: false, models: [] },
  write: { kind: 'opaque', shape: 'value', nullable: false, models: [] },
} } as const satisfies ClientManifest
const manifest = { ...legacy, requiredCapabilities: [DATA_INCARNATION_CAPABILITY] }
const WRITE = 'mutation($value:Int!){write(value:$value)}'
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
const response = (body: unknown) => new Response(JSON.stringify(body), { status: 200 })
const open: GolemClient[] = []
afterEach(() => { for (const client of open.splice(0)) { client.dispose(); client.queryClient.clear() } vi.restoreAllMocks() })
function build(options: { opted?: boolean; credentials?: () => Credentials | Promise<Credentials>; fetch?: typeof fetch } = {}) {
  let identity = A
  const calls: Array<Record<string, any>> = []
  const send = vi.fn<typeof fetch>(async (_url, init) => {
    const body = JSON.parse(String(init?.body))
    calls.push(body)
    if (body.extensions.golem.discover) {
      expect(Object.keys(body)).toEqual(['extensions'])
      expect(body.extensions.golem.dataIncarnation).toBeUndefined()
      expect(body.extensions.golem.discover.nonce).toMatch(/^[0-9a-f]{32}$/)
      expect(body.extensions.golem.discover.nonce).not.toBe('0'.repeat(32))
      expect(init?.cache).toBe('no-store')
      return response({ data: null, extensions: { golem: { ...body.extensions.golem, dataIncarnation: identity } } })
    }
    return response({ data: { value: identity }, extensions: { golem: { dataIncarnation: identity } } })
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest: options.opted === false ? legacy : manifest, credentials: options.credentials, fetch: options.fetch ?? send, createSocket: () => { throw new Error('static request opened a socket') } })
  open.push(client)
  return { client, queryClient, send, calls, identity: (value: string) => { identity = value } }
}

describe('authenticated incarnation and the single client session boundary', () => {
  it('refuses unready mutations before credentials, coalesces discovery, and lets the initiating static read finish', async () => {
    const credentials = vi.fn(() => ({}))
    const world = build({ credentials })
    await expect(world.client.mutate(WRITE, { value: 1 })).rejects.toMatchObject({ code: 'GOLEM_CLIENT_NOT_READY', dispatched: false })
    expect(credentials).not.toHaveBeenCalled()
    expect(world.send).not.toHaveBeenCalled()
    const old = ['golem', 'old-page']
    world.queryClient.setQueryData(old, { pages: [{ old: true }], pageParams: ['old-token'] })
    world.queryClient.setQueryData(['other'], { kept: true })
    const data = world.queryClient.fetchQuery(world.client.staticQueryOptions('{value}'))
    await Promise.all([world.client.ready(), world.client.ready()])
    await expect(data).resolves.toEqual({ value: A })
    expect(world.calls.filter((call) => call.extensions.golem.discover)).toHaveLength(1)
    expect(world.queryClient.getQueryData(old)).toBeUndefined()
    expect(world.queryClient.getQueryData(['other'])).toEqual({ kept: true })
    expect(world.calls.at(-1)?.extensions.golem.dataIncarnation).toBe(A)
  })

  it.each(['reset', 'dispose'] as const)('does not send an old mutation queued for credentials across %s', async (boundary) => {
    const credentials = deferred<Credentials>()
    const entered = deferred<void>()
    let held = false
    const world = build({ credentials: () => { if (held) { entered.resolve(); return credentials.promise } return {} } })
    await world.client.ready()
    held = true
    const pending = world.client.mutate(WRITE, { value: 1 }).catch((error: unknown) => error)
    await entered.promise
    held = false
    if (boundary === 'reset') await world.client.resetSession()
    else world.client.dispose()
    credentials.resolve({ headers: { authorization: 'old' } })
    expect(await pending).toMatchObject({ dispatched: false, code: boundary === 'reset' ? 'GOLEM_SESSION_CHANGED' : 'GOLEM_CLIENT_DISPOSED' })
    expect(world.calls.every((call) => call.extensions.golem.discover)).toBe(true)
  })

  it('reports a sent stale success as UNKNOWN without invalidating the new session', async () => {
    const held = deferred<Response>(); const sent = deferred<void>()
    const world = build()
    await world.client.ready()
    world.send.mockImplementationOnce(async () => { sent.resolve(); return held.promise })
    const pending = world.client.mutate(WRITE, { value: 1 }).catch((error: unknown) => error)
    await sent.promise
    world.identity(B)
    await world.client.resetSession()
    const invalidations = vi.spyOn(world.queryClient, 'invalidateQueries')
    const freshKey = ['golem', 'fresh']; world.queryClient.setQueryData(freshKey, { fresh: true })
    held.resolve(response({ data: { write: true }, extensions: { golem: { dataIncarnation: A } } }))
    expect(await pending).toBeInstanceOf(GolemCommitUnknownError)
    expect(invalidations).not.toHaveBeenCalled()
    expect(world.queryClient.getQueryData(freshKey)).toEqual({ fresh: true })
    await world.client.transport.execute({ query: '{value}' })
    expect(world.calls.at(-1)?.extensions.golem.dataIncarnation).toBe(B)
  })

  it.each(['PARTIAL', 'UNKNOWN'] as const)('preserves explicit %s evidence across reset without fresh invalidation', async (commit) => {
    const held = deferred<Response>(); const sent = deferred<void>()
    const world = build(); await world.client.ready()
    world.send.mockImplementationOnce(async () => { sent.resolve(); return held.promise })
    const pending = world.client.mutate(WRITE, { value: 1 }).catch((error: unknown) => error)
    await sent.promise; await world.client.resetSession()
    const invalidations = vi.spyOn(world.queryClient, 'invalidateQueries')
    held.resolve(response({ data: { write: 'retained evidence' }, errors: [{ message: 'commit evidence', extensions: { code: 'EVIDENCE' } }], extensions: { golem: { dataIncarnation: A, commit } } }))
    const failure = await pending
    expect(failure).toBeInstanceOf(commit === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)
    expect(failure).toMatchObject({ code: 'EVIDENCE', data: { write: 'retained evidence' } })
    expect(invalidations).not.toHaveBeenCalled()
  })

  it.each(['PARTIAL', 'UNKNOWN'] as const)('preserves the exact explicit %s error object across the final microtask fence', async (commit) => {
    const world = build(); await world.client.ready()
    const failure = new (commit === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)(
      new GolemSessionError('changed', true), [], { write: 'evidence' },
    )
    vi.spyOn(world.client.transport, 'execute').mockImplementation(async () => {
      queueMicrotask(() => { void world.client.resetSession() })
      throw failure
    })
    const invalidations = vi.spyOn(world.queryClient, 'invalidateQueries')
    await expect(world.client.mutate(WRITE, { value: 1 })).rejects.toBe(failure)
    expect(invalidations).not.toHaveBeenCalled()
  })

  it('does not adopt a business-response replacement or reuse a cached old page parameter', async () => {
    const world = build(); await world.client.ready()
    const key = ['golem', 'pages']; world.queryClient.setQueryData(key, { pages: [{ value: A }], pageParams: ['old-token'] })
    world.identity(B)
    const discover = deferred<Response>(); const entered = deferred<Record<string, any>>()
    const original = world.send.getMockImplementation()!
    world.send.mockImplementation(async (input, init) => {
      const body = JSON.parse(String(init?.body))
      if (body.extensions.golem.discover) { entered.resolve(body); return discover.promise }
      return original(input, init)
    })
    await expect(world.client.transport.execute({ query: '{value}' })).rejects.toBeInstanceOf(GolemSessionError)
    expect(world.queryClient.getQueryData(key)).toBeUndefined()
    const request = await entered.promise
    await expect(world.client.mutate(WRITE, { value: 2 })).rejects.toMatchObject({ code: 'GOLEM_CLIENT_NOT_READY' })
    discover.resolve(response({ data: null, extensions: { golem: { ...request.extensions.golem, dataIncarnation: B } } }))
    await world.client.ready()
    await world.client.transport.execute({ query: '{value}' })
    expect(world.calls.at(-1)?.extensions.golem.dataIncarnation).toBe(B)
  })

  it('ignores a late old discovery and adopts only the current nonce response', async () => {
    const old = deferred<Response>(); const started = deferred<Record<string, any>>()
    const world = build()
    world.send.mockImplementationOnce(async (_url, init) => { started.resolve(JSON.parse(String(init?.body))); return old.promise })
    const stale = world.client.ready().catch((error: unknown) => error)
    const request = await started.promise
    world.identity(B); await world.client.resetSession()
    old.resolve(response({ data: null, extensions: { golem: { ...request.extensions.golem, dataIncarnation: A } } }))
    expect(await stale).toBeInstanceOf(GolemSessionError)
    await world.client.transport.execute({ query: '{value}' })
    expect(world.calls.at(-1)?.extensions.golem.dataIncarnation).toBe(B)
  })

  it('captures prepared arguments and session before a scheduler, and refuses replay', async () => {
    const world = build(); await world.client.ready()
    const variables = { value: 1, nested: { list: [{ value: 'original' }] } }
    const first = world.client.mutations.prepare(WRITE, variables)
    variables.value = 2
    variables.nested.list[0]!.value = 'rewritten'
    variables.nested.list.push({ value: 'new' })
    await first()
    expect(world.calls.at(-1)?.variables).toEqual({ value: 1, nested: { list: [{ value: 'original' }] } })
    await expect(first()).rejects.toMatchObject({ code: 'GOLEM_MUTATION_REPLAY_REFUSED' })
    const queued = world.client.mutations.prepare(WRITE, { value: 3 })
    await world.client.resetSession()
    const calls = world.send.mock.calls.length
    await expect(queued()).rejects.toMatchObject({ code: 'GOLEM_SESSION_CHANGED', dispatched: false })
    expect(world.send.mock.calls).toHaveLength(calls)
  })

  it.each(['reset', 'dispose'] as const)('fences a mutation when a cache notification synchronously triggers %s', async (boundary) => {
    for (const outcome of ['success', 'PARTIAL', 'UNKNOWN'] as const) {
      const world = build(); await world.client.ready()
      const first = world.client.staticQueryOptions('{value}')
      const second = world.client.staticQueryOptions('query Second{value}')
      await world.queryClient.fetchQuery(first)
      await world.queryClient.fetchQuery(second)
      const discovery = deferred<void>()
      const send = world.send.getMockImplementation()!
      world.send.mockImplementation(async (input, init) => { await discovery.promise; return send(input, init) })
      const failure = outcome === 'success' ? undefined : new (outcome === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)(
        new GolemSessionError('changed', true), [], { write: 'commit evidence' },
      )
      vi.spyOn(world.client.transport, 'execute').mockImplementation(async () => {
        if (failure !== undefined) throw failure
        return { write: true } as never
      })
      const invalidations = vi.spyOn(world.queryClient, 'invalidateQueries')
      let triggered = false
      let reset: Promise<void> | undefined
      const stop = world.queryClient.getQueryCache().subscribe((event) => {
        if (triggered || event.type !== 'updated' || event.action.type !== 'setState' || !event.query.state.isInvalidated) return
        triggered = true
        if (boundary === 'reset') reset = world.client.resetSession()
        else world.client.dispose()
        // No old invalidation may change data inserted after the reset.
        world.queryClient.setQueryData(second.queryKey, { fresh: true })
      })
      try {
        const pending = world.client.mutate(WRITE, { value: 1 })
        if (failure === undefined) await expect(pending).rejects.toBeInstanceOf(GolemCommitUnknownError)
        else await expect(pending).rejects.toBe(failure)
        expect(triggered).toBe(true)
        expect(invalidations).not.toHaveBeenCalled()
        expect(world.queryClient.getQueryData(second.queryKey)).toEqual({ fresh: true })
        expect(world.queryClient.getQueryState(second.queryKey)?.isInvalidated).toBe(false)
      } finally { stop(); discovery.resolve(); await reset }
    }
  })

  it('fences the final mutation microtask and preserves legacy wire/readiness', async () => {
    const world = build({ opted: false })
    await world.client.ready()
    expect(world.send).not.toHaveBeenCalled()
    vi.spyOn(world.client.transport, 'execute').mockImplementation(async () => {
      queueMicrotask(() => { void world.client.resetSession() })
      return { write: true } as never
    })
    await expect(world.client.mutate(WRITE, { value: 1 })).rejects.toBeInstanceOf(GolemCommitUnknownError)
    vi.restoreAllMocks()
    await world.client.mutate(WRITE, { value: 2 })
    expect(world.calls[0]?.extensions.golem).toEqual(golemBlock(legacy))
    world.client.dispose()
    await expect(world.client.mutate(WRITE, { value: 3 })).rejects.toMatchObject({ code: 'GOLEM_CLIENT_DISPOSED' })
    expect(world.calls).toHaveLength(1)
  })
})

class BoundarySocket {
  readonly protocol = 'graphql-transport-ws'
  readyState = 0
  onopen: (() => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  readonly subscribed = deferred<{ id: string; payload: Record<string, any> }>()
  readonly messages: Array<Record<string, any>> = []
  constructor(readonly ackIdentity: string, readonly automaticLiveAck = false, automaticOpen = true) { if (automaticOpen) queueMicrotask(() => { this.readyState = 1; this.onopen?.() }) }
  send(text: string) {
    const message = JSON.parse(text); this.messages.push(message)
    if (message.type === 'connection_init') this.deliver({ type: 'connection_ack', payload: { golem: { dataIncarnation: this.ackIdentity } } })
    if (message.type === 'subscribe') {
      this.subscribed.resolve(message)
      if (this.automaticLiveAck) queueMicrotask(() => this.deliver({ id: message.id, type: 'next', payload: { data: null, extensions: { golem: { live: true, dataIncarnation: this.ackIdentity } } } }))
    }
  }
  close(code = 1000, reason = '') { this.readyState = 3; this.onclose?.({ code, reason }) }
  deliver(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }) }
}

it.each(['reset', 'dispose'] as const)('closes a socket that opens after %s without an unhandled cleanup rejection', async (boundary) => {
  const created = deferred<BoundarySocket>()
  const queryClient = new QueryClient()
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest,
    fetch: async (_url, init) => { const request = JSON.parse(String(init?.body)); return response({ data: null, extensions: { golem: { ...request.extensions.golem, dataIncarnation: A } } }) },
    createSocket: () => { const socket = new BoundarySocket(A, false, false); created.resolve(socket); return socket },
  })
  open.push(client)
  await client.ready()
  const handlers = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
  const socket = await created.promise
  if (boundary === 'reset') await client.resetSession()
  else client.dispose()
  socket.readyState = 1
  await socket.onopen?.()
  await new Promise((resolve) => setTimeout(resolve, 0))
  expect(socket.readyState).toBe(3)
  expect(socket.messages).toEqual([])
  expect(handlers.onAck).not.toHaveBeenCalled()
  expect(handlers.onEvent).not.toHaveBeenCalled()
  expect(handlers.onEnd).not.toHaveBeenCalled()
  stop()
})

it('stamps connection and subscribe, disposes the old socket on reset, and never adopts an ack replacement', async () => {
  let identity = A
  const sockets: BoundarySocket[] = []
  const created: Array<ReturnType<typeof deferred<BoundarySocket>>> = [deferred(), deferred()]
  const queryClient = new QueryClient()
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest,
    fetch: async (_url, init) => { const request = JSON.parse(String(init?.body)); return response({ data: null, extensions: { golem: { ...request.extensions.golem, dataIncarnation: identity } } }) },
    createSocket: () => { const socket = new BoundarySocket(identity); sockets.push(socket); created[sockets.length - 1]?.resolve(socket); return socket },
  })
  open.push(client)
  await client.ready()
  const ack = vi.fn(); const event = vi.fn()
  const handlers = { onAck: ack, onEvent: event, onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
  const first = await created[0]!.promise
  const message = await first.subscribed.promise
  expect(first.messages[0]?.payload.golem).toEqual({ ...golemBlock(manifest), dataIncarnation: A })
  expect(message.payload.extensions.golem).toEqual({ ...golemBlock(manifest), dataIncarnation: A })
  first.deliver({ id: message.id, type: 'next', payload: { data: null, extensions: { golem: { live: true, dataIncarnation: A } } } })
  expect(ack).toHaveBeenCalledTimes(1)
  identity = B
  await client.resetSession()
  expect(first.readyState).toBe(3)
  first.deliver({ id: message.id, type: 'next', payload: { data: { secret: true }, extensions: { golem: { dataIncarnation: A } } } })
  expect(event).not.toHaveBeenCalled()
  const secondStop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
  const second = await created[1]!.promise
  const secondMessage = await second.subscribed.promise
  expect(secondMessage.payload.extensions.golem.dataIncarnation).toBe(B)
  // A current live ack claiming A fences the session; it does not roll it back.
  second.deliver({ id: secondMessage.id, type: 'next', payload: { data: null, extensions: { golem: { live: true, dataIncarnation: A } } } })
  await expect(client.mutate(WRITE, { value: 2 })).rejects.toMatchObject({ code: 'GOLEM_CLIENT_NOT_READY' })
  expect(ack).toHaveBeenCalledTimes(1)
  stop(); secondStop()
})


const recoveryManifest = {
  ...continuationManifest,
  requiredCapabilities: [DATA_INCARNATION_CAPABILITY, OPERATION_INVALIDATION_CAPABILITY],
  roots: {
    ...continuationManifest.roots,
    homeFeed: { ...continuationManifest.roots.homeFeed, invalidation: { kind: 'operation', subscriptionField: 'homeFeedChanges' } },
    write: legacy.roots.write,
  },
} as const satisfies ClientManifest

async function recoveryWorld() {
  let identity = A
  let discoveries = 0
  let writes = 0
  let failDiscovery = false
  const gate = deferred<void>(); const discovered = deferred<Record<string, any>>()
  const requests: Array<Record<string, any>> = []
  const sockets: BoundarySocket[] = []
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
  const client = new GolemClient({ queryClient, url: 'http://recovery.test/graphql', manifest: recoveryManifest,
    fetch: async (_url, init) => {
      const request = JSON.parse(String(init?.body)); requests.push(request)
      if (request.extensions.golem.discover) {
        discoveries++
        if (discoveries > 1) {
          discovered.resolve(request)
          await gate.promise
          if (failDiscovery) return response({ errors: [{ message: 'authentication revoked', extensions: { code: 'UNAUTHENTICATED' } }] })
        }
        return response({ data: null, extensions: { golem: { ...request.extensions.golem, dataIncarnation: identity } } })
      }
      if (request.query.includes('mutation')) { writes++; return response({ data: { write: true }, extensions: { golem: { dataIncarnation: identity } } }) }
      if (request.query.includes('value')) return response({ data: { value: identity }, extensions: { golem: { dataIncarnation: identity } } })
      const alias = request.query.includes('staticPage:') ? 'staticPage' : 'livePage'
      const after = request.variables?.golemAfter
      return response({ data: { [alias]: { items: [{ kind: identity, index: after === undefined ? 1 : 2 }], [CONTINUATION_RESPONSE_KEY]: after === undefined ? 'second-page' : null } }, extensions: { golem: { dataIncarnation: identity } } })
    },
    createSocket: () => { const socket = new BoundarySocket(identity, true); sockets.push(socket); return socket },
  })
  open.push(client)
  await client.ready()
  const detach = client.attach()
  const liveOptions = client.continuationQueryOptions('query{livePage:homeFeed(seed:7,take:1){items{kind index}}}')
  const live = new InfiniteQueryObserver(queryClient, liveOptions)
  const statics = new InfiniteQueryObserver(queryClient, client.staticContinuationQueryOptions('query{staticPage:homeFeed(seed:7,take:1){items{kind index}}}'))
  const stopLive = live.subscribe(() => undefined); const stopStatic = statics.subscribe(() => undefined)
  await live.refetch({ throwOnError: true }); await statics.refetch({ throwOnError: true })
  await live.fetchNextPage({ throwOnError: true }); await statics.fetchNextPage({ throwOnError: true })
  expect(live.getCurrentResult().data?.pages).toHaveLength(2)
  expect(statics.getCurrentResult().data?.pages).toHaveLength(2)
  return {
    client, queryClient, requests, sockets, live, statics, liveOptions,
    replace: () => { identity = B }, fail: () => { failDiscovery = true }, discovered: discovered.promise,
    release: () => gate.resolve(), discoveries: () => discoveries, writes: () => writes,
    close: () => { gate.resolve(); stopLive(); stopStatic(); detach() },
  }
}

it.each(['http', 'live-ack', 'stream-error'] as const)('recovers active live and static page chains through fresh discovery after %s mismatch', async (kind) => {
  const world = await recoveryWorld()
  try {
    world.replace()
    if (kind === 'http') {
      await expect(world.client.transport.execute({ query: '{value}' })).rejects.toBeInstanceOf(GolemSessionError)
    } else {
      const socket = world.sockets[0]!
      const subscription = await socket.subscribed.promise
      socket.deliver(kind === 'live-ack'
        ? { id: subscription.id, type: 'next', payload: { data: null, extensions: { golem: { live: true, dataIncarnation: 'c'.repeat(32) } } } }
        : { id: subscription.id, type: 'error', payload: [{ message: 'incarnation changed', extensions: { code: 'GOLEM_DATA_INCARNATION_MISMATCH' } }] })
    }
    await world.discovered
    expect(world.live.getCurrentResult().data).toBeUndefined()
    expect(world.statics.getCurrentResult().data).toBeUndefined()
    expect(world.sockets[0]?.readyState).toBe(3)
    await expect(world.client.mutate('mutation{write}')).rejects.toMatchObject({ code: 'GOLEM_CLIENT_NOT_READY' })
    const ready = world.client.ready()
    world.release()
    await ready
    await vi.waitFor(() => {
      expect(world.live.getCurrentResult().data?.pageParams).toEqual([null])
      expect(world.statics.getCurrentResult().data?.pageParams).toEqual([null])
      expect(world.live.getCurrentResult().data?.pages).toEqual([{ livePage: { items: [{ kind: B, index: 1 }], [CONTINUATION_RESPONSE_KEY]: 'second-page' } }])
      expect(world.statics.getCurrentResult().data?.pages).toEqual([{ staticPage: { items: [{ kind: B, index: 1 }], [CONTINUATION_RESPONSE_KEY]: 'second-page' } }])
    })
    expect(world.discoveries()).toBe(2)
    expect(world.writes()).toBe(0)
    expect(world.sockets).toHaveLength(2)
    expect(world.sockets[1]?.messages[0]?.payload.golem.dataIncarnation).toBe(B)
    const fresh = world.requests.filter((request) => request.extensions.golem.dataIncarnation === B)
    expect(fresh.every((request) => request.variables?.golemAfter === undefined)).toBe(true)
  } finally { world.close() }
})

it('surfaces failed fresh discovery on active live and static reads without replaying a mutation', async () => {
  const world = await recoveryWorld()
  try {
    world.replace(); world.fail()
    await expect(world.client.mutate('mutation{write}')).rejects.toBeInstanceOf(GolemCommitUnknownError)
    await world.discovered
    world.release()
    await vi.waitFor(() => {
      expect(world.live.getCurrentResult().error).toMatchObject({ code: 'UNAUTHENTICATED' })
      expect(world.statics.getCurrentResult().error).toMatchObject({ code: 'UNAUTHENTICATED' })
    })
    const query = world.queryClient.getQueryCache().find({ queryKey: world.liveOptions.queryKey, exact: true })!
    expect(world.client.live.statusOf(query)).toMatchObject({ state: 'FAILED', error: { code: 'UNAUTHENTICATED' } })
    expect(world.live.getCurrentResult().data).toBeUndefined()
    expect(world.statics.getCurrentResult().data).toBeUndefined()
    expect(world.discoveries()).toBe(2)
    expect(world.writes()).toBe(1)
    expect(world.sockets).toHaveLength(1)
  } finally { world.close() }
})

it.each(['deliver', 'reset', 'dispose'] as const)('terminates a raw mismatched subscriber once after fencing, unless superseded by %s', async (action) => {
  const world = await recoveryWorld()
  const ended = vi.fn(); const event = vi.fn()
  const stop = world.client.transport.subscribe({ query: 'subscription{rawChanges}' }, { onAck: vi.fn(), onEvent: event, onInterrupted: vi.fn(), onEnd: ended })
  try {
    const socket = world.sockets[0]!
    await vi.waitFor(() => expect(socket.messages.filter((message) => message.type === 'subscribe')).toHaveLength(2))
    const subscription = socket.messages.find((message) => message.type === 'subscribe' && message.payload.query.includes('rawChanges'))!
    world.replace()
    socket.deliver({ id: subscription.id, type: 'next', payload: { data: null, extensions: { golem: { live: true, dataIncarnation: B } } } })
    if (action === 'reset') void world.client.resetSession().catch(() => undefined)
    if (action === 'dispose') world.client.dispose()
    await Promise.resolve()
    if (action === 'deliver') {
      expect(ended).toHaveBeenCalledTimes(1)
      expect(ended.mock.calls[0]?.[0]).toMatchObject({ kind: 'fatal', error: { code: 'GOLEM_SESSION_CHANGED', dispatched: false } })
      expect(world.live.getCurrentResult().data).toBeUndefined()
      socket.deliver({ id: subscription.id, type: 'complete' })
      socket.deliver({ id: subscription.id, type: 'error', payload: [{ message: 'late' }] })
      await world.discovered
      expect(ended).toHaveBeenCalledTimes(1)
    } else expect(ended).not.toHaveBeenCalled()
    expect(event).not.toHaveBeenCalled()
  } finally { stop(); world.close() }
})


it.each(['reset', 'dispose'] as const)('fences pending socket credentials across %s without constructing an old socket', async (boundary) => {
  for (const outcome of ['resolve', 'reject'] as const) {
    const entered = deferred<void>()
    let resolve!: (value: Credentials) => void
    let reject!: (reason: unknown) => void
    const pending = new Promise<Credentials>((yes, no) => { resolve = yes; reject = no })
    const failure = new Error('old credentials refused')
    let attempt = 0
    const factory = vi.fn((_url: string, _protocols: string | string[], _credentials: Credentials) => new BoundarySocket(A, true))
    const queryClient = new QueryClient()
    const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest: legacy,
      credentials: () => { if (++attempt === 1) { entered.resolve(); return pending } return { headers: { authorization: 'fresh' } } },
      createSocket: factory,
    })
    open.push(client)
    await client.ready()
    const handlers = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
    const stop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
    await entered.promise
    if (boundary === 'reset') await client.resetSession()
    else client.dispose()
    if (outcome === 'resolve') resolve({ headers: { authorization: 'stale' } })
    else reject(failure)
    // Vitest also observes unhandled rejections during this turn; do not
    // suppress them with a process/window listener in the regression.
    await new Promise((done) => setTimeout(done, 0))
    expect(factory).not.toHaveBeenCalled()
    expect(handlers.onAck).not.toHaveBeenCalled()
    expect(handlers.onEvent).not.toHaveBeenCalled()
    expect(handlers.onEnd).not.toHaveBeenCalled()
    if (boundary === 'reset') {
      const fresh = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
      const stopFresh = client.transport.subscribe({ query: 'subscription{changes}' }, fresh)
      await vi.waitFor(() => expect(fresh.onAck).toHaveBeenCalledTimes(1))
      expect(factory).toHaveBeenCalledTimes(1)
      expect(factory.mock.calls[0]?.[2]).toEqual({ headers: { authorization: 'fresh' } })
      expect(fresh.onEnd).not.toHaveBeenCalled()
      stopFresh()
    }
    stop()
    client.dispose()
  }
})

it('delivers the original current credential error once without creating a socket, then admits fresh credentials', async () => {
  const entered = deferred<void>()
  let reject!: (reason: unknown) => void
  const pending = new Promise<Credentials>((_resolve, no) => { reject = no })
  const failure = new Error('credential refresh refused')
  let attempt = 0
  const factory = vi.fn(() => new BoundarySocket(A, true))
  const queryClient = new QueryClient()
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest: legacy,
    credentials: () => { if (++attempt === 1) { entered.resolve(); return pending } return {} }, createSocket: factory,
  })
  open.push(client)
  await client.ready()
  const handlers = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
  await entered.promise
  reject(failure)
  await vi.waitFor(() => expect(handlers.onEnd).toHaveBeenCalledTimes(1))
  expect(handlers.onEnd.mock.calls[0]?.[0]).toEqual({ kind: 'fatal', status: 'invalid', error: failure })
  expect(handlers.onEnd.mock.calls[0]?.[0].error).toBe(failure)
  expect(client.transport.status).toBe('invalid')
  expect(factory).not.toHaveBeenCalled()
  expect(attempt).toBe(1)
  const fresh = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stopFresh = client.transport.subscribe({ query: 'subscription{changes}' }, fresh)
  await vi.waitFor(() => expect(fresh.onAck).toHaveBeenCalledTimes(1))
  expect(factory).toHaveBeenCalledTimes(1)
  expect(attempt).toBe(2)
  expect(client.transport.status).toBe('open')
  stopFresh(); stop()
  // The failed stream must release its graphql-ws subscription lock too;
  // disposing the whole client in afterEach would hide that leak.
  await vi.waitFor(() => expect(factory.mock.results[0]?.value.readyState).toBe(3))
})

it('refreshes socket credentials on each ordinary reconnect after preparation routing', async () => {
  const sockets: BoundarySocket[] = []
  const seen: Credentials[] = []
  let attempt = 0
  const transport = new Transport({ url: 'http://session.test/graphql', manifest: legacy,
    credentials: async () => ({ headers: { authorization: 'attempt-' + (++attempt) } }),
    createSocket: (_url, _protocols, credentials) => { seen.push(credentials); const socket = new BoundarySocket(A, true); sockets.push(socket); return socket },
    sleep: async () => undefined,
  })
  const handlers = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stop = transport.subscribe({ query: 'subscription{changes}' }, handlers)
  try {
    await vi.waitFor(() => expect(handlers.onAck).toHaveBeenCalledTimes(1))
    sockets[0]!.close(1012, 'restart')
    await vi.waitFor(() => expect(handlers.onAck).toHaveBeenCalledTimes(2))
    expect(seen).toEqual([{ headers: { authorization: 'attempt-1' } }, { headers: { authorization: 'attempt-2' } }])
    expect(handlers.onInterrupted).toHaveBeenCalledTimes(1)
    expect(handlers.onEnd).not.toHaveBeenCalled()
  } finally { stop(); transport.dispose() }
})


it('routes native-default socket preparation failure without constructing a socket and then accepts fresh credentials', async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  const constructed = vi.fn()
  class NativeBoundarySocket extends BoundarySocket {
    static readonly CONNECTING = 0
    static readonly OPEN = 1
    static readonly CLOSING = 2
    static readonly CLOSED = 3
    constructor(_url: string, _protocols: string | string[]) { super(A, true); constructed() }
  }
  const failure = new Error('native credential preparation refused')
  let reject!: (reason: unknown) => void
  const pending = new Promise<Credentials>((_resolve, no) => { reject = no })
  const entered = deferred<void>()
  let attempt = 0
  const queryClient = new QueryClient()
  // Deliberately omit createSocket: production's native fallback must pass
  // through the same preparation fence as application-supplied factories.
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest: legacy,
    credentials: () => { if (++attempt === 1) { entered.resolve(); return pending } return {} },
  })
  open.push(client)
  const stops: Array<() => void> = []
  try {
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: NativeBoundarySocket })
    await client.ready()
    const failed = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
    stops.push(client.transport.subscribe({ query: 'subscription{changes}' }, failed))
    await entered.promise
    reject(failure)
    await vi.waitFor(() => expect(failed.onEnd).toHaveBeenCalledTimes(1))
    expect(failed.onEnd.mock.calls[0]?.[0]).toEqual({ kind: 'fatal', status: 'invalid', error: failure })
    expect(failed.onEnd.mock.calls[0]?.[0].error).toBe(failure)
    expect(constructed).not.toHaveBeenCalled()
    const fresh = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
    stops.push(client.transport.subscribe({ query: 'subscription{changes}' }, fresh))
    await vi.waitFor(() => expect(fresh.onAck).toHaveBeenCalledTimes(1))
    expect(constructed).toHaveBeenCalledTimes(1)
    expect(attempt).toBe(2)
    expect(fresh.onEnd).not.toHaveBeenCalled()
  } finally {
    for (const stop of stops) stop()
    client.dispose()
    queryClient.clear()
    if (previous === undefined) Reflect.deleteProperty(globalThis, 'WebSocket')
    else Object.defineProperty(globalThis, 'WebSocket', previous)
  }
})

it.each(['reset', 'dispose'] as const)('fences credential preparation when connecting status synchronously triggers %s', async (boundary) => {
  const credentials = vi.fn(() => ({}))
  const factory = vi.fn(() => new BoundarySocket(A, true))
  const queryClient = new QueryClient()
  const client = new GolemClient({ queryClient, url: 'http://session.test/graphql', manifest: legacy, credentials, createSocket: factory })
  open.push(client)
  await client.ready()
  let armed = true
  let reset: Promise<void> | undefined
  const off = client.transport.onStatus((status) => {
    if (status !== 'connecting' || !armed) return
    armed = false
    if (boundary === 'reset') reset = client.resetSession()
    else client.dispose()
  })
  const handlers = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
  const stop = client.transport.subscribe({ query: 'subscription{changes}' }, handlers)
  try {
    await new Promise((done) => setTimeout(done, 0))
    await reset
    expect(armed).toBe(false)
    expect(credentials).not.toHaveBeenCalled()
    expect(factory).not.toHaveBeenCalled()
    expect(handlers.onAck).not.toHaveBeenCalled()
    expect(handlers.onEvent).not.toHaveBeenCalled()
    expect(handlers.onEnd).not.toHaveBeenCalled()
    off()
    if (boundary === 'reset') {
      const fresh = { onAck: vi.fn(), onEvent: vi.fn(), onInterrupted: vi.fn(), onEnd: vi.fn() }
      const stopFresh = client.transport.subscribe({ query: 'subscription{changes}' }, fresh)
      try {
        await vi.waitFor(() => expect(fresh.onAck).toHaveBeenCalledTimes(1))
        expect(credentials).toHaveBeenCalledTimes(1)
        expect(factory).toHaveBeenCalledTimes(1)
      } finally { stopFresh() }
    }
  } finally { off(); stop(); client.dispose(); await reset }
})

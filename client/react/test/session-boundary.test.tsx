import { MutationCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRoot } from 'react-dom/client'
import { parse } from 'graphql'
import type { DocumentDecoration } from 'gql.tada'
import { afterEach, expect, it, vi } from 'vitest'
import { DATA_INCARNATION_CAPABILITY, GolemClient, GolemCommitUnknownError, GolemPartialCommitError, GolemError, type ClientManifest } from '@eleven-am/golem-client'
import { GolemProvider, useGolemMutation, type GolemMutationOptions } from '../src/index.js'

const manifest = { formatVersion: 3, contract: 'hook-session', models: {}, roots: { write: { kind: 'opaque', shape: 'value', nullable: false, models: [] } } } as const satisfies ClientManifest
const WRITE = 'mutation($value:Int!){write(value:$value)}'
type Vars = Readonly<Record<string, unknown>>
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
const cleanups: Array<() => void> = []
afterEach(() => { for (const close of cleanups.splice(0)) close(); vi.restoreAllMocks() })
async function mount(options: GolemMutationOptions<unknown, Vars>, send: typeof fetch, cache?: MutationCache, selectedManifest: ClientManifest = manifest) {
  const queryClient = new QueryClient({ mutationCache: cache, defaultOptions: { mutations: { retry: true, retryDelay: 0 } } })
  const client = new GolemClient({ queryClient, url: 'http://hook-session.test/graphql', manifest: selectedManifest, fetch: send })
  let current: ReturnType<typeof useGolemMutation<string>> | undefined
  function Hook() { current = useGolemMutation(WRITE, options); return null }
  const container = document.createElement('div'); document.body.appendChild(container)
  const root = createRoot(container)
  cleanups.push(() => { root.unmount(); client.dispose(); queryClient.clear(); container.remove() })
  root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><Hook /></GolemProvider></QueryClientProvider>)
  await vi.waitFor(() => expect(current).toBeDefined())
  return { client, queryClient, result: () => current!, unmount: () => root.unmount() }
}

it.each(['mutate', 'mutateAsync'] as const)('captures %s before an async onMutate queue and preserves callback variables', async (method) => {
  const hold = deferred<void>(); const entered = deferred<void>()
  const onError = vi.fn(); const callError = vi.fn(); const onSettled = vi.fn()
  const cacheVariables: unknown[] = []
  const cacheError = vi.fn(); const cacheSettled = vi.fn()
  const cache = new MutationCache({ onMutate: (variables) => { cacheVariables.push(variables) }, onError: cacheError, onSettled: cacheSettled })
  const send = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ data: { write: true } })))
  const world = await mount({ retry: true, onMutate: async () => { entered.resolve(); await hold.promise; return 'optimistic' }, onError, onSettled }, send, cache)
  const variables = { value: 9 }
  let result: Promise<unknown> | undefined
  if (method === 'mutateAsync') result = world.result().mutateAsync(variables, { onError: callError }).catch((error: unknown) => error)
  else world.result().mutate(variables, { onError: callError })
  await entered.promise
  expect(cacheVariables).toEqual([{ value: 9 }]) // no internal intent envelope
  expect(cacheVariables[0]).toBe(variables)
  await world.client.resetSession()
  hold.resolve()
  if (result !== undefined) expect(await result).toMatchObject({ code: 'GOLEM_SESSION_CHANGED', dispatched: false })
  await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1))
  expect(onError.mock.calls[0]?.[1]).toBe(variables)
  expect(onError.mock.calls[0]?.[2]).toBe('optimistic')
  expect(callError).not.toHaveBeenCalled() // the boundary resets the observed mutation
  expect(onSettled.mock.calls[0]?.[2]).toBe(variables)
  expect(cacheError.mock.calls[0]?.[1]).toBe(variables)
  expect(cacheSettled.mock.calls[0]?.[2]).toBe(variables)
  expect(send).not.toHaveBeenCalled()
})

it('never retries a sent mutation after reset, even with hook and global retry:true', async () => {
  const held = deferred<Response>(); const sent = deferred<void>()
  const send = vi.fn<typeof fetch>(async () => { sent.resolve(); return held.promise })
  const success = vi.fn(); const error = vi.fn()
  const world = await mount({ retry: true, retryDelay: 0, onSuccess: success, onError: error }, send)
  const pending = world.result().mutateAsync({ value: 7 }).catch((failure: unknown) => failure)
  await sent.promise
  const record = world.queryClient.getMutationCache().getAll()[0]!
  expect(record.options.retry).toBe(false)
  await world.client.resetSession()
  expect(world.result().data).toBeUndefined()
  held.resolve(new Response(JSON.stringify({ data: { write: true } })))
  expect(await pending).toBeInstanceOf(GolemCommitUnknownError)
  expect(send).toHaveBeenCalledTimes(1)
  expect(success).not.toHaveBeenCalled()
  expect(error).toHaveBeenCalledTimes(1)
  expect(record.options.retry).toBe(false)
  expect(world.queryClient.getMutationCache().getAll()).toEqual([])
})

it('retains pending scope order across reset and removes stale records after settlement', async () => {
  const first = deferred<Response>(); const sent = deferred<void>()
  const send = vi.fn<typeof fetch>(async (_input, init) => {
    const value = JSON.parse(String(init?.body)).variables.value
    if (value === 1) { sent.resolve(); return first.promise }
    return new Response(JSON.stringify({ data: { write: value } }))
  })
  const world = await mount({ scope: { id: 'ordered-writes' } }, send)
  const one = world.result().mutateAsync({ value: 1 }).catch((error: unknown) => error)
  await sent.promise
  const two = world.result().mutateAsync({ value: 2 }).catch((error: unknown) => error)
  await world.client.resetSession()
  expect(world.queryClient.getMutationCache().getAll()).toHaveLength(2)
  expect(world.queryClient.getMutationCache().getAll().every((record) => record.state.status === 'pending')).toBe(true)
  const three = world.result().mutateAsync({ value: 3 })
  await Promise.resolve()
  expect(send).toHaveBeenCalledTimes(1)
  first.resolve(new Response(JSON.stringify({ data: { write: true } })))
  expect(await one).toBeInstanceOf(GolemCommitUnknownError)
  expect(await two).toMatchObject({ code: 'GOLEM_SESSION_CHANGED', dispatched: false })
  await expect(three).resolves.toEqual({ write: 3 })
  expect(send).toHaveBeenCalledTimes(2)
  expect(world.queryClient.getMutationCache().getAll()).toHaveLength(1)
  expect(world.queryClient.getMutationCache().getAll()[0]?.state.data).toEqual({ write: 3 })
})


it('keeps overlapping original variables and hook callbacks while only the latest call receives callbacks', async () => {
  const gates = [deferred<void>(), deferred<void>()]
  const entered = [deferred<void>(), deferred<void>()]
  const globals = { onMutate: vi.fn(), onSuccess: vi.fn(), onSettled: vi.fn() }
  const cache = new MutationCache(globals)
  const onSuccess = vi.fn()
  const oldSuccess = vi.fn(); const latestSuccess = vi.fn()
  const send = vi.fn<typeof fetch>(async (_input, init) => {
    const request = JSON.parse(String(init?.body))
    return new Response(JSON.stringify({ data: { write: request.variables.value } }))
  })
  const world = await mount({ onMutate: async (variables) => {
    const index = Number(variables.value) - 1
    entered[index]!.resolve()
    await gates[index]!.promise
    return variables.value
  }, onSuccess }, send, cache)
  const first = { value: 1 }; const second = { value: 2 }
  const one = world.result().mutateAsync(first, { onSuccess: oldSuccess })
  await entered[0]!.promise
  const two = world.result().mutateAsync(second, { onSuccess: latestSuccess })
  await entered[1]!.promise
  gates[1]!.resolve()
  await expect(two).resolves.toEqual({ write: 2 })
  await vi.waitFor(() => expect(world.result().data).toEqual({ write: 2 }))
  gates[0]!.resolve()
  await expect(one).resolves.toEqual({ write: 1 })
  expect(world.result().data).toEqual({ write: 2 })
  expect(world.result().variables).toBe(second)
  expect(onSuccess.mock.calls.map((call) => call[1])).toEqual([second, first])
  expect(globals.onMutate.mock.calls[0]?.[0]).toBe(first)
  expect(globals.onMutate.mock.calls[1]?.[0]).toBe(second)
  expect(globals.onSuccess.mock.calls[0]?.[1]).toBe(second)
  expect(globals.onSuccess.mock.calls[1]?.[1]).toBe(first)
  expect(globals.onSettled).toHaveBeenCalledTimes(2)
  expect(latestSuccess).toHaveBeenCalledTimes(1)
  expect(oldSuccess).not.toHaveBeenCalled()
  world.result().reset()
  await vi.waitFor(() => expect(world.result().isIdle).toBe(true))
  expect(world.result().data).toBeUndefined()
})

it('preserves typed no-vars undefined through reordered global queues, callbacks and mutation state', async () => {
  type Data = { write: boolean }
  const noVarsDocument = parse('mutation { write }') as ReturnType<typeof parse> & DocumentDecoration<Data, void>
  const gates = [deferred<void>(), deferred<void>()]
  const entered = [deferred<void>(), deferred<void>()]
  let order = 0
  const onSuccess = vi.fn(); const onSettled = vi.fn(); const callSuccess = vi.fn()
  const cache = new MutationCache({ onMutate: async (variables) => {
    expect(variables).toBeUndefined()
    const index = order++
    entered[index]!.resolve()
    await gates[index]!.promise
  }, onSuccess, onSettled })
  const queryClient = new QueryClient({ mutationCache: cache, defaultOptions: { mutations: { retry: true, retryDelay: 0 } } })
  const send = vi.fn<typeof fetch>(async (_input, init) => {
    expect(JSON.parse(String(init?.body)).variables).toBeUndefined()
    return new Response(JSON.stringify({ data: { write: true } }))
  })
  const client = new GolemClient({ queryClient, url: 'http://hook-session.test/graphql', manifest, fetch: send })
  let result: ReturnType<typeof useGolemMutation<typeof noVarsDocument, string>> | undefined
  const hookSuccess = vi.fn()
  function Hook() {
    result = useGolemMutation<typeof noVarsDocument, string>(noVarsDocument, {
      retry: true, onMutate: (variables) => { expect(variables).toBeUndefined(); return 'context' },
      onSuccess: (data, variables, context) => { const checked: string = context; hookSuccess(data, variables, checked) },
    })
    return null
  }
  const container = document.createElement('div'); document.body.appendChild(container)
  const root = createRoot(container)
  cleanups.push(() => { root.unmount(); client.dispose(); queryClient.clear(); container.remove() })
  root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><Hook /></GolemProvider></QueryClientProvider>)
  await vi.waitFor(() => expect(result).toBeDefined())
  const first = result!.mutateAsync()
  await entered[0]!.promise
  const second = result!.mutateAsync(undefined, { onSuccess: callSuccess })
  await entered[1]!.promise
  // The second invocation starts first; it must keep its own prepared closure.
  gates[1]!.resolve()
  await expect(second).resolves.toEqual({ write: true })
  gates[0]!.resolve()
  await expect(first).resolves.toEqual({ write: true })
  expect(send).toHaveBeenCalledTimes(2)
  expect(hookSuccess).toHaveBeenCalledTimes(2)
  expect(callSuccess.mock.calls[0]?.[1]).toBeUndefined()
  expect(onSuccess.mock.calls.every((call) => call[1] === undefined)).toBe(true)
  expect(onSettled.mock.calls.every((call) => call[2] === undefined)).toBe(true)
  expect(queryClient.getMutationCache().getAll().every((value) => value.state.variables === undefined && value.options.retry === false)).toBe(true)
  expect(result!.variables).toBeUndefined()
})


it('detaches on unmount while pending hook and global callbacks still finish', async () => {
  const held = deferred<Response>(); const sent = deferred<void>()
  const hookSuccess = vi.fn(); const globalSuccess = vi.fn(); const callSuccess = vi.fn()
  const cache = new MutationCache({ onSuccess: globalSuccess })
  const send = vi.fn<typeof fetch>(async () => { sent.resolve(); return held.promise })
  const world = await mount({ onSuccess: hookSuccess }, send, cache)
  const variables = { value: 5 }
  const pending = world.result().mutateAsync(variables, { onSuccess: callSuccess })
  await sent.promise
  world.unmount()
  held.resolve(new Response(JSON.stringify({ data: { write: true } })))
  await expect(pending).resolves.toEqual({ write: true })
  expect(hookSuccess.mock.calls[0]?.[1]).toBe(variables)
  expect(globalSuccess.mock.calls[0]?.[1]).toBe(variables)
  expect(callSuccess).not.toHaveBeenCalled()
})


it.each(['reset', 'adoption', 'dispose'] as const)('clears completed Golem mutation results and cache on %s while retaining unrelated records', async (boundary) => {
  let identity = 'a'.repeat(32)
  const send = vi.fn<typeof fetch>(async (_input, init) => {
    const body = JSON.parse(String(init?.body))
    const golem = body.extensions.golem
    return new Response(JSON.stringify(golem.discover
      ? { data: null, extensions: { golem: { ...golem, dataIncarnation: identity } } }
      : { data: { write: { privateRow: true } }, extensions: { golem: { dataIncarnation: identity } } }))
  })
  const world = await mount({ meta: { application: 'preserved' } }, send, undefined, { ...manifest, requiredCapabilities: [DATA_INCARNATION_CAPABILITY] })
  await world.client.ready()
  const cache = world.queryClient.getMutationCache()
  const unrelated = cache.build(world.queryClient, { mutationFn: async () => ({ ordinary: true }), meta: { application: 'ordinary' } })
  await unrelated.execute(undefined)
  await world.result().mutateAsync({ value: 4 })
  await vi.waitFor(() => expect(world.result().data).toEqual({ write: { privateRow: true } }))
  const owned = cache.getAll().find((record) => record !== unrelated)!
  expect(owned.meta).toEqual({ application: 'preserved', golem: { kind: 'mutation' } })
  if (boundary === 'dispose') world.client.dispose()
  else if (boundary === 'reset') await world.client.resetSession()
  else {
    identity = 'b'.repeat(32)
    await expect(world.client.transport.execute({ query: '{write}' })).rejects.toMatchObject({ code: 'GOLEM_SESSION_CHANGED' })
    await world.client.ready()
  }
  await vi.waitFor(() => expect(world.result().isIdle).toBe(true))
  expect(world.result().data).toBeUndefined()
  expect(world.result().variables).toBeUndefined()
  expect(world.result().context).toBeUndefined()
  expect(cache.getAll()).toEqual([unrelated])
  expect(unrelated.state.data).toEqual({ ordinary: true })
})

it('clears restored owned mutation records during initial authenticated adoption', async () => {
  const send = vi.fn<typeof fetch>(async (_input, init) => {
    const body = JSON.parse(String(init?.body))
    return new Response(JSON.stringify({ data: null, extensions: { golem: { ...body.extensions.golem, dataIncarnation: 'a'.repeat(32) } } }))
  })
  const world = await mount({}, send, undefined, { ...manifest, requiredCapabilities: [DATA_INCARNATION_CAPABILITY] })
  const cache = world.queryClient.getMutationCache()
  const restored = cache.build(world.queryClient, { mutationFn: async () => ({ privateRow: true }), meta: { golem: { kind: 'mutation' } } })
  await restored.execute(undefined)
  expect(cache.getAll()).toContain(restored)
  await world.client.ready()
  expect(cache.getAll()).not.toContain(restored)
})

it.each(['PARTIAL', 'UNKNOWN'] as const)('keeps exact %s rejection evidence while clearing a stale pending result after reset', async (kind) => {
  const held = deferred<Response>(); const sent = deferred<void>()
  const world = await mount({}, vi.fn<typeof fetch>(async () => { sent.resolve(); return held.promise }))
  const failure = new (kind === 'PARTIAL' ? GolemPartialCommitError : GolemCommitUnknownError)(new GolemError({ message: 'explicit evidence' }), [], { write: { evidence: true } })
  vi.spyOn(world.client.transport, 'execute').mockImplementation(async () => { sent.resolve(); await held.promise; throw failure })
  const callError = vi.fn()
  const pending = world.result().mutateAsync({ value: 8 }, { onError: callError }).catch((error: unknown) => error)
  await sent.promise
  await world.client.resetSession()
  held.resolve(new Response())
  expect(await pending).toBe(failure)
  expect(failure.data).toEqual({ write: { evidence: true } })
  expect(world.queryClient.getMutationCache().getAll()).toEqual([])
  expect(world.result().data).toBeUndefined()
  expect(world.result().error).toBeNull()
  expect(callError).not.toHaveBeenCalled()
})

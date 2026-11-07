import { QueryClient } from '@tanstack/query-core'
import { describe, expect, it, vi } from 'vitest'
import { GolemClient, GolemKeepaliveTooLargeError, KEEPALIVE_BODY_LIMIT, Transport } from '../src/index.js'
import { manifest } from './harness/social.js'

const OK = () => Promise.resolve(new Response(JSON.stringify({ data: { ok: true } }), { status: 200 }))

function build(credentials?: () => { headers: Record<string, string> }) {
  const send = vi.fn<typeof fetch>(OK)
  const transport = new Transport({ url: 'http://golem.test/graphql', manifest, fetch: send, credentials })
  return { send, transport }
}

const init = (send: ReturnType<typeof build>['send']) => send.mock.calls[0]?.[1] as RequestInit

describe('keepalive on the transport', () => {
  it('sets keepalive only when requested', async () => {
    const on = build()
    await on.transport.execute({ query: '{ a }', keepalive: true })
    expect(init(on.send).keepalive).toBe(true)
    const off = build()
    await off.transport.execute({ query: '{ a }' })
    expect(init(off.send).keepalive).toBeFalsy()
  })

  it('still sends the protocol block and credentials', async () => {
    const { send, transport } = build(() => ({ headers: { authorization: 'Bearer t' } }))
    await transport.execute({ query: '{ a }', keepalive: true })
    const sent = init(send)
    expect((sent.headers as Record<string, string>).authorization).toBe('Bearer t')
    expect(JSON.parse(sent.body as string).extensions.golem.contract).toBe(manifest.contract)
  })

  it('refuses an oversized body before any fetch', async () => {
    const { send, transport } = build()
    const variables = { blob: 'x'.repeat(KEEPALIVE_BODY_LIMIT) }
    await expect(transport.execute({ query: '{ a }', variables, keepalive: true })).rejects.toBeInstanceOf(GolemKeepaliveTooLargeError)
    expect(send).not.toHaveBeenCalled()
    await transport.execute({ query: '{ a }', variables })
    expect(send).toHaveBeenCalledTimes(1)
  })
})

describe('mutate options', () => {
  const MUTATION = 'mutation($id: UUID!) { deletePost(where: { ID: $id }) { id } }'

  function client() {
    const send = vi.fn<typeof fetch>(OK)
    const golem = new GolemClient({ queryClient: new QueryClient(), url: 'http://golem.test/graphql', manifest, fetch: send })
    return { golem, send }
  }

  it('accepts a bare AbortSignal', async () => {
    const { golem, send } = client()
    const controller = new AbortController()
    await golem.mutate(MUTATION, { id: 'a' }, controller.signal)
    const sent = send.mock.calls[0]?.[1] as RequestInit
    expect(sent.signal).toBe(controller.signal)
    expect(sent.keepalive).toBeFalsy()
    golem.dispose()
  })

  it('threads signal and keepalive from an options object', async () => {
    const { golem, send } = client()
    const controller = new AbortController()
    await golem.mutate(MUTATION, { id: 'a' }, { signal: controller.signal, keepalive: true })
    const sent = send.mock.calls[0]?.[1] as RequestInit
    expect(sent.signal).toBe(controller.signal)
    expect(sent.keepalive).toBe(true)
    golem.dispose()
  })
})

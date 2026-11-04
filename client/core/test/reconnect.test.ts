import { afterEach, describe, expect, it } from 'vitest'
import { actor, collector, CREATE_POST, headerSocket, manifest, POST_EVENTS, postInput, signUp, socialUrl, transport } from './harness/social.js'
import { startProxy, type TcpProxy } from './harness/proxy.js'
import { Transport } from '../src/index.js'

let proxy: TcpProxy | undefined

afterEach(async () => {
  await proxy?.close()
  proxy = undefined
})

describe('reconnect after transport loss, through a real TCP proxy', () => {
  it('reports the interruption, backs off, reconnects and acknowledges again', async () => {
    const who = actor()
    const direct = transport({ credentials: who.credentials })
    await signUp(direct, who)
    proxy = await startProxy(Number(new URL(socialUrl()).port))
    const sleeps: number[] = []
    const viaProxy = new Transport({
      url: `http://127.0.0.1:${proxy.port}/graphql`,
      manifest,
      credentials: who.credentials,
      createSocket: headerSocket,
      random: () => 0.5,
      sleep: async (milliseconds) => void sleeps.push(milliseconds),
    })
    const seen = collector()
    viaProxy.subscribe({ query: POST_EVENTS }, seen.handlers)
    await seen.until(() => seen.acks() === 1)
    const statuses: string[] = []
    viaProxy.onStatus((status) => statuses.push(status))
    proxy.drop()
    await seen.until(() => seen.interrupts() === 1)
    await seen.until(() => seen.acks() === 2)
    expect(seen.ends).toEqual([])
    expect(sleeps.length).toBeGreaterThanOrEqual(1)
    expect(sleeps[0]).toBe(125)
    expect(statuses).toContain('reconnecting')
    expect(viaProxy.status).toBe('open')
    await direct.execute({ query: CREATE_POST, variables: { data: postInput('after reconnect') } })
    await seen.until(() => seen.events.length === 1)
    viaProxy.dispose()
    direct.dispose()
  })
})

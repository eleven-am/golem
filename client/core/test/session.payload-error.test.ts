import { describe, expect, it } from 'vitest'
import { Session, type StreamEnd } from '../src/index.js'
import { manifest } from './harness/social.js'

class ScriptedSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  readonly protocol = 'graphql-transport-ws'
  readyState = 0
  readonly sent: Array<{ type: string; id?: string }> = []
  onopen: (() => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  private readonly waiters: Array<{ type: string; resolve: (message: { type: string; id?: string }) => void }> = []

  constructor() {
    queueMicrotask(() => {
      this.readyState = 1
      this.onopen?.()
    })
  }

  send(data: string): void {
    const message = JSON.parse(data) as { type: string; id?: string }
    this.sent.push(message)
    if (message.type === 'connection_init') {
      this.deliver({ type: 'connection_ack' })
    }
    for (const waiter of this.waiters.splice(0)) {
      if (waiter.type === message.type) {
        waiter.resolve(message)
      } else {
        this.waiters.push(waiter)
      }
    }
  }

  close(code = 1000, reason = ''): void {
    this.readyState = 3
    this.onclose?.({ code, reason })
  }

  deliver(message: unknown): void {
    this.onmessage?.({ data: JSON.stringify(message) })
  }

  sentOf(type: string): Promise<{ type: string; id?: string }> {
    const found = this.sent.find((message) => message.type === type)
    if (found) {
      return Promise.resolve(found)
    }
    return new Promise((resolve) => this.waiters.push({ type, resolve }))
  }
}

describe('a payload error that does not end the server operation', () => {
  it('disposes the operation so the server stops it', async () => {
    let socket: ScriptedSocket | undefined
    const session = new Session({
      url: 'http://golem.invalid/graphql',
      manifest,
      createSocket: () => {
        socket = new ScriptedSocket()
        return socket
      },
    })
    const ends: StreamEnd[] = []
    const stop = session.subscribe(
      { query: 'subscription { postEvents { id } }' },
      { onAck: () => undefined, onEvent: () => undefined, onInterrupted: () => undefined, onEnd: (end) => ends.push(end) },
    )
    const subscribed = await (async () => {
      while (!socket) {
        await Promise.resolve()
      }
      return socket.sentOf('subscribe')
    })()
    socket?.deliver({ type: 'next', id: subscribed.id, payload: { data: null, errors: [{ message: 'boom' }] } })
    expect(ends).toHaveLength(1)
    expect(ends[0]?.kind).toBe('error')
    stop()
    const completed = socket?.sent.find((message) => message.type === 'complete')
    expect(completed).toMatchObject({ type: 'complete', id: subscribed.id })
  })
})

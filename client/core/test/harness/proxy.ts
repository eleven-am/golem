import { createConnection, createServer, type Server, type Socket } from 'node:net'

export type Direction = 'upstream' | 'downstream'

export interface TcpProxy {
  readonly port: number
  hold(direction: Direction): void
  release(direction: Direction): void
  stall(direction: Direction): void
  unstall(direction: Direction): void
  drop(): void
  close(): Promise<void>
}

interface Lane {
  readonly source: Socket
  readonly target: Socket
  readonly buffered: Buffer[]
}

export async function startProxy(targetPort: number): Promise<TcpProxy> {
  const sockets = new Set<Socket>()
  const held = new Set<Direction>()
  const stalled = new Set<Direction>()
  const lanes = new Map<Direction, Set<Lane>>([
    ['upstream', new Set()],
    ['downstream', new Set()],
  ])
  const flush = (direction: Direction) => {
    for (const lane of lanes.get(direction) ?? []) {
      while (lane.buffered.length > 0 && !lane.target.destroyed) {
        lane.target.write(lane.buffered.shift() as Buffer)
      }
    }
  }
  const server: Server = createServer((client) => {
    const upstream = createConnection({ host: '127.0.0.1', port: targetPort })
    sockets.add(client)
    sockets.add(upstream)
    const up: Lane = { source: client, target: upstream, buffered: [] }
    const down: Lane = { source: upstream, target: client, buffered: [] }
    lanes.get('upstream')?.add(up)
    lanes.get('downstream')?.add(down)
    const forward = (direction: Direction, lane: Lane) => {
      if (stalled.has(direction)) {
        lane.source.pause()
      }
      lane.source.on('data', (chunk: Buffer) => {
        if (held.has(direction)) {
          lane.buffered.push(chunk)
        } else if (!lane.target.destroyed) {
          lane.target.write(chunk)
        }
      })
    }
    forward('upstream', up)
    forward('downstream', down)
    const cleanup = () => {
      sockets.delete(client)
      sockets.delete(upstream)
      lanes.get('upstream')?.delete(up)
      lanes.get('downstream')?.delete(down)
      client.destroy()
      upstream.destroy()
    }
    client.on('close', cleanup)
    upstream.on('close', cleanup)
    client.on('error', () => undefined)
    upstream.on('error', () => undefined)
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (typeof address !== 'object' || address === null) {
    throw new Error('proxy has no port')
  }
  return {
    port: address.port,
    hold(direction) {
      held.add(direction)
    },
    release(direction) {
      held.delete(direction)
      flush(direction)
    },
    stall(direction) {
      stalled.add(direction)
      for (const lane of lanes.get(direction) ?? []) {
        lane.source.pause()
      }
    },
    unstall(direction) {
      stalled.delete(direction)
      for (const lane of lanes.get(direction) ?? []) {
        lane.source.resume()
      }
    },
    drop() {
      for (const socket of sockets) {
        socket.destroy()
      }
    },
    close() {
      for (const socket of sockets) {
        socket.destroy()
      }
      return new Promise((resolve) => server.close(() => resolve()))
    },
  }
}

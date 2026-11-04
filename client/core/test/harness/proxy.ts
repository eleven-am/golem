import { createConnection, createServer, type Server, type Socket } from 'node:net'

export interface TcpProxy {
  readonly port: number
  drop(): void
  close(): Promise<void>
}

export async function startProxy(targetPort: number): Promise<TcpProxy> {
  const sockets = new Set<Socket>()
  const server: Server = createServer((client) => {
    const upstream = createConnection({ host: '127.0.0.1', port: targetPort })
    sockets.add(client)
    sockets.add(upstream)
    client.pipe(upstream)
    upstream.pipe(client)
    const cleanup = () => {
      sockets.delete(client)
      sockets.delete(upstream)
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

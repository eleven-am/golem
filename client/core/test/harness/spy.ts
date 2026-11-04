import type { LiveTransport, StreamEnd, StreamHandlers, SubscriptionRequest, Transport } from '../../src/index.js'

export interface Spy {
  readonly transport: LiveTransport
  readonly ends: string[]
  subscribes(): number
  stops(): number
  renews(): number
}

export function describeEnd(end: StreamEnd): string {
  switch (end.kind) {
    case 'error':
      return end.errors[0]?.code ?? 'EMPTY'
    case 'fatal':
      return `fatal:${end.status}`
    default:
      return end.kind
  }
}

export function spy(real: Transport): Spy {
  let subscribes = 0
  let stops = 0
  let renews = 0
  const ends: string[] = []
  return {
    ends,
    subscribes: () => subscribes,
    stops: () => stops,
    renews: () => renews,
    transport: {
      execute: (request) => real.execute(request),
      renew: () => {
        renews++
        real.renew()
      },
      subscribe: (request: SubscriptionRequest, handlers: StreamHandlers) => {
        subscribes++
        const stop = real.subscribe(request, {
          ...handlers,
          onEnd: (end) => {
            ends.push(describeEnd(end))
            handlers.onEnd(end)
          },
        })
        return () => {
          stops++
          stop()
        }
      },
    },
  }
}

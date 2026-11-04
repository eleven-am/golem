import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GolemClient, GolemConfigurationError } from '@eleven-am/golem-client'
import { GolemProvider, useGolemClient } from '../src/index.js'
import { Boundary } from './boundary.js'
import { manifest, screen, until, type Screen } from './harness.js'

const open: Screen[] = []

afterEach(async () => {
  startedWhenRendered.length = 0
  vi.restoreAllMocks()
  for (const one of open.splice(0)) {
    await one.close()
  }
})

async function fresh(): Promise<Screen> {
  const created = await screen()
  open.push(created)
  return created
}

const startedWhenRendered: boolean[] = []

function Probe() {
  const client = useGolemClient()
  startedWhenRendered.push(client.live.started)
  return <p>inside</p>
}

describe('client construction', () => {
  it('refuses a second GolemClient on the same QueryClient', async () => {
    const one = await fresh()
    expect(() => new GolemClient({ queryClient: one.queryClient, url: 'http://127.0.0.1:1/graphql', manifest })).toThrow(GolemConfigurationError)
  })

  it('requires the QueryClient', () => {
    expect(() => new GolemClient({ queryClient: undefined as unknown as QueryClient, url: 'http://127.0.0.1:1/graphql', manifest })).toThrow(GolemConfigurationError)
  })

  it('lets a disposed client release its QueryClient', async () => {
    const queryClient = new QueryClient()
    const first = new GolemClient({ queryClient, url: 'http://127.0.0.1:1/graphql', manifest })
    first.dispose()
    const second = new GolemClient({ queryClient, url: 'http://127.0.0.1:1/graphql', manifest })
    second.dispose()
  })

  it('starts live sync without React', async () => {
    const one = await fresh()
    const stop = one.client.live.start()
    expect(one.client.live.started).toBe(true)
    stop()
    expect(one.client.live.started).toBe(false)
  })
})

describe('GolemProvider', () => {
  it('throws when the QueryClient is not the one the client was built with', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const one = await fresh()
    const other = new QueryClient()
    one.root.render(
      <QueryClientProvider client={other}>
        <Boundary>
          <GolemProvider client={one.client}>
            <Probe />
          </GolemProvider>
        </Boundary>
      </QueryClientProvider>,
    )
    await until(() => one.container.querySelector('[data-testid="caught"]') !== null)
    expect(one.text()).toContain('GolemConfigurationError')
    expect(one.text()).toContain('QueryClient')
    expect(one.client.live.started).toBe(false)
  })

  it('refuses hooks outside a provider', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const one = await fresh()
    one.render(
      <Boundary>
        <Probe />
      </Boundary>,
    )
    await until(() => one.container.querySelector('[data-testid="caught"]') !== null)
    expect(one.text()).toContain('GolemProvider')
  })

  it('attaches live sync, renders its children and releases it on unmount', async () => {
    const one = await fresh()
    one.render(
      <GolemProvider client={one.client}>
        <Probe />
      </GolemProvider>,
    )
    await until(() => one.text() === 'inside')
    expect(one.client.live.started).toBe(true)
    expect(startedWhenRendered.length).toBeGreaterThan(0)
    expect(startedWhenRendered.every(Boolean)).toBe(true)
    one.root.unmount()
    await until(() => !one.client.live.started)
  })

  it('attaches once under StrictMode double subscription', async () => {
    const one = await fresh()
    one.render(
      <GolemProvider client={one.client}>
        <Probe />
      </GolemProvider>,
      { strict: true },
    )
    await until(() => one.text() === 'inside')
    expect(one.client.live.started).toBe(true)
  })

  it('keeps live sync running while another provider of the same client is mounted', async () => {
    const one = await fresh()
    one.render(
      <>
        <GolemProvider client={one.client}>
          <Probe />
        </GolemProvider>
        <GolemProvider client={one.client}>
          <Probe />
        </GolemProvider>
      </>,
    )
    await until(() => one.text() === 'insideinside')
    one.render(
      <GolemProvider client={one.client}>
        <Probe />
      </GolemProvider>,
    )
    await until(() => one.text() === 'inside')
    expect(one.client.live.started).toBe(true)
  })

  it('does not stop live sync that the application started itself', async () => {
    const one = await fresh()
    one.client.live.start()
    one.render(
      <GolemProvider client={one.client}>
        <Probe />
      </GolemProvider>,
    )
    await until(() => one.text() === 'inside')
    one.root.unmount()
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(one.client.live.started).toBe(true)
  })
})

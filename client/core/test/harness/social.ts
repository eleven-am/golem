import { randomUUID } from 'node:crypto'
import { copyFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { inject } from 'vitest'
import { golemClientManifest } from '../../../../go/examples/social/social/zz_golem_client.gen.js'
import { startSocialProcess } from './process.js'
import { Transport, type Credentials, type StreamEnd, type StreamHandlers, type TransportOptions } from '../../src/index.js'

export const socialUrl = (): string => inject('socialUrl')

export const manifest = golemClientManifest

export function actor(): { readonly id: string; readonly credentials: () => Credentials } {
  const id = randomUUID()
  return { id, credentials: () => ({ headers: { 'x-golem-user': id } }) }
}

export function headerSocket(url: string, protocols: string | string[], credentials: Credentials): WebSocket {
  const Impl = WebSocket as unknown as new (url: string, init: { protocols: string | string[]; headers: Record<string, string> }) => WebSocket
  return new Impl(url, { protocols, headers: { ...credentials.headers } })
}

export function transport(overrides: Partial<TransportOptions> & { credentials?: TransportOptions['credentials'] } = {}): Transport {
  return new Transport({
    url: `${socialUrl()}/graphql`,
    manifest,
    createSocket: headerSocket,
    ...overrides,
  })
}

export interface Collector {
  readonly acks: () => number
  readonly events: ReadonlyArray<Record<string, unknown>>
  readonly ends: ReadonlyArray<StreamEnd>
  readonly interrupts: () => number
  readonly handlers: StreamHandlers
  until(predicate: () => boolean, milliseconds?: number): Promise<void>
}

export function collector(): Collector {
  let acks = 0
  let interrupts = 0
  const events: Array<Record<string, unknown>> = []
  const ends: StreamEnd[] = []
  return {
    acks: () => acks,
    events,
    ends,
    interrupts: () => interrupts,
    handlers: {
      onAck: () => {
        acks++
      },
      onEvent: (body) => {
        events.push(body.data as Record<string, unknown>)
      },
      onInterrupted: () => {
        interrupts++
      },
      onEnd: (end) => {
        ends.push(end)
      },
    },
    async until(predicate, milliseconds = 10_000) {
      const deadline = Date.now() + milliseconds
      while (!predicate()) {
        if (Date.now() > deadline) {
          throw new Error('condition not reached in time')
        }
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
    },
  }
}

export const POST_EVENTS = 'subscription { postEvents { eventID version type id entity { id title } } }'

export const CREATE_USER = 'mutation($data: UserCreateInput!) { createUser(data: $data) { id handle } }'

export const CREATE_POST =
  'mutation($data: PostCreateInput!) { createPost(data: $data) { id title } }'

export function postInput(title: string): Record<string, unknown> {
  return { title, body: 'body', published: true, liveDate: '2026-01-01', liveTime: '10:00:00', metadata: {}, topics: [] }
}

export async function signUp(client: Transport, who: { readonly id: string }): Promise<void> {
  await client.execute({ query: CREATE_USER, variables: { data: { id: who.id, handle: `h-${who.id}`, email: `${who.id}@example.test` } } })
}

export interface SpawnedSocial {
  readonly url: string
  stop(graceMilliseconds?: number): Promise<void>
  restart(): Promise<void>
  dispose(): Promise<void>
}

export async function spawnSocial(env: Readonly<Record<string, string>>): Promise<SpawnedSocial> {
  const work = mkdtempSync(join(tmpdir(), 'golem-client-extra-'))
  const dsn = `file:${join(work, 'social.sqlite')}`
  copyFileSync(inject('socialTemplateDatabase'), join(work, 'social.sqlite'))
  const launch = { binary: inject('socialBinary'), cwd: inject('socialRoot'), dsn, env }
  let running = await startSocialProcess(launch)
  const port = running.port
  return {
    url: running.url,
    stop: (graceMilliseconds = 500) => running.stop(graceMilliseconds),
    async restart() {
      running = await startSocialProcess({ ...launch, port })
    },
    async dispose() {
      await running.stop(500)
      rmSync(work, { recursive: true, force: true })
    },
  }
}

export async function setPrincipalMode(baseUrl: string, user: string, mode: '' | 'unresolvable' | 'forbidden'): Promise<void> {
  const response = await fetch(`${baseUrl}/test/principal?user=${user}&mode=${mode}`, { method: 'POST' })
  if (response.status !== 204) {
    throw new Error(`principal mode refused: ${response.status}`)
  }
}

export async function invalidatePolicies(baseUrl: string): Promise<void> {
  const response = await fetch(`${baseUrl}/test/invalidate-policies`, { method: 'POST' })
  if (response.status !== 204) {
    throw new Error(`invalidate refused: ${response.status}`)
  }
}

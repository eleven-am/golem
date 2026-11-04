import { describe, expect, it } from 'vitest'
import {
  CODE_CONTRACT_MISMATCH,
  CODE_PROTOCOL_MISMATCH,
  GolemContractMismatchError,
  GolemDeadlineRequiredError,
  GolemError,
  GolemHttpError,
  GolemProtocolMismatchError,
  classifyClose,
  failureOf,
} from '../src/index.js'
import { actor, collector, headerSocket, manifest, POST_EVENTS, socialUrl, transport } from './harness/social.js'

const WRONG_CONTRACT = { contract: 'f'.repeat(64) }

async function postRaw(extensions: unknown, headers: Record<string, string> = {}) {
  const response = await fetch(`${socialUrl()}/graphql`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...headers },
    body: JSON.stringify({ query: '{ posts { id } }', extensions }),
  })
  return { status: response.status, body: await response.json() }
}

async function refusalOf(golem: unknown, headers: Record<string, string> = {}): Promise<{ code: number; reason: string }> {
  const url = `${socialUrl().replace('http', 'ws')}/graphql`
  const Impl = WebSocket as unknown as new (url: string, init: { protocols: string; headers: Record<string, string> }) => WebSocket
  const socket = new Impl(url, { protocols: 'graphql-transport-ws', headers })
  return new Promise((resolve, reject) => {
    socket.onopen = () => socket.send(JSON.stringify({ type: 'connection_init', payload: { golem } }))
    socket.onclose = (event) => resolve({ code: event.code, reason: event.reason })
    socket.onerror = () => reject(new Error('socket error'))
  })
}

describe('negotiation on the real server', () => {
  it('maps an HTTP contract mismatch to a typed, non-retryable error', async () => {
    const client = transport({ manifest: WRONG_CONTRACT })
    const failure = await client.execute({ query: '{ posts { id } }' }).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(GolemContractMismatchError)
    expect((failure as GolemError).code).toBe(CODE_CONTRACT_MISMATCH)
    expect((failure as GolemError).retryable).toBe(false)
  })

  it('maps an HTTP protocol mismatch to a typed, non-retryable error', async () => {
    const { body } = await postRaw({ golem: { protocol: 2, contract: manifest.contract } })
    const failure = failureOf(body)
    expect(failure).toBeInstanceOf(GolemProtocolMismatchError)
    expect(failure?.code).toBe(CODE_PROTOCOL_MISMATCH)
    expect(failure?.retryable).toBe(false)
  })

  it('classifies the real 4426 closes for protocol and contract', async () => {
    const protocol = await refusalOf({ protocol: 2, contract: manifest.contract })
    expect(protocol).toEqual({ code: 4426, reason: 'golem:protocol' })
    const protocolDisposition = classifyClose(protocol.code, protocol.reason, false)
    expect(protocolDisposition.action === 'fatal' && protocolDisposition.status).toBe('protocol-mismatch')
    const contract = await refusalOf({ protocol: 1, contract: WRONG_CONTRACT.contract })
    expect(contract).toEqual({ code: 4426, reason: 'golem:contract' })
    const contractDisposition = classifyClose(contract.code, contract.reason, false)
    expect(contractDisposition.action === 'fatal' && contractDisposition.status).toBe('contract-mismatch')
  })

  it('ends a subscription on a contract mismatch, latches the session and does not retry', async () => {
    const sleeps: number[] = []
    let sockets = 0
    const client = transport({
      manifest: WRONG_CONTRACT,
      credentials: actor().credentials,
      sleep: async (ms) => void sleeps.push(ms),
      createSocket: (url, protocols, credentials) => {
        sockets++
        return headerSocket(url, protocols, credentials)
      },
    })
    const first = collector()
    client.subscribe({ query: POST_EVENTS }, first.handlers)
    await first.until(() => first.ends.length === 1)
    const end = first.ends[0]
    expect(end).toMatchObject({ kind: 'fatal', status: 'contract-mismatch' })
    expect(end?.kind === 'fatal' && end.error).toBeInstanceOf(GolemContractMismatchError)
    expect(client.status).toBe('contract-mismatch')
    expect(sleeps).toEqual([])
    const second = collector()
    client.subscribe({ query: POST_EVENTS }, second.handlers)
    await second.until(() => second.ends.length === 1)
    expect(second.ends[0]).toMatchObject({ kind: 'fatal', status: 'contract-mismatch' })
    expect(sockets).toBe(1)
    client.dispose()
  })
})

describe('the credential deadline on the real server', () => {
  it('refuses a socket whose principal has no expiry, and still serves HTTP', async () => {
    const who = actor()
    const credentials = () => ({ headers: { 'x-golem-user': who.id, 'x-golem-test-unbounded': '1' } })
    const client = transport({ credentials })
    const seen = collector()
    client.subscribe({ query: POST_EVENTS }, seen.handlers)
    await seen.until(() => seen.ends.length === 1)
    const end = seen.ends[0]
    expect(end).toMatchObject({ kind: 'fatal', status: 'deadline-required' })
    expect(end?.kind === 'fatal' && end.error).toBeInstanceOf(GolemDeadlineRequiredError)
    expect(seen.acks()).toBe(0)
    const rows = await client.execute<{ posts: unknown[] }>({ query: '{ posts { id } }' })
    expect(Array.isArray(rows.posts)).toBe(true)
    client.dispose()
  })

  it('closes with 4401 at the deadline, asks for credentials once more, and reconnects', async () => {
    const who = actor()
    let calls = 0
    const credentials = () => {
      calls++
      return { headers: { 'x-golem-user': who.id, 'x-golem-test-deadline-ms': '1500' } }
    }
    const client = transport({ credentials })
    const first = collector()
    client.subscribe({ query: POST_EVENTS }, first.handlers)
    await first.until(() => first.acks() === 1)
    expect(calls).toBe(1)
    await first.until(() => first.ends.length === 1)
    expect(first.ends[0]).toEqual({ kind: 'reauthenticate' })
    const second = collector()
    client.subscribe({ query: POST_EVENTS }, second.handlers)
    await second.until(() => second.acks() === 1)
    expect(calls).toBe(2)
    expect(client.status).toBe('open')
    client.dispose()
  })
})

describe('commit reports on the real server', () => {
  it('raises GolemPartialCommitError when a later root fails after an earlier root committed', async () => {
    const who = actor()
    const client = transport({ credentials: who.credentials })
    const handle = `h-${who.id}`
    const failure = await client
      .execute({
        query: `mutation($a: UserCreateInput!, $b: UserCreateInput!) {
          first: createUser(data: $a) { id }
          second: createUser(data: $b) { id }
        }`,
        variables: {
          a: { id: who.id, handle, email: `${who.id}@example.test` },
          b: { handle, email: `other-${who.id}@example.test` },
        },
      })
      .catch((error: unknown) => error)
    expect(failure).toMatchObject({ name: 'GolemPartialCommitError', retryable: false })
    expect((failure as { errors: unknown[] }).errors).toHaveLength(1)
    expect((failure as { path: unknown }).path).toEqual(['second'])
    const stored = await client.execute<{ user: { id: string } | null }>({ query: 'query($id: UUID!) { user(where: { ID: $id }) { id } }', variables: { id: who.id } })
    expect(stored.user?.id).toBe(who.id)
  })

  it('raises a plain GolemError when a single root fails without committing', async () => {
    const who = actor()
    const client = transport({ credentials: who.credentials })
    const failure = await client
      .execute({ query: 'mutation($b: UserCreateInput!) { createUser(data: $b) { id } }', variables: { b: { id: randomOther(), handle: 'x', email: 'x@example.test' } } })
      .catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(GolemError)
    expect((failure as GolemError).name).toBe('GolemError')
  })

  it('types a non-GraphQL HTTP failure with its status', async () => {
    const client = transport({ credentials: () => ({ headers: { authorization: 'Bearer nope' } }) })
    const failure = await client.execute({ query: '{ posts { id } }' }).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(GolemHttpError)
    expect((failure as GolemHttpError).status).toBe(401)
  })
})

function randomOther(): string {
  return crypto.randomUUID()
}

import { describe, expect, it } from 'vitest'
import { actor, collector, CREATE_POST, POST_EVENTS, postInput, signUp, transport } from './harness/social.js'

describe('live subscription against the real social server', () => {
  it('delivers the acknowledgement before any event and then the event', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    await signUp(client, alice)
    const seen = collector()
    const stop = client.subscribe({ query: POST_EVENTS }, seen.handlers)
    await seen.until(() => seen.acks() === 1)
    expect(seen.events).toHaveLength(0)
    await client.execute({ query: CREATE_POST, variables: { data: postInput('live one') } })
    await seen.until(() => seen.events.length === 1)
    const event = seen.events[0] as { postEvents: { type: string; version: string; entity: { title: string } } }
    expect(event.postEvents.type).toBe('CREATED')
    expect(event.postEvents.entity.title).toBe('live one')
    expect(typeof event.postEvents.version).toBe('string')
    expect(seen.acks()).toBe(1)
    stop()
    client.dispose()
  })

  it('ends a refused subscription with its error and no acknowledgement', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    const seen = collector()
    client.subscribe({ query: 'subscription { postEvents { nonexistentField } }' }, seen.handlers)
    await seen.until(() => seen.ends.length === 1)
    expect(seen.acks()).toBe(0)
    const end = seen.ends[0]
    expect(end?.kind).toBe('error')
    client.dispose()
  })
})

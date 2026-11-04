import { describe, expect, it } from 'vitest'
import { actor, transport } from './harness/social.js'

describe('http round trip against the real social server', () => {
  it('creates a user, reads it back and lists it', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    const created = await client.execute<{ createUser: { id: string; handle: string } }>({
      query: 'mutation($data: UserCreateInput!) { createUser(data: $data) { id handle } }',
      variables: { data: { id: alice.id, handle: `h-${alice.id}`, email: `${alice.id}@example.test` } },
    })
    expect(created.createUser.id).toBe(alice.id)
    const read = await client.execute<{ user: { handle: string } }>({
      query: 'query($id: UUID!) { user(where: { ID: $id }) { handle } }',
      variables: { id: alice.id },
    })
    expect(read.user.handle).toBe(`h-${alice.id}`)
  })
})

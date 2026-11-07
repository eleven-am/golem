import { afterEach, describe, expect, it, vi } from 'vitest'
import { GolemProvider, useGolemMutation } from '../src/index.js'
import { screen, until, type Screen } from './harness.js'
import { postInput } from '../../core/test/harness/social.js'

const CREATE = 'mutation($data: PostCreateInput!) { createPost(data: $data) { id title } }'

const open: Screen[] = []

afterEach(async () => {
  vi.restoreAllMocks()
  for (const one of open.splice(0)) {
    await one.close()
  }
})

function Fire({ keepalive }: { keepalive?: boolean }) {
  const mutation = useGolemMutation(CREATE, keepalive === undefined ? undefined : { keepalive })
  return <button data-testid="fire" onClick={() => mutation.mutate({ data: postInput('keepalive') })}>fire</button>
}

async function press(keepalive?: boolean): Promise<RequestInit[]> {
  const one = await screen()
  open.push(one)
  const sent: RequestInit[] = []
  const real = globalThis.fetch
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    if (init !== undefined) {
      sent.push(init)
    }
    return real(input, init)
  })
  one.render(
    <GolemProvider client={one.client}>
      <Fire keepalive={keepalive} />
    </GolemProvider>,
  )
  await until(() => one.container.querySelector('[data-testid="fire"]') !== null, 15_000, 'button')
  one.container.querySelector<HTMLButtonElement>('[data-testid="fire"]')?.click()
  await until(() => sent.some((init) => typeof init.body === 'string' && init.body.includes('createPost')), 15_000, 'mutation request')
  return sent.filter((init) => typeof init.body === 'string' && init.body.includes('createPost'))
}

describe('useGolemMutation keepalive', () => {
  it('sends the mutation with keepalive when the hook asks for it', async () => {
    const sent = await press(true)
    expect(sent[0]?.keepalive).toBe(true)
  })

  it('sends without keepalive by default', async () => {
    const sent = await press()
    expect(sent[0]?.keepalive).toBeFalsy()
  })
})

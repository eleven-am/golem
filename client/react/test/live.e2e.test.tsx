import { randomUUID } from 'node:crypto'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GolemNotLiveError } from '@eleven-am/golem-client'
import { GolemProvider, useGolemInfiniteQuery, useGolemMutation, useGolemQuery, useGolemStaticQuery, useGolemStatus } from '../src/index.js'
import { postInput } from '../../core/test/harness/social.js'
import { Boundary } from './boundary.js'
import { DELETE_POST, UPDATE_POST, until, type Screen, screen } from './harness.js'

const LIST = 'query($t: String!) { posts(where: { title: { contains: $t }, published: { equals: true } }, orderBy: [{ title: asc }]) { id title } }'

const PAGED = 'query($t: String!, $take: Int!) { posts(where: { title: { contains: $t }, published: { equals: true } }, orderBy: [{ title: asc }], take: $take) { id title } }'

const CREATE = 'mutation($data: PostCreateInput!) { createPost(data: $data) { id title } }'

const COUNT = 'query($t: String!) { aggregatePosts(where: { title: { contains: $t } }) { count } }'

type Rows = { posts: Array<{ id: string; title: string }> }

function usePostList(token: string) {
  const query = useGolemQuery(LIST, { t: token })
  const status = useGolemStatus(LIST, { t: token })
  const rows = (query.data as Rows | undefined)?.posts ?? []
  return { titles: rows.map((row) => row.title), state: status?.state ?? 'NONE' }
}

function PostList({ token }: { token: string }) {
  const view = usePostList(token)
  return (
    <div>
      <p data-testid="state">{view.state}</p>
      <ul>
        {view.titles.map((title) => (
          <li key={title}>{title}</li>
        ))}
      </ul>
    </div>
  )
}

function StatusOnly({ token }: { token: string }) {
  const status = useGolemStatus(LIST, { t: token })
  return <p data-testid="status-only">{status?.state ?? 'NONE'}</p>
}

function useComposer(token: string) {
  const mutation = useGolemMutation(CREATE)
  const create = () => mutation.mutate({ data: { ...postInput(`${token}-composed`), published: true } })
  return { create, pending: mutation.isPending }
}

function Composer({ token }: { token: string }) {
  const view = useComposer(token)
  return (
    <button data-testid="compose" disabled={view.pending} onClick={view.create}>
      compose
    </button>
  )
}

function usePages(token: string) {
  const query = useGolemInfiniteQuery(PAGED, { t: token, take: 2 })
  const titles = (query.data?.pages ?? []).flatMap((page) => (page as Rows).posts.map((row) => row.title))
  return { titles, more: query.hasNextPage, next: () => void query.fetchNextPage() }
}

function Pages({ token }: { token: string }) {
  const view = usePages(token)
  return (
    <div>
      <p data-testid="pages">{view.titles.join(',')}</p>
      <button data-testid="more" disabled={!view.more} onClick={view.next}>
        more
      </button>
    </div>
  )
}

function useTotal(token: string) {
  const query = useGolemStaticQuery(COUNT, { t: token })
  return (query.data as { aggregatePosts: { count: string } } | undefined)?.aggregatePosts.count
}

function Total({ token }: { token: string }) {
  const total = useTotal(token)
  return <p data-testid="total">{total === undefined ? 'none' : String(total)}</p>
}

function LiveCount() {
  const query = useGolemQuery(COUNT, { t: 'x' })
  return <p>{String(query.data)}</p>
}

const open: Screen[] = []

afterEach(async () => {
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

const token = () => `rx${randomUUID().slice(0, 6)}`

const listed = (one: Screen) => Array.from(one.container.querySelectorAll('li')).map((item) => item.textContent ?? '').sort()

const stateOf = (one: Screen) => one.container.querySelector('[data-testid="state"]')?.textContent

describe('useGolemQuery against the social server', () => {
  it('shows create, update, unpublish and delete with no cache work in the app', async () => {
    const one = await fresh()
    const tag = token()
    const keep = await one.createPost(`${tag}-keep`)
    const exit = await one.createPost(`${tag}-exit`)
    one.render(
      <GolemProvider client={one.client}>
        <PostList token={tag} />
      </GolemProvider>,
    )
    await until(() => stateOf(one) === 'LIVE' && listed(one).length === 2, 15_000, 'first render')
    expect(listed(one)).toEqual([`${tag}-exit`, `${tag}-keep`])
    await new Promise((resolve) => setTimeout(resolve, 800))

    const added = await one.createPost(`${tag}-added`)
    await until(() => listed(one).includes(`${tag}-added`), 15_000, 'create')

    await one.run(UPDATE_POST, { id: keep, data: { title: { set: `${tag}-renamed` } } })
    await until(() => listed(one).includes(`${tag}-renamed`) && !listed(one).includes(`${tag}-keep`), 15_000, 'update')

    await one.run(UPDATE_POST, { id: exit, data: { published: { set: false } } })
    await until(() => !listed(one).includes(`${tag}-exit`), 15_000, 'exit')

    await one.run(DELETE_POST, { id: added })
    await until(() => !listed(one).includes(`${tag}-added`), 15_000, 'delete')
    expect(listed(one)).toEqual([`${tag}-renamed`])
  })

  it('shows a row created by useGolemMutation in the list', async () => {
    const one = await fresh()
    const tag = token()
    const mutate = vi.spyOn(one.client, 'mutate')
    one.render(
      <GolemProvider client={one.client}>
        <PostList token={tag} />
        <Composer token={tag} />
      </GolemProvider>,
    )
    await until(() => stateOf(one) === 'LIVE', 15_000, 'live')
    await new Promise((resolve) => setTimeout(resolve, 500))
    one.container.querySelector<HTMLButtonElement>('[data-testid="compose"]')?.click()
    await until(() => listed(one).includes(`${tag}-composed`), 15_000, 'mutation row')
    expect(mutate).toHaveBeenCalledTimes(1)
    expect(mutate.mock.calls[0]?.[0]).toBe(CREATE)
  })

  it('updates a component that only reads the live status', async () => {
    const one = await fresh()
    const tag = token()
    one.render(
      <GolemProvider client={one.client}>
        <PostList token={tag} />
        <StatusOnly token={tag} />
      </GolemProvider>,
    )
    await until(() => one.container.querySelector('[data-testid="status-only"]')?.textContent === 'LIVE', 15_000, 'status only')
  })

  it('works under StrictMode', async () => {
    const one = await fresh()
    const tag = token()
    await one.createPost(`${tag}-one`)
    one.render(
      <GolemProvider client={one.client}>
        <PostList token={tag} />
      </GolemProvider>,
      { strict: true },
    )
    await until(() => stateOf(one) === 'LIVE' && listed(one).length === 1, 15_000, 'strict first render')
    await new Promise((resolve) => setTimeout(resolve, 800))
    await one.createPost(`${tag}-two`)
    await until(() => listed(one).length === 2, 15_000, 'strict create')
  })

  it('refuses a document that cannot be live', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const one = await fresh()
    one.render(
      <GolemProvider client={one.client}>
        <Boundary>
          <LiveCount />
        </Boundary>
      </GolemProvider>,
    )
    await until(() => one.container.querySelector('[data-testid="caught"]') !== null)
    expect(one.text()).toContain(GolemNotLiveError.name)
  })
})

describe('other hooks', () => {
  it('pages with useGolemInfiniteQuery and keeps the pages live', async () => {
    const one = await fresh()
    const tag = token()
    for (const name of ['a', 'b', 'c']) {
      await one.createPost(`${tag}-${name}`)
    }
    one.render(
      <GolemProvider client={one.client}>
        <Pages token={tag} />
      </GolemProvider>,
    )
    const pages = () => one.container.querySelector('[data-testid="pages"]')?.textContent
    await until(() => pages() === `${tag}-a,${tag}-b`, 15_000, 'first page')
    await new Promise((resolve) => setTimeout(resolve, 800))
    one.container.querySelector<HTMLButtonElement>('[data-testid="more"]')?.click()
    await until(() => pages() === `${tag}-a,${tag}-b,${tag}-c`, 15_000, 'second page')
    await one.createPost(`${tag}-aa`)
    await until(() => pages() === `${tag}-a,${tag}-aa,${tag}-b,${tag}-c`, 15_000, 'live across pages')
  })

  it('serves a static document with useGolemStaticQuery', async () => {
    const one = await fresh()
    const tag = token()
    await one.createPost(`${tag}-count`)
    one.render(
      <GolemProvider client={one.client}>
        <Total token={tag} />
      </GolemProvider>,
    )
    await until(() => one.text() === '1', 15_000, 'count')
  })
})

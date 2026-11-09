import { randomUUID } from 'node:crypto'
import { afterEach, describe, expect, it } from 'vitest'
import { CREATE_COMMENT, DELETE_COMMENT, DELETE_POST, idle, POST_WITH_COMMENTS, sleep, until, watch, world, type Watch, type World } from './harness/live.js'

const open: World[] = []

async function fresh(): Promise<World> {
  const created = await world()
  open.push(created)
  return created
}

afterEach(async () => {
  for (const one of open.splice(0)) {
    await one.close()
  }
})

const token = () => `t${randomUUID().slice(0, 8)}`

const SINGLE = 'query($id: UUID!) { post(where: { ID: $id }) { title } }'

const PUBLISHED = 'query($t: String!) { posts(where: { title: { contains: $t }, published: { equals: true } }) { title } }'

const SEARCH = 'query($t: String!) { searchPosts(take: 20, where: { title: { contains: $t } }) { title } }'

const RANKED = 'query($t: String!) { posts(where: { title: { contains: $t } }, orderBy: [{ priority: asc }]) { title } }'

const names = (data: unknown, root: string): string[] =>
  ((data as Record<string, Array<{ title: string }> | undefined> | undefined)?.[root] ?? []).map((row) => row.title).sort()

async function settled(view: Watch): Promise<void> {
  await idle(view)
}

describe('removal while every response is held', () => {
  it('nulls a single root slot', async () => {
    const w = await fresh()
    const id = await w.createPost(token())
    const view = watch(w, SINGLE, { id })
    await until(() => (view.data() as { post?: unknown } | undefined)?.post != null)
    await settled(view)
    const before = view.fetches()
    w.gate.hold()
    await w.run(DELETE_POST, { id })
    await until(() => (view.data() as { post: unknown }).post === null)
    await until(() => view.fetches() === before + 1)
    w.gate.release()
  })

  it('removes a comment from a nested list and keeps its sibling', async () => {
    const w = await fresh()
    const post = await w.createPost(token())
    const gone = randomUUID()
    const stays = randomUUID()
    await w.run(CREATE_COMMENT, { id: gone, post, author: w.who.id, body: 'gone' })
    await w.run(CREATE_COMMENT, { id: stays, post, author: w.who.id, body: 'stays' })
    const view = watch(w, POST_WITH_COMMENTS, { id: post })
    const bodies = () => ((view.data() as { post?: { comments: Array<{ body: string }> } } | undefined)?.post?.comments ?? []).map((one) => one.body).sort()
    await until(() => bodies().length === 2)
    await settled(view)
    const before = view.fetches()
    w.gate.hold()
    await w.run(DELETE_COMMENT, { id: gone })
    await until(() => bodies().length === 1)
    expect(bodies()).toEqual(['stays'])
    await until(() => view.fetches() === before + 1)
    w.gate.release()
  })

  it('removes a row that leaves a published filter when its own field changes (EXITED)', async () => {
    const w = await fresh()
    const mine = token()
    const leaving = await w.createPost(`${mine}-leaving`)
    await w.createPost(`${mine}-stays`)
    const view = watch(w, PUBLISHED, { t: mine })
    await until(() => names(view.data(), 'posts').length === 2)
    await settled(view)
    const before = view.fetches()
    w.gate.hold()
    await w.updatePost(leaving, { published: { set: false } })
    await until(() => names(view.data(), 'posts').length === 1)
    expect(names(view.data(), 'posts')).toEqual([`${mine}-stays`])
    await until(() => view.fetches() === before + 1)
    w.gate.release()
  })
})

describe('ranked and custom results', () => {
  async function pair() {
    const alice = await fresh()
    const bob = await fresh()
    return { alice, bob }
  }

  it('(a) a deleted post leaves a custom result slot after the refetch', async () => {
    const { alice, bob } = await pair()
    const mine = token()
    const gone = await alice.createPost(`${mine}-gone`)
    await alice.createPost(`${mine}-stays`)
    const view = watch(bob, SEARCH, { t: mine })
    await until(() => names(view.data(), 'searchPosts').length === 2)
    await settled(view)
    const before = view.fetches()
    bob.gate.hold()
    await alice.run(DELETE_POST, { id: gone })
    await until(() => view.fetches() === before + 1)
    expect(names(view.data(), 'searchPosts')).toHaveLength(2)
    bob.gate.release()
    await until(() => names(view.data(), 'searchPosts').length === 1)
    expect(names(view.data(), 'searchPosts')).toEqual([`${mine}-stays`])
  })

  it('(b) a post the viewer can no longer read leaves a custom result slot after the refetch', async () => {
    const { alice, bob } = await pair()
    const mine = token()
    const hidden = await alice.createPost(`${mine}-hidden`)
    await alice.createPost(`${mine}-stays`)
    const view = watch(bob, SEARCH, { t: mine })
    await until(() => names(view.data(), 'searchPosts').length === 2)
    await settled(view)
    const before = view.fetches()
    bob.gate.hold()
    await alice.updatePost(hidden, { published: { set: false } })
    await until(() => view.fetches() === before + 1)
    expect(names(view.data(), 'searchPosts')).toHaveLength(2)
    bob.gate.release()
    await until(() => names(view.data(), 'searchPosts').length === 1)
    expect(names(view.data(), 'searchPosts')).toEqual([`${mine}-stays`])
  })

  it('(d) a new match appears only after the refetch lands', async () => {
    const { alice, bob } = await pair()
    const mine = token()
    await alice.createPost(`${mine}-first`)
    const view = watch(bob, SEARCH, { t: mine })
    await until(() => names(view.data(), 'searchPosts').length === 1)
    await settled(view)
    const before = view.fetches()
    bob.gate.hold()
    await alice.createPost(`${mine}-second`)
    await until(() => view.fetches() === before + 1)
    await sleep(300)
    expect(names(view.data(), 'searchPosts')).toEqual([`${mine}-first`])
    bob.gate.release()
    await until(() => names(view.data(), 'searchPosts').length === 2)
  })

  it('(e) a draft the viewer can never read produces no frame and no refetch', async () => {
    const { alice, bob } = await pair()
    const mine = token()
    await alice.createPost(`${mine}-first`)
    const view = watch(bob, SEARCH, { t: mine })
    await until(() => names(view.data(), 'searchPosts').length === 1)
    await settled(view)
    const before = view.fetches()
    await alice.createPost(`${mine}-draft`, false)
    await sleep(1200)
    expect(view.fetches()).toBe(before)
    expect(names(view.data(), 'searchPosts')).toEqual([`${mine}-first`])
  })

  it('an UPDATED of a held row in a custom slot refetches', async () => {
    const { alice, bob } = await pair()
    const mine = token()
    const id = await alice.createPost(`${mine}-old`)
    const view = watch(bob, SEARCH, { t: mine })
    await until(() => names(view.data(), 'searchPosts').length === 1)
    await settled(view)
    const before = view.fetches()
    await alice.updatePost(id, { title: { set: `${mine}-new` } })
    await until(() => view.fetches() === before + 1)
    await until(() => names(view.data(), 'searchPosts')[0] === `${mine}-new`)
  })

  it('every UPDATED on an ordered list refetches', async () => {
    const w = await fresh()
    const mine = token()
    const id = await w.createPost(`${mine}-a`)
    const view = watch(w, RANKED, { t: mine })
    await until(() => names(view.data(), 'posts').length === 1)
    await settled(view)
    const before = view.fetches()
    await w.updatePost(id, { title: { set: `${mine}-b` } })
    await until(() => view.fetches() === before + 1)
    await until(() => names(view.data(), 'posts')[0] === `${mine}-b`)
    await w.updatePost(id, { priority: { set: 9 } })
    await until(() => view.fetches() === before + 2)
  })
})

describe('bounded lists refill after a removal', () => {
  const FIRST = 'query($t: String!) { posts(where: { title: { contains: $t }, published: { equals: true } }, take: 1, orderBy: [{ title: asc }]) { title } }'

  it.each([
    ['DELETED', (w: World, id: string) => w.run(DELETE_POST, { id })],
    ['EXITED', (w: World, id: string) => w.updatePost(id, { published: { set: false } })],
  ])('a %s row leaves a take of one and the next row refills it', async (_name, remove) => {
    const w = await fresh()
    const mine = token()
    const first = await w.createPost(`${mine}-a`)
    await w.createPost(`${mine}-b`)
    const view = watch(w, FIRST, { t: mine })
    await until(() => names(view.data(), 'posts').length === 1)
    expect(names(view.data(), 'posts')).toEqual([`${mine}-a`])
    await settled(view)
    await remove(w, first)
    await until(() => names(view.data(), 'posts')[0] === `${mine}-b`)
  })
})

describe('a composite-key model', () => {
  it('removes a PostTag from the delete event while every response is held', async () => {
    const w = await fresh()
    const mine = token()
    const post = await w.createPost(`${mine}-host`)
    const gone = `${mine}-gone`
    const stays = `${mine}-stays`
    for (const name of [gone, stays]) {
      await w.run('mutation($data: TagCreateInput!) { createTag(data: $data) { id } }', { data: { name } })
      await w.run('mutation($data: PostTagCreateInput!) { createPostTag(data: $data) { postID } }', { data: { post: { connect: { ID: post } }, tag: { connect: { Name: name } } } })
    }
    const view = watch(w, 'query($p: String!) { postTags(where: { tagName: { startsWith: $p } }) { tagName } }', { p: mine })
    const tagNames = () => ((view.data() as { postTags?: Array<{ tagName: string }> } | undefined)?.postTags ?? []).map((row) => row.tagName).sort()
    await until(() => tagNames().length === 2)
    await settled(view)
    const before = view.fetches()
    w.gate.hold()
    await w.run('mutation($post: UUID!, $name: String!) { deletePostTag(where: { PostID_TagName: { postID: $post, tagName: $name } }) { postID } }', { post, name: gone })
    await until(() => tagNames().length === 1)
    expect(tagNames()).toEqual([stays])
    await until(() => view.fetches() === before + 1)
    w.gate.release()
  })
})

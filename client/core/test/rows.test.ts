import { describe, expect, it } from 'vitest'
import { GolemProtocolError, RowStore } from '../src/index.js'
import { socialManifest } from './fixtures/manifests.js'

const store = new RowStore(socialManifest)

type Loose = Record<string, any>

const post = (id: string, title: string, version: string, extra: Record<string, unknown> = {}): Loose => ({ __typename: 'Post', id, _version: version, title, ...extra })
const comment = (id: string, body: string, version: string): Loose => ({ __typename: 'Comment', id, _version: version, body })

describe('RowStore keys and versions', () => {
  it('keys a row by typename and primary key, and refuses unknown or incomplete rows', () => {
    expect(store.keyOf('Post', { id: 'a' })).toBe('Post:a')
    expect(store.keyOf('Post', {})).toBeUndefined()
    expect(store.keyOf('Nope', { id: 'a' })).toBeUndefined()
  })

  it('reads every versioned row anywhere in the data, keeping the highest version of a repeated key', () => {
    const data = { posts: [post('a', 'x', '3'), post('b', 'y', '9007199254740993')], other: { post: post('a', 'x', '5', { comments: [comment('c', 'hi', '2')] }) } }
    const found = store.versions(data)
    expect(found.get('Post:a')).toBe(5n)
    expect(found.get('Post:b')).toBe(9007199254740993n)
    expect(found.get('Comment:c')).toBe(2n)
  })

  it('ignores rows without a version', () => {
    expect(store.versions({ users: [{ __typename: 'User', id: 'u', handle: 'h' }] }).size).toBe(0)
  })

  it('names a delete event by its id when it carries no entity, and fails for a composite key', () => {
    expect(store.eventKey('Post', 'a', null)).toBe('Post:a')
    expect(store.eventKey('Post', 'ignored', { id: 'a', __typename: 'Post' })).toBe('Post:a')
    expect(() => store.eventKey('Nope', 'a', null)).toThrow(GolemProtocolError)
  })
})

describe('RowStore rewriting', () => {
  it('patches only the held scalars of the matching row and shares everything else', () => {
    const first = post('a', 'old', '1', { metadata: { language: 'en' } })
    const second = post('b', 'keep', '1')
    const data = { posts: [first, second] }
    const next = store.patch(data, 'Post:a', { id: 'a', _version: '2', title: 'new', metadata: { language: 'fr' }, body: 'not selected', __typename: 'Post' }) as typeof data
    expect(next.posts[0]).toMatchObject({ title: 'new', _version: '2', metadata: { language: 'fr' } })
    expect(next.posts[0]).not.toHaveProperty('body')
    expect(next.posts[1]).toBe(second)
    expect(store.patch(data, 'Post:zzz', { title: 'x' })).toBe(data)
  })

  it('removes a row from lists, nulls single slots and reaches nested relations', () => {
    const data = {
      posts: [post('a', 'x', '1'), post('b', 'y', '1')],
      single: post('a', 'x', '1'),
      holder: post('h', 'h', '1', { comments: [comment('c1', 'one', '1'), comment('c2', 'two', '1')] }),
    }
    const next = store.remove(data, new Set(['Post:a', 'Comment:c1'])) as typeof data & { single: unknown }
    expect(next.posts.map((row) => row.id)).toEqual(['b'])
    expect(next.single).toBeNull()
    expect((next.holder as { comments: Array<{ id: string }> }).comments.map((row) => row.id)).toEqual(['c2'])
    expect(store.remove(data, new Set(['Post:none']))).toBe(data)
  })

  it('adopts cached scalars into a fresh row but never touches its relations', () => {
    const cached = post('a', 'newer', '5', { comments: [comment('c', 'cached child', '5')] })
    const fresh = post('a', 'older', '2', { comments: [comment('c', 'fresh child', '6'), comment('d', 'extra', '1')] })
    const next = store.adopt({ post: fresh }, store.rows({ post: cached }), new Set(['Post:a'])) as { post: typeof fresh }
    expect(next.post.title).toBe('newer')
    expect(next.post._version).toBe('5')
    expect(next.post.comments).toBe(fresh.comments)
  })

  it('also keeps an aliased relation, recognised by the rows it holds', () => {
    const cached = post('a', 'newer', '5', { discussion: [comment('c', 'cached child', '5')] })
    const fresh = post('a', 'older', '2', { discussion: [comment('c', 'fresh child', '6')] })
    const next = store.adopt({ post: fresh }, store.rows({ post: cached }), new Set(['Post:a'])) as { post: typeof fresh }
    expect(next.post.discussion).toBe(fresh.discussion)
  })
})

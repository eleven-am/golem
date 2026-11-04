import { describe, expect, it } from 'vitest'
import { GolemProtocolError, Patcher } from '../src/index.js'
import { socialManifest, socialStrictCommentPost } from './fixtures/manifests.js'

const store = new Patcher(socialManifest)

type Loose = Record<string, any>

const post = (id: string, title: string, version: string, extra: Record<string, unknown> = {}): Loose => ({ __typename: 'Post', id, _version: version, title, ...extra })
const comment = (id: string, body: string, version: string): Loose => ({ __typename: 'Comment', id, _version: version, body })

describe('Patcher keys and versions', () => {
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

  it('names a delete event by its id when it carries no entity', () => {
    expect(store.eventKey('Post', 'a', null)).toBe('Post:a')
    expect(store.eventKey('Post', 'ignored', { id: 'a', __typename: 'Post' })).toBe('Post:a')
    expect(() => store.eventKey('Nope', 'a', null)).toThrow(GolemProtocolError)
  })

  it('names a composite-key delete by the identity object, in the manifest key order', () => {
    expect(store.eventKey('PostTag', { tagName: 't', postID: 'p' }, null)).toBe(store.keyOf('PostTag', { postID: 'p', tagName: 't' }))
    expect(store.eventKey('PostTag', { tagName: 't', postID: 'p' }, null)).toBe('PostTag:p\u0001t')
    expect(() => store.eventKey('PostTag', 'p', null)).toThrow(GolemProtocolError)
    expect(() => store.eventKey('PostTag', { postID: 'p' }, null)).toThrow(GolemProtocolError)
  })
})

describe('Patcher rewriting', () => {
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
    const eviction = store.remove(data, new Set(['Post:a', 'Comment:c1']), new Set(['single']))
    const next = eviction.data as typeof data & { single: unknown }
    expect(next.posts.map((row) => row.id)).toEqual(['b'])
    expect(next.single).toBeNull()
    expect((next.holder as { comments: Array<{ id: string }> }).comments.map((row) => row.id)).toEqual(['c2'])
    expect(eviction.held.size).toBe(0)
    expect(store.remove(data, new Set(['Post:none']), new Set(['single'])).data).toBe(data)
  })
})

describe('Patcher single slots', () => {
  const nullable = new Set(['post'])

  it('nulls a nullable root slot and reports no held key', () => {
    const data = { post: post('a', 'x', '1') }
    const eviction = store.remove(data, new Set(['Post:a']), nullable)
    expect(eviction.data).toEqual({ post: null })
    expect(eviction.held.size).toBe(0)
  })

  it('keeps a root slot that is not known to be nullable and reports the key as held', () => {
    const row = post('a', 'x', '1')
    const data = { featured: row, posts: [post('b', 'y', '1')] }
    const eviction = store.remove(data, new Set(['Post:a']), nullable)
    expect(eviction.data).toBe(data)
    expect(eviction.held).toEqual(new Set(['Post:a']))
  })

  it('keeps a nested single slot even when a root slot of the same name is nullable', () => {
    const inner = post('a', 'x', '1')
    const data = { post: post('h', 'host', '1', { comments: [comment('c', 'one', '1')] }), other: { post: inner } }
    const eviction = store.remove(data, new Set(['Post:a']), new Set(['post']))
    expect(eviction.data).toBe(data)
    expect(eviction.held).toEqual(new Set(['Post:a']))
  })

  it('nulls a nested to-one relation only when the manifest marks it nullable', () => {
    const child = post('a', 'x', '1')
    const data = { comments: [{ __typename: 'Comment', id: 'c', _version: '1', body: 'b', post: child }] }
    const nulled = store.remove(data, new Set(['Post:a']), new Set()).data as typeof data
    expect(nulled.comments[0]?.post).toBeNull()
    const strict = new Patcher(socialStrictCommentPost)
    const kept = strict.remove(data, new Set(['Post:a']), new Set())
    expect(kept.data).toBe(data)
    expect(kept.held).toEqual(new Set(['Post:a']))
  })

  it('removes from a list and shares every untouched row', () => {
    const keep = post('b', 'y', '1')
    const data = { posts: [post('a', 'x', '1'), keep], single: post('s', 's', '1') }
    const next = store.remove(data, new Set(['Post:a']), nullable).data as typeof data
    expect(next.posts).toEqual([keep])
    expect(next.posts[0]).toBe(keep)
    expect(next.single).toBe(data.single)
  })
})

describe('Patcher differs', () => {
  const data = { posts: [post('a', 'x', '1', { priority: 3 })] }

  it('is true only when the entity carries a different value of a named field', () => {
    expect(store.differs(data, 'Post:a', { priority: 4 }, ['priority'])).toBe(true)
    expect(store.differs(data, 'Post:a', { priority: 3 }, ['priority'])).toBe(false)
    expect(store.differs(data, 'Post:a', { title: 'z' }, ['priority'])).toBe(false)
    expect(store.differs(data, 'Post:zzz', { priority: 4 }, ['priority'])).toBe(false)
    expect(store.differs(data, 'Post:a', { priority: 4 }, [])).toBe(false)
  })
})

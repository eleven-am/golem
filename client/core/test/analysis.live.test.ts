import { describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemError, streamRequest } from '../src/index.js'
import { actor, collector, CREATE_POST, manifest, postInput, signUp, transport } from './harness/social.js'

const CREATE_COMMENT =
  'mutation($id: UUID!, $post: UUID!, $author: UUID!, $body: String!) { createComment(data: {id: $id, body: $body, post: {connect: {ID: $post}}, author: {connect: {ID: $author}}}) { id } }'

const DOCUMENT =
  'query($id: UUID!) { post(where: { ID: $id }) { title comments { body } } }'

interface Row {
  readonly id: string
  readonly _version: string
  readonly __typename: string
}

describe('document analysis against the real social server', () => {
  const analyzer = new DocumentAnalyzer(manifest)

  it('returns the injected key, _version and __typename on every row', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    await signUp(client, alice)
    const created = await client.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput('round trip') } })
    const postID = created.createPost.id
    await client.execute({
      query: CREATE_COMMENT,
      variables: { id: crypto.randomUUID(), post: postID, author: alice.id, body: 'first' },
    })
    const analysis = analyzer.analyze(DOCUMENT)
    const data = await client.execute<{ post: Row & { title: string; comments: Array<Row & { body: string }> } }>({
      query: analysis.query,
      variables: { id: postID },
    })
    expect(data.post).toMatchObject({ id: postID, __typename: 'Post', title: 'round trip' })
    expect(data.post._version).toMatch(/^\d+$/)
    expect(data.post.comments).toHaveLength(1)
    expect(data.post.comments[0]).toMatchObject({ __typename: 'Comment', body: 'first' })
    expect(data.post.comments[0]?._version).toMatch(/^\d+$/)
    client.dispose()
  })

  it('serves a model without events without _version', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    await signUp(client, alice)
    const analysis = analyzer.analyze('{ users(take: 1) { handle } }')
    const data = await client.execute<{ users: Array<Row & { handle: string }> }>({ query: analysis.query })
    expect(data.users[0]?.__typename).toBe('User')
    expect(data.users[0]).not.toHaveProperty('_version')
    await expect(client.execute({ query: '{ users(take: 1) { handle _version } }' })).rejects.toBeInstanceOf(GolemError)
    client.dispose()
  })

  it('runs every planned stream, scoped to the parent, with the same row identity', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    await signUp(client, alice)
    const first = await client.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput('watched') } })
    const second = await client.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput('other') } })
    const analysis = analyzer.analyze(DOCUMENT)
    expect(analysis.live).toBe(true)
    const variables = { id: first.createPost.id }
    const seen = analysis.streams.map(() => collector())
    const stops = analysis.streams.map((stream, index) => client.subscribe(streamRequest(stream, variables), seen[index]!.handlers))
    await Promise.all(seen.map((one) => one.until(() => one.acks() === 1)))

    await client.execute({ query: CREATE_COMMENT, variables: { id: crypto.randomUUID(), post: second.createPost.id, author: alice.id, body: 'elsewhere' } })
    const commentID = crypto.randomUUID()
    await client.execute({ query: CREATE_COMMENT, variables: { id: commentID, post: first.createPost.id, author: alice.id, body: 'here' } })

    const comments = seen[1]!
    await comments.until(() => comments.events.length >= 1)
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(comments.events).toHaveLength(1)
    const event = comments.events[0] as { commentEvents: { type: string; id: string; version: string; entity: Row & { body: string } } }
    expect(event.commentEvents.type).toBe('CREATED')
    expect(event.commentEvents.entity).toMatchObject({ id: commentID, body: 'here', __typename: 'Comment' })
    expect(event.commentEvents.entity._version).toBe(event.commentEvents.version)

    const fetched = await client.execute<{ post: { comments: Array<Row> } }>({ query: analysis.query, variables })
    expect(fetched.post.comments.find((row) => row.id === commentID)?._version).toBe(event.commentEvents.version)
    const postEvents = seen[0]!.events as Array<{ postEvents: { id: string } }>
    expect(postEvents.every((one) => one.postEvents.id === first.createPost.id)).toBe(true)

    for (const stop of stops) {
      stop()
    }
    client.dispose()
  })

  it('runs the policy-only slot stream of an opaque root', async () => {
    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    await signUp(client, alice)
    const analysis = analyzer.analyze('{ searchPosts(take: 3, where: { title: { contains: "slot" } }) { title } }')
    expect(analysis.streams).toHaveLength(1)
    const seen = collector()
    const stop = client.subscribe(streamRequest(analysis.streams[0]!), seen.handlers)
    await seen.until(() => seen.acks() === 1)
    const created = await client.execute<{ createPost: { id: string } }>({ query: CREATE_POST, variables: { data: postInput('slot') } })
    const mine = () => (seen.events as Array<{ postEvents: { id: string; entity: Row & { title: string } } }>).filter((one) => one.postEvents.id === created.createPost.id)
    await seen.until(() => mine().length === 1)
    const event = mine()[0] as { postEvents: { entity: Row & { title: string } } }
    expect(event.postEvents.entity).toMatchObject({ title: 'slot', __typename: 'Post' })
    stop()
    client.dispose()
  })

  it('says PostTag is not live, and the server really has no PostTag events', async () => {
    const analysis = analyzer.analyze('{ postTags { tagName } }')
    expect(analysis.live).toBe(false)
    expect(analysis.notLive.map((error) => [error.reason, error.subject])).toEqual([['model-without-events', 'PostTag']])

    const alice = actor()
    const client = transport({ credentials: alice.credentials })
    const seen = collector()
    client.subscribe({ query: 'subscription { postTagEvents { eventID } }' }, seen.handlers)
    await seen.until(() => seen.ends.length === 1)
    expect(seen.acks()).toBe(0)
    expect(seen.ends[0]?.kind).toBe('error')
    client.dispose()
  })
})

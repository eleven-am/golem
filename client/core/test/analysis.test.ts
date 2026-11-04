import { QueryClient } from '@tanstack/query-core'
import { parse, print, type DocumentNode } from 'graphql'
import { describe, expect, expectTypeOf, it } from 'vitest'
import { DocumentAnalyzer, GolemClient, GolemDocumentError, GolemMultipleOperationsError, analyzeMutation, GolemNotLiveError, assertLive, streamRequest } from '../src/index.js'
import {
  socialHiddenCommentInverse,
  socialManifest,
  socialWithEventfulUsers,
  socialWithPostTagEvents,
  teamsManifest,
  socialPostNotNullable,
  socialWithRoot,
} from './fixtures/manifests.js'

const normal = (text: string): string => print(parse(text))

const flat = (text: string | undefined): string => (text ?? '').replace(/\s+/g, ' ').replace(/\( /g, '(').replace(/ \)/g, ')')

const social = new DocumentAnalyzer(socialManifest)

function streamQueries(analyzer: DocumentAnalyzer, text: string): string[] {
  return analyzer.analyze(text).streams.map((stream) => stream.query)
}

describe('identity injection', () => {
  it.each([
    ['a list root', '{ posts { title } }', ['id', '_version', '__typename']],
    ['a single root', 'query($id: UUID!) { post(where: { ID: $id }) { title } }', ['id', '_version', '__typename']],
  ])('adds the key, _version and __typename on %s', (_name, text, injected) => {
    const printed = social.analyze(text).query
    const selection = printed.slice(printed.indexOf('title'))
    for (const field of injected) {
      expect(selection).toContain(field)
    }
  })

  it.each(['id', '_version', '__typename'])('injects %s unconditionally when the selection only carries it under a directive', (field) => {
    const printed = social.analyze(`query($s: Boolean!) { posts { title ${field} @skip(if: $s) } }`).query
    expect(printed).toContain(`${field} @skip(if: $s)`)
    expect(printed.replace(`${field} @skip(if: $s)`, '')).toContain(field)
  })

  it('keeps an unconditional required field and adds nothing beside it', () => {
    expect(social.analyze('{ posts { id title } }').query).toBe(normal('{ posts { id title _version __typename } }'))
  })

  it('adds the whole compound key and _version on a keyed model with events', () => {
    const analyzer = new DocumentAnalyzer(socialWithPostTagEvents)
    expect(analyzer.analyze('{ postTags { tagName } }').query).toBe(
      normal('{ postTags { tagName postID _version __typename } }'),
    )
  })

  it('adds no _version to a model that has no events', () => {
    const printed = social.analyze('{ users { handle } }').query
    expect(printed).toBe(normal('{ users { handle id __typename } }'))
  })

  it('injects into nested relation selections and fragments', () => {
    const analysis = social.analyze(`
      query { posts { ...P } }
      fragment P on Post { title comments { body } }
    `)
    expect(analysis.query).toBe(
      normal(`
        query { posts { ...P id _version __typename } }
        fragment P on Post { title comments { body id _version __typename } id _version __typename }
      `),
    )
  })

  it('does not duplicate a field the query already selects', () => {
    expect(social.analyze('{ posts { id title } }').query).toBe(normal('{ posts { id title _version __typename } }'))
  })

  it('refuses an alias that hides a key', () => {
    expect(() => social.analyze('{ posts { id: title } }')).toThrow(GolemDocumentError)
  })

  it('leaves selections of non-model roots alone', () => {
    expect(social.analyze('{ aggregatePosts { _count } }').query).toBe(normal('{ aggregatePosts { _count } }'))
  })
})

describe('stream set', () => {
  it('opens the root stream with the list root filter and the selected scalars', () => {
    const analysis = social.analyze('query($w: PostWhereInput) { posts(where: $w, take: 5) { title views } }')
    expect(analysis.live).toBe(true)
    expect(analysis.streams).toHaveLength(1)
    const [stream] = analysis.streams
    expect(stream?.eventsRoot).toBe('postEvents')
    expect(stream?.scope).toBe('exact')
    expect(stream?.variableNames).toEqual(['w'])
    expect(stream?.query).toBe(
      normal(`subscription($w: PostWhereInput) { postEvents(where: $w) {
        eventID version type id entity { title views id _version __typename } } }`),
    )
  })

  it('translates a unique root into an equality filter on the key', () => {
    const [stream] = social.analyze('query($id: UUID!) { post(where: { ID: $id }) { title } }').streams
    expect(flat(stream?.query)).toContain('postEvents(where: {id: {equals: $id}})')
    expect(stream?.variableNames).toEqual(['id'])
  })

  it('translates a compound unique root', () => {
    const analyzer = new DocumentAnalyzer(socialWithPostTagEvents)
    const [stream] = analyzer.analyze(
      'query($p: UUID!, $t: String!) { postTag(where: { PostID_TagName: { postID: $p, tagName: $t } }) { tagName } }',
    ).streams
    expect(flat(stream?.query)).toContain('postTagEvents(where: {postID: {equals: $p}, tagName: {equals: $t}})')
    expect(stream?.variableNames).toEqual(['p', 't'])
  })

  it('lifts a to-many relation through the inverse into its own stream', () => {
    const analysis = social.analyze('query($id: UUID!) { post(where: { ID: $id }) { title comments { body } } }')
    expect(analysis.streams.map((stream) => stream.eventsRoot)).toEqual(['postEvents', 'commentEvents'])
    const comment = analysis.streams[1]
    expect(comment?.scope).toBe('exact')
    expect(comment?.query).toBe(
      normal(`subscription($id: UUID!) { commentEvents(where: {post: {is: {id: {equals: $id}}}}) {
        eventID version type id entity { body id _version __typename } } }`),
    )
  })

  it('keeps the relation field filter next to the lifted filter', () => {
    const comment = social.analyze(
      'query($id: UUID!) { post(where: { ID: $id }) { comments(where: { body: { contains: "x" } }) { body } } }',
    ).streams[1]
    expect(flat(comment?.query)).toContain(
      'commentEvents(where: {AND: [{body: {contains: "x"}}, {post: {is: {id: {equals: $id}}}}]})',
    )
  })

  it('lifts through a to-one relation whose inverse is a list with some', () => {
    const post = social.analyze('query($w: CommentWhereInput) { comments(where: $w) { post { title } } }').streams[1]
    expect(post?.eventsRoot).toBe('postEvents')
    expect(flat(post?.query)).toContain('postEvents(where: {comments: {some: $w}})')
  })

  it('lifts two levels by composing the scopes', () => {
    const replies = social.analyze(
      'query($id: UUID!) { post(where: { ID: $id }) { comments { replies { body } } } }',
    ).streams[2]
    expect(flat(replies?.query)).toContain(
      'commentEvents(where: {parent: {is: {post: {is: {id: {equals: $id}}}}}})',
    )
  })

  it('opens the policy-only slot stream for an opaque root', () => {
    const analysis = social.analyze('{ searchPosts(take: 3, where: { title: { contains: "a" } }) { title } }')
    expect(analysis.live).toBe(true)
    const [stream] = analysis.streams
    expect(stream?.patchable).toBe(false)
    expect(stream?.scope).toBe('policy-only')
    expect(stream?.query).toBe(
      normal(`subscription { postEvents { eventID version type id entity { title id _version __typename } } }`),
    )
    expect(stream?.variableNames).toEqual([])
  })

  it('opens the slot stream for a computed field that returns model rows', () => {
    const analyzer = new DocumentAnalyzer(teamsManifest)
    const analysis = analyzer.analyze('{ teams { name openInvites { email } } }')
    expect(analysis.streams.map((stream) => [stream.eventsRoot, stream.patchable, stream.scope])).toEqual([
      ['teamEvents', false, 'policy-only'],
      ['inviteEvents', false, 'policy-only'],
    ])
    const team = analysis.streams[0]
    expect(team?.query).not.toContain('openInvites')
  })

  it('broadens, never narrows, when the key cannot be read statically', () => {
    const eventful = new DocumentAnalyzer(socialWithEventfulUsers)
    const exact = eventful.analyze('query($u: UserWhereUniqueInput!) { user(where: $u) { handle } }')
    expect(exact.streams[0]?.scope).toBe('policy-only')
    const byHandle = eventful.analyze('{ user(where: { Handle: "bob" }) { handle } }')
    expect(byHandle.streams[0]?.scope).toBe('policy-only')
  })

  it('reads a variable under a single-row root as a unique input whatever its type is called', () => {
    const eventful = new DocumentAnalyzer(socialWithEventfulUsers)
    const analysis = eventful.analyze('query($u: UserKey!) { user(where: $u) { handle } }')
    expect(analysis.streams[0]?.scope).toBe('policy-only')
  })

  it('reads a variable under a list root as a filter whatever its type is called', () => {
    const analysis = social.analyze('query($w: PostFilter) { posts(where: $w) { title } }')
    expect(analysis.streams[0]?.scope).toBe('exact')
    expect(flat(analysis.streams[0]?.query)).toContain('where: $w')
  })

  it('gives one response key that names different fields a stream of its own', () => {
    const streams = social.analyze('{ titles: posts { label: title } bodies: posts { label: body } }').streams
    const entities = streams.map((stream) => flat(stream.query).split('entity {')[1] ?? '')
    expect(entities).toHaveLength(2)
    expect(entities.some((entity) => entity.includes('label: title') && !entity.includes('label: body'))).toBe(true)
    expect(entities.some((entity) => entity.includes('label: body') && !entity.includes('label: title'))).toBe(true)
  })

  it('merges the selections of repeated occurrences of one stream', () => {
    const analysis = social.analyze('{ a: posts { title } b: posts { body } }')
    expect(analysis.streams).toHaveLength(1)
    expect(flat(analysis.streams[0]?.query)).toContain('entity { title body id _version __typename }')
  })

  it('reads fragments spread under a row for the stream selection', () => {
    const analysis = social.analyze(`
      query { posts { ...P } }
      fragment P on Post { title views }
    `)
    expect(flat(analysis.streams[0]?.query)).toContain('entity { title views id _version __typename }')
  })

  it('hands a request with only the variables the stream uses', () => {
    const [, comment] = social.analyze('query($id: UUID!, $other: Int) { post(where: { ID: $id }) { comments { body } } }').streams
    expect(comment).toBeDefined()
    expect(streamRequest(comment!, { id: 'x', other: 3 })).toEqual({ query: comment?.query, variables: { id: 'x' } })
  })
})

describe('G7: a query is live, or it says it is not', () => {
  const notLive = (analyzer: DocumentAnalyzer, text: string): GolemNotLiveError => {
    const analysis = analyzer.analyze(text)
    expect(analysis.live).toBe(false)
    expect(analysis.streams).toEqual([])
    try {
      assertLive(analysis)
    } catch (error) {
      expect(error).toBeInstanceOf(GolemNotLiveError)
      return error as GolemNotLiveError
    }
    throw new Error('assertLive did not throw')
  }

  it('names a model without events', () => {
    const error = notLive(social, '{ postTags { tagName } }')
    expect([error.reason, error.subject]).toEqual(['model-without-events', 'PostTag'])
    expect(error.message).toContain('PostTag')
  })

  it('goes live once the model declares Subscriptions', () => {
    const analysis = new DocumentAnalyzer(socialWithPostTagEvents).analyze('{ postTags { tagName } }')
    expect(analysis.live).toBe(true)
    expect(analysis.streams.map((stream) => stream.eventsRoot)).toEqual(['postTagEvents'])
  })

  it('names the model reached through a relation', () => {
    const error = notLive(social, '{ posts { title author { handle } } }')
    expect([error.reason, error.subject]).toEqual(['model-without-events', 'User'])
  })

  it('names a custom root whose result holds no model rows', () => {
    const error = notLive(social, '{ publishPost }')
    expect([error.reason, error.subject]).toEqual(['opaque-root-without-models', 'publishPost'])
  })

  it('names a relation whose inverse is hidden', () => {
    const error = notLive(new DocumentAnalyzer(socialHiddenCommentInverse), '{ posts { comments { body } } }')
    expect([error.reason, error.subject]).toEqual(['hidden-inverse', 'Post.comments'])
  })

  it('names an aggregate root', () => {
    const error = notLive(social, '{ aggregatePosts { _count } }')
    expect([error.reason, error.subject]).toEqual(['non-row-root', 'aggregatePosts'])
  })

  it.each(['aggregate', 'groupBy', 'updateMany', 'deleteMany', 'value'] as const)('classifies a root by the manifest shape %s, whatever its name', (shape) => {
    const renamed = {
      ...socialManifest,
      roots: { ...socialManifest.roots, posts: { kind: 'generated' as const, models: ['Post'], shape, nullable: false } },
    }
    const error = notLive(new DocumentAnalyzer(renamed), '{ posts { title } }')
    expect([error.reason, error.subject]).toEqual(['non-row-root', 'posts'])
  })

  it('treats a root named like an aggregate as rows when the manifest says rows', () => {
    const renamed = {
      ...socialManifest,
      roots: { ...socialManifest.roots, aggregatePosts: { kind: 'generated' as const, models: ['Post'], shape: 'rows' as const, nullable: false } },
    }
    expect(new DocumentAnalyzer(renamed).analyze('{ aggregatePosts { title } }').live).toBe(true)
  })

  it('keeps every reason, not only the first', () => {
    const analysis = social.analyze('{ postTags { tagName } publishPost }')
    expect(analysis.notLive.map((error) => error.subject)).toEqual(['PostTag', 'publishPost'])
  })

  it('lets static data keep the injected document', () => {
    const analysis = social.analyze('{ postTags { tagName } }')
    expect(analysis.query).toBe(normal('{ postTags { tagName postID __typename } }'))
  })
})

describe('document handling', () => {
  it('caches the analysis per document node and per text', () => {
    const analyzer = new DocumentAnalyzer(socialManifest)
    const node = parse('{ posts { title } }')
    expect(analyzer.analyze(node)).toBe(analyzer.analyze(node))
    expect(analyzer.analyze('{ posts { title } }')).toBe(analyzer.analyze('{ posts { title } }'))
    expect(analyzer.analyze(parse('{ posts { title } }'))).not.toBe(analyzer.analyze(node))
  })

  it('refuses a document with more than one operation, naming the count', () => {
    const analyzer = new DocumentAnalyzer(socialManifest)
    const text = 'query A { posts { title } } query B { comments { body } }'
    const refusal = (() => {
      try {
        analyzer.analyze(text)
      } catch (error) {
        return error
      }
    })()
    expect(refusal).toBeInstanceOf(GolemMultipleOperationsError)
    expect(refusal).toBeInstanceOf(GolemDocumentError)
    expect((refusal as GolemMultipleOperationsError).operations).toBe(2)
    expect(() => analyzeMutation('mutation A { createTag(data: { name: "x" }) { id } } mutation B { createTag(data: { name: "y" }) { id } }', socialManifest)).toThrow(GolemMultipleOperationsError)
  })

  it('leaves no operation name option on the public entry points', () => {
    const client = new GolemClient({ queryClient: new QueryClient(), url: 'ws://unused/graphql', manifest: socialManifest })
    type Document = DocumentNode | string
    type Variables = Readonly<Record<string, unknown>>
    expectTypeOf(client.queryOptions).parameters.toEqualTypeOf<[Document, Variables?]>()
    expectTypeOf(client.infiniteQueryOptions).parameters.toEqualTypeOf<[Document, Variables?]>()
    expectTypeOf(client.staticQueryOptions).parameters.toEqualTypeOf<[Document, Variables?]>()
    expectTypeOf(client.mutate).parameters.toEqualTypeOf<[Document, Variables?, AbortSignal?]>()
    expectTypeOf(DocumentAnalyzer.prototype.analyze).parameters.toEqualTypeOf<[Document]>()
    client.dispose()
  })

  it('refuses a mutation, an unknown root and a missing fragment', () => {
    expect(() => social.analyze('mutation { createTag(data: { name: "x" }) { id } }')).toThrow(GolemDocumentError)
    expect(() => social.analyze('{ nonsense { id } }')).toThrow(GolemDocumentError)
    expect(() => social.analyze('{ posts { ...Missing } }')).toThrow(GolemDocumentError)
  })

  it('does not change the original document', () => {
    const node = parse('{ posts { title } }')
    const before = print(node)
    social.analyze(node)
    expect(print(node)).toBe(before)
  })
})

describe('patchable streams and nullable roots', () => {
  const patchable = (document: string, model = 'Post') => social.analyze(document).streams.find((stream) => stream.model === model)?.patchable

  it('patches a plain list, a where-scoped list and a single row', () => {
    expect(patchable('{ posts { title } }')).toBe(true)
    expect(patchable('{ posts(where: { published: { equals: true } }) { title } }')).toBe(true)
    expect(patchable('{ post(where: { ID: "a" }) { title } }')).toBe(true)
  })

  it.each([
    ['orderBy', '{ posts(orderBy: { priority: asc }) { title } }'],
    ['distinct', '{ posts(distinct: [published]) { title published } }'],
    ['take', '{ posts(take: 2) { title } }'],
    ['skip', '{ posts(skip: 1) { title } }'],
    ['cursor', '{ posts(cursor: { ID: "a" }) { title } }'],
    ['a custom root', '{ searchPosts(take: 5, where: {}) { title } }'],
  ])('refetches a stream under %s', (_name, document) => {
    expect(patchable(document)).toBe(false)
  })

  it('refetches the stream of a row that selects a relation, and of the relation itself', () => {
    const analysis = social.analyze('{ comments { body post { title } } }')
    expect(analysis.streams.map((stream) => [stream.model, stream.patchable])).toEqual([
      ['Comment', false],
      ['Post', false],
    ])
  })

  it('refetches a shared stream when any occurrence of it is not plain', () => {
    expect(patchable('{ posts { title } searchPosts(take: 5, where: {}) { title } }')).toBe(false)
  })

  it('reports the response keys of generated single roots as nullable, aliases included', () => {
    const analysis = social.analyze('query($id: UUID!) { first: post(where: { ID: $id }) { title } posts { title } searchPosts(take: 1, where: {}) { title } }')
    expect([...analysis.nullableRoots]).toEqual(['first'])
  })
})

describe('nullable roots from the manifest', () => {
  const custom = (nullable: boolean) =>
    new DocumentAnalyzer(socialWithRoot('featuredPost', { kind: 'opaque', models: ['Post'], shape: 'row', nullable }))

  it('reports a custom single root only when the manifest marks it nullable', () => {
    expect([...custom(true).analyze('{ featuredPost { title } }').nullableRoots]).toEqual(['featuredPost'])
    expect([...custom(false).analyze('{ featuredPost { title } }').nullableRoots]).toEqual([])
  })

  it('does not report a generated single root the manifest marks non-nullable', () => {
    const strict = new DocumentAnalyzer(socialPostNotNullable)
    expect([...strict.analyze('query($id: UUID!) { post(where: { ID: $id }) { title } }').nullableRoots]).toEqual([])
  })
})

describe('composite keys', () => {
  it('selects the identity object of a composite-key model in its stream', () => {
    const analysis = new DocumentAnalyzer(socialWithPostTagEvents).analyze('{ postTags { tagName } }')
    expect(flat(analysis.streams[0]?.query)).toContain('id { postID tagName }')
  })

  it('keeps a bare id for a single-key model', () => {
    expect(flat(social.analyze('{ posts { title } }').streams[0]?.query)).not.toContain('id {')
  })
})

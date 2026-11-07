# Using golem from React

You have a golem server and want a React front end that stays live: a list
that gains, changes and loses rows as other people write, with no polling and
no cache code of your own.

This page walks through it with the social example
(`go/examples/social`): `Post` and `Comment` have events, `User` does not.
Every name below exists in that example's generated schema.

## 1. Give the model events

A model is live when it declares `golem.Subscriptions`:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.Subscriptions[Post](),
	)
}
```

Then regenerate:

```sh
golem generate --schema ./social --app-out ./social
```

`generate` writes `zz_golem_client.gen.ts` beside the SDL. It is the
**client manifest**: the facts about your API that GraphQL does not carry. Copy
or import it into your front end, and regenerate it whenever the schema
changes. Never edit it. See
[The client manifest](./GUIDE.md#the-client-manifest) and
[Subscriptions and events](./GUIDE.md#subscriptions-and-events).

Every model a live query reaches needs `Subscriptions`. A query over a model
without events is not live; use a static query for it (step 7).

## 2. Install

```sh
npm i @eleven-am/golem-client @eleven-am/golem-react @tanstack/react-query
```

The client also needs its peers: `graphql` and `graphql-ws`, plus `react`,
and `gql.tada` for typed documents. The
[client README](../../client/README.md#install) lists the versions.

## 3. Create the clients and the provider

One `QueryClient`, one `GolemClient` on it, one `GolemProvider`:

```tsx
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { GolemClient } from '@eleven-am/golem-client'
import { GolemProvider } from '@eleven-am/golem-react'
import { golemClientManifest } from './zz_golem_client.gen'

export const queryClient = new QueryClient()

export const golem = new GolemClient({
  queryClient,
  url: 'https://api.example.com/graphql',
  manifest: golemClientManifest,
  credentials: () => ({
    headers: { authorization: `Bearer ${session.token()}` },
    connectionParams: { token: session.token() },
  }),
})

export function Root() {
  return (
    <QueryClientProvider client={queryClient}>
      <GolemProvider client={golem}>
        <App />
      </GolemProvider>
    </QueryClientProvider>
  )
}
```

- `url` is your GraphQL endpoint. Queries and mutations go to it over HTTP.
  Live streams use a WebSocket to the same URL.
- `credentials` is called every time the client sends a request or opens a
  socket, so return the current token each time. The server closes a live
  socket at the principal's deadline and the client reconnects with fresh
  credentials. See [The socket deadline](./GUIDE.md#the-socket-deadline).
- `GolemProvider` must sit inside the `QueryClientProvider` of
  `golem.queryClient`, or it throws `GolemConfigurationError`. It renders its
  children once live sync is running.

## 4. A live list

```tsx
import { useGolemQuery, useGolemStatus } from '@eleven-am/golem-react'

const PUBLISHED_POSTS = `
  query PublishedPosts {
    posts(where: { published: { equals: true } }) {
      id
      title
    }
  }
`

type PublishedPosts = { posts: Array<{ id: string; title: string }> }

export function PublishedList() {
  const posts = useGolemQuery(PUBLISHED_POSTS)
  const status = useGolemStatus(PUBLISHED_POSTS)
  const rows = (posts.data as PublishedPosts | undefined)?.posts ?? []
  return (
    <>
      <small>{status?.state}</small>
      <ul>
        {rows.map((post) => (
          <li key={post.id}>{post.title}</li>
        ))}
      </ul>
    </>
  )
}
```

This is a plain GraphQL query. The client opens a subscription for the
`published: true` filter and keeps the TanStack cache in step: a new published
post appears, a retitled one updates, an unpublished one leaves. Your
component never writes to the cache.

- The server judges each event against the caller's policy and the query's
  filter. A row that stops matching arrives as an exited event, see
  [Rows that leave a list](./GUIDE.md#rows-that-leave-a-list).
- The client adds `_version` and `__typename` to the selection, and the
  primary key (`id`) if you left it out. Row versions tell it which of two
  copies of a row is newer. See [Row versions](./GUIDE.md#row-versions).
- `useGolemStatus` returns the state of this query: `CONNECTING`, `LIVE`,
  `RECOVERING`, `LIMIT`, `FORBIDDEN`, `NOT_FOUND` or `FAILED`. It is
  `undefined` before the query exists. It takes the same document and
  variables as the query.
- With a plain string the hook's `data` is `unknown`, so the example gives it a
  type. A gql.tada typed document gives the result and variable types for you.

How the client picks streams and when it patches or refetches is in
[How live queries work](../../client/README.md#how-live-queries-work).

## 5. A mutation

```tsx
import { useGolemMutation } from '@eleven-am/golem-react'

const RETITLE = `
  mutation Retitle($id: UUID!, $title: String!) {
    updatePost(where: { ID: $id }, data: { title: { set: $title } }) {
      id
      title
    }
  }
`

export function Retitle({ id }: { id: string }) {
  const retitle = useGolemMutation(RETITLE)
  return (
    <button disabled={retitle.isPending} onClick={() => retitle.mutate({ id, title: 'New title' })}>
      Retitle
    </button>
  )
}
```

The mutation is a normal HTTP request. The client never writes its result into
the cache. It invalidates the cached queries over the models the mutation
touches, and the live event or the refetch brings every list up to date, in
this tab and in every other one.

A mutation document holds exactly one operation.

## 6. Infinite scroll

```tsx
import { useGolemInfiniteQuery } from '@eleven-am/golem-react'

const FEED = `
  query Feed($take: Int = 20) {
    posts(take: $take, orderBy: [{ createdAt: desc }]) {
      id
      title
    }
  }
`

type Feed = { posts: Array<{ id: string; title: string }> }

export function FeedList() {
  const feed = useGolemInfiniteQuery(FEED)
  const rows = feed.data?.pages.flatMap((page) => (page as Feed).posts) ?? []
  return (
    <>
      <ul>
        {rows.map((post) => (
          <li key={post.id}>{post.title}</li>
        ))}
      </ul>
      {feed.hasNextPage && <button onClick={() => feed.fetchNextPage()}>More</button>}
    </>
  )
}
```

- Give `take` a variable with a default (`$take: Int = 20`), or a literal
  integer. The default is the page size the client uses.
- Do not write `cursor` or `skip`. The client builds both from the last row of
  the previous page, using the manifest. A document that sets them is refused
  with `GolemDocumentError`.
- `hasNextPage` is true while the last page came back full.
- Pages stay live. For the status of an infinite query, pass
  `{ infinite: true }`: `useGolemStatus(FEED, {}, { infinite: true })`.
- `take` above the server's page limit is a typed error, not clamped. See
  [Caps](./GUIDE.md#caps).

## 7. Counts and aggregates

Aggregates, group-by and relation group-by are live through `useGolemQuery`.
They hold no rows, so the client never patches them: any event on a model the
result comes from refetches the whole document, through the same coalescer as
every other refetch. An aggregate's `where` scopes its stream to the rows the
count covers, and the server's policy filter still decides which events arrive.

```tsx
import { useGolemQuery } from '@eleven-am/golem-react'

const PUBLISHED_COUNT = `
  query PublishedCount {
    aggregatePosts(where: { published: { equals: true } }) {
      count
    }
  }
`

export function PublishedCount() {
  const result = useGolemQuery(PUBLISHED_COUNT)
  const count = (result.data as { aggregatePosts: { count: string | number } } | undefined)?.aggregatePosts.count
  return <span>{String(count)}</span>
}
```

Put counts in their own query. Every event on a derived stream refetches the
whole document, so a count beside a long list refetches the list too.

Custom query roots whose result holds model rows behave the same way: they
refetch on any event and are never patched. The many-row mutations
(`updateMany`, `deleteMany`) are mutations, not queries, so no hook applies.

`useGolemStaticQuery` stays available when you want a count fetched once and
never refreshed. A document is still not live, and `useGolemQuery` throws
`GolemNotLiveError` naming the root and the reason, when a model it reaches has
no events or a custom root's result holds no model rows.

## 8. What the server must be

- **golem go/v0.8.3 or later.** The generated manifest from that release
  carries `nullable` on every root and relation and the `deletedRow` root
  shape. The client reads both and does not guess.
- **golem go/v0.8.3 or later for relation group-by.** From that release the
  manifest lists every model a `relationGroupBy` root groups by, so renaming a
  related row refreshes the groups. With a go/v0.8.2 manifest the root
  refetches only on events of its own model.
- **Regenerate the manifest after every schema change.** The manifest holds the
  contract fingerprint, and every live request carries it. A client built from
  an older manifest is refused with `GOLEM_CONTRACT_MISMATCH`, which surfaces
  as `GolemContractMismatchError`. `golem check` fails when the generated
  manifest falls behind the schema, so run it in CI. See
  [The live handshake and the subscription ack](./GUIDE.md#the-live-handshake-and-the-subscription-ack).
- **A socket deadline for every principal.** A principal with no deadline is
  refused with `GOLEM_PRINCIPAL_DEADLINE_REQUIRED` (`GolemDeadlineRequiredError`).
  That is a server fix: see [The socket deadline](./GUIDE.md#the-socket-deadline).
- **`golem.Subscriptions` on every model a live query reaches.**

The full error list is in the
[client README](../../client/README.md#errors).

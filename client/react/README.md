# @eleven-am/golem-react

Live React queries for golem servers. You write an ordinary GraphQL query; the hooks open the matching subscriptions behind it, and creates, updates, deletes and rows leaving a result patch the TanStack Query cache by themselves. Your components never write to the cache.

Built on `@eleven-am/golem-client` and `@tanstack/react-query`.

## Install

```sh
npm install @eleven-am/golem-react @eleven-am/golem-client @tanstack/react-query react gql.tada graphql graphql-ws
```

Peer dependencies: `@eleven-am/golem-client` ^0.2.1, `@tanstack/react-query` ^5, `react` >=18, `gql.tada` ^1.8. The client in turn needs `graphql` ^16 and `graphql-ws` ^6 (and `@tanstack/query-core` ^5, which `@tanstack/react-query` brings).

## Setup

The client lives on your application's `QueryClient`. It does not make a second one.

The manifest is `zz_golem_client.gen.ts`, written by `golem generate` beside your GraphQL schema on the server (go/v0.8.3 or later). Copy or import it into your front end, and regenerate it whenever the schema changes. Never edit it.

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

- `credentials` is called again whenever a request or a connection is made, including after the server closes a socket at its credential deadline, so return the current token each time.
- `GolemProvider` throws `GolemConfigurationError` when the surrounding `QueryClientProvider` does not hold `golem.queryClient`. It attaches live sync with `useSyncExternalStore` (no effects) and renders its children once live sync is running, so no query is fetched before its subscriptions are open. It renders nothing on the server.
- A second `GolemClient` on a `QueryClient` that already has one throws `GolemConfigurationError`. Call `golem.dispose()` to release it.

## Hooks

Every hook takes a GraphQL document, as a string or as a gql.tada typed document (the result and variable types come from the document; a plain string gives `unknown` data). The examples use the social example schema.

### useGolemQuery

`useQuery` over `client.queryOptions`. The query is live: its streams are open while the component is mounted.

```tsx
const POSTS = `
  query PublishedPosts {
    posts(where: { published: { equals: true } }) { id title }
  }
`

function Posts() {
  const posts = useGolemQuery(POSTS)
  const rows = (posts.data as { posts: Array<{ id: string; title: string }> } | undefined)?.posts ?? []
  return <ul>{rows.map((post) => <li key={post.id}>{post.title}</li>)}</ul>
}
```

Aggregates, group-by, relation group-by and custom roots that return model rows are live: any event on a model they come from refetches the document through the coalescer, and they are never patched. Put counts in their own query. It throws `GolemNotLiveError` (naming the reason and the root) for a document that reaches a model without events or a custom root whose result holds no model rows. Use `useGolemStaticQuery` for those. Relation group-by needs a go/v0.8.3 manifest to refetch on events of the models it groups by.

### useGolemStatus

The live status of a query, or `undefined` before the query exists. It takes the same document and variables as the query; pass `{ infinite: true }` as the third argument for an infinite query. It is per query, not global.

```tsx
const status = useGolemStatus(POSTS)
return <small>{status?.state}</small>
```

### useGolemConnection

The connection state of the whole client, as one of the `transport.status` values: `idle` before any live query has opened a connection, then `connecting`, `open`, `reconnecting` or one of the fatal states. It is global, unlike `useGolemStatus`.

### useGolemMutation

`useMutation` over `client.mutate`. A committed mutation invalidates every cached query over the models it touches, so the live event or the refetch brings it up to date. The mutation result is never written into the cache. A document holds exactly one operation.

```tsx
const RETITLE = `
  mutation Retitle($id: UUID!, $title: String!) {
    updatePost(where: { ID: $id }, data: { title: { set: $title } }) { id title }
  }
`

const retitle = useGolemMutation(RETITLE)
retitle.mutate({ id, title: 'New title' })
```

### useGolemInfiniteQuery

`useInfiniteQuery` over a list query with a `take`. Pages stay live. Give `take` a variable with a default, or a literal; never write `cursor` or `skip`, the client adds them from the manifest. `hasNextPage` is true while the last page is full.

```tsx
const FEED = `
  query Feed($take: Int = 20) {
    posts(take: $take, orderBy: [{ createdAt: desc }]) { id title }
  }
`

const feed = useGolemInfiniteQuery(FEED)
const pages = feed.data?.pages ?? []
// feed.hasNextPage, feed.fetchNextPage()
```

### useGolemStaticQuery

`useQuery` for a document you want fetched once and never refreshed, or one that cannot be live (a model without events, a custom root with no model rows). Counts and aggregates are live through `useGolemQuery`.

```tsx
const COUNT = `
  query PublishedCount {
    aggregatePosts(where: { published: { equals: true } }) { count }
  }
`

const total = useGolemStaticQuery(COUNT)
const liveTotal = useGolemQuery(COUNT)
```

## Live status

`useGolemStatus` reads the state of one query:

| State | Meaning |
|---|---|
| `CONNECTING` | Subscriptions are being opened; the first fetch waits for their acks. |
| `LIVE` | Every stream is acknowledged; the cache follows the server. |
| `RECOVERING` | A stream or the socket was lost; it is reopening and the query will refetch once after the acks. |
| `LIMIT` | The connection's subscription cap was reached. The data is still fetched, but not live until a slot frees. |
| `FORBIDDEN` | The server stopped a stream because the principal lost access. The query refetches once and does not resubscribe. |
| `NOT_FOUND` | A row held in a single result slot left the result; the query refetches. |
| `FAILED` | A stream failed for good (`error` holds the cause). |

TanStack's own `isFetching`, `isError` and `data` keep their usual meaning.

## How live queries work

#### Connections

A `GolemClient` opens one WebSocket (graphql-ws), and only when the first stream needs it. The socket is lazy and reconnects forever, with backoff, except when the server refuses the handshake for good (protocol or contract mismatch, a missing deadline). Queries, refetches and mutations are plain HTTP `fetch` requests.

#### Subscriptions

The client reads your query document and opens one subscription per (model, filter) pair in it.

- A literal or variable `where` gives an `exact`-scoped stream: the server sends only events for rows that match.
- No `where` gives a `policy-only` stream: the server sends every event the caller's policy allows for that model.
- A nested relation is a pair of its own. When the parent has a `where` and the manifest knows the relation's inverse, the child stream is scoped by that parent filter. Otherwise it is `policy-only` unless the relation has its own `where`.
- Two selections with the same model and filter share one stream. Different filters do not. (Two selections that select the same response key differently get separate streams.)
- Components that run the same query with the same variables share one TanStack query, and so one set of streams.

| Document | Streams |
|---|---|
| `posts(where: { published: { equals: true } }) { id title }` | 1 (`Post`, exact) |
| `posts { id comments { id } }` | 2 (`Post` and `Comment`, both policy-only) |
| Two lists, both `posts(where: { published: { equals: true } })` | 1 |
| `posts(where: { published: { equals: true } })` and `posts(where: { published: { equals: false } })` | 2 |

A model without events (no `golem.Subscriptions`) makes the document not live: `useGolemQuery` throws `GolemNotLiveError`.

#### Filtering is automatic

You never filter events in the client. The server judges each event against the caller's policy and the stream's filter, and sends one of four event types: `CREATED` and `UPDATED` for a row that matches, `EXITED` for a row that stops matching or that the caller may no longer read, and `DELETED` for a removed row. A row that starts to match arrives as `CREATED` or `UPDATED`. See [Rows that leave a list](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#rows-that-leave-a-list).

#### Patch or refetch

- `UPDATED` is patched into the cache in place only when the stream's selection is plain: a top-level list or single-row root, arguments limited to `where` (or the unique selector of a single-row root), no `orderBy`, `distinct`, `take`, `skip` or cursor, and scalar fields only. Every other `UPDATED` refetches the query.
- `CREATED` refetches.
- `DELETED` and `EXITED` remove the held row from the cache at once. The query then refetches, because a removal changes a list or empties a slot.
- A response that carries an older row version than the cache already holds is discarded, and the query refetches.
- A mutation result is never written into the cache. A committed mutation invalidates the cached queries over the models it touches (TanStack marks them stale and refetches the ones in use).
- Refetches that arrive close together are coalesced into one.

#### Row versions

The client adds `_version` and `__typename` to every selection of a model with events, and the primary key fields (`id` for most models) when you left them out. It compares `_version` to decide which copy of a row is newer. You can select them yourself, but not alias another field to those names. See [Row versions](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#row-versions).

#### Status is per query

`useGolemStatus` reports one query, keyed by its document and variables. It is not a global connection state. For that, read the transport:

- `golem.transport.status`: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`.
- `golem.transport.onStatus(listener)`: calls you on every change and returns an unsubscribe function.

`useGolemConnection()` wraps both and returns the transport status. It re-renders on every change.

```tsx
const connection = useGolemConnection()
return connection === 'open' ? null : <small>{connection}</small>
```

#### Why there is a manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator (`golem generate` writes `zz_golem_client.gen.ts`), and `golem check` fails when it falls behind the schema. The client reads it and never guesses. See [The client manifest](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-client-manifest).

#### Server concepts

- [Subscriptions and events](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#subscriptions-and-events)
- [Row versions](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#row-versions)
- [Rows that leave a list](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#rows-that-leave-a-list)
- [The live handshake and the subscription ack](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-live-handshake-and-the-subscription-ack)
- [The socket deadline](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-socket-deadline)

For a walkthrough from a model to a live component, see [Using golem from React](https://github.com/eleven-am/golem/blob/main/docs/golem-go/REACT.md).

## Mutations on page unload

Browsers cancel ordinary fetches while a page unloads. Pass `keepalive: true` to `useGolemMutation` and that mutation's requests are sent with `fetch(..., { keepalive: true })`.

```tsx
const endSession = useGolemMutation(END_SESSION, { keepalive: true })

addEventListener('pagehide', () => endSession.mutate({ id: sessionId }))
```

The response may never be processed, so treat the mutation as possibly committed; the next page load refetches the authoritative state. Browsers cap keepalive request bodies at 64 KiB in total, so a larger body throws `GolemKeepaliveTooLargeError` before anything is sent.

## Errors

All server errors are `GolemError` (`code`, `path`, `extensions`, `retryable`) or a subclass:

- `GolemProtocolMismatchError`, `GolemContractMismatchError`: the client and server disagree on the live protocol or the contract. Regenerate the manifest.
- `GolemDeadlineRequiredError` (`GOLEM_PRINCIPAL_DEADLINE_REQUIRED`): the server refused a live socket because the principal has no deadline. This is a server fix.
- `GolemUnauthenticatedError`, `GolemHttpError`.
- `GolemPartialCommitError`, `GolemCommitUnknownError`: a mutation whose commit state is partial or unknown. Affected live queries are invalidated for you, as they are after every committed mutation.

Client-side failures: `GolemConfigurationError` (bad setup), `GolemDocumentError` (a document the client cannot run, for example one that sets `cursor` or `skip` itself) and its subclass `GolemMultipleOperationsError` (a document with more than one operation), `GolemNotLiveError`, `GolemNotFoundError`, `GolemProtocolError`.

## Requirements

- golem **go/v0.8.3 or later** on the server, with `golem.Subscriptions` on every model a live query reaches.
- `react` >=18, `@tanstack/react-query` 5.
- A socket deadline for every principal, and a manifest regenerated after every schema change. See [The socket deadline](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-socket-deadline) and [The client manifest](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-client-manifest).

## Links

- [Using golem from React](https://github.com/eleven-am/golem/blob/main/docs/golem-go/REACT.md): a walkthrough from a model to a live component.
- [golem Go guide](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md): models, policies, events, the manifest and the socket deadline.
- [Client workspace overview](https://github.com/eleven-am/golem/blob/main/client/README.md)

License: GPL-3.0.

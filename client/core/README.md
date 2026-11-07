# @eleven-am/golem-client

Live GraphQL for golem servers, without a UI framework: `GolemClient`, the live protocol, the HTTP and WebSocket transport, the TanStack Query cache patcher and typed errors. Works with TanStack Query core, so any framework adapter can sit on top. For React use [`@eleven-am/golem-react`](https://www.npmjs.com/package/@eleven-am/golem-react), which wraps this package in hooks.

## Install

```sh
npm install @eleven-am/golem-client @tanstack/query-core graphql graphql-ws
```

Peer dependencies: `@tanstack/query-core` ^5, `graphql` ^16, `graphql-ws` ^6.

## GolemClient

```ts
import { QueryClient } from '@tanstack/query-core'
import { GolemClient } from '@eleven-am/golem-client'
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
```

| Option | Meaning |
|---|---|
| `queryClient` | Required. Your application's TanStack `QueryClient`. A second `GolemClient` on a `QueryClient` that already has one throws `GolemConfigurationError`. |
| `url` | The GraphQL endpoint. HTTP requests go to it; the WebSocket connects to the same URL. |
| `manifest` | The generated `zz_golem_client.gen.ts` of your golem server (go/v0.8.3 or later). |
| `credentials` | Optional function returning `{ headers, connectionParams }`. Called again for every request and every connection, including after the server closes a socket at its credential deadline, so return the current token each time. |
| `createSocket` | Optional `(url, protocols, credentials) => socket` factory, for a WebSocket implementation of your own (for example in Node). |
| `fetch` | Optional `fetch` implementation for HTTP requests. Defaults to `globalThis.fetch`. |

## Without React

```ts
import { QueryObserver } from '@tanstack/query-core'

const POSTS = `
  query PublishedPosts {
    posts(where: { published: { equals: true } }) { id title }
  }
`

const stop = golem.live.start()
const observer = new QueryObserver(queryClient, golem.queryOptions(POSTS))
const unsubscribe = observer.subscribe((result) => render(result.data))
```

- `golem.live.start()` starts live sync and returns a function that stops it. Start it before you observe a query, so no query is fetched before its subscriptions are open.
- `golem.queryOptions(document, variables?)` returns TanStack options for a live query. It throws `GolemNotLiveError` for a document that cannot be live.
- `golem.infiniteQueryOptions(document, variables?)` returns options for `InfiniteQueryObserver` over a list query with a `take`. Give `take` a variable with a default, or a literal; never write `cursor` or `skip`.
- `golem.staticQueryOptions(document, variables?)` returns options for a document fetched once and never refreshed, including one that cannot be live (a model without events, a custom root with no model rows). Counts, aggregates, group-by and custom roots with model rows are live through `golem.queryOptions`: any event on a model they come from refetches the document through the coalescer, and they are never patched.
- `golem.mutate(document, variables?)` runs a mutation over HTTP and returns its data. A document holds exactly one operation; one with several is refused with `GolemMultipleOperationsError`.

```ts
const data = await golem.mutate(
  `mutation Retitle($id: UUID!, $title: String!) {
    updatePost(where: { ID: $id }, data: { title: { set: $title } }) { id title }
  }`,
  { id, title: 'New title' },
)
```

## Transport status and dispose

`golem.transport.status` is the state of the WebSocket: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`. `golem.transport.onStatus(listener)` calls you on every change and returns an unsubscribe function.

```ts
const off = golem.transport.onStatus((status) => console.log(status))
```

The status of one query (below) is separate from this connection state.

`golem.dispose()` stops live sync, closes the transport and releases the `QueryClient` for another `GolemClient`.

## Live status

`golem.live.statusOf(query)` returns `{ state, error }` for a TanStack `Query`, or `undefined` before live sync tracks it. `golem.live.subscribeStatus(listener)` calls you on every change.

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

## The manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator: `golem generate` writes `zz_golem_client.gen.ts` beside your schema, and `golem check` fails when it falls behind. The client reads it and never guesses. Regenerate it after every schema change; never edit it. See [The client manifest](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-client-manifest).

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

`golem.live.statusOf` reports one query, keyed by its document and variables. It is not a global connection state. For that, read the transport:

- `golem.transport.status`: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`.
- `golem.transport.onStatus(listener)`: calls you on every change and returns an unsubscribe function.

Wrap them in your framework's external-store primitive. In React that is `useSyncExternalStore`:

```tsx
import { useSyncExternalStore } from 'react'

function useConnection() {
  return useSyncExternalStore(
    (notify) => golem.transport.onStatus(notify),
    () => golem.transport.status,
  )
}
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

Browsers cancel ordinary fetches while a page unloads. To send a last mutation on `pagehide`, pass `keepalive`; only that request is sent with `fetch(..., { keepalive: true })`.

```ts
addEventListener('pagehide', () => {
  void golem.mutate(END_SESSION, { id: sessionId }, { keepalive: true }).catch(() => {})
})
```

The third argument is either an `AbortSignal` or `{ signal?, keepalive? }`. The response may never be processed, so treat the mutation as possibly committed; the next page load refetches the authoritative state. Browsers cap keepalive request bodies at 64 KiB in total, so a larger body throws `GolemKeepaliveTooLargeError` before anything is sent.

## Errors

All server errors are `GolemError` (`code`, `path`, `extensions`, `retryable`) or a subclass:

- `GolemProtocolMismatchError`, `GolemContractMismatchError`: the client and server disagree on the live protocol or the contract. Regenerate the manifest.
- `GolemDeadlineRequiredError` (`GOLEM_PRINCIPAL_DEADLINE_REQUIRED`): the server refused a live socket because the principal has no deadline. This is a server fix.
- `GolemUnauthenticatedError`, `GolemHttpError`.
- `GolemPartialCommitError`, `GolemCommitUnknownError`: a mutation whose commit state is partial or unknown. Affected live queries are invalidated for you, as they are after every committed mutation.

Client-side failures: `GolemConfigurationError` (bad setup), `GolemDocumentError` (a document the client cannot run, for example one that sets `cursor` or `skip` itself) and its subclass `GolemMultipleOperationsError` (a document with more than one operation), `GolemNotLiveError`, `GolemNotFoundError`, `GolemProtocolError`.

## Requirements

- golem **go/v0.8.3 or later** on the server, with `golem.Subscriptions` on every model a live query reaches.
- `@tanstack/query-core` 5.
- A socket deadline for every principal. See [The socket deadline](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-socket-deadline).

## React

Use [`@eleven-am/golem-react`](https://www.npmjs.com/package/@eleven-am/golem-react) for hooks and a provider on top of this client.

## Links

- [Using golem from React](https://github.com/eleven-am/golem/blob/main/docs/golem-go/REACT.md): a walkthrough from a model to a live component.
- [golem Go guide](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md): models, policies, events, the manifest and the socket deadline.
- [Client workspace overview](https://github.com/eleven-am/golem/blob/main/client/README.md)

License: GPL-3.0.

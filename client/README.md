# golem client

Live GraphQL for golem backends in TypeScript. Two packages:

| Package | What it is |
|---|---|
| [`@eleven-am/golem-client`](./core/README.md) | The framework-free core: `GolemClient`, the live protocol, the transport, the cache patcher and typed errors. Works with TanStack Query core. |
| [`@eleven-am/golem-react`](./react/README.md) | React bindings: `GolemProvider` and the hooks, on top of `@tanstack/react-query`. |

A golem live query is an ordinary TanStack Query. You write a plain GraphQL query, the client opens the matching subscriptions behind it, and creates, updates, deletes and rows leaving a result patch the TanStack cache by themselves. Your components never write to the cache.

When the client patches and when it refetches:

- An UPDATED event is patched in place only when the stream's selection is plain: a top-level list or single-row root, arguments limited to `where`, no `orderBy`, `distinct`, `take`, `skip` or cursor, and only scalar fields selected, with no relation or computed fields at any depth.
- Every other UPDATED event refetches the query: ordered, windowed or distinct lists, custom roots, and any selection that reaches a relation.
- CREATED events refetch the query.
- DELETED and EXITED events remove the row from the cache at once, and the query refetches when the removal changes a list or empties a single slot.

License: GPL-3.0.

## Install

```sh
npm install @eleven-am/golem-client @tanstack/query-core graphql graphql-ws
npm install @eleven-am/golem-react @tanstack/react-query react gql.tada
```

Peers: `@tanstack/query-core` ^5 (core), `@tanstack/react-query` ^5 and `react` >=18 (React), `graphql` ^16, `graphql-ws` ^6 (core), and `gql.tada` ^1.8 (React, for the typed document types).

## Setup

The client lives on your application's `QueryClient`. It does not make a second one.

```ts
import { QueryClient } from '@tanstack/react-query'
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

- `manifest` is the generated `zz_golem_client.gen.ts` of your golem server (go/v0.8.3 or later). It carries the contract fingerprint and, per model and root, the primary key, cursor selector, events, relations (with `nullable`), and each root's shape.
- `credentials` is called again whenever a connection is made, including after the server closes a socket at its credential deadline, so return the current token each time.
- `queryClient` is required. A second `GolemClient` on a `QueryClient` that already has one throws `GolemConfigurationError`. Call `golem.dispose()` to release it.

### React

```tsx
import { QueryClientProvider } from '@tanstack/react-query'
import { GolemProvider } from '@eleven-am/golem-react'

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

`GolemProvider` throws `GolemConfigurationError` when the surrounding `QueryClientProvider` does not hold `golem.queryClient`. It attaches live sync with `useSyncExternalStore` (no effects) and renders its children once live sync is running, so no query is ever fetched before its subscriptions are open. It renders nothing on the server.

### Without React

```ts
const stop = golem.live.start()
const observer = new QueryObserver(queryClient, golem.queryOptions(POSTS, { tag: 'news' }))
observer.subscribe((result) => render(result.data))
```

`client.live.start()` needs no React. `golem.queryOptions`, `golem.infiniteQueryOptions` and `golem.staticQueryOptions` return plain TanStack options; `golem.mutate(document, variables)` runs a mutation. A document holds exactly one operation; a document with several is refused with `GolemMultipleOperationsError`, so split it into one document per operation.

## Hooks

Every hook takes a GraphQL document, as a string or as a gql.tada typed document (the result and variable types come from the document).

| Hook | Does |
|---|---|
| `useGolemQuery(doc, variables?, options?)` | `useQuery` over `client.queryOptions`. The query is live: its streams are open while the component is mounted, and the cache follows the server. |
| `useGolemInfiniteQuery(doc, variables?, options?)` | `useInfiniteQuery` over a list query with a `take`. Pages stay live. You never write `cursor` or `skip`; the client adds them from the manifest. `hasNextPage` is true while the last page is full. |
| `useGolemMutation(doc, options?)` | `useMutation` over `client.mutate`. A committed mutation marks every cached query over the models it touches as stale, so the live event or the refetch brings it up to date; the mutation result is never written into the cache. |
| `useGolemStatus(doc, variables?, { infinite? })` | The live status of a query (below), or `undefined` before the query exists. |
| `useGolemStaticQuery(doc, variables?, options?)` | `useQuery` for a document you want fetched once and never refreshed. Counts and aggregates are live through `useGolemQuery`. |

```tsx
function Posts({ tag }: { tag: string }) {
  const posts = useGolemQuery(POSTS, { tag })
  const status = useGolemStatus(POSTS, { tag })
  return (
    <>
      <small>{status?.state}</small>
      <ul>{posts.data?.posts.map((post) => <li key={post.id}>{post.title}</li>)}</ul>
    </>
  )
}
```

Aggregates, group-by, relation group-by and custom roots that return model rows are live: any event on a model they come from refetches the document through the coalescer, and they are never patched. Put counts in their own query. `useGolemQuery` throws `GolemNotLiveError` (naming the reason and the root) for a document that reaches a model without events or a custom root whose result holds no model rows. Use `useGolemStaticQuery` for those. Relation group-by needs a go/v0.8.3 manifest to refetch on events of the models it groups by.

## Live status

`useGolemStatus` reads `client.live.statusOf(query)`:

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

### Connections

A `GolemClient` opens one WebSocket (graphql-ws), and only when the first stream needs it. The socket is lazy and reconnects forever, with backoff, except when the server refuses the handshake for good (protocol or contract mismatch, a missing deadline). Queries, refetches and mutations are plain HTTP `fetch` requests.

### Subscriptions

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

### Filtering is automatic

You never filter events in the client. The server judges each event against the caller's policy and the stream's filter, and sends one of four event types: `CREATED` and `UPDATED` for a row that matches, `EXITED` for a row that stops matching or that the caller may no longer read, and `DELETED` for a removed row. A row that starts to match arrives as `CREATED` or `UPDATED`. See [Rows that leave a list](../docs/golem-go/GUIDE.md#rows-that-leave-a-list).

### Patch or refetch

- `UPDATED` is patched into the cache in place only when the stream's selection is plain: a top-level list or single-row root, arguments limited to `where` (or the unique selector of a single-row root), no `orderBy`, `distinct`, `take`, `skip` or cursor, and scalar fields only. Every other `UPDATED` refetches the query.
- `CREATED` refetches.
- `DELETED` and `EXITED` remove the held row from the cache at once. The query then refetches, because a removal changes a list or empties a slot.
- A response that carries an older row version than the cache already holds is discarded, and the query refetches.
- A mutation result is never written into the cache. A committed mutation invalidates the cached queries over the models it touches (TanStack marks them stale and refetches the ones in use).
- Refetches that arrive close together are coalesced into one.

### Row versions

The client adds `_version` and `__typename` to every selection of a model with events, and the primary key fields (`id` for most models) when you left them out. It compares `_version` to decide which copy of a row is newer. You can select them yourself, but not alias another field to those names. See [Row versions](../docs/golem-go/GUIDE.md#row-versions).

### Status is per query

`useGolemStatus` reports one query, keyed by its document and variables. It is not a global connection state. For that, read the transport:

- `golem.transport.status`: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`.
- `golem.transport.onStatus(listener)`: calls you on every change and returns an unsubscribe function.

Wrap them in `useSyncExternalStore`:

```tsx
import { useSyncExternalStore } from 'react'

function useConnection() {
  return useSyncExternalStore(
    (notify) => golem.transport.onStatus(notify),
    () => golem.transport.status,
  )
}
```

### Why there is a manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator (`golem generate` writes `zz_golem_client.gen.ts`), and `golem check` fails when it falls behind the schema. The client reads it and never guesses. See [The client manifest](../docs/golem-go/GUIDE.md#the-client-manifest).

### Server concepts

- [Subscriptions and events](../docs/golem-go/GUIDE.md#subscriptions-and-events)
- [Row versions](../docs/golem-go/GUIDE.md#row-versions)
- [Rows that leave a list](../docs/golem-go/GUIDE.md#rows-that-leave-a-list)
- [The live handshake and the subscription ack](../docs/golem-go/GUIDE.md#the-live-handshake-and-the-subscription-ack)
- [The socket deadline](../docs/golem-go/GUIDE.md#the-socket-deadline)

For a walkthrough from a model to a live component, see [Using golem from React](../docs/golem-go/REACT.md).

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

## Server requirements

- **golem go/v0.8.3 or later.** The generated manifest carries `nullable` on every root and relation and the `deletedRow` root shape; the client reads both and does not guess.
- **`golem.Subscriptions` on every model a live query reaches.** A live stream whose rule or filter reaches a model without `Subscriptions` is refused when it is opened.
- **A socket deadline for every principal.** The server closes a live socket at the principal's deadline (close `4401`) and the client reconnects with fresh credentials from your `credentials` getter and refetches. A principal with no deadline is refused with `GOLEM_PRINCIPAL_DEADLINE_REQUIRED` (close `4426`): set a context deadline where the principal is resolved, or mark an expiry-free principal and set `EventLimits.RevalidateEvery`. See the GUIDE, "The socket deadline".
- **The contract fingerprint.** Every live request carries `{"protocol": 1, "contract": ...}`. A server built from a different schema answers `GOLEM_CONTRACT_MISMATCH`.
- **Page sizes.** `take` above the server's limit is a typed error (`BAD_USER_INPUT` or `QUERY_LIMIT_EXCEEDED`), not clamped.

## Development

```sh
cd client
npm ci
npm run build
npm test
npm run check:no-use-effect
```

Release: bump both versions in a commit to main; the Publish client workflow publishes any version not yet on npm.

The tests run against the real social example server (`go/examples/social`) and need Go.

Frontend rule enforced in CI: there is no `useEffect` or `useLayoutEffect` anywhere under `client/`.

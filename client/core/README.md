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
- `golem.staticInfiniteQueryOptions(document, variables?)` uses the same generated-list cursor planner without live sync or subscriptions. It works for models without events, forwards cancellation to HTTP, and uses a separate static cache entry. Normal TanStack refetch options still apply. Call `resetSession()` after changing the authenticated principal or selected profile; it clears cached pages and starts a new chain. Custom DTO continuation pages require a separate page contract; this method currently accepts generated list roots.
- `golem.continuationQueryOptions(document, variables?)` is the live counterpart for a custom page root with generated `invalidation` metadata. Its operation subscription acknowledges before the first fetch and emits no row/data packets. A terminal invalidation clears the whole chain, waits for replacement acknowledgement and restarts at `null`; old in-flight pages cannot append. TanStack invalidation (including committed generic mutations) also resets the chain. `refetchType: 'none'` clears without making a request; explicit ordinary refetch retains the current traversal semantics. A quick remount can reuse open coverage, but reopening after a subscription gap starts a fresh population.
- `golem.staticContinuationQueryOptions(document, variables?)` pages a registered custom `PageQuery` from a format 3 manifest. It uses opaque `string | null` page parameters and adds the continuation selection automatically. Select one pageable root directly and leave `after` to the client. Empty or short pages continue when the server supplies `next`; only `next: null` ends traversal. Expiry and invalid tokens remain errors: discard accumulated pages before starting a new chain. Call `resetSession()` on principal/profile changes too.
- `golem.staticQueryOptions(document, variables?)` returns ordinary TanStack query options without event streams, including for a model without events or a custom root with no model rows. Static queries and continuation pages preserve authored model field selections and carry SDK read metadata for own-write invalidation without creating live entries. Normal TanStack refetch options still apply. Counts, aggregates, group-by and custom roots with model rows can be live through `golem.queryOptions`: any event on a model they come from refetches the document through the coalescer, and they are never patched.
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

`golem.dispose()` stops live sync, cancels reads, clears cached Golem rows and page chains, closes the transport and releases the `QueryClient` for another `GolemClient`. Unrelated cache entries remain intact.

Socket credentials are prepared again for each connection attempt. If that
preparation rejects, the stream ends with the original error and no socket is
created for that attempt; a fresh subscription or session reset can retry with
current credentials. Reset and disposal fence pending preparation and late
socket opens, including asynchronous cleanup failures from the old session.

## Live status

`golem.live.statusOf(query)` returns `{ state, error }` for a TanStack `Query`, or `undefined` before live sync tracks it. `golem.live.subscribeStatus(listener)` calls you on every change.

| State | Meaning |
|---|---|
| `CONNECTING` | Subscriptions are being opened; the first fetch waits for their acks. |
| `LIVE` | Every stream is acknowledged and any recovery fetch has committed for the current generation. |
| `RECOVERING` | Coverage was lost. Cached data is cleared; replacement streams must acknowledge and a current-generation fetch must succeed before returning to LIVE. |
| `LIMIT` | The connection's subscription cap was reached. The data is still fetched, but not live until a slot frees. |
| `FORBIDDEN` | The server stopped a stream because the principal lost access. Cached data is cleared immediately and streams remain stopped until an explicit session reset. |
| `NOT_FOUND` | A row held in a single result slot left the result; the query refetches. |
| `FAILED` | A stream failed for good (`error` holds the cause). |

TanStack's own `isFetching`, `isError` and `data` keep their usual meaning.

### Initialization and authenticated data incarnation

A manifest requiring `data-incarnation-v1` needs authenticated discovery before
its first mutation. Call `await golem.ready()` during initialization. Concurrent
calls share one discovery; reads initialize lazily. An unready mutation fails
with `GOLEM_CLIENT_NOT_READY` before credentials or HTTP dispatch. Discovery is
an authenticated POST with a fresh nonce and no business operation. Its result
binds later HTTP requests and WebSocket subscriptions to that database identity.
The supported opted entry points are `GolemClient` and `Transport`; a standalone
`Session` does not own authenticated HTTP discovery.
The capability can coexist with `operation-invalidation-v1`. Manifests without
it need no discovery and preserve their existing wire contract.

Only a fresh discovery can adopt an identity. A business response or socket
acknowledgement claiming another identity clears Golem data and starts fresh
authenticated discovery. The client remains unready until that discovery
succeeds. Active reads restart from their initial page after adoption; discovery
or authentication failure appears as a query error and `FAILED` live status.
Raw `Transport` subscribers receive a typed terminal session refusal and can
subscribe again after readiness. A reset also performs discovery before its
promise resolves. The old socket is disposed
before the replacement session becomes ready.

### Principal, profile and logout boundaries

Update the state read by `credentials`, then call and await `golem.resetSession()`:

```ts
session.set(nextSession) // a new principal/profile, or logout
await golem.resetSession()
```

Reset clears all cached Golem rows and page chains immediately, including inactive queries and static/continuation pages. It cancels outstanding reads, fences late responses even if an HTTP implementation ignores abort, renews transport credentials and reopens active live streams. Live refetches wait for the new acknowledgements; active static queries refetch from their initial page. Unrelated TanStack cache entries are preserved. Reset does not detect changes to arbitrary state captured by your credentials callback: call it explicitly for every principal/profile switch and logout. Raw `transport` requests share the same readiness and session fence; they do not write query-cache data. Detaching live sync closes live streams without changing the principal or cancelling independent static reads. Disposal fences all outstanding Golem reads. Core consumers must retain the returned `queryFn` and `structuralSharing` options: together they own the request and cache-commit fences. Overriding either leaves the supported recovery boundary. React hooks reserve both options.

Mutations capture their session and JSON arguments at invocation. A mutation
queued before reset or disposal fails before dispatch. If it was already sent,
a stale success becomes `GolemCommitUnknownError`; read back authoritative
state and do not replay it automatically. Explicit server PARTIAL/UNKNOWN
errors retain their commit evidence. Old outcomes never invalidate the new
session's cache. Session boundaries also clear SDK-owned completed TanStack
mutation records. Pending records retain scope ordering until settlement and
are then removed if stale; unrelated mutations are preserved.
`golem.onSessionReset(listener)` provides a read-only lifecycle notification
for adapters that retain operation results. It runs on reset, authenticated
identity adoption and disposal and returns an unsubscribe function. `golem.mutations.prepare(document, variables, options)` offers
the same invocation capture for an external scheduler and returns a single-use
operation. Preparing requires readiness too.

Recovery uses a query generation that survives stream replacement. Resyncs and loss of network coverage clear uncertain data immediately; an older response cannot restore it. Replacement opens share a fixed 250 ms cooldown plus 0–50 ms jitter within each query. The deadline does not move when more invalidations arrive. After all replacement acknowledgements, recovery refetches immediately; the ordinary event coalescer cannot delay it. Client cooldown scheduling adds at most 300 ms before reopening, excluding acknowledgements and network time. Ordinary event refetches retain their configured coalescing window and maximum wait. Ordinary connection failures retain exponential backoff up to 30 seconds. Permission and credential invalidation never wait to hide data. These cooldown values are provisional; bounded fake-clock tests exercise the coordination, while real workload latency measurements remain required.

Own successful mutations and explicit PARTIAL/UNKNOWN commit errors invalidate
SDK reads in the same session, including static reads and models without event
streams. Generated reads use their public model dependencies, including selected
nested rows and public relation filters. Opaque/custom roots and computed
results conservatively depend on every own write; the browser does not infer
hidden policy dependencies. Unscoped mutations invalidate all SDK reads.
Write reach comes only from generated `mutationScope` metadata, separately from
the root's response `models`. Generated nested writes include their conservative
model reach; custom writes are unscoped. Older manifests without the additive
hint remain supported and conservatively invalidate all SDK reads. The hint
requires no new manifest version or protocol capability.
Inactive matching reads become stale; active ones refetch under normal TanStack
policy. Unrelated generated-model reads and application cache entries stay
unchanged. Static mode opens no subscriptions.

Invalidating a custom continuation query, static or live, discards its pages and
restarts at `null`. A held or already-settled obsolete result cannot refill the
chain. Ordinary explicit refetch retains the current traversal semantics.
Generated cursor pagination retains its normal refetch traversal.

## Declared operation invalidation

Custom DTO/scalar queries become live only when their generated root declares `invalidation: { kind: 'operation', subscriptionField: '…Changes' }`. The client observes that operation, independently of returned model rows. It preserves authored DTO/model projections and does not add public IDs, versions or typenames to those results. An unexpected data packet on the invalidation stream is a protocol failure; acknowledgement and terminal recovery use the same coordinator as generated lists.

Such manifests remain format 3 and require `operation-invalidation-v1`. Matching clients advertise that capability on HTTP and WebSocket initialization, including static requests. Unknown required capabilities are a configuration error; an old client cannot silently infer returned-row streams against an opted server. Manifests without required capabilities retain their existing handshake bytes. Manually wrapped generated Go servers must pass the exact active `Config.Bundle` so capability requirements cannot be lost behind an executor wrapper.

The capability requirement applies to the whole opted server schema: older clients are rejected for ordinary/static queries and mutations too, not only for the new subscription. Upgrade the client and regenerate its manifest with that server contract. These APIs require the matching go/v0.9.0 contract; the minimum server version listed below for older client features does not imply support for operation invalidation.

Live admission remains a server authorization and coverage check. Readable dependencies currently require local ordinary row/filter/field-mask conditions and declared before-image coverage of the watched and controlling scalars. Ordinary related-row paths are refused; hidden policy facts require genuine bound scope authority and the certified historical writer, which the default V3 writer does not supply. The client does not fall back to broader model streams or infer authority from DTO values. Use `staticQueryOptions` or `staticContinuationQueryOptions` when live observation is unavailable; static mode skips subscriptions but still sends required capabilities. See [the supported server contract](../../docs/golem-go/GUIDE.md#declared-operation-invalidation).

## The manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator: `golem generate` writes `zz_golem_client.gen.ts` beside your schema, and `golem check` fails when it falls behind. The client reads it and never guesses. Regenerate it after every schema change; never edit it. See [The client manifest](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#the-client-manifest).

## Typed DTO results and nested inputs

go/v0.9.0 registers named output/input structs and generates their GraphQL
shapes and manifest metadata. Ordinary GraphQL selections and gql.tada typing
apply to nested DTO fields too; select the nested fields your UI needs. DTO
members named `id` or `_version` are ordinary result values, not model patch
identities. Embedded model rows are authorized and hydrated by the server.
The resolver remains responsible for the sensitivity of ordinary DTO scalars.

JavaScript object omission and explicit `null` keep their GraphQL meanings.
Server argument fields using `golem.InputValue[T]` can distinguish them; do not
replace omitted members with null while preparing variables. An exactly-one
input accepts one supplied non-null choice and is validated before the server
resolver runs. Client document typing does not replace that runtime check.

Static APIs work without event streams. A scalar/DTO result becomes live only
through declared operation invalidation, rather than by guessing dependencies
from its fields. Custom infinite pages use the continuation APIs, not the
generated-list cursor APIs. See the [server DTO/input contract](../../docs/golem-go/GUIDE.md#named-output-objects-and-embedded-model-rows)
and [React continuation hooks](../react/README.md#usegolemcontinuationquery).

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

Automatically inferred row streams require `golem.Subscriptions` on their models. Custom roots can instead use declared operation invalidation; without an applicable observation contract, `useGolemQuery` throws `GolemNotLiveError`.

#### Filtering is automatic

You never filter events in the client. The server judges each event against the caller's policy and the stream's filter, and sends one of four event types: `CREATED` and `UPDATED` for a row that matches, `EXITED` for a row that stops matching or that the caller may no longer read, and `DELETED` for a removed row. A row that starts to match arrives as `CREATED` or `UPDATED`. See [Rows that leave a list](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#rows-that-leave-a-list).

#### Patch or refetch

- `UPDATED` is patched into the cache in place only when the stream's selection is plain: a top-level list or single-row root, arguments limited to `where` (or the unique selector of a single-row root), no `orderBy`, `distinct`, `take`, `skip` or cursor, and scalar fields only. Every other `UPDATED` refetches the query.
- `CREATED` refetches.
- `DELETED` and `EXITED` remove the held row from the cache at once. The query then refetches, because a removal changes a list or empties a slot.
- A response that carries an older row version than the cache already holds is discarded, and the query refetches.
- A mutation result is never written into the query cache. A committed mutation invalidates the cached queries over the models it touches (TanStack marks them stale and refetches the ones in use).
- Refetches that arrive close together are coalesced into one.

#### Row versions

The client adds `_version` and `__typename` to every selection of a model with events, and the primary key fields (`id` for most models) when you left them out. It compares `_version` to decide which copy of a row is newer. You can select them yourself, but not alias another field to those names. See [Row versions](https://github.com/eleven-am/golem/blob/main/docs/golem-go/GUIDE.md#row-versions).

#### Status is per query

`golem.live.statusOf` reports one query, keyed by its document and variables. It is not a global connection state. For that, read the transport:

- `golem.transport.status`: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`.
- `golem.transport.onStatus(listener)`: calls you on every change and returns an unsubscribe function.

Wrap them in your framework's external-store primitive. In React, `useGolemConnection()` from `@eleven-am/golem-react` does it.

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
- `GolemPartialCommitError`, `GolemCommitUnknownError`: a mutation whose commit state is partial or unknown. Affected live queries are invalidated in the same session, as after every committed mutation; stale-session outcomes do not invalidate replacement data.

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

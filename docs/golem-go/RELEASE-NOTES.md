# Release notes — Go module

The Go module is released independently of the TypeScript packages. Its
versions are the `go/v*` tags; the root `v*` tags belong to the TypeScript
packages and do not describe this module.

```
go get github.com/eleven-am/golem/go@v0.8.5
```

The module lives in the repository's `go/` directory, so its tags carry that
prefix. A plain `v0.3.0` tag would not make this module fetchable. Tags before
`go/v0.3.0` predate these notes and are not described here.

---

## go/v0.8.5

**Live events arrive in about 20 ms instead of about 250 ms.** The event publisher now wakes as soon as a transaction that recorded outbox facts commits, instead of waiting for its next poll. A rolled-back write never wakes it. Polling remains the fallback for other processes and for CDC.

**The durable actions recipe cannot deadlock or cancel the wrong job.**
- `CancelExport` now cancels the job before it updates the status row, the same lock order as the failure callback. A terminal failure racing a cancel of the same export no longer deadlocks on PostgreSQL.
- The status update matches the job ID it read, so a job requested in between is never marked cancelled.
- The mutation returns the row as updated.
- The recipe's request and cancel transactions retry conflicts with `golem.RetryConflicts(3)`.

---

## go/v0.8.4

**Custom operations can cancel jobs on their own transaction.** `tx.CancelJob(ctx, jobID)` on `CallerTx` and `SystemTx` returns the same `(bool, error)` as `QueueOperator().Cancel`, but the cancellation commits or rolls back with the operation's other writes. Cancel only a job ID read in the same transaction from a row the caller's policy allowed, never an ID taken from arguments. A PostgreSQL serialization failure during `CancelJob` or `tx.Enqueue` is now reported as a conflict, so `RetryConflicts` retries it. The durable-actions recipe no longer needs an `Operator` package variable.

**A queue plan names only what it changes.** Fields left out of a `ReconfigureQueue` plan keep their value in the active plan, so raising one cap no longer reverts an earlier change to another. In go/v0.8.3 a plan restated the full configuration.

**Fewer writes on terminal failure.** Only job types that have an `OnJobFailed` callback persist a deferred failure intent. Other types fail with a single write.

**An `internal` primary key is refused at compile time** with `P1_GOLEM_TAG_INTERNAL_PRIMARY_KEY`, instead of a client-manifest error.

**One enqueue path for every hook.** `HookEnqueue` in single-row, batch and nested hooks uses the same transactional path. Tests prove the queue worker wakes right after commit on each of them.

---

## go/v0.8.3

**Hooks can enqueue durable jobs on their own transaction.** `golem.HookEnqueue(ctx, result.Executor(), pending)` enqueues a queue job inside a transaction-after hook, so the job commits or rolls back with the hook's write. A failed enqueue rolls back only its own savepoint, so the hook can handle the error. Without `Config.Queue` it returns `CodeConfigInvalid`, which rolls the write back unless the hook swallows the error. Use it instead of `AfterCommit` when a committed write must have a job.

**Queue failure callbacks.** A job type can declare a failure callback with `OnJobFailed`, registered through `Config.QueueFailed`. The callback runs in the same system transaction that marks the job failed, so a status-row write, any `tx.Enqueue` and the `failed` state commit together.
- If the callback errors or panics, the transaction rolls back and the job is finalized again after its lease expires. `Operator().Cancel` stops a stuck job.
- A job that crashes on its last attempt is now finalized by a worker instead of inside `Claim`, so its callback runs too.
- A terminal decision is persisted before the callback runs, so a failing callback cannot cause the handler to run again.
- The callback's deadline is the job's remaining lease, so it never outlives its `ExclusiveBy` ownership.
- The callback may run more than once, so it must not cause external side effects.
- Every process that registers the type must also register its callback.

**Upgrade every worker before relying on `OnJobFailed`:** a go/v0.8.2 worker still fails exhausted leases inside `Claim` without calling it.

**Queue limits change on a running application.** `app.ReconfigureQueue(ctx, queue.Plan{...})` changes `Concurrency`, `ClaimBatch`, per-type `MaxConcurrent` and resource capacities without a restart, and `app.QueuePlan()` reports the active plan and its generation.
- A plan names only the fields it changes; everything else keeps its value in the active plan.
- A plan that fails validation changes nothing.
- Lowering a cap lets running jobs finish.
- Costs, resource membership and lease settings still require a restart.
- Raising a shared resource takes full effect once the jobs leased under the old capacity end.

**Custom operations see the caller's actor.** Custom query and mutation resolvers now receive the caller's actor, so `golem.ActorFrom[Actor](ctx)` works in them as it does in hooks. Call resolvers through the generated `Mutate` or `Query`; calling the resolver function directly skips the actor.

**The `internal` field tag.** A field tagged `golem:"internal"` stays fully available to Go and system code, including unique lookups, filters and order. It is removed from:
- every GraphQL type, input, enum and aggregate;
- introspection and subscription payloads;
- the client manifest.

Use it instead of `hidden` for values such as API-key hashes. On a model with a GraphQL create root, a required `internal` field must be `system` or hook-owned. A unique one must be `system`, because a hook cannot own a unique field. Kernel-mode schemas have no such requirement. `internal` together with `hidden` is refused.

**Live derived queries in the client manifest.** A `relationGroupBy` root's `models` now lists the related models it groups by, so a live client refreshes the groups when a related row changes. Regenerate to get it.

**A durable actions recipe.** QUEUE.md gains a tested recipe:
- a status model users watch live;
- a mutation that records the action and enqueues it in one transaction;
- an idempotent, at-least-once handler;
- `OnJobFailed` to record failure;
- cooperative cancel;
- the guard for the window between the handler committing and the queue recording success.

---

## go/v0.8.2

**The client manifest names each root's result shape.** Every entry of `roots` in `zz_golem_client.gen.ts` gains `shape`: `rows`, `row`, `deletedRow` (a generated delete root, which returns the row it removes), `aggregate`, `groupBy`, `updateMany`, `deleteMany`, or `value` (a custom root whose result is not a model or a list of one). A client reads it instead of guessing from the root name. Regenerate to get it.

**The client manifest names nullability.** Every root and every relation gains `nullable`, read from the generated schema's type, so a client nulls a removed single-row slot only where the schema allows it and reports a typed not-found elsewhere. Regenerate to get it.

**The client manifest names each model's unique input.** Every model gains `uniqueInput`, the type name of its unique input read from the generated schema (`null` when the model has no list or single-row root), so a client never builds `<Model>WhereUniqueInput` from the model name. Regenerate to get it.

**The socket closes with 4401 at credential expiry, with no error frame first.** When a connection's credential deadline passed while a subscription was open, the server could send that subscription a `GOLEM_SUBSCRIPTION_CANCELLED` error frame before the 4401 close, and a client that read the frame as a normal end never saw the close and never reauthenticated. Operation error frames are no longer sent once the connection's context is done, so the close is the only signal.

---

## go/v0.8.1

**Relation-only updates write the version.** A relation-only update of a row in a model with `Subscriptions` now writes that row's version column, so database `UPDATE` triggers on that table fire for it.

**CDC changes whose live row is gone are superseded.** A CDC create or update whose row no longer exists when the recorder stamps it (an external update followed by a delete, or an insert and a delete in one source transaction) is no longer a failed batch. It is neither stamped nor published, and the observer receives a suppression with the new reason `events.SuppressionCDCSuperseded` (`cdc_superseded`, mapped to `observe.ReasonCDCSuperseded`). The delete that follows, in the same or a later batch, is recorded and published as usual, and so are the batch's other changes. A replay of the batch gives the same result. The batch's recorded events are numbered contiguously from 1; event identities still derive from the source ordinals.

**The live protocol.** WebSocket and SSE subscriptions now require the `golem`
block `{"protocol": 1, "contract": "<hex>"}`: in `connection_init.payload.golem`
on WebSocket, in `extensions.golem` on SSE. There is no live path for a client
without it. A mismatch closes the WebSocket with `4426` (`golem:protocol` or
`golem:contract`) and is refused on HTTP and SSE as `GOLEM_PROTOCOL_MISMATCH`
or `GOLEM_CONTRACT_MISMATCH`. The `golem` key is removed from the payload before
`WebSocketInit` sees it. Plain HTTP queries and mutations are unchanged; a block
sent with one is checked. After a subscription is registered the server sends
exactly one `next` frame, `{"data":null,"extensions":{"golem":{"live":true}}}`,
before any event; a refused subscription gets no ack. `graphql.Response` gains
an optional `Extensions` field. A client that subscribes without the block must
add it before upgrading to this version; see the GUIDE section "The live
handshake and the subscription ack".

**The client manifest.** `golem generate` now also writes `zz_golem_client.gen.ts`
beside the SDL: the contract fingerprint, each model's primary key, cursor
selector, events root and relations, the models behind computed fields and
custom roots, and which roots are generated or opaque. It is listed in the
generated manifest and checked by `golem check`; regenerate your application to
add it. Kernel mode (`WithoutGraphQL`) does not write it. See the GUIDE section
"The client manifest".

**Event streams end when a policy is unavailable.** An event for a model or a relation target whose policy cannot be built used to be dropped silently, so a stream stayed open and quiet. The stream now ends with `GOLEM_SUBSCRIPTION_REVALIDATION`, for created, updated and deleted events alike. Reopen it once the policy builds. The suppression reason `policy_unavailable` (`events.SuppressionPolicyUnavailable`) is gone because the stream now ends instead.

**Streams end when access they depend on changes, and on `InvalidatePolicies()`.** A stream whose read rule or `where` reaches another model, such as `Comments.Post.Is(...)`, now watches that model. When a row it reads changes in a field the rule or filter uses, and the subscriber could read that row before or can read it now, the stream ends with `GOLEM_SUBSCRIPTION_RESYNC` and `extensions.reason` `DEPENDENCY_CHANGED` (Go: `events.ReasonOf`), so a client that reloads no longer sees rows it lost access to. Updates to other fields, and rows the subscriber could never read, do not end it. Creates and deletes end it only for a to-many relation. `application.InvalidatePolicies()` now also ends every open event stream with `GOLEM_SUBSCRIPTION_RESYNC` (no reason), whether or not the policy cache is enabled. A stream whose rule or filter reaches a model without `golem.Subscriptions` is refused when opened with `GOLEM_SUBSCRIPTION_INVALID` and reason `DEPENDENCY_UNOBSERVABLE`, naming the model: add `Subscriptions` to it or change the rule. New public API: `events.Reason`, `events.ReasonDependencyChanged`, `events.ReasonDependencyUnobservable`, `events.ReasonOf`, `events.FailureWithReason` and `(*events.Error).Reason` and `.Detail`. See the GUIDE section "A stream that reads another model".

**Sockets close at their credential's deadline.** A WebSocket or SSE
subscription now needs a deadline derived from its principal's context: the
context's own deadline (the credential expiry), or, for a context marked with
the new `golem.WithoutCredentialExpiry` and with `EventLimits.RevalidateEvery`
set, the open time plus `RevalidateEvery`. A principal with neither is refused
at the handshake with `GOLEM_PRINCIPAL_DEADLINE_REQUIRED` (WebSocket close
`4426`; SSE status 400); HTTP queries and mutations are unaffected. At the
deadline the server closes the WebSocket with `4401` and a client reconnects;
the principal is resolved fresh on the new socket. The marker is read only from
the Go context, never from a client. Set the credential expiry as the context
deadline where you resolve the principal, or mark API-key principals and set
`RevalidateEvery`; see the GUIDE section "The socket deadline". New public API:
`golem.WithoutCredentialExpiry` and `golem.HasNoCredentialExpiry`.

**Partial commits are never reported as clean failures.** A GraphQL response
that has errors after the operation committed at least one write now carries
`extensions.golem.commit`: `"PARTIAL"` when a write was committed, or
`"UNKNOWN"` when a `COMMIT` failed without saying whether the database applied
it. A failure that committed nothing, a no-op update and a response without
errors carry no extension, and their bytes are unchanged. The report is one
request-scoped record, shared with `COMMITTED_RESULT_UNAVAILABLE`: a response
whose error says `committed: true` is always `PARTIAL`. A client that retried
every errored mutation must stop retrying a response that carries the
extension; see the GUIDE section "Partial commits are never reported as clean
failures".

**Created and updated events no longer read the row once per subscriber.**
Each event now reads the changed row once, with the to-one relations your
policies and filters reference, and judges every subscriber against that read.
A subscriber it proves cannot see the row costs nothing further. Before, every
subscriber of a model ran its own principal resolution, read hooks and read for
every event, so the work grew with subscribers times events and streams ended
under load. Subscribers that can see the row, or that the shared read cannot
decide (to-many relations and conditions the evaluator refuses), run exactly the
path they ran before, with a fresh principal resolution per delivery, so what is
delivered and what a payload holds is unchanged.

Two behaviours differ from v0.8.0:

- **The exclusion decision uses the policy as it was when the stream opened.**
  A stream whose rule was false on a row when it opened does not receive events
  for that row after the policy broadens, until the stream is reopened (at its
  deadline, or when `InvalidatePolicies` ends it). Access
  that is revoked takes effect on the next event as before.
- **Read hooks do not run for subscribers the shared read excludes.** A
  read-before or read-after hook now runs once per candidate subscriber of an
  event instead of once per subscriber. Hooks used for audit or counting see
  fewer calls.

If the shared read fails, the streams that needed it end with
`GOLEM_SUBSCRIPTION_REVALIDATION`.

Measured with the `examples/loadmix` tool (one Article and one ChatMessage
subscription per user, an owner rule, PostgreSQL, 180 s steady windows). On
v0.8.0, every event ran a full principal resolution, hook pass and read for each
of about 96 subscribers at 100 users and about 240 at 250 users. At 100 users
delivery lag p99 was 13.8 s. At 250 users the stream fell 41,729 of 50,306
events behind, with a lag p99 of 186.8 s. Now an event costs one shared read
plus one full path for the one subscriber that owns the row (0.96 per event).
Measured on this release, with row versions, update before-snapshots, exits
and dependency streams included:

| Load | Delivered / expected | Delivery lag p99 | Article commit p99 |
|---|---|---|---|
| 100 users, steady | 20,192 / 20,192 | 261 ms | 10.0 ms |
| 250 users, steady | 50,306 / 50,306 | 259 ms | 6.4 ms |
| 100 users, burst | 25,989 / 25,989 | 255 ms | 5.5 ms |

No delivery was foreign or duplicated and no stream ended. At 500 users every
event still arrives, but the Article hub saturates (lag p99 about 9.6 s in the
development measurement), so about 250 to 400 subscribers per model hub is the
comfortable range.

**Rows of models with `Subscriptions` carry a version.** Every write golem
issues gives the row a version higher than any version that key has committed;
a recreated key starts above its deleted row. GraphQL exposes it as
`_version: String!` and Go as the read-only `GolemVersion` handle. See
"Row versions" in the guide.

**The migration that adds `golem_version` and the version source.** After you
regenerate, `golem migration new` produces one migration that:

- adds `golem_version bigint NOT NULL DEFAULT 0` (SQLite: `INTEGER NOT NULL
  DEFAULT 0`) to the table of every model with `Subscriptions`; existing rows
  keep version 0 until their next write;
- creates the version source first. PostgreSQL: the sequence
  `"<system schema>"."golem_row_version"` with `CACHE 1`, `NO CYCLE`. SQLite: the
  one-row table `golem_row_version`.

Nothing else is renamed or rewritten. The change needs no approval. Apply it
before you start the new build: at startup the new build refuses a database
that has versioned tables but no version source, a PostgreSQL sequence whose
cache setting is not 1, and a SQLite counter that does not hold exactly one row.

**Names reserved.** A field or column named `GolemVersion`, `_version`,
`golem_version` or `_golem_version` is now refused at generation. Rename yours
before regenerating. A GraphQL field called `version` is unaffected.

**Regenerate, and drain undelivered events on go/v0.8.0 first.** This release
reads only the new event fact format (fact codec V3, which stores the row
version with every fact). Facts written by go/v0.8.0 or earlier (format V1 or V2)
cannot be read, and nothing converts them. Run the drain steps of the go/v0.8.0
notes on go/v0.8.0, regenerate, and only then deploy this version. If you skip
the drain, the publisher blocks each such delivery with
`last_failure_code = 'format-retired'` (visible through `EventOperator().Inspect`)
and its decode error is `GOLEM_EVENT_FORMAT_RETIRED: stored event fact (format
V1) is no longer readable: drain undelivered events on go/v0.8.0 before
upgrading` (`V2` for a V2 fact). Under this version such a delivery can only be
retired with `EventOperator().Retire(ctx, causation)`; its events are lost.
Bundles of contract format 4 and 5 are no longer accepted in
`HistoricalEventBundles` either: startup stops with "historical contract format
N is not supported by this golem: deliver or drain undelivered events before
upgrading, then remove this bundle from HistoricalEventBundles".

**Events carry `version`.** Every event type has `version: String!`, a decimal
string. It is always the version of the write that produced the event, from the
stored fact; for a deleted event, the row's last version. If a later write lands
before delivery, the event keeps its own version and the selected entity carries
the `_version` it was read at. `golem.EventMetadata` gains `Version() int64`. Use it to ignore an event
older than a row you already hold.

**Rows that leave a list are delivered as `EXITED`.** An update that moves a row
out of a stream's `EventWhere` filter or read rule used to be silent. A
subscriber that held the row now receives an event with action
`golem.EventExited` (`type: EXITED` over GraphQL), the row's key, the version of
the update and a null entity; a subscriber that never held the row still
receives nothing. Every update fact stores a before snapshot, the row as it was
before the update, with the delete snapshot's field set and size limit, and
golem judges the subscriber's rule and filter against it. When it cannot decide
(a filter or read hook outside the snapshot, a to-many relation, a before
snapshot over `MutationLimits.MaxDeleteSnapshotBytes`, or a CDC update recorded
without a `Before` image) the stream ends with `GOLEM_SUBSCRIPTION_RESYNC`.
A client that switches on `Metadata().Action()` must handle the new action;
treat it like a delete for the list you hold. `golem generate` adds `EXITED` to
the GraphQL `GolemEventType` enum: regenerate your application. Update facts in
the outbox grow by the snapshot, which for most models is the primary key,
foreign keys and bounded scalars of the row. See "Rows that leave a list" in the
guide.

**For CDC adapter authors: an update's `Before` image is optional.** With it, the
fact stores the before snapshot and the subscribers that held the row receive
`EXITED`. Without it, every subscriber that cannot be ruled out from the new row
ends with `RESYNC`, so give the `Before` image whenever your source log has it.
`events.CDCChangeInput.Before` is still required for deletes.

**For CDC adapter authors: row images carry `golem_version`.** The exact images
an adapter hands to `Emit` hold every stored scalar of the model, and that now
includes `golem_version` (the value your source database holds). When the
emitter records a created or updated change it writes a new version into that
row, in the same database transaction that stores the fact, and the event
carries it, so your adapter must treat the transaction as a golem write through
`CorrelatesGolemTransaction`, as it does for the fact inserts. A delete takes the
version from its `Before` image; version 0 (a row never stamped) is published as
0, and an image without the `golem_version` column fails with
`GOLEM_CDC_INVALID`, as does a replay whose stored facts disagree with the
rebuilt ones. `CorrelatesGolemTransaction` must be true for the stamp's own
source transaction, or the stamp is captured as a new change and stamped again. A replay of a recorded batch
reuses the versions it recorded.

---

## go/v0.8.0

**GraphQL subscriptions no longer report a cancelled operation as an internal
error.** When a client completed or dropped a subscription, or the server shut
down, while an event was already dequeued, the event could be projected on the
cancelled operation context. About half the time that produced a false
"gqlgen executable returned no response" report to `ReportInternalError`, and
on a connection-level cancel it could put an `INTERNAL_SERVER_ERROR` frame on
the wire. The projection of an already-dequeued event now ignores cancellation,
and a cancelled operation writes no further frame on WebSocket or SSE.

**This release requires regenerating.** The generated contract format is now 7.
An application built from code generated by v0.7.x stops at startup with
"generated contract format 6 predates this golem (format 7): regenerate with
golem generate". The runtime and the GraphQL executor decode the generated
bundle through one shared decoder, so both refuse it the same way, and an
unknown field in the bundle is refused instead of ignored. A pending reviewed
backfill written by v0.7.x (`.golem-pending-backfill.json`) is refused with an
instruction to delete it and run `golem migration new` again after
regenerating.

**Drain undelivered events before upgrading.** Regenerating changes every
generation digest, so v0.8.0 cannot read any V1 event fact still in the outbox.
It also cannot read a V2 fact whose model's event schema changed after the fact
was written. v0.8.0 does not accept v0.7.x bundles as `HistoricalEventBundles`.
Deliver everything while still on v0.7.x:

1. Stop writes to models with events (put the application in maintenance, or
   stop the processes that write), and keep one v0.7.x process running
   `RunEventPublisher`.
2. Check for undelivered events. On PostgreSQL, qualify both tables with the
   schema that holds golem's system tables (the system namespace in your
   generated migration). On SQLite, use the names as they are:

   ```sql
   SELECT COUNT(DISTINCT o."causation_id") AS undelivered
   FROM "_golem_outbox" o
   LEFT JOIN "_golem_outbox_delivery" d ON d."causation_id" = o."causation_id"
   WHERE d."causation_id" IS NULL OR d."status" IN ('pending', 'leased', 'blocked');
   ```

3. Run the publisher until that count is 0. Blocked deliveries are not retried
   on their own. List them with
   `SELECT "causation_id", "last_failure_code" FROM "_golem_outbox_delivery" WHERE "status" = 'blocked';`,
   then call `EventOperator().Resume(ctx, causation)` to retry each one, or
   `EventOperator().Retire(ctx, causation)` to give it up.
4. Deploy v0.8.0 only after the count is 0, and remove any v0.7.x bundle from
   `HistoricalEventBundles`.

If you skip the drain, an unreadable fact is not delivered. The publisher blocks
it with `last_failure_code = 'schema-unavailable'` (visible through
`EventOperator().Inspect`). Its decode error is
`GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V2) needs event schema
<hex>, which no active or historical bundle provides: deliver or drain events
written before the schema changed, or supply the bundle that wrote them`; for a
V1 fact, the message names the generation digest. Under v0.8.0 such an event can
only be retired. A v0.7.x bundle left in `HistoricalEventBundles` stops startup
with "historical contract format 6 is not supported by this golem: deliver or
drain undelivered events before upgrading, then remove this bundle from
HistoricalEventBundles". V2 events written under an event schema that has not
changed are still delivered normally.

**Custom and computed model results are read through the caller.** A custom
query, custom mutation or computed field that returns model rows used to be
encoded from the rows the resolver built. Relations and `_count` on them failed
with `INTERNAL_SERVER_ERROR` (for example `me { team { owner } }`), and what a
client saw depended on how the resolver obtained its rows and on which fields
were selected. golem now reads every returned row again by its primary key
through the caller, with the client's selection, in one read per root and one
per computed field. Relations, `_count` and computed fields work, read policy
and field masks are the caller's, and order, duplicates and nested lists are
kept. Semantic and full-text search results take the same path, so a scalar-only
selection now costs one read. In queries and computed fields, a row the caller
cannot read or that was deleted is left out of its list, and a single result that
is gone is `null`, or `NOT_FOUND` when the field is non-null.
- A custom mutation never hides its commit. Any failure after the resolver
  returned (an unreadable or vanished result, a read or encode error) is
  `COMMITTED_RESULT_UNAVAILABLE` with `extensions.committed: true`, and
  `reason: RESULT_NOT_READABLE` plus `unreadable: <rows>` when the caller cannot
  read the result. The root is `null`, a list is never shortened, and a non-null
  root makes `data` `null`. New: `golem.CodeCommittedResultUnavailable`,
  `golem.ReasonResultNotReadable`, `graphql.CustomResultError`.
- A row built by hand with `golem.RuntimeReadRow` has no identity and is now an
  internal fault in a custom or computed result.
- A computed field that returns rows of another model (`GraphQLObject[Invite]()`
  on `Team`) failed to encode before; it works now.
- A custom root may return a list of lists of rows.
- The semantic and full-text result transport is gone: `golem.RuntimeSemanticRow`
  and its hydration helpers, `RuntimeSemanticResultWithIdentity` and
  `RuntimeFullTextResultWithIdentity` are removed (`RuntimeFullTextResult` and
  `RuntimeIdentityHydrationRequest` replace them). They were generated-code
  plumbing, not application API.

**Every list a custom operation or computed field returns declares its maximum.**
`golem generate` now fails with `P5_GRAPHQL_LIST_MAX_REQUIRED`, naming the
operation or `Model.field` and the list level, for each custom or computed list
without one. `golem.Query` and `golem.Mutation` take a trailing
`golem.MaxItems(outermost, nested...)`, with one value for each list level, and
`golem.GraphQLList` takes the maximum as a second argument. Each value is at
least 1. The maximum is independent of `MaxPageSize`, which stays the ceiling
for `take`. Before:

```go
golem.Query(graphql, "searchPosts", SearchPosts)
golem.ComputedField(graphql, "related", golem.GraphQLList(golem.GraphQLObject[Post]().NonNull()).NonNull(), Post{}.Related)
```

After:

```go
golem.Query(graphql, "searchPosts", SearchPosts, golem.MaxItems(50))
golem.ComputedField(graphql, "related", golem.GraphQLList(golem.GraphQLObject[Post]().NonNull(), 10).NonNull(), Post{}.Related)
golem.Query(graphql, "grouped", Grouped, golem.MaxItems(10, 5)) // [][]golem.Row[Post]
```

A resolver that returns more items fails with `LIMIT_EXCEEDED`,
`extensions.limit` `list_items` and `extensions.max`, is passed to
`ReportInternalError`, and is never shortened. For a custom mutation, whose
writes have committed, the error is `COMMITTED_RESULT_UNAVAILABLE` with
`committed: true` and `reason: LIMIT_EXCEEDED`. New: `golem.MaxItems`,
`golem.CustomOption`, `golem.LimitListItems`. Generated semantic and full-text
searches declare their maximum themselves: the smallest of 1000, the model's
`MaxPageSize` and its `MaxTake`. `golem.GraphQLList` changes signature, so call
sites that passed one argument no longer compile.

**Query cost matches what lists return.** A list is costed at its `take`, else
its default page, else its declared bound, so `me { ... }` over a list declared
`MaxItems(1)` no longer costs as if it returned hundreds of rows, and
`MaxPageSize` is no longer a cost multiplier. A `groupBy` or `relationGroupBy`
without `take` is costed at `MaxGroups`, so some such queries now exceed
`MaxComplexity` and need a `take`. A relation list now advertises the listed
model's default page size in `take: Int = N`; it showed the parent's, while the
query applied the listed model's. The printed default now matches what the query
returns, so a client that relied on the old printed number may see a different
one (in `examples/social`, `User.posts` and `User.comments` change from 25 to 50). Generated
semantic and full-text searches advertise their `take` default, the model's
default page size narrowed to the search maximum.
- Breaking for code that builds a server by hand: `graphql.Executor` has a new
  method, `ListBounds() graphql.ListBounds`, returning a table keyed
  `"Type.field"` (`graphql.NewListBounds`) for every list of objects without a
  `take` default. `graphql.NewServer` refuses an executor that omits a bound,
  declares one for any other field, or declares one below 1. Generated executors
  provide it, so generated applications need no change.
- A generated executor now refuses to start, instead of failing each query, when
  an exposed model's default page size is above the `MaxPageSize` limit: raise
  `MaxPageSize` or lower the model's default.

**Hooks see the same input values on every path.** `FrozenMutationField.Value()`
returned an internal `policyir.Value` when a write came from a GraphQL mutation
(or from a nested connect or disconnect), and a Go value when it came from the
generated Go API, so a hook's type assertion worked on one path and failed on
the other. It now returns a Go value on both: the field's Go type, an enum as
its wire `string`, and a scalar list as a plain `[]T` of the element's type
(`golem.List[T]` and typed enum values are converted when the input is frozen).
An application that type-switched on `policyir.Value`, or that asserted
`golem.List[T]` or a generated enum type on the value, must update that code;
code that already handled plain Go values needs nothing, and a workaround that
accepted both forms can be deleted. `golem.RuntimeMutationInputFromFields` and
`RuntimeMutationInputFromValues` now refuse a `policyir.Value` operand with
`BAD_USER_INPUT`, and the binder accepts a scalar list given as either a
`golem.List[T]` or a plain `[]T`.

**A request the GraphQL server refuses now says why.** Every limit error names
the limit and its value: `GraphQL query exceeds MaxDepth (12)`, with
`extensions.limit` and `extensions.max`, and `extensions.cost` for
`MaxComplexity`. Before, they all read `GraphQL query exceeds a configured
limit`. A validation failure used to be a bare `GraphQL validation failed`. It is
now one error per problem, at most eight and then `and N more validation
errors`, each with a fixed message for its rule, `extensions.rule`, and
`locations` in the new `graphql.Error.Locations` (`[]graphql.Location`). The
messages are fixed by golem and never repeat text from your request. They are the
same over HTTP, server-sent events and WebSocket, and with `Introspection` on or
off. A refused introspection query reads `GraphQL introspection is disabled`.
- `graphql.Error` gains a field, so an unkeyed `graphql.Error{message, path,
  extensions}` literal no longer compiles; use field names.
- A request with an empty `query` is `BAD_REQUEST`, not `QUERY_LIMIT_EXCEEDED`.
- A `graphql-transport-ws` subscribe message that is malformed or exceeds
  `MaxVariableBytes` gets the HTTP decoder's error (`invalid GraphQL request`,
  `GraphQL variables exceed MaxVariableBytes (N)`) instead of `invalid GraphQL
  subscription request`.
- A client that matches the old message text must match `extensions.code`, or
  `extensions.limit`, instead.

**A read your document cannot make says which argument and why.** A query or
subscription that passes validation but whose values cannot be bound used to
fail with `BAD_USER_INPUT` and the message `GraphQL operation could not be
bound`. Now the message names the argument and the rule, for example `Argument
take on User must be between -500 and 500, excluding 0` or `Input where.id.equals
on User is not a valid UUID`, the error `path` is the response name of the root
field, and the extensions carry `argument` (the dotted input path, with `[n]` for
a list item), `reason` (`INVALID_VALUE` or `LIMIT_EXCEEDED`), and `min`, `max` or
`limit` where they apply. A negative `take` without `orderBy` is refused at the
root it was sent to (`Argument take on User is negative and requires orderBy`)
and an omitted `$variable` given to `take` or `skip` reads `cannot be null`.
Messages name only schema terms and never repeat your values. A failure that a
valid document cannot cause, such as a contract that does not match the generated
schema, is `INTERNAL_SERVER_ERROR` and is passed to `ReportInternalError`, where
before it was shown as `BAD_USER_INPUT`.
- A subscription that cannot be bound now returns the same typed error from
  `Subscribe` instead of `GOLEM_SUBSCRIPTION_INVALID: GraphQL subscription could
  not be bound`. Pass it to `graphql.PresentError`.

**A write or an analytics query your document cannot make says the same.** The
generic `GraphQL operation could not be bound` is gone. A mutation, a nested
write, a custom mutation input or selector, and `aggregate`, `groupBy` and
`relationGroupBy` now refuse with the same typed messages, `path` and
extensions as a read: `Argument data on User must not be empty`, `Input
data.id on User is not a valid UUID`, `Argument
expectedVersion on Note must be at least 1`, `Argument take on Post must be
between -100 and 100, excluding 0`, `Key id on Post must be listed in by`,
`Input having.count.gte on Post is not a valid BigInt`. A nested path shows the
list index, and an input or list bound reports `MaxInputDepth`, `MaxInputNodes`
or `MaxListItems` without a location. A `having` that filters on a key that is
not in `by`, and an `orderBy` that names the same term twice, are named instead
of reading `could not be bound`. A failure a valid document cannot cause is
`INTERNAL_SERVER_ERROR`, as for reads.
A `having` comparison that sets `mode` on `equals`, `not`, `lt`, `lte`, `gt` or `gte` is now refused (`Input having.key.title.mode on Post is not supported for this field or provider`). `mode` applies only to `contains`, `startsWith` and `endsWith`. A request that sets it on those operators was accepted before, with `mode` ignored, so an `insensitive` equality compared case-sensitively; it is now refused with `BAD_USER_INPUT`.
Nothing runs before the refusal, so a refused mutation has no committed effect.

**The generated schema no longer advertises types no operation can reach.** The
SDL used to carry the whole input family of every model whatever you enabled:
a model without `create` still had `PostCreateInput`, an application without
analytics still had every `*AnalyticsFilter`, and every scalar filter and
update envelope was emitted even when no field used it. `golem generate` now
keeps only the types a client can reach from `Query`, `Mutation` and
`Subscription` through fields, arguments and input fields, and drops the rest.
Introspection and `server.SDL()` describe the smaller schema, and a client
generator no longer produces dead types. Nothing a request could send before is
refused now. A type that is reachable stays byte for byte as it was, and
`@oneOf` is declared only when an input uses it. Regenerate to get the smaller
schema.

**CDC deletes are decided like ordinary deletes.** A CDC batch is now recorded
in `_golem_outbox` before it is published, so a delete whose read policy reaches
a related parent consults the facts of the same source transaction, exactly as
a delete written through golem does. Before, every such CDC delete ended the
subscriber's stream with `GOLEM_SUBSCRIPTION_RESYNC`; now it is delivered or
suppressed exactly, and resyncs only when a parent the same source transaction
created or updated decides it, or when retention has already removed the batch.
- The batch's facts and a `delivered` delivery row are written in one database
  transaction, so the outbox publisher never publishes a CDC batch a second time.
- A replayed batch records nothing new and is published again. A replay whose
  facts differ from the recorded ones fails with `GOLEM_CDC_INVALID` and is not
  published.
- A batch with more facts than `MutationLimits.MaxFacts` or more fact bytes than
  `MutationLimits.MaxOutboxBytes` fails with `GOLEM_CDC_INVALID` instead of
  being published.
- Batches the adapter correlates with a golem transaction are still suppressed
  and are not recorded.

**For CDC adapter authors: the emitter now writes to golem's database.** Each
non-correlated batch is recorded before `Emit` publishes it, so the database
the App opened must accept writes to `_golem_outbox` and
`_golem_outbox_delivery`. If recording fails, `Emit` returns
`GOLEM_CDC_UNAVAILABLE` and publishes nothing; keep the checkpoint and retry, as
for a transport failure.

---

## go/v0.7.3

**This release requires Go 1.26.** The SQLite driver moves from
`github.com/ncruces/go-sqlite3` v0.23.1 to v0.36.0, which needs Go 1.26.

**The SQLite driver is current, and WAL works on NFS 4.1.** The older driver
grew the WAL `-shm` file with `fallocate`, which Linux NFS 4.1 mounts reject, so
`sqlite.Open` failed there. The new driver falls back when `fallocate` is not
supported, so the default WAL profile now works on such a mount for a single
node.

**Vector ranking uses SQLite's vec1 instead of sqlite-vec.** sqlite-vec only ran
on the old driver. Semantic search still ranks in one in-database statement over
the same stored float32 vectors, now with `vec1_cos_distance`; no data migration
or re-embedding is needed, and distances match sqlite-vec within 1e-6. Full-text
search is registered explicitly, since it came bundled with sqlite-vec before.
- vec1 computes in float32, so an embedding whose norm is outside [1e-9, 1e9] is
  refused with `SEMANTIC_VECTOR_INVALID` on every provider. During a refresh only
  that record is quarantined; the rest of the batch is stored.
- Rounding could give a vector that is a scaled copy of the query a distance just
  below zero, which failed the whole query. Distances in [-1e-6, 0) now count as
  0.
- A database that still holds a pre-v0.5.1 `vec0` vector table, or whose current
  semantic state predates exact vectors, is refused with
  `SQLITE_VEC_LEGACY_STATE`; migrate it with go/v0.7.1 first. A fresh database
  can still be created from a migration history that passed through that state.
See [SEMANTIC.md](./SEMANTIC.md).

**A system escape ends with its transaction operation.** A `SystemTx` taken with
`SystemEscape` from a transaction-operation handle is refused with "operation
has ended" once that operation returns, like the handle itself.

**Analytics filters are refused like read filters.** An aggregate, count or
groupBy `where` that cannot be bound (an unknown field, a text operand on a
datetime, an unrepresentable datetime) is now `BAD_USER_INPUT`, as on
`FindMany`, instead of `FORBIDDEN`.

**A policy that fails to build gives the same error with or without the policy
cache.**

**SQLite on a network filesystem can keep write-ahead logging.**
`sqlite.OpenNetworkFilesystemWAL` runs WAL with exclusive locking on a single
connection, so SQLite keeps the WAL index in process memory and never creates
the `-shm` file that NFS mounts cannot grow. Every connection sets exclusive
locking before its first database access, and the profile verifies WAL,
exclusive locking and `synchronous=FULL`. While the handle is open no other
process can open the database. A non-empty `-shm` file next to the database is
refused, and `locking_mode` is now a provider-owned pragma that no profile
accepts from the data source name. `CheckpointForBackup` truncates and verifies
the WAL for this profile. See [SQLite durability profiles](./SQLITE_DURABILITY.md).

**`sqlite.OpenNetworkFilesystem` converts a WAL database back to rollback
journaling on open.** Previously a database whose header still declared WAL,
for example one left behind by `sqlite.Open` or `sqlite.OpenNetworkFilesystemWAL`
with an empty `-shm` and a `-wal` file, could not be opened on a network
filesystem because the journal-mode transition failed. The rollback profile now
reads the database header and, when it declares WAL, converts the file on a
dedicated exclusive-locking connection: a truncating checkpoint, then
`journal_mode=DELETE`, then an integrity check that must report `ok`. Rows held
only in the `-wal` file are preserved. The conversion refuses to run beside a
non-empty `-shm` file and fails closed when another process holds the database;
it never deletes or truncates sidecar files itself. Databases already in
rollback-journal mode and the WAL profiles are unaffected.

## go/v0.7.1

**SQLite databases on network filesystems can opt into a compatible profile.**
`sqlite.OpenNetworkFilesystem` uses verified rollback journaling and a single
connection while retaining `synchronous=FULL`, foreign keys, the bounded busy
timeout, and provider-owned transaction locking. The existing `sqlite.Open`
entry point remains unchanged and continues to enforce WAL with its verified
four-connection pool. `CheckpointForBackup` now dispatches by the profile stored
on the verified handle: it truncates and verifies WAL for the default profile,
while a closed rollback-journal database is verified without attempting a WAL
checkpoint. See [SQLite durability profiles](./SQLITE_DURABILITY.md).

## go/v0.7.0

**This release changes behaviour you may depend on.** Error codes, write
authorization, datetime handling, delete events and a few signatures change.
[MIGRATION.md](./MIGRATION.md) lists every breaking change and what to do. Two
need action before you deploy: drain the v0.6.x event outbox, because every
subscribable model's event schema changes, and make sure `DefinePolicy` is a
pure function of the actor, because its result is now cached.

### Policies

**Relation checks see only rows the caller can read, everywhere.** A read
rule, field rule, write rule, filter or delete-event check that tests a
relation now ranges only over target rows the caller can read, as reads
already did when they ran. A filter or rule that did not repeat the target's
read rule, such as `Comments.Post.Is(Posts.Priority.Eq(0))`, failed every read
with `FORBIDDEN`; those reads now succeed. SQL field masks over relations
tested related rows the caller could not read, and a masked value could reveal
whether such a row matched; they now use the same rule as reads. GUIDE.md,
[How policies are proved](./GUIDE.md#how-policies-are-proved), explains the
rule. Breaking: see [MIGRATION.md](./MIGRATION.md#policies).

**Update and delete rules no longer repeat the read rule.** An existing-row
write had to contain the read rule's conditions verbatim, or it failed with
`BAD_USER_INPUT`. Updates, deletes, upserts, `UpdateMany`, `DeleteMany` and
their nested forms are now planned over the action rule, the selector and the
read condition of the rows they touch, so a rule wider than the read rule
narrows to readable rows. A caller with no grant for the action is refused
with `FORBIDDEN` and reason `NOT_GRANTED` before planning. A write rule's
relation checks can no longer probe rows the caller cannot read, and an upsert
without a create grant can no longer reveal whether an unreadable row exists:
its create branch is refused with `NOT_GRANTED` before any hook runs. Breaking:
see [MIGRATION.md](./MIGRATION.md#policies).

**Policy-checked calls cost close to system calls.** Policy conditions are now
immutable and built in linear time; a 1,024-leaf condition took about 900
million allocations to build. A caller `Create` on SQLite went from 3.04 ms to
0.94 ms and `FindFirst` from 10.99 ms to 2.17 ms in the policy benchmark.
The policy set also remembers the conditions it resolves for each model, so
repeated calls by one actor skip that work: a caller `Update` fell from 26,100
allocations to 2,240 and a caller `Delete` from 22,100 to 1,210 against
go/v0.6.8.
`ForPrincipal` caches the built policy set per actor, in a bounded LRU
(`Config.PolicyCache`), and builds each model's policy only when a call first
needs it: a warm `ForPrincipal` went from 25.4 µs and 321 allocations to
0.83 µs and 16. `ResolvePrincipal` still runs on every call, so revocation is
unaffected. `InvalidatePolicies` drops the cache. With `SnapshotActor` set,
the cache is used only with `Config.PolicyCacheKey`. Breaking: `DefinePolicy`
must be a pure function of the actor, and a broken policy fails the calls that
use its model instead of `ForPrincipal`; see
[MIGRATION.md](./MIGRATION.md#policies).

### Errors

**Errors say what went wrong.** `golem.Error` gains `Reason`, `Limit` and
`LimitMax`, and an `Is` method, so `errors.Is(err, golem.CodeForbidden)` and
`errors.Is(err, golem.ReasonNotGranted)` both work. `Field` was always empty;
it now names the field a refusal is about, but only a field the request itself
named. Every limit refusal has the new code `LIMIT_EXCEEDED` and names the
limit, such as `touched_rows`, `outbox_bytes` or `take`, with its maximum;
outbox and event-count overflows used to escape untyped and reach GraphQL
clients as `INTERNAL_SERVER_ERROR`. Value refusals carry `VALUE_TOO_LONG`,
`VALUE_PRECISION`, `VALUE_RANGE` or `INVALID_VALUE` and never echo the value.
GraphQL errors carry `extensions.reason`, and `extensions.limit` and
`extensions.max` for limits. GUIDE.md,
[Limits and error codes](./GUIDE.md#limits-and-error-codes), is the reference.
Breaking: see [MIGRATION.md](./MIGRATION.md#error-codes-and-messages).

**Datetimes finer than a microsecond work.** A `time.Time` with nanoseconds,
which `time.Now()` returns on Linux, was refused in writes and filters, so
code that passed on macOS failed in production. Writes now truncate datetime
and time values toward the past to the field's precision. A filter or policy
operand finer than a microsecond is rewritten to an exact equivalent: `LT(v)`
becomes `LTE(floor v)`, `Eq(v)` matches nothing, and `Ne(v)` matches every
row, NULL included, as it would natively. Unique selectors, cursors, list
operators, scoped reads and analytics refuse such an operand with
`VALUE_PRECISION`, naming the field. Breaking: see
[MIGRATION.md](./MIGRATION.md#datetimes).

### Transactions and operations

**`LockKeys` serializes a check-then-write.** Two transactions that each count
rows and then insert under a limit could both pass the check.
`tx.LockKeys(ctx, golem.MustLockKey("quota", id))` takes ordered advisory
locks on PostgreSQL that hold until the transaction ends, across replicas.
Keys taken after a row lock fail at once with `CONFLICT` instead of risking a
deadlock. On SQLite it only validates the keys. GUIDE.md,
[Lock keys serialize check-then-write](./GUIDE.md#lock-keys-serialize-check-then-write).

**Serializable transactions, with opt-in retry.** `Transaction` takes options:
`golem.Isolation(golem.Serializable)` runs the transaction at PostgreSQL's
serializable level, and `golem.RetryConflicts(n)` runs the callback again, up
to `n` times in all, after a `CONFLICT`. Without options nothing is replayed,
as before. A serialization failure or deadlock at commit was reported as
`P4_RUNTIME_TRANSACTION`; it is now `CONFLICT` under either level. GUIDE.md,
[Serializable transactions and retry](./GUIDE.md#serializable-transactions-and-retry).

**Go code can own a write without `SystemEscape`.** `golem.Within` granted
writes only inside a GraphQL custom mutation, so Go code holding a caller
transaction used `SystemEscape` for writes only an operation should make.
`DefineOperations` with `golem.TxOperation(ops, "name", Fn)` declares a
transaction operation, and the generated `Run(ctx, tx, Fn, args)` runs it on
the caller's transaction with its `Within` grants. The grant ends when the
operation returns; its writes commit with the transaction. Operations never
appear in the SDL and work in kernel mode. GUIDE.md,
[Operations on a transaction](./GUIDE.md#operations-on-a-transaction).

**Operations can count rows the caller cannot read.** Inside a `Within` block
on a transaction operation, `CanCount(predicate)` widens the rows `Count`
sees, and `CanAggregate(predicate, fields...)` the rows `Aggregate` sees, with
only the named fields usable as measures and filters. The widening applies
only to those two calls, only while the operation runs; row reads, `GroupBy`,
events, explanations and writes never see it. `MIN` and `MAX` disclose the
values of a named field. GUIDE.md,
[Counts and aggregates an operation owns](./GUIDE.md#counts-and-aggregates-an-operation-owns).

### Kernel mode

**golem without GraphQL.** `golem.WithoutGraphQL(schema)` in `DefineSchema`
makes `golem generate` skip the SDL, gqlgen and every GraphQL file, so an
application that calls golem only from Go no longer compiles a generated
GraphQL executor or depends on gqlgen and gqlparser. Callers, transactions,
hooks, events, the queue, `Mutate` and transaction operations work unchanged.
`golem.Query` and `golem.ComputedField` are refused with
`P5_GRAPHQL_DISABLED_EXTENSION`. Switching modes needs no migration. Without
the declaration, output is byte-identical to before. GUIDE.md,
[kernel mode](./GUIDE.md#using-golem-from-go-without-graphql-kernel-mode).

### Events and subscriptions

**Delete events reach the subscribers they should.** Filtered subscriptions
never received deletes, and unfiltered ones only when the read policy was
local to the row or crossed one relation; other deletes were dropped with no
signal. A delete is now authorized by evaluating the subscriber's read rule
over the deleted row's snapshot, with queries for the parents it reaches, to
true, false or unknown. Allowed deletes are delivered, denied and filtered-out
ones are not, and when authorization is unknown the subscriber's stream ends
with `GOLEM_SUBSCRIPTION_RESYNC` instead of losing the delete. A parent
deleted in the same transaction, as in a cascade, is judged from its own
snapshot. Read hooks now apply to deletes. GUIDE.md,
[Who receives a delete](./GUIDE.md#who-receives-a-delete). Breaking: see
[MIGRATION.md](./MIGRATION.md#events).

**Bulk deletes fit in the outbox.** A delete stored the whole row in the
1 MiB per-transaction outbox, so deleting 30 rows of 40 KB failed while
creating them succeeded. The snapshot is now a declared set:
`golem.Subscriptions[M](golem.DeleteSnapshot[M](fields...))`, by default the key,
the foreign keys and the bounded scalars. A row whose snapshot still exceeds
`MutationLimits.MaxDeleteSnapshotBytes` (64 KiB by default) is stored with its
identity only, and the delete succeeds. The event schema fingerprint changes
for every subscribable model: drain the outbox before deploying; see
[MIGRATION.md](./MIGRATION.md#events).

**Models hidden from GraphQL can have events.** `golem.Subscriptions` on a
model with `GraphQLHidden()` failed with `P7_SUBSCRIPTION_MODEL_HIDDEN`. GraphQL
still never exposes the model.

**Long-lived subscriptions are re-authorized.** A stream kept its
authorization from the moment it opened. `EventLimits.RevalidateEvery`
re-resolves the principal, re-checks the read grant and re-runs read hooks on
a jittered interval, ending the stream with
`GOLEM_SUBSCRIPTION_REVALIDATION` or the new `GOLEM_SUBSCRIPTION_FORBIDDEN`.
`golem.EventRecheck` and `GraphQLConfig.SubscriptionRecheck` add your own
check. GUIDE.md,
[Re-checking long-lived streams](./GUIDE.md#re-checking-long-lived-streams).

**Streams can be capped per principal.** `EventLimits.MaxSubscriptionsPerPrincipal`
with `Config.SubscriptionPrincipalKey` caps the streams one principal holds in
a process, across Go, WebSocket and SSE, with the new
`GOLEM_SUBSCRIPTION_LIMIT`. Going over the per-connection WebSocket cap is now
an error on that operation instead of closing the connection with 4400.
Breaking: see [MIGRATION.md](./MIGRATION.md#websocket-and-sse).

**GraphQL over Server-Sent Events.** A `POST` whose `Accept` header prefers
`text/event-stream` was refused with `406 SUBSCRIPTION_TRANSPORT_UNSUPPORTED`.
It is now served in graphql-sse's distinct-connections mode: a subscription
sends a `next` frame per event, then `complete`, and a query or mutation one
`next`. An `Accept` that also takes JSON at equal or higher quality streams
subscriptions only. `EventLimits.KeepAlive` sets the keep-alive for SSE and
WebSocket; `WebSocketKeepAlive` remains as a deprecated alias.

**A second process-local publisher is detected.** `events.NewMemoryTransport`
delivers only to subscribers in the publishing process, and two such
publishers on one PostgreSQL database each claimed part of the outbox
silently. `RunEventPublisher` now tries to hold an advisory lock while it runs.
A publisher that cannot take it reports `events.ObservationTopology` and keeps
running; with `EventLimits.RequireExclusiveProcessLocalPublisher` it fails
with `GOLEM_EVENT_CONFIG`. A subscriber in a process with no local publisher
is reported too. GUIDE.md,
[Event transports](./GUIDE.md#event-transports).

**Fix: an expired `Recv` context now reports why.** When the context passed to
`EventStream.Recv` was cancelled or hit its deadline, `Recv` returned
`GOLEM_SUBSCRIPTION_CANCELLED` with no cause, so a caller could not tell a
`Recv` timeout, which leaves the stream open, from a closed stream. The error
now wraps that context's error: `errors.Is(err, context.DeadlineExceeded)` or
`errors.Is(err, context.Canceled)` holds, as does the existing code check, and
the next `Recv` on the same stream receives the next event. When the context
passed to subscribe ends, the error wraps that context's error too. `Close`
still yields `GOLEM_SUBSCRIPTION_CANCELLED` with no cause. The error text gains
the cause as a suffix, such as `GOLEM_SUBSCRIPTION_CANCELLED: context deadline
exceeded`, and `*events.Error` now unwraps; `events.Cancelled(cause)` builds
this error. Over GraphQL WebSocket, an operation whose context is cancelled
because its connection or the server is closing now ends without sending a
`GOLEM_SUBSCRIPTION_CANCELLED` error frame; other terminal codes still send one.

---

## go/v0.6.8

**Fix: `golem migration apply` accepts enum checks on PostgreSQL.** Applying a
migration failed with `application physical fingerprint mismatch` when a model
had an enum with a single value, such as `Platform = twitch`, or a `Check` that
compares an enum field with an enum value, such as `Status.Eq(StatusFailed)`,
alone or combined with another condition such as `FailureCode.IsNotNull()`.
Any `Check` that tests a nullable field with `IsNull` or `IsNotNull` failed the
same way. PostgreSQL stores these checks in a rewritten form, and Golem read them back as
different from the checks it had created. They now match. A check whose live
value or column was changed by hand is still reported as drift.

---

## go/v0.6.7

**Fix: removing and re-adding a link in one nested write works.** A nested
`Update` that combined `DeleteMany` with `Create` on the same relation, such as
`PostTags.DeleteMany(tag = "go")` with `PostTags.Create(tag = "go")` to relink
a tag, always failed. With no existing link it failed with `FORBIDDEN:
mutation is not authorized`, though nothing was unauthorized; with an existing
link it failed with `CONFLICT` from the database's duplicate-key error (23505
on PostgreSQL). The create ran first, so the delete then removed the row just
created, or the create collided with the row the delete was meant to remove.
Now the delete runs first and the link exists afterwards. This affected
v0.6.4 to v0.6.6.

**Behaviour change: the operations on one relation run in a fixed order.** The
nested operations you give one relation field in a single write now run in
three steps, whatever order you list them in:

1. `Delete`, `DeleteMany`, `Disconnect`, `Set`
2. `Update`, `UpdateMany`, `Upsert`
3. `Create`, `CreateMany`, `Connect`, `ConnectOrCreate`

Before, they ran `Create`, `CreateMany`, `Connect`, `ConnectOrCreate`,
`Disconnect`, `Set`, `Update`, `UpdateMany`, `Upsert`, `Delete`, `DeleteMany`.
Operations on different relation fields keep their previous order, and so do
operations within the same step except where listed below. These
combinations on one relation now behave differently:

- `Create` or `CreateMany` with `UpdateMany`, `DeleteMany` or `Set`: these
  no longer reach the rows this write creates. With `UpdateMany` the new rows
  keep the values you created them with; with `DeleteMany` or `Set` they stay
  related. Before, `UpdateMany` changed the new rows too, `Set` disconnected
  them unless it named them, and a caller's `DeleteMany` matching a new row
  failed with `FORBIDDEN`.
- `Create` with `Update`, `Delete` or `Disconnect` naming the row being
  created: now fails with `NOT_FOUND`, because the row does not exist yet when
  the update, delete or disconnect runs. Before, the row was created and then
  updated or disconnected; with `Delete`, a caller's write failed with
  `FORBIDDEN`.
- `Create` with an `Upsert` on the same target: the upsert now creates the
  row, and the `Create` then fails with `CONFLICT`. Before, the `Create` made
  the row and the upsert updated it.
- `Connect` or `ConnectOrCreate` with `UpdateMany` or `DeleteMany`: the rows
  being connected are no longer updated or deleted by them.
- `Connect` with `Update` or `Delete` naming the row being connected: now
  fails with `NOT_FOUND`, because the row is not yet related when the update
  or delete runs. Before, the row was connected and then updated or deleted.
- `Connect` with `Set`: the connected row is now added to the set. Before,
  `Set` ran last and dropped it unless the set named it.
- `Connect` with `Disconnect` of the same row: the row ends up connected.
  Before, it ended up disconnected.
- `Set` with `DeleteMany`: `DeleteMany` now deletes the rows related before
  the write, then `Set` relates what it names. A `Set` naming a row the
  `DeleteMany` deleted fails with `NOT_FOUND`. Before, `Set` ran first and
  `DeleteMany` then deleted the rows of the new set.
- `Disconnect` or `Set` with `Delete` of the same row: the delete now runs
  first, so the disconnect, or a set naming that row, fails with
  `NOT_FOUND`. `Disconnect` with `Delete` of the same row already failed with
  `NOT_FOUND`, the other way round.
- `Update`, `UpdateMany` or `Upsert` with `Delete` or `DeleteMany`: the
  delete now runs first, so rows it removes are not updated first, get no
  update hooks and no update event. An `Update` naming a row the delete
  removed fails with `NOT_FOUND`. Before, a caller's `Update` with a `Delete`
  of the same row failed with `FORBIDDEN`.

**Fix: a row removed later in the same write is no longer reported as
`FORBIDDEN`.** When a caller's nested write created or updated a row and a
later operation in the same write removed it, for example a `DeleteMany` on
another relation field reaching the same table, or a cascade from a delete,
the write failed with `FORBIDDEN: mutation is not authorized`. It now fails
with `CONFLICT: a later nested operation removed a row this mutation wrote`,
and nothing is written. A row that exists but fails your policy still fails
with `FORBIDDEN`.

**Faster writes on PostgreSQL when the policy reads only the row.** Since
v0.6.5 a write locked its rows with one statement and then read them again
with another. Now, when the write's filter, policy and field grants read only
the row being written, one statement locks the rows and returns them as
locked. A single-row update or delete, an `UpdateMany` or `DeleteMany`, and
each nested relation a write touches then run one statement fewer (a
single-row update: 7 statements inside its transaction, now 6). When any of
them follows a relation to another table, the write keeps the separate read
after the lock, so a grant revoked while it waited is still seen. In alternating
single-writer runs, update latency fell by about 0.8ms and throughput rose by
about 12%. Results, lock order and `CONFLICT` behaviour under contention are
unchanged: no deadlocks in contention runs of 32 writers on 10 and 50 shared
rows.

**Fix: a reference through a unique key could be locked out of order.** A
write setting a foreign key that references another row's unique (not
primary) key could, if that value moved to a different row while the write
ran, let the database lock the new row outside golem's lock order, which can
deadlock. Such a write now fails with `CONFLICT`; retry it.

---

## go/v0.6.6

**Fix: `DeleteMany` and `UpdateMany` no longer fail when other writers change
their rows (PostgreSQL, regression in v0.6.5).** In v0.6.5 a `DeleteMany` or
`UpdateMany`, root or nested, failed with `CONFLICT: batch mutation conflicted`
(`mutation conflicted` when nested) whenever another write deleted one of its
rows, changed a row so it no longer matched, or added a row matching its filter
while it ran. Under steady concurrent inserts and deletes on the same rows,
most such calls failed. Now the write changes exactly the rows that match its
filter while it holds them, as a plain SQL `DELETE` or `UPDATE` does, and as
v0.6.4 did: a row another write removed or changed away first is left out, and
a row that starts to match after the write selected its rows is left alone.
The returned count, the hooks and the change events cover exactly the rows
changed. In a contention test of 32 writers deleting and adding comments on
the same 10 posts, failed `DeleteMany` calls fell from about two thirds to none.

**Fix: a cascade no longer fails when a dependent row is removed first.** A
delete whose cascaded dependent was deleted by another write while it ran
failed with `CONFLICT`. Now that row is simply no longer affected and gets no
event. A delete still fails with `CONFLICT` when another write adds a row its
cascade would remove; retry it.

**Behaviour change: a single target that stops matching is missing.** A single
update or delete whose target another write changed, after golem selected it,
so that it no longer matches the target or your policy, now fails with
`NOT_FOUND`, as a target deleted first already did. Before, it failed with
`CONFLICT`.

---

## go/v0.6.5

**Behaviour change: an upsert's create input must name its target, whichever
branch runs.** An upsert, root or nested at any depth, whose create input does
not set every field of its unique target to the target's value is now refused
with `BAD_USER_INPUT` (`upsert create input does not set the target selector`).
A root upsert is refused before any query runs. A nested upsert is refused when
its parent row has been written and before the upsert's own probe or either
branch runs, so the parent's values are known however the parent was selected.
Before, the refusal came only when the create branch ran, so the same call
succeeded or failed depending on whether the row already existed. A
foreign-key field of the target is satisfied by a `Connect` on its relation to
the row holding that value, or by the value the nested parent's row gives it.

**Behaviour change: `ConnectOrCreate` follows the same rule.** A nested
`ConnectOrCreate` whose create input does not reproduce its target is refused
with `BAD_USER_INPUT` (`connectOrCreate create input does not set the target
selector`) at the same point, before its probe or either branch runs. Before,
it created a row under a different identity and linked it whenever the target
was absent. A `Before` create hook that rewrites a nested upsert or
`ConnectOrCreate` create away from its target is refused before the rewritten
row is written; before, a rewritten `ConnectOrCreate` create was linked.

**Behaviour change: arithmetic on a foreign key is refused.** An `Increment` or
`Decrement` on a field that is part of a relation's foreign key is refused with
`BAD_USER_INPUT: foreign key <Field> cannot be changed by arithmetic` before
any statement runs, for every caller, in updates and `UpdateMany`. Set the new
value instead.

**Upgrading.** Give every upsert and `ConnectOrCreate` create input the
target's values, even where the update or connect branch is the one you
expect to run.

**Behaviour change: concurrent PostgreSQL writes no longer deadlock.** Before,
two valid concurrent writes that touched the same rows could deadlock, and
PostgreSQL aborted one after a second's wait. This covers every PostgreSQL
write: creates, updates, deletes, upserts, `UpdateMany`, `DeleteMany`, nested
writes, links and cascades. Cases that deadlocked:

- two updates linking self-related rows to each other in opposite directions;
- a cascade delete of a parent racing an update that relinked one of its
  children;
- a cascade delete of a parent racing a transaction that updated one of its
  children and then created another;
- two transactions that both reference a parent row and then both update it;
- an `UpdateMany` racing a link between the same rows;
- a nested upsert racing a root upsert on the same selector.

Now one write completes and the other either waits for it or fails at once
with `CONFLICT`. A foreign-key value that names no row still fails as before.

**Behaviour change: contention can fail with `CONFLICT`.** Where waiting could
deadlock, a write does not wait: it fails with `CONFLICT: mutation conflicted`
(`batch mutation conflicted` for a batch). It also fails with `CONFLICT` if
another write changed the rows it was about to change, for example deleting
one row of an `UpdateMany`, adding a row that matches its filter, or changing a
row's matching values. Retry the write. A single update, delete or upsert
target that another write deleted first is reported exactly as a missing
target: `NOT_FOUND` for an update or delete, and the create branch for an
upsert. Upserts and nested writes containing an upsert or connectOrCreate
retry the whole attempt before reporting `CONFLICT`, except inside an
application transaction. A PostgreSQL deadlock, lock-timeout or serialization
failure inside a nested write now reports `CONFLICT`. Before, it reported
`BAD_USER_INPUT: mutation request is invalid`.

**Cost.** A PostgreSQL write now runs two more statements for rows the same
transaction has not already written, so a single-row update or delete runs
three statements instead of one. A write that sets a foreign key runs one more
statement, or three when the key references a unique field other than the
parent's primary key; this applies only to relations with a database foreign
key. SQLite is unchanged.

---

## go/v0.6.4

**Behaviour change: a transaction serves one call chain at a time.** A call
that overlaps an active call on the same transaction, without belonging to its
chain, now fails immediately with `P4_RUNTIME_TRANSACTION: transaction used
concurrently; a transaction serves one call chain at a time` instead of
waiting. That covers:

- sibling goroutines in a transaction callback;
- overlapping calls through one hook's executor;
- a hook calling the transaction with a context that isn't its own, which
  previously deadlocked.

Calls made one after another, and calls nested through a hook's context or
executor, are unchanged. Use a transaction from one goroutine; a loop is just
as fast, because one database connection runs one statement at a time. Do
slow non-database work before opening the transaction, and use the queue or a
fresh caller after commit for background work.

**`golem generate`, `check` and `doctor` are about twice as fast.** Each of
them ran its whole build twice, though only the final emission stage reads the
reviewed migration history, and every build ran gqlgen twice although its
unstamped output does not depend on the generation stamp. A project is now
analysed once per command, and gqlgen runs once with its unstamped output
stamped for each build. On the example app, `generate` went from 16.6s to
7.4s, `check` from 19.8s to 7.5s and `doctor` from 16.1s to 6.5s. Generated
output is byte-identical.

**A failed savepoint release no longer leaves state open.** A nested write
whose `RELEASE SAVEPOINT` fails now rolls back its savepoint and closes its
mutation scope.

---

## go/v0.6.3

**A custom mutation can own a write that policy closes to everyone else.** The
pattern "an operation validates, then writes; policy closes the generic path so
callers cannot bypass it" was not expressible. `CannotCreate` also refused the
operation's own `Create`, because a custom resolver runs as the caller and
policy made no exception for it. The only escape, `SystemEscape`, hands out an
unrestricted system client to any code holding the transaction.

`golem.Within(rules, Resolver)` grants a write only while that custom mutation
runs:

```go
func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	owned := Invites.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CanUpdate(owned)
	rules.CannotUpdateFields(golem.All[Invite](), Invites.Status)
	golem.Within(rules, InviteMember).CanCreate(owned)
	golem.Within(rules, AcceptInvite).CanUpdateFields(owned, Invites.Status)
}
```

Callers get no create and cannot change `Status`; the `InviteMember` and
`AcceptInvite` custom mutations can. GUIDE.md, "Operations that own a write",
has the full example.

- `Within` offers grant verbs only: `CanCreate`, `CanUpdate`, `CanDelete`,
  `CanCreateFields` and `CanUpdateFields`. A grant is still a predicate over the
  actor, so the operation writes only the rows it allows.
- Inside the operation a write is allowed when a `Within` grant covering it
  allows it, or when the caller's own policy does. A `Cannot*` rule on anything
  the grant does not name still refuses.
- The grant exists only in the running operation. Generic GraphQL and Go
  writes, calling the resolver function directly, a caller or goroutine that
  outlives the operation, and another mutation in the same document are all
  refused with the caller's usual error.
- A `Within` naming something other than a generated custom mutation fails the
  policy build with an error naming the function.
- `Mutate(ctx, caller, Resolver, args)`, generated for applications with custom
  mutations, runs one from Go through the same dispatch, hooks and observation
  as GraphQL. Each run is observed as `mutation.custom`.

The change is additive: existing policies, resolvers and generated code for
schemas without custom mutations are unchanged. Regenerate to use `Within` and
`Mutate`. `SystemEscape` remains available.

**GraphQL responses list fields in selection order.** HTTP responses and
subscription frames re-encoded their data through a Go map, so every object's
keys came out in alphabetical order. They now follow the operation's selection,
including aliases and fragments. Only key order changes on the wire;
`Server.Execute` still returns maps.

**A failed hook write leaves nothing behind.** A write made through a hook's
`HookExecutor` now runs in its own savepoint. If it returns an error, including
an error from its own after-hook, its rows, events and after-commit hooks are
all discarded. Before, a failed nested create could still commit its rows when
the calling hook handled the error, and a failed scalar, upsert or batch write
failed the whole outer write even when the hook handled it. A hook can now
handle a failed write and continue.

**Transaction-bound handles stop at the end of their scope.** A hook's
`HookExecutor` works only while that hook runs; a call after the hook returns,
from any goroutine, is refused with `P4_RUNTIME_HOOK_EXECUTOR`. A write through
a caller transaction after its callback has returned is refused with
`P4_RUNTIME_TRANSACTION`; before, such a write could commit without its change
event. Calls already in flight finish first. Concurrent writes through hook
executors on one transaction now run one at a time instead of corrupting each
other's savepoints.

A transaction now runs every read and write one at a time, so using one
transaction from several goroutines is safe but not parallel. Inside a hook,
use the context the hook receives, or its `HookExecutor`; a transaction call
made from a hook with an unrelated context waits for the write that ran the
hook, and would never finish.

**Tooling.** The CLI end-to-end tests run in parallel child processes, which
cuts the package's run time roughly in half.

---

## go/v0.6.2

**Take this release if a policy or a filter uses OR.** An unparenthesised
disjunction could escape the restriction it was combined with, so a read
returned rows the caller's policy forbids and a write changed rows outside its
relation.

- **Scoped reads.** The policy and the caller's `Where` were joined as
  `policy AND <predicate>` with neither side wrapped. A top-level `OrScoped`, or
  a disjunctive policy, therefore escaped the other side: a caller allowed two
  rows received three, including another user's unpublished row.
- **Analytics relation hops.** A disjunctive policy on the target of a
  relation hop was appended to the join condition without parentheses and
  escaped the correlation, so counts included rows the caller cannot read.
- **Nested writes.** An OR in a nested `updateMany` escaped the relation guard:
  `Users.Update(alice, Posts.UpdateMany(A OR B))` also rewrote another user's
  post. The same composition appeared in a mutation's selection constraint and
  in the verification of a nested create.

Every place golem combines a policy, predicate or relation condition is now
parenthesised, and one test drives a disjunctive policy and a disjunctive
filter through twenty read and write paths on every provider. No regeneration
or migration is needed.

**Scoped reads refuse an unreadable filter with `FORBIDDEN`.** Filtering a
scoped read on a field the caller may not read, such as a field readable only
on the caller's own rows, is refused with `FORBIDDEN`, as `FindMany` refuses
it. It used to report `BAD_USER_INPUT`, which looked like a malformed query.
Scoped filters on whole-second `time` values now match on SQLite, `Count()` in
`Having` no longer fails on PostgreSQL, and selected `date` values decode on
PostgreSQL.

**Schema-package code can use every generated symbol.** `golem generate`
type-checked a model package against a reduced stand-in for its generated
code, so a resolver or helper there that used `System`, `App`, a selector such
as `ByID`, a semantic or full-text search method, or events failed with
`P1_METHOD_TYPECHECK`, and the project could not be regenerated at all.
Generation now checks such code against the complete generated surface. A new
index and a resolver that uses it work in one `generate`. Projects that already
generated produce identical output.

---

## go/v0.6.1

**GraphQL introspection works.** In every earlier release, setting
`GraphQLConfig.Introspection: true` had no effect. The generated executor
rejected `__schema`, `__type` and a root-level `__typename` with
`BAD_USER_INPUT` before gqlgen could answer them. Even with that fixed, every
list in an introspection query was charged as a page of `MaxPageSize` rows,
and the standard graphql-js `IntrospectionQuery` is 15 levels deep against a
default `MaxDepth` of 12, so no configuration let a real client introspect.

- `__schema`, `__type` and a root-level `__typename`, including
  `mutation { __typename }`, are answered alongside generated roots in the same
  operation.
- Introspection fields cost one each and do not count toward `MaxDepth`. The
  standard `IntrospectionQuery` passes at default limits. Data fields selected
  beside introspection are costed and depth-limited exactly as before,
  including inside fragments.
- An operation may select at most one `__schema` root and eight `__type` roots;
  more is refused with `QUERY_LIMIT_EXCEEDED`.
- With `Introspection: false`, `__schema` and `__type` are still refused with
  `GRAPHQL_VALIDATION_FAILED`. `{ __typename }` works either way.
- Introspection exposes exactly what `SDL()` publishes; hidden models, fields
  and operations are absent from both.
- A GraphQL server built without an executable schema refuses these meta roots
  with `GRAPHQL_VALIDATION_FAILED` instead of omitting them. Generated
  applications always have one.

- An operation that selects only these meta fields resolves the principal, so a
  caller your `ResolvePrincipal` refuses is refused exactly as for a data query.
  It builds no model policy, opens no transaction and issues no SQL.

GUIDE.md now documents enabling introspection. No migration is needed.
Introspection works without regenerating, but code generated by v0.6.0 still
builds every model's policy for a meta-only operation. Regenerate to get the
lighter path, which adds `runtime.(*App).AuthenticatePrincipal` to the
generated wiring.

---

## go/v0.6.0

**This release changes behaviour you may depend on.** Three changes can make
code that worked on v0.5.4 fail or behave differently; read them before
upgrading. Everything else is a fix that needs no action, apart from one
optional regeneration described under "Regenerating".

### Changes that need your attention

**A caller can link only to a row it can read.** Every way a caller writes a
foreign key now requires the target row to be readable by that caller: create,
update, update-many, upsert, nested `Connect`, `ConnectOrCreate`, set and
disconnect, through the programmatic clients, `HookExecutor` and GraphQL. A
target the caller cannot read is reported exactly like a target that does not
exist. In v0.5.4 a direct foreign-key write checked nothing about the target,
so a caller could attach a row to one it could not read, and learn whether an
ID existed by comparing the outcome with a random ID. System clients and
foreign-key values written by hooks are not checked. Field modes apply as
before.

`Connect` and `ConnectOrCreate` now require read access to the target instead
of update access. A caller who could read a row but not update it used to get
`NOT_FOUND` from `Connect`, and `ConnectOrCreate` then tried to create a
duplicate and failed with `CONFLICT`; both now succeed. `ConnectOrCreate`
against an existing row the caller cannot read reports not found instead of
attempting a create.

**Cascaded deletes now produce change events.** Deleting a row whose
dependents are declared with `OnDelete(golem.Cascade)`, `SetNull` or
`SetDefault` now locks those dependents in the same transaction and emits a
delete or update event for each, and marks their semantic index entries the
way an ordinary delete or update does. In v0.5.4 the database removed or
changed them silently: subscribers never heard of it, and a cascaded row's
semantic vector stayed stored until a full reconcile. Permission to delete the
parent covers its dependents, as before; subscribers still receive only events
for rows their own read policy allows. A delete whose cascade would touch more
rows than `MutationLimits.MaxTouchedRows` (1,000 by default) is now refused
whole with the row-limit error instead of proceeding. That refusal counts every
dependent, including rows the caller cannot read, so it can tell a caller that
more dependents exist than the limit allows.

**A subscription that may have missed events now ends.** When the event
transport drops or fails mid-stream, every affected subscription ends with the
new code `GOLEM_SUBSCRIPTION_RESYNC` (`events.CodeSubscriptionResync`). Before,
the hub reconnected behind the subscriber's back and anything published during
the gap was lost without a signal, on both the in-memory and NATS transports.
On this code, refetch the state you derive from events and subscribe again, as
you already do for `GOLEM_SUBSCRIPTION_OVERFLOW`. GraphQL clients receive the
code unchanged.

On NATS, losing the connection to the broker now ends every open
subscription this way. Core NATS does not replay messages, so an event
published while a subscriber's connection was still reconnecting used to be
lost without a signal, even when both nodes reconnected within milliseconds.
Events published during the outage are not redelivered to subscribers whose
stream ended; they refetch.

For the same reason, `Subscribe` now returns only once the event source is
connected. A transient connection failure is retried while it waits, and
cancelling the context ends the wait; while NATS is down, a new subscription
waits for the connection to return. It used to return at once and retry in
the background, dropping events published before the connection succeeded.

### Regenerating

Upgrading the module alone requires no migration, and a database created by
v0.5.4 opens and runs unchanged. Regenerating your schema brings two
improvements, each as a reviewed migration:

- **Event delivery claim index.** The migration admits a new index,
  `golem_outbox_delivery_claim`, which golem creates the first time it claims
  deliveries, the same way v0.5.0 created the queue-history indexes. The
  migration's SQL is empty. Once the index exists, an older library refuses to
  start because of it. To go back after applying the migration, drop
  `golem_outbox_delivery_claim` and delete that migration's ledger row.
  Rolling the module back without having applied the migration needs none of
  this.
- **Full-text normalization, if you have a full-text index.** Each index moves
  to the current normalization through one reviewed rewrite: its storage and
  triggers are dropped, recreated and backfilled inside the migration
  transaction. Until you apply it, existing indexes behave exactly as in
  v0.5.4.

Migrations written by v0.6.0 no longer record approvals for constraints that
are dropped and re-added while still accepting every existing value, such as
those renamed by a table or column rename. The v0.5 command-line tool reads
such a migration as missing an approval, so author and apply migrations with
the v0.6.0 tool once you start using it.

### Fixes

**Full-text matching.** On a newly generated or rewritten index, case is
folded independently of the database: on a PostgreSQL database created with
`LC_CTYPE=C`, `καφές` now finds `Καφές`. Indexed text and queries are
normalized to Unicode NFC in both folding modes, so text stored decomposed is
found by a composed query; under `FoldNone` on SQLite, writes to such an index
therefore need a connection opened through golem, as `FoldDiacritics` writes
already did. `"service fe"*` is now accepted as a phrase whose last word is a
prefix; it used to fail with a misleading error.

**SQLite rows containing an empty `Bytes` value read correctly.** The SQLite
driver golem uses reads every column after a zero-length BLOB from the wrong
position: integers came back as 0, text as `""`, later columns took their
neighbour's value, and only a type check such as a UUID raised an error. Golem
now returns `Bytes` columns from SQLite in a form the driver reads correctly.
Golem's own writes never stored an empty BLOB before this release, so a v0.5
application was affected only if one reached a `Bytes` column another way,
such as raw SQL, another writer or imported data. If yours could have, rows
read through v0.5 may have carried shifted values into anything derived from
them.

**Values.** Strings containing a NUL byte are rejected with `BAD_USER_INPUT`
before any SQL runs, on both providers. On SQLite they used to be stored and
then match predicates incorrectly, because SQLite's string functions stop at
NUL; rows already stored that way still read. An empty `[]byte` is stored as
empty instead of NULL, on every write path, and reads back as an empty,
non-nil slice. `golem.NewDecimal(0, scale)` returns `Decimal{}`, so a
zero reads the same on both providers. When an upsert takes its create
branch, its create input must set every field of the target to the target's
value, or the upsert is refused with `BAD_USER_INPUT` and nothing is created.
Before, an upsert by ID whose create input left the ID to a default created a
row with a different ID, so repeating the upsert kept creating unrelated rows.
Set the ID explicitly in the create input, or target a field you do set. A nullable computed field over a
masked value resolves to null instead of an internal server error.

**Queue and events.**
- A single failed lease renewal no longer cancels a running job or event
  publish. The lease is treated as lost only when renewal is refused or the
  lease has expired. Before, a `MaxAttempts: 1` job could be finalized as
  failed although its handler never failed.
- A batch acknowledged during shutdown is recorded instead of being delivered
  again after the lease expires.
- A job that succeeds while the worker is shutting down is recorded instead of
  running again on restart.
- One full subscriber on the in-memory transport no longer stalls delivery to
  every other subscriber for up to five minutes; only its own stream ends.
- A slow subscribe no longer blocks a WebSocket connection's pings and stops.

**Migrations.**
- Renaming a table or key column that another table references now applies on
  both providers. It used to leave migration history stuck.
- Reordering fields works: SQLite rebuilds the table, and PostgreSQL, which
  cannot reorder columns in place, no longer compares physical column order in
  drift checks. PostgreSQL used to refuse any field not added at the end.
- SQLite can raise or remove a string's length limit.
- Drift checks now notice a timestamp or time column gaining or losing its
  time zone; they used to report such a schema as current.
- Renames, raised length limits and newly optional columns no longer ask for a
  `DATA_LOSS` approval. When approvals are missing, all of them are listed at
  once with a ready `--approve` line.
- The errors for a required field without a default, and for a missing
  migration file, now name the field and the real path.

GUIDE.md now documents approvals, what each provider changes in place, and
drift.

**Costs that grew with data.** Measured at a small and a large size, per
operation:

| Operation | Sizes | SQLite before | SQLite after | PostgreSQL before | PostgreSQL after |
| --- | --- | --- | --- | --- | --- |
| Queue claim | 1k → 100k jobs | 0.26 → 24.8 ms | 0.15 → 0.15 ms | 0.78 → 25.7 ms | 0.83 → 0.91 ms |
| Event delivery claim, with the index | 1k → 100k groups | 0.13 → 17.9 ms | 0.03 → 0.02 ms | 2.2 → 24.8 ms | 1.4 ms at 100k |
| SQLite event retention | 2k → 200k groups | 1.28 → 180 ms | 0.16 → 0.19 ms | — | — |
| Batched relation loading with `take` | 200 → 20k children per parent | 6.7 → 315 ms | 0.29 → 0.28 ms | 1.8 → 61 ms | 2.3 → 1.0 ms |

The queue claim change needs no migration. Batched relation loading walks an
index in order when the sort fields are not nullable, and on PostgreSQL text
fields only under the C collation; otherwise it takes each parent's top rows
without sorting the whole batch. A long-running SQLite process now refreshes
planner statistics every four hours instead of only at close, removing
full-text shadow-table statistics in the same step. A failed refresh is
reported to `Config.Observer` as a `runtime.maintenance` operation, and the
schedule continues.

On PostgreSQL, the delivery claim index is built with `CREATE INDEX
CONCURRENTLY` by one node at a time, so outbox writes continue while it builds,
and nodes without the lock keep claiming without it. On both providers an index
that is dropped later is rebuilt.

---

## go/v0.5.4

**Take this release if you use full-text search on SQLite.** In v0.5.3, every
write to a table carrying a full-text index got slower as the index grew. Over
20,000 inserts the last rows cost eight to twelve times as much as the first;
this release keeps them flat.

| 20,000 inserts after a migration | First 2,000 rows | Last 2,000 rows | Last ÷ first |
| --- | ---: | ---: | ---: |
| v0.5.3 | 196–203 µs/row | 1,652–2,336 µs/row | 8–12× |
| v0.5.4 | 220–284 µs/row | 221–230 µs/row | 0.8–1.0× |

The cause was planner statistics. Applying a migration ran `ANALYZE` while the
full-text index was still nearly empty, recording that its internal storage
held two rows. FTS5 reads that storage by rowid range on every write, and the
planner, trusting the statistic, chose to scan the whole table instead. The
table grew with every write, so every write got slower. Closing the database
refreshed statistics the same way, so it could poison an index the session had
never touched.

Migrations now analyze only real tables, never the shadow tables SQLite keeps
behind a virtual table, and closing the database removes any shadow-table
statistics its refresh recorded. Close still refreshes statistics for tables
written during the session, which ordinary indexes depend on after bulk
ingestion.

**A database v0.5.3 already poisoned is repaired on open.** Opening removes
stale shadow-table statistics and replaces the pooled connections that had
loaded them: a connection keeps the statistics it has read even after they are
deleted, so removing the rows alone would leave those connections scanning. You
do not need to regenerate or migrate.

Ordinary tables were never affected. The repair covers every shadow table
SQLite reports, not only full-text ones, so it also removes the statistics
v0.5.3 recorded for the `vec0` table behind a semantic index created before
v0.5.1 and not migrated since.

**The semantic outage cost is corrected.** SEMANTIC.md said an outage costs at
most five provider calls per pass. That holds once an index has stored a
document; an index with nothing stored yet has no document to probe with,
isolates the refused batch instead, and costs up to ten.

---

## go/v0.5.3

**SQLite full-text search now ranks in one linear pass.** The default ranking
counts distinct matched terms per field and applies the field's declared
weight, preserving caller isolation without corpus-wide statistics. An
opt-in BM25 mode is available when corpus-wide term frequency is appropriate.
The ranking query no longer executes a correlated `MATCH` subquery for every
result candidate. Enabling or disabling BM25 on an existing index is a reviewed
rewrite migration. SQLite BM25 can reveal aggregate corpus influence through
scores and ordering, so multi-principal applications should retain the default.

Measured on 200,000 roughly 2 KB documents on an Apple M3 Pro, using weighted
subject, participants and body fields (3/2/1) and five repetitions after
fixture creation:

| SQLite default-ranking query | Median |
| --- | ---: |
| One term, 100 matches | 0.51 ms |
| One term, 1,000 matches | 1.58 ms |
| One term, 10,000 matches | 12.58 ms |
| Three terms | 13.52 ms |
| 10,000-match term with a 0.1% predicate | 7.97 ms |
| Common term, 200,000 matches | 259.51 ms |

The SQLite Unicode-folding callback now returns ASCII text directly. On the
same machine, a 2,070-byte ASCII value fell from 35.5 µs and two allocations
to 0.84 µs and one allocation; non-ASCII folding semantics are unchanged.

Synchronous FTS5 maintenance remains intentionally transactional. A 50,000-row
benchmark with roughly 2 KB bodies measured 36.9 µs per unindexed row and
344.2 µs per indexed row. Profiling at 200,000 rows showed that even a bare
FTS5 insert with no Golem key table, folding, replacement cleanup or additional
trigger statements costs 4.25–4.72 times the owner-table insert. The lower
overhead target proposed during development is therefore not attainable without
weakening immediate search consistency or removing phrase, field or BM25
behavior; this release keeps those guarantees.

**Search APIs are complete across execution modes.** Full-text search has an
additive `TextSearch…Select` form so callers can avoid hydrating large fields.
Full-text and semantic Search/Similar methods are generated for caller and
system transaction clients, and every stage runs on the transaction-bound
executor. Multiple predicates are accepted and combined with `AND`; existing
`TextSearch…(...Predicate)` call sites remain source-compatible.

Committed 200,000-row benchmarks on the same machine measured primary-key and
unique lookups at 107 µs and 88 µs. Indexed ranges returning 20, 200 and
2,000 rows took 94 µs, 324 µs and 7.48 ms. Through the generated public API,
exact SQLite semantic Search and Similar at 1,024 dimensions took 863 ms and
882 ms unfiltered, and 3.2 ms and 4.0 ms with a 0.1% candidate predicate.

**Full-text failures now preserve their public boundary.** Invalid query
syntax and request limits return `BAD_USER_INPUT` with a specific safe reason.
Database execution, streaming and decoding failures follow the internal-error
path instead of being presented as caller mistakes.

`golem doctor` now reports the drifting object's type, name and owning table in
both human and JSON output. Applying a SQLite migration refreshes planner
statistics after schema verification, and closing a verified SQLite handle
runs bounded planner optimization so statistics reflect later ingestion.
Removing a Golem-derived semantic or
full-text index no longer needs a data-loss approval, while historical plans
that already recorded the exact legacy approval remain valid.

SQLite full-text indexes using the default `FoldDiacritics` mode require
inserts and indexed-field updates to use a connection opened through Golem's
SQLite provider. Those triggers call Golem's Unicode-folding function, which a
plain `sqlite3` connection does not register. PostgreSQL direct SQL writes are
unchanged.

---

## go/v0.5.2

**First-class full-text indexes are available on SQLite and PostgreSQL.**
`FullTextIndex` declares weighted local text fields and generates
`TextSearch…` caller/system methods plus a caller-only GraphQL root. Storage is
maintained by database triggers in the model write transaction, reviewed
migrations backfill existing rows, and ranking joins the authorized candidate
query before applying the result limit. Indexed-field read masks are part of
candidate authorization. User query text is parsed as literal OR terms,
phrases and bounded prefixes rather than passed to FTS5 or `tsquery`.

SQLite uses contentless-delete FTS5 storage and PostgreSQL uses a `tsvector`
side table with a GIN index. Both providers share canonical Unicode diacritic
folding. PostgreSQL requires UTF-8 and the deterministic
`pg_catalog."und-x-icu"` collation. Regenerate and apply the reviewed migration
before calling generated full-text methods.

**SQLite predicates no longer wrap every condition in `CASE`.** Equality adds
the one required `IS NOT NULL` guard, while other predicates retain their own
NULL behavior. Indexed primary-key and selective candidate predicates are
sargable again without changing three-valued policy semantics.

---

## go/v0.5.1

**Semantic search no longer makes SQLite scan a `vec0` virtual table before
authorization.** Current SQLite semantic vectors use a strict,
dimension-checked BLOB table, and the exact ranking statement drives from the
authorized candidate query before calculating cosine distance. PostgreSQL
continues to use `pgvector` and exact ranking. A reviewed migration replaces
the derived SQLite vector cache, marks existing semantic rows pending on both
providers, and a deduplicated startup drain rebuilds them with the current
embedding contract. Existing semantic rows remain unavailable to ranking until
that rebuild succeeds, so the embedding provider must be available during the
upgrade.

**Embedding providers can distinguish indexed documents from search queries.**
`embedding.Input.Purpose()` reports `PurposeDocument` or `PurposeQuery`, so
providers can correctly map vendor APIs that require separate document and
query modes. Indexed text is now readable newline-separated values; Golem
retains its binary-safe framing only for change detection. The embedding-contract
version participates in the space fingerprint, so vectors produced under the
old contract never mix with new results.

---

## go/v0.5.0

**Golem can now add a column to its own internal tables on an existing
database.** A semantic shadow state table carries a declared contract version,
and a reviewed step between two versions is planned as an additive
`ALTER TABLE ... ADD COLUMN` rather than a drop and recreate. Your vectors are
never discarded and nothing is re-embedded. The first use of this is
`ambiguous_strikes`, which bounds how long the drain retries a document the
embedding provider keeps refusing without saying why: five strikes, then the
row is quarantined with `error_code` `EMBEDDING_REFUSED_UNCLASSIFIED`. A strike
is only ever charged in a pass where a provider call succeeded, so an outage
can never quarantine a healthy row.

**Three operator-history indexes on `golem_queue`.** Measured on the same
200,000-row queue on both providers, best of repeated runs:

| Operation | SQLite before | SQLite after | PostgreSQL before | PostgreSQL after |
| --- | --- | --- | --- | --- |
| `List`, no state filter | 146 ms | 0.12 ms | 18.5 ms | 0.03 ms |
| `List`, `status='succeeded'` | 76 ms | 0.05 ms | 18.5 ms | 0.03 ms |
| `List`, two states | 11.5 ms | 0.08 ms | 5.1 ms | 0.13 ms |
| `ListFailed` | 6.8 ms | 0.05 ms | 3.9 ms | 0.05 ms |
| Retention, periodic batch | 51.9 ms | 0.68 ms | 8.2 ms | 0.82 ms |
| Retention sweeping half the table | — | — | 11.1 ms | 6.6 ms |

Every shape improves on both providers. The one modest case is a PostgreSQL
retention run whose cutoff sweeps half the table: a parallel sequential scan is
genuinely the right plan there, so the index only takes it from 11.1 ms to
6.6 ms. A periodic retention run, which is what a deployment actually issues,
is ten times faster.

The index set is `golem_queue_enqueued (enqueued_at, id)`,
`golem_queue_history (status, enqueued_at, id)` and a covering
`golem_queue_terminal (status, finished_at, id)`. On SQLite, retention still
merges one ordered run per state, so its 76x comes from the index being
covering, not from the sort disappearing. SQLite names the index explicitly, because without
`ANALYZE` its planner otherwise picks the claim index and sorts; forcing the
plain `(enqueued_at, id)` index instead would have made a rare-status `List`
around a thousand times *slower*, which is why the set is shaped this way.

**Both changes require regenerating and applying one migration, and in
exchange a library upgrade on its own stays reversible.** Golem creates the new
indexes only when your generated schema admits them, so bumping the module
without regenerating changes nothing and can be rolled back. Regenerating and
applying the migration is the deliberate one-way step, exactly like every other
golem migration. Until you take it you get no speedup and no strike bound. One
regeneration delivers both.

Golem's migrations are forward-only. If you need to go back *after* applying
this one, drop `golem_queue_enqueued`, `golem_queue_history` and
`golem_queue_terminal`, delete this migration's ledger row, and drop
`ambiguous_strikes` from each `_golem_semantic_*_state` table — on SQLite that
last one is a table rebuild. Rolling the module back without having applied the
migration needs none of this.

**A healthy document in a refused batch is now stored instead of waiting.** An
unclassified refusal is retried one row at a time, so only the culprit stays
pending — including the rows after it in its own batch and every later batch in
the page. The outage budget grew to pay for that: a total outage now costs at
most five provider calls per pass rather than two — two batch calls, after which
the pass stops for the rest of its page, and up to three re-embeds of
already-stored documents that prove the provider is answering before any strike
is charged. Finding which document in a refused batch is at fault waits until
the provider has answered, so it costs nothing during an outage; when it runs it
is bounded at eight calls a pass. An index with nothing stored is the exception
and costs up to ten: it has no document to probe with, so isolating the batch is
the only way it can learn anything, and it does that until one document embeds.
Neither count grows with the number of batches in the page.

**Startup now verifies every index on `golem_queue` on both providers, not
just `golem_queue_dedupe`.** `golem_queue_claim` and `golem_queue_exclusive` are
checked for the first time, so a database whose claim index was altered by hand
will now fail startup with an error naming the object and telling you to drop
it. A database created by any release from go/v0.3.0 through go/v0.4.0 passes
unchanged. **An index you added to `golem_queue` yourself is now refused** —
golem owns that table; move the index to one of yours.

---

## go/v0.4.0

**Security: a caller can no longer set a moded foreign key through a
relation.** Writing `author_id` directly was refused when the field was
`system`, `readOnly`, `immutable` (on update) or `hidden`, but connecting,
setting or disconnecting the relation wrote the same key unchecked. Every
earlier release allowed it. The relation write is now refused with the same
message and public code as the direct write, whether or not a hook runs. A
system client may still assign a `system` key. If your application relied on a
caller connecting over a moded key, that write now fails; give the key the
mode you actually want it to have, or perform the assignment with
`SystemEscape`.

**Hooks can author a `system` field on every write path.** v0.3.2 left
nested-child hooks, upsert, versioned mutations, update-many batches and
`HookExecutor` refusing one. Each now accepts a field the hook wrote and still
refuses one the caller's own input named, whatever the hook does to it.
`HookCreateRow` can now create a row whose required `system` field a hook
supplies. A versioned upsert on a model with a required `system` field could
never run; it now does.

**SQLite similarity ranking is exact.** It asked sqlite-vec for a window of
nearest neighbours and filtered afterwards, and when more rows sat at the same
distance than the window held, it returned the wrong ones. It now runs the same
exact query as PostgreSQL. A search is one statement.

**An embedding-provider outage no longer dead-letters the drain.** An outage,
rate limit or unclassifiable error used to spend an attempt, and after five the
drain job was dead-lettered; it could also quarantine every row in the batch as
if the documents were bad. Only a refusal the provider marks as invalid input
quarantines a row now. Everything else retries without spending an attempt,
one refused row no longer holds up the rest of the index, and an outage costs
at most two provider calls per pass. See SEMANTIC.md.

**Event publishers no longer take more leases than they can work.** A publisher
claimed `ClaimRows` groups at once but ran `PublisherConcurrency` of them, so
the rest sat leased and unrenewed, and another publisher could take them over
and deliver the same event twice. A claim is now capped at
`PublisherConcurrency`; the default `ClaimRows` of 64 behaves as 8 under the
default concurrency. On shutdown, leases that never started are released
instead of stranded until they expire. The claim observation reports the size
actually requested.

**`events.RetentionDisabled` turns event retention off.** There was no way to
do it. `Limits.RetentionEnabled()` reports it.

**The queue checks its own tables.** A same-named `golem_queue` without its
primary key, or a `golem_queue_dedupe` of any other shape, was accepted, and
deduplication quietly stopped working. Startup now refuses it. Tables created
by v0.3.0 through v0.3.3 pass unchanged. On SQLite a deduplicated enqueue is
now one statement, so a job finishing mid-enqueue can no longer make it fail.

**Queue finalization failures are observed.** When the store rejected a job's
completion, retry, cancel or release, the error was discarded. It is now
observed as a queue commit failure. A lease another worker fenced stays silent,
as before.

**`queue.Backoff.Delay` is defined for every value.** Negative values panicked,
and a large base with a large attempt looped for about 2⁶³ iterations. A zero or
negative `Base` or `Cap` now takes the default.

**Built with Go 1.25.14.** `go.mod` pins the toolchain; 1.25.2 had twenty
reachable standard-library vulnerabilities.

Outbox rows written by something other than golem — manual SQL, a partial
restore — are not delivered until the publisher restarts. Restart it after
inserting them.

---

## go/v0.3.3

**Take this release if you ever set `queue.RetentionDisabled`.** It did the
opposite of what it says, in every release that offered it — v0.3.0, v0.3.1
and v0.3.2. Each shipped the constant and a QUEUE.md instructing its use,
and none gated the worker on it.

`RetentionDisabled` is a negative duration, and the worker scheduled its
next pass with `now.Add(RetentionEvery)` — permanently in the past. The
configuration documented as turning retention off ran a retention `DELETE`
on every dispatch iteration instead of never.

`RetentionAge` still gated the delete, so recent rows survived and nothing
looked wrong. What was lost is what the setting was chosen to keep: terminal
jobs past the age, deleted hard, nothing archived, nothing logged on the
success path. If you also lowered `RetentionAge` toward its one-hour
minimum, you lost more.

**If you ran any of v0.3.0, v0.3.1 or v0.3.2 with retention disabled, your
job history was trimmed to `RetentionAge` regardless.** golem cannot recover
it; restore from your own database backups if you need it.

---

## go/v0.3.2

A field an application owns and no client may set.

`golem:"system"` withholds a field's write builders from caller clients and
keeps them on system clients; `golem:"system;immutable"` keeps only create.
The field never enters a generated GraphQL input, stays readable, and a
policy granting it to a caller does not override the mode.

`SystemEscape(tx)` returns a system client bound to the caller's own
transaction, so a write the caller may not perform commits or rolls back
with the work that caused it. Every call is observed as
`transaction.system_escape`. It reaches extension bodies and hooks, where
custom mutations live.

A hook may author such a field. A field the caller's own input named stays
refused whatever a hook does to it, so a hook chain cannot launder a value.

Nested-child hooks, upsert, versioned mutations and update-many batches
still refuse a hook-authored system field. Each fails closed.

No migration is required. A schema declaring no `system` field generates
byte-identical output.

---

## go/v0.3.1

Fixes an upgrade blocker in v0.3.0. Anyone on v0.3.0 with a semantic index
should take this release; anyone on v0.2.1 should upgrade straight to it.

**An application could not apply migrations authored before v0.3.0.**
v0.3.0 added an identity projection to semantic managed storage, and
migrations predating it carry none — which the renderer already handled by
replaying sealed history in the shape it was reviewed in. The post-apply
introspection did not honour that, re-rendering the same extension without
the replay flag and refusing DDL golem had just written. Applying a
pre-existing initial migration failed even against an empty database, on
both providers, with no way forward but discarding migration history.

### Upgrading from v0.2.1

```
golem migration new --schema ./app --name semantic_identity
golem generate --schema ./app --app-out ./app
golem migration apply --provider sqlite --dsn "file:app.db"
```

That order is enforced: `generate` refuses a schema with no migration for
it and says so. Migration names must match `[a-z][a-z0-9_]{0,62}`, so
`semantic_identity` is accepted and `semantic-identity` is not.

This carries an existing database to current storage in place; no reset is
needed.

---

## go/v0.3.0

The first release intended for use from another application.

### Semantic indexes

Declaring an index generates two capabilities rather than one — search by
text, and find rows like this row:

```go
caller.Notes.SearchRelated(ctx, "flour water", 10)
caller.Notes.SimilarRelated(ctx, notes.Notes.ByID.Value(id), 10)
```

Search authorizes before it ranks: the candidate set is the rows the caller
may read, and ranking happens inside it. Ranking first and filtering
afterwards would let distance disclose the existence and rough content of
rows the caller cannot see.

Similarity resolves its source through an ordinary authorized read, so a key
parameter cannot enumerate hidden rows. Absent, unreadable, and
identity-masked sources produce one indistinguishable `CodeNotFound`. The
source is excluded from its own results before ranking.

Embedding runs on the queue: writes mark rows stale and a worker embeds them,
so rows become searchable shortly after they are written rather than
immediately. You supply the embedding provider; golem never calls a vendor
for you.

See [SEMANTIC.md](./SEMANTIC.md).

### The durable queue is reachable

`Enqueue`, `QueueOperator` and `RunQueueWorker` are generated onto the
application, and enqueueing can share a transaction with the write that
caused it — the job row commits with your data or neither does. Both
providers behave identically.

See [QUEUE.md](./QUEUE.md).

### Serving a frontend

`render` serves a single-page application's shell and rewrites its metadata
per route, so a crawler asking for `/n/42` sees that page's title rather than
the shell's. It needs no schema or database.

See [RENDER.md](./RENDER.md).

### Errors name what they refuse

Configuration and limit failures name the field and the bound rather than
returning an opaque code:

```
GraphQL limit MaxDepth is 33, outside the portable range 1..32
```

Detail is golem-authored and printed; an untrusted cause stays available to
`errors.Is` and is never printed, so provider credentials and payloads cannot
reach an error string. A type-checked test enforces that at every call site.

### Documentation that runs

Five pages, each executed by a test that extracts its code, writes it into an
empty directory, generates, migrates and runs it. See
[README.md](./README.md).

---

## Behaviour worth checking before you upgrade

**Queue retention is on by default.** Workers delete terminal job rows older
than thirty days, checked every minute. Earlier versions deleted nothing. To
keep history:

```go
limits := queue.DefaultLimits()
limits.RetentionEvery = queue.RetentionDisabled
```

**A stale semantic row does not rank.** When a write changes an embedded
column the row is excluded until re-embedded, and a similarity request whose
source is stale fails rather than ranking on a vector known to be out of
date.

**No full semantic reconcile runs by default.** `SemanticReconcileInterval`
is zero unless set, so golem repairs drift introduced through its own write
path and nothing else. If anything else writes to your database — raw SQL, a
restore, another service — set an interval.

**Declaring a version token removes nested relation methods.** Nested inputs
cannot express a per-row version expectation, so generated code omits them
rather than accepting one it cannot honour.

**Error strings changed.** Classification did not: `Is`, `CodeOf` and every
switch on an error code behave as before. Only the text differs, so anything
matching on message text will need updating.

---

## Known limits

Not implemented, and not planned without a separate proposal: MySQL,
federation, automatic production migration, raw SQL through authorized
surfaces, a second policy engine, online migration orchestration, distributed
SQLite, and vendor CDC drivers.

Queue recurring or cron scheduling and job priority are deliberate omissions.
A cron loop calling `Enqueue` with a dedupe key is a few lines of application
code; building it in drags timezones, drift and leader election into a queue
that has none of them.

**Transport availability differs by provider.** SQLite is the single-node
profile with a process-local event transport; the NATS adapter is
unavailable on it. PostgreSQL is the multi-node profile where instances share
one database and use NATS for cross-process fan-out. Provider portability
means the same model, policy, query, mutation, event and error contracts — not
identical transport, scaling or failover topology.

# P7 events, subscriptions, outbox, and CDC plan

Status: **implementation and verification complete; final commit identifier pending**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 3, 5, 8, 16,
17, 20, 21, and 23. P1 owns stable schema identities, physical schemas, and
migrations. P2 owns policy construction, implication, and field
classification. P3 owns authorized reads and exact row decoding. P4 owns
commit-derived mutation facts and their atomic insertion into
`_golem_outbox`. P5 owns the generated GraphQL host and reserved subscription
names. P7 composes those contracts; it does not create a second policy, read,
mutation, scalar, or GraphQL engine.

The exact application-facing contract is frozen in
[`PUBLIC-EVENT-ABI.md`](./PUBLIC-EVENT-ABI.md). Completion is governed by
[`P7-EVIDENCE.md`](./P7-EVIDENCE.md). P7 is not complete while any mandatory
ledger row is `PENDING`, `FAIL`, skipped on a required provider, or supported
only by prose.

## 1. Definition of down

P7 is done when a freshly generated social application can do all of the
following without handwritten event, authorization, or GraphQL resolver code:

- publish every committed P4 created/updated/deleted fact from the durable
  outbox with a stable event ID and explicit at-least-once semantics;
- recover from a crash before claim, during a lease, after transport acceptance
  but before acknowledgement, and during cleanup without losing an event;
- run multiple publisher workers on SQLite or PostgreSQL without concurrent
  ownership of one live lease;
- preserve declared `transaction_ordinal` order within one causation while
  making no false global-order or exactly-once claim;
- expose generated typed Go event identities and a versioned public envelope;
- expose a generated caller event stream that uses the same fresh evaluator as
  GraphQL, without System/Tx/raw-event publishing capabilities;
- expose one generated GraphQL subscription root for every explicitly
  subscription-enabled model;
- re-resolve the retained principal, rebuild model policies, re-read or
  privately evaluate the entity, classify the filter/selection, and authorize
  every event in a new execution;
- deliver creates, updates, deletes, composite identities, nested-write facts,
  and one event per affected batch row with the documented entity semantics;
- disconnect a slow subscriber with a stable overflow reason instead of
  dropping an event and continuing;
- tear down source membership, evaluation work, loaders, goroutines, and
  transport state on cancellation;
- report bounded, sanitized publisher/subscription metrics without allowing an
  observer failure to change correctness;
- state and prove that external SQL writes are invisible when no CDC adapter is
  installed; and
- route every installed CDC adapter through the same fact validation,
  authorization, hub, and GraphQL delivery path.

Completion requires live SQLite and PostgreSQL tests, restart and crash-point
tests, race/repeat/fuzz tests, independent authorization oracles, and every
named mutation in `P7-EVIDENCE.md` being killed. Reserved names, generated
method signatures, a unit-only queue, or a happy-path publisher are not
completion evidence.

## 2. Exact boundary

### 2.1 Included

- The already-committed P4 fact codec `golem.fact.v1` and immutable
  `_golem_outbox` V1 rows as the authoritative Golem-write source.
- A separate provider-managed delivery-state system object. Delivery state is
  mutable; fact rows are not.
- Bounded claim, lease, retry, acknowledgement, blocked-fact handling, and
  retention.
- PostgreSQL multi-worker claiming and SQLite single-writer-compatible claiming
  with identical observable semantics.
- A closed event transport SPI used only for Golem event notices and streams.
- A safe in-process transport for one-process development/test deployments and
  explicit diagnostics that it is not cross-process fan-out.
- Generated model event types, scalar/composite identity values, caller-visible
  event IDs, causation IDs, transaction ordinals, type, recorded time, and
  nullable entity.
- `graphql-transport-ws` execution on the existing `http.Handler`, plus direct
  subscription execution for tests/embedding. The legacy WebSocket subprotocol
  and GraphQL-over-SSE are not silently accepted.
- Subscription-time model-read refusal and fresh per-event authorization.
- P2/P3 classification and SQL execution for `where` and entity selection.
- Bounded per-subscriber queues, bounded evaluation concurrency, cancellation,
  overflow, fan-out grouping, and observer metrics.
- Optional provider/application CDC adapter SPI, installed-adapter conformance,
  and explicit no-adapter behavior.
- Upgrade migration from the P6 system schema to the P7 delivery-state schema.

### 2.2 Excluded from P7 completion

- Exactly-once publication or exactly-once subscriber delivery.
- A generic job queue, delayed-job framework, scheduler, workflow engine, or
  arbitrary message broker API.
- Shipping Kafka, NATS, Redis, cloud-bus, or vendor-specific transport drivers
  in core. Their adapters may live in separate modules after their own evidence.
- A required concrete CDC implementation. Core P7 requires the adapter boundary,
  disabled behavior, validation harness, and tests for every adapter that is
  installed; PostgreSQL logical replication and SQLite filesystem tailing are
  not falsely claimed by the core.
- Cross-transaction global event ordering, broker partition ordering, or a
  database commit sequence that the current fact format does not contain.
- Event replay as an application history API, event sourcing, temporal queries,
  or reconstructing model state from the outbox.
- Exposing private pre-delete snapshots to GraphQL or ordinary typed consumers.
- Re-running mutation hooks during publication, retry, CDC ingestion, or
  subscription evaluation.
- Custom GraphQL subscription resolvers, live queries, federation, schema
  stitching, or the legacy `graphql-ws` protocol.
- MySQL.

The event transport is a narrow infrastructure seam, not the queue package that
the product constitution excludes.

## 3. Decisions frozen before implementation

### 3.1 Facts remain immutable; delivery state is separate

P4 currently inserts thirteen columns into the closed `_golem_outbox` V1 table:
event/version/codec/generation/model/action, before and after identities,
causation/ordinal, metadata, private delete snapshot, and recorded time. It has
no delivery state. P7 must not reinterpret `recorded_at` as a lease or mutate a
lossless fact into a worker record.

P7 adds `_golem_outbox_delivery` as a separately versioned system object keyed
by `causation_id`, one row per outer mutation transaction. It records only
operational state:

```text
causation_id         primary key; one state row per outer transaction
status               pending | leased | delivered | blocked | retired
first_recorded_at     immutable UTC microsecond instant for scan order
attempt_count        non-negative integer
available_at         UTC microsecond instant
lease_token          nullable UUID
lease_until          nullable UTC microsecond instant
delivered_at         nullable UTC microsecond instant
last_failure_code    nullable bounded sanitized code
blocked_at           nullable UTC microsecond instant
retired_at           nullable UTC microsecond instant
updated_at            UTC microsecond instant
```

Absence of a state row means an immediately pending causal group. The upgrade
backfills existing V1 groups idempotently, and every new P7 fact flush inserts
its causal delivery row in the same mutation transaction. Claim also tolerates
and atomically materializes an absent row so a crash during upgrade/backfill
cannot lose an immutable fact. This permits upgrading an existing database
without rewriting or losing P4 facts. The state table may be mutated; outbox
facts may only be inserted or deleted by retention
after the complete causal group is durably delivered and older than the
configured retention floor.

The state row deliberately has no impossible foreign key to the outbox's
event-keyed primary key; retention deletes a complete causal group and its state
atomically. No public API exposes the state table or accepts its physical name.
Migration rendering, introspection, drift checking, and fingerprints cover the
object on both providers.

SQLite stores instants as Unix microsecond `INTEGER`; PostgreSQL uses
`timestamptz(6)`. Identifiers/codes are bounded canonical `TEXT`. Closed checks
enforce coherent nullable columns for every status, and a pending-work index is
ordered by status, availability, first recorded time, and causation ID.

### 3.2 At-least-once means a visible duplicate window

One delivery attempt is:

```text
claim/lease one complete causal group in database transaction
  -> load and validate all immutable facts in ordinal order
  -> publish one causal EventBatch to configured transport
  -> transport reports durable acceptance
  -> acknowledge delivery state in database transaction
```

A crash after transport acceptance and before database acknowledgement causes
the complete causal batch, with the same event IDs, to be published again after
lease expiry. That duplicate is required by the guarantee. Consumers deduplicate
by event ID when they need an exact external side effect. Golem never relabels
this as exactly once.

Transport rejection or timeout schedules a bounded exponential retry with
deterministic capped jitter derived from causation ID and attempt number. Publisher
code never sleeps while holding a database transaction or lease-management
lock. Configuration freezes base delay, cap, lease duration, claim size,
concurrency, retention, and shutdown grace within hard maxima.

Transient transport failures retry indefinitely; an arbitrary attempt ceiling
must not turn an outage into data loss. A corrupt, unsupported, wrong-generation,
or internally inconsistent causal group becomes `blocked` and remains durable
and inspectable until a trusted operator repairs configuration/data and resumes
it. Blocking emits an observer failure; it does not delete the facts or pretend
delivery succeeded. An explicit trusted operator operation may resume or retire
a blocked group. Ordinary application callers cannot.

`attempt_count` increments with checked arithmetic and saturates at its storage
maximum without stopping retries. `retired` is an explicit audited operator
decision that stops delivery but retains the immutable facts; it is neither
`delivered` nor eligible for ordinary retention.

### 3.3 Ordering is causation-local

P4 gives all facts from one outer mutation transaction a common causation ID and
contiguous positive ordinals in declared graph order. P7 claims and publishes
the entire causal group as one batch, verifies ordinals are exactly `1..N`, and
preserves that order in the transport call. A worker cannot split a causal group
or publish a later ordinal through another lease. A partial transport acceptance
retries the complete batch, so a delivered prefix may be duplicated.

Different causations may publish concurrently. `recorded_at` is a deterministic
scan key, not a globally authoritative commit timestamp. P7 promises neither
global order nor order between independently committed transactions.

### 3.4 Transport acceptance and fan-out are distinct

The publisher writes a closed, versioned causal `EventBatch` to an
`EventTransport`. The transport may redeliver, but it must preserve every event
ID, byte sequence, and ordinal. Each
application process consumes the configured model stream into one
reference-counted local hub per schema generation/model. The local hub fans out
to subscribers; it does not acknowledge the durable outbox.

The built-in memory transport is bounded and valid only when publisher and all
subscribers share one process. Startup and diagnostics name that scope. A
multi-replica deployment must install a cross-process transport adapter; Golem
does not silently make a local channel look distributed.

The private delete snapshot is part of the trusted event envelope because a
consumer process may need it for authorization. Transport adapters are trusted
infrastructure and therefore a data-processing boundary. The ordinary public
event view never exposes those bytes. Malformed, wrong-generation, unsupported,
or oversized transport envelopes are rejected before hub fan-out.

### 3.5 Subscription identity is retained; policy state is not

At subscription start, GraphQL extracts `P` from context and transfers ownership
of it into an immutable principal snapshot or application-provided snapshot
function. Mutable principal shapes without an explicit safe snapshot fail before
hub registration.

The retained value is only revalidation input. For every event, the runtime:

1. creates a child context tied to the subscription cancellation;
2. calls the existing `ResolvePrincipal` again;
3. snapshots the resulting actor again;
4. rebuilds every model policy through the existing P2 runtime;
5. creates a new execution ID and new loaders;
6. validates the event generation, model, action, identity, and private snapshot;
7. authorizes/filter-evaluates the event through P3/P2 primitives;
8. serializes the selected public payload; and
9. tears down the event execution before reading the next event.

No actor, policy set, decision, row, loader, or computed-field cache survives
between events. Revocation therefore takes effect on the next event.

### 3.6 Created, updated, and deleted delivery

- **Created/updated:** re-read by exact after-identity under the fresh caller
  read scope conjoined with the subscription `where`. Select only the normalized
  entity projection and its classified dependencies. If absent, invisible, or
  filtered, suppress without revealing which case.
- **Deleted without `where`:** evaluate model/instance read authorization against
  the complete stored-scalar private pre-delete image. Relation-dependent policy
  checks may freshly hydrate current targets from captured foreign-key scalars;
  if the required relation state no longer exists or cannot be proven, suppress
  as unverifiable. `entity` is always null.
- **Deleted with `where`:** suppress with the stable `deletion-filter` reason.
  P7 does not implement a second in-memory SQL/policy evaluator for arbitrary
  GraphQL filters over the snapshot.
- **Identity-only selection:** still performs fresh model/instance authorization.
  It may omit the entity re-read only when the policy proof and action semantics
  make the private fact sufficient.
- **Malformed/unknown event:** suppress or block according to whether the source
  is transport or durable outbox; never crash a connection or leak bytes.

The public payload includes the event ID independently from the model identity.
The entity field is nullable for every action and is never populated for a
delete.

### 3.7 Sharing is conservative and never crosses principals

One event may be evaluated once for multiple subscriptions only when all of the
following canonical keys match: schema generation, model, exact event ID,
immutable principal audit/scope identity, policy-generation token, normalized
`where`, normalized entity selection (including computed dependencies), and
execution-independent encoder shape.

P7's safe baseline is no sharing across distinct retained principal snapshots,
even if application audit strings happen to match. `AuditPrincipal` is for
observability, not an authorization equivalence proof. Implementations may share
parse/bind artifacts that contain no caller result. They may not share a caller
row, policy decision, masked value, computed result, or loader.

Evaluation sharing is disabled whenever the entity selection invokes a read
hook or computed field, because current contracts do not prove those functions
execution-independent or pure. Those subscribers still share the model source,
not their caller-specific evaluation.

## 4. One event architecture

```text
P4 mutation transaction
  -> immutable _golem_outbox V1 fact commits with data

P7 publisher worker
  -> provider claim/lease in _golem_outbox_delivery
  -> decode + validate golem.fact.v1
  -> publish golem.event.v1 to configured EventTransport
  -> acknowledge, retry, or block

configured EventTransport stream
  -> validate generation/codec/limits
  -> one bounded local hub per schema generation + model
  -> subscriber queue
  -> fresh principal/actor/policy/execution
  -> authorized re-read or private delete evaluation
  -> normalized GraphQL event payload
  -> graphql-transport-ws frame
```

CDC joins only before the transport/hub boundary:

```text
installed CDC adapter
  -> provider cursor + stable source record identity
  -> schema-bound change validation and canonical fact construction
  -> same golem.event.v1 transport
  -> same hub, authorization, and GraphQL path
```

### 4.1 Package ownership

```text
golem                         declarations, sealed public typed event values
internal/compiler/ir          normalized subscription contract metadata
internal/event/codec          golem.event.v1 encode/decode and validation
internal/event/outbox         fact reads, delivery state, retries, retention
internal/event/provider       provider-neutral claim/ack program
internal/provider/sqlite      SQLite claim/state/migration/introspection
internal/provider/postgresql  PostgreSQL claim/state/migration/introspection
events                        public transport, worker, observer, CDC contracts
internal/subscription         hub, queue, grouping, fresh evaluation
internal/graphql/subscription operation binding and payload encoding
graphql                       WebSocket protocol, transport limits, safe errors
runtime                       app lifecycle, publisher, fresh caller execution
```

No package in this list parses model tags, invents physical identifiers, builds
a second model registry, accepts raw SQL, or calls application mutation hooks.

## 5. ContractIR, schema, and generation

P7 bumps ContractIR and GraphQL ABI versions. For every model it normalizes:

- `subscriptions` enabled/disabled (default false);
- event root name;
- event payload and identity type names;
- primary-key component identities in declared order;
- public event metadata fields;
- the complete stored-scalar private delete snapshot inventory in one canonical
  stable order; and
- normalized subscription limits that are contract-specific.

The authoring constructor is `golem.Subscriptions[M]()`. It is a model option;
it implies P4 event capture and P7 GraphQL subscription generation. P7 adds
`Events` to public `golem.GraphQLRootNames`. The compiler rejects subscription
enablement for a hidden/unexposed model, a model without a primary key, an
identity containing an unexposed key component, an invalid/colliding root, or a
delete policy whose required snapshot cannot be captured losslessly.

P4's codec can carry a private delete snapshot, but the current mutation planner
does not request one. Go policy factories are actor-dependent executable code,
so the compiler cannot soundly infer one minimal scalar dependency set that
works for every future subscriber. P7 therefore freezes a complete stored-scalar
pre-image for each subscription-enabled model, in canonical stable order, and
wires that inventory into every root, nested, batch, upsert-delete, and system
delete fact requirement. It never includes relation objects or computed values.

The pre-image is trusted private data and can include hidden/write-only values;
it remains in the database/transport authorization boundary and has no public
accessor. P4's exact fact-count and encoded-byte ceilings still apply: an
oversized pre-image refuses and rolls back the delete instead of silently
omitting authorization data. Relation-dependent authorization is freshly
hydrated from captured key scalars when possible and otherwise suppresses as
`deletion-unverifiable`. Codec support alone is not completion.

Changing subscription enablement, root names, event limits, or GraphQL event
types changes ContractIR and generated artifacts, but not ModelIR, application
tables, or the model fingerprint. The P7 system delivery object is a platform
schema upgrade, not a per-model subscription migration.

Generation produces:

- typed event action and typed scalar/composite identity values;
- immutable event metadata/accessors;
- per-model event payload and subscription client binding;
- GraphQL event SDL, enum, identity object when composite, and Subscription root;
- transport codec registry keyed by generation/model/version; and
- application runtime wiring for event configuration and lifecycle.

Generation remains byte-deterministic, atomic, and stale-artifact-cleaning.

The current P4 decoder accepts only the active generation digest. Durable facts
can outlive a deployment and even a contract-only regeneration. P7 therefore:

- keeps decoding `golem.fact.v1` through a historical fact-schema resolver keyed
  by generation digest;
- emits `golem.fact.v2` for new mutations with a distinct event-schema digest
  derived from stable model/key/value/snapshot identities and independent of
  GraphQL names, operations, and other contract-only changes; and
- registers current and explicitly supplied historical generated event schemas.

V1 remains supported until its backlog is drained; it is not rewritten in
place. A missing/incompatible schema blocks the causal group instead of
acknowledging or discarding it. Publication also cross-checks every duplicated
outbox column against canonical decoded metadata before transport. A model
migration that removes a needed historical event schema must fail startup or
remain blocked until the backlog is deliberately resolved.

## 6. Publisher and provider semantics

### 6.1 Claim protocol

The provider-neutral claim program selects bounded complete causation groups
ordered by their minimum `recorded_at` and causation ID. PostgreSQL uses a transaction with row
locking/`SKIP LOCKED` or an equivalently proven atomic statement. SQLite uses a
dedicated connection and `BEGIN IMMEDIATE`. Both create or replace only expired
delivery leases and return the exact causal groups owned by a fresh unguessable
lease token. Claim limits count causal groups, while one group may contain P4's
full bounded fact count/bytes and can never be split merely to fit a row batch.

Every ack/retry/block transition is conditional on `(causation_id, lease_token,
status = leased)`. A stale worker cannot acknowledge a lease re-owned after
expiry. Database time, not a worker's wall clock, decides lease expiry. A long
publish renews/fences its token. Provider tests deliberately skew process clocks.

No transport call, observer callback, codec expansion, or retry wait occurs
inside a claim/ack database transaction. Claim transactions are bounded by the
configured batch and statement-parameter ceilings.

### 6.2 Shutdown and ownership

`RunPublisher` is context-owned. On cancellation it stops claiming, permits
in-flight transport calls only through the bounded shutdown grace, and releases
or lets leases expire. It does not mark an unconfirmed publication delivered.
Only one `RunPublisher` may own a particular runtime publisher instance, while
multiple independent instances may coordinate through the database.

An application may host GraphQL subscriptions without owning a publisher when a
separate worker publishes the same database's outbox to the shared transport.
Conversely, a publisher may run without GraphQL. Startup validates that every
subscription-enabled application has a transport; it does not require the local
process to own the publisher.

### 6.3 Retention

Retention is disabled by a zero policy rather than guessing application replay
needs. When enabled, cleanup deletes only facts whose state is `delivered`, whose
delivery and recorded times are older than the configured floor, and which are
not leased. Blocked, pending, and leased facts are never age-deleted. Cleanup is
bounded and cancellable, uses the same provider ownership rules, and emits
sanitized counts.

Manual resume/retire operations are system/operator capabilities with explicit
causation IDs and audit callbacks. They are absent from caller and GraphQL
clients.

## 7. Event codec and public data

`golem.event.v1` wraps either supported `golem.fact.v1` or `golem.fact.v2`. Its canonical
encoding includes version/codec, event ID, generation fingerprint, stable model
ID, event-schema fingerprint when available, action, ordered before/after
identity, causation ID, ordinal, recorded time, and the private delete snapshot.
It is deterministic and bounded before
allocation.

The existing P4 exact scalar codec remains authoritative for Boolean, signed
integers, float bit patterns, Decimal coefficient/scale, string, bytes, UUID,
Date, Time, DateTime, enum identity, JSON, scalar lists, composite identities,
and rows. P7 adds hostile-input decoding, unknown-version refusal, truncation,
length/depth/count guards, canonical re-encoding tests, and generation lookup.

The public view excludes raw metadata and delete snapshot. GraphQL uses exact
custom scalar transport already owned by P5. Event IDs and causation IDs are
UUIDs; model identity uses the generated key type rather than a lossy string.

## 8. Subscription execution

### 8.1 Subscribe-time work

Before hub membership, the server parses/validates the operation under P5
limits, binds exactly one subscription root, freezes variables/filter/selection,
classifies all visible positions, snapshots the principal, creates a fresh caller
once to establish current model-read eligibility, and validates transport and
queue limits. Failure performs no registration.

One GraphQL subscription operation has exactly one root field as required by the
GraphQL subscription model. Query/mutation roots, introspection, custom roots,
aliases/fragments/directives, and computed selections retain P5 behavior where
legal. A subscription cannot smuggle System, DB, Tx, raw SQL, or a custom event
source.

Generated caller event streams bind the same sealed event filter/selection,
retain the caller's safely snapshotted principal, and enter the same evaluator.
They are absent from System and transaction clients. They do not expose replay,
cursor, raw notice, publish, snapshot, or broker APIs.

### 8.2 Per-event work

Per event, fresh caller creation happens before any caller-dependent sharing.
The normalized filter is executed as an authorized P3 exact-identity read for
created/updated events. Selected ordinary, relation, count, and computed fields
use the existing P3/P5 selection engine and fresh event loaders. Conditional
fields retain P5 nullability and masking rules. A field that influences the
filter is classified even when it is not selected.

A subscriber is disconnected on principal revalidation failure, policy-build
failure, internal invariant failure, source closure, transport corruption, or
queue overflow. A normal non-match or authorization suppression keeps the
subscription alive and emits a reason metric without identity details.

### 8.3 Backpressure and lifecycle

Each subscriber has one bounded FIFO queue, default 64 and hard maximum 4,096.
Each hub has bounded input buffering and evaluation concurrency. Enqueue on a
full subscriber queue removes that subscriber and returns
`GOLEM_SUBSCRIPTION_OVERFLOW`; it never discards the oldest/newest item and
continues.

Cancellation is selected alongside source receive and output send. It cancels
database work, removes membership before closing output, closes the output once,
and prevents an in-flight evaluator from sending after close. The last member
stops the model source. Race and goroutine-leak tests cover subscribe/cancel,
source close, overflow, observer panic, transport reconnect, and application
shutdown.

## 9. GraphQL transport

The existing handler keeps GraphQL-over-HTTP query/mutation behavior unchanged.
P7 adds the `graphql-transport-ws` subprotocol on upgrade requests. Connection
initialization has bounded bytes/time, authentication occurs before operation
start, operation IDs are unique per connection, ping/pong and close behavior are
bounded, and one connection has explicit maximum active subscriptions.

An optional `WebSocketInit` callback may validate a bounded initialization
payload and derive the authenticated connection context before
`PrincipalFromContext`. It cannot return an App, Caller, System, DB, Tx, event
source, or policy override. `GraphQLServer.Shutdown(ctx)` stops new operations,
cancels active evaluations, deregisters subscribers, closes owned transport
streams, and waits only through the caller's deadline.

The server does not accept the obsolete `graphql-ws` protocol as if it were the
new protocol. GraphQL-over-SSE is outside the frozen P7 ABI; a later addition
requires its own protocol/limit evidence and cannot redefine subscription
semantics.

The protocol layer never hand-serializes a model entity around gqlgen. The P7
operation compiler prepares an authorized typed event value/channel, and the
active generated gqlgen executable remains the field/alias/directive/scalar
serializer used by P5.

Public errors use stable codes and sanitized messages. Trusted observers receive
internal errors separately. WebSocket close codes/reasons are bounded and never
include SQL, driver text, principal values, row data, fact bytes, or private
snapshots.

## 10. CDC boundary

Without configured CDC adapters, only P4 outbox facts are event sources. Startup
diagnostics and runtime capabilities report `externalWritesObserved=false`.
Direct inserts/updates/deletes performed by another process produce no event and
no GraphQL notification; a named test proves this rather than treating it as a
bug.

An installed adapter owns provider log/cursor reading and must provide:

- stable adapter identity/version and provider scope;
- a durable source position contract;
- stable source-record identity used to derive/deduplicate event IDs;
- stable UTC-microsecond source transaction time preserved across replay so the
  same source record produces the same canonical event bytes;
- schema generation and model identity;
- created/updated/deleted action and exact before/after values sufficient to
  derive ordered identity;
- a private pre-delete image sufficient for delete authorization; and
- explicit acknowledgement/replay behavior.

Golem validates and canonicalizes the record before emitting `golem.event.v1`.
It never substitutes the current worker clock for the adapter's stable source
transaction time.
It rejects unsupported models/types/generations, missing identity, incomplete
delete images, noncanonical values, and oversized records. CDC never invokes
mutation authorization or hooks because the external write already happened;
it still invokes subscription authorization before delivery.

Adapter checkpoints are adapter-owned durable state; P7 does not silently add a
second core system object or pretend an in-memory cursor survives restart. An
adapter observing the same provider log as Golem writes must correlate and
suppress changes already represented by transactional outbox causation, so one
Golem mutation does not become both an outbox event and a CDC event.

Every concrete adapter must pass the common adapter harness plus provider/live
restart tests. Merely satisfying the Go interface is not a supported-adapter
claim.

## 11. Limits and observability

Zero values select these defaults; applications may lower them and may raise
only to the hard maximum:

| Limit | Default | Hard maximum |
| --- | ---: | ---: |
| claimed causal groups | 64 | 1,024 |
| publisher concurrent deliveries | 8 | 128 |
| lease duration | 30 s | 10 min |
| publish attempt timeout | 15 s | 2 min |
| retry base | 250 ms | 1 min |
| retry cap | 5 min | 24 h |
| encoded event bytes | 2 MiB | 16 MiB |
| subscriber queue | 64 | 4,096 |
| hub input queue | 256 | 16,384 |
| event evaluation concurrency | 32 | 256 |
| active subscriptions per connection | 32 | 256 |
| connection-init bytes | 64 KiB | 1 MiB |
| connection-init timeout | 10 s | 60 s |
| shutdown grace | 15 s | 2 min |
| retention delete rows | 256 | 4,096 |

P4's per-transaction fact/outbox limits remain authoritative before commit.
P5's request/AST/input/selection/complexity limits still apply to subscription
documents. The stricter applicable limit wins; no layer silently clamps.

Observers expose counters/gauges/histograms for claims, lease conflicts, publish
attempts/results/latency, retries, blocked groups, pending/leased age, cleanup,
active hubs/subscribers, source reconnects, evaluations/latency, deliveries,
suppression by closed reason enum, queue depth/capacity, overflow, cancellation,
and CDC ingestion. Labels contain provider, stable model ID, action, outcome, and
reason only. They exclude principal, selector, filter values, entity data, SQL,
binds, snapshots, and transport payloads. Observer panic is recovered and
reported only through a final safe fallback; it never changes state.

## 12. Work waves and dependencies

### P7-A — public contracts, ContractIR, and system schema

- Freeze `PUBLIC-EVENT-ABI.md` in code.
- Add subscription authoring and normalized contract metadata.
- Add the delivery-state system object, migrations, renderers, introspection,
  drift checks, fingerprints, and upgrade fixture.
- Freeze limits, stable errors, observer records, and lifecycle ownership.

This wave is serial and blocks all others.

### P7-B — event codec and generated typed surface

- Retain historical `golem.fact.v1`, add event-schema-bound `golem.fact.v2`, and
  add `golem.event.v1`.
- Generate typed actions, identities, payloads, registries, and composite types.
- Prove exact scalars, canonical bytes, hostile input bounds, and determinism.

### P7-C — provider delivery coordinator and publisher

- Implement pending discovery, whole-causation claiming, leases, ack, retry,
  blocking, shutdown, retention, and operator recovery.
- Prove SQLite/PostgreSQL parity and multi-worker crash/restart behavior.

### P7-D — transport SPI and bounded local hub

- Implement transport validation, built-in memory transport, source lifecycle,
  model hubs, bounded queues, overflow, grouping, and observers.
- Prove duplicates, reconnects, cancellation, and no goroutine leaks.

P7-B, P7-C, and P7-D may proceed in parallel after P7-A against frozen fixtures.

### P7-E — fresh subscription authorization

- Bind/freeze filter and selection.
- Snapshot the retained principal safely.
- Build one fresh caller and execution per event.
- Implement created/updated re-read, delete snapshot authorization,
  classification, suppression, and selected entity encoding.
- Prove no cross-principal sharing or lifetime cache.

P7-E depends on P7-B/D and existing P2/P3/P5 seams.

### P7-F — generated GraphQL subscriptions and WebSocket transport

- Emit subscription SDL/code/config only for enabled models.
- Extend the operation compiler and generated gqlgen adapter.
- Implement `graphql-transport-ws` connection/operation lifecycle and limits.
- Prove GraphQL/typed-event agreement and query/mutation non-regression.

P7-F depends on P7-E.

### P7-G — CDC boundary and common adapter harness

- Implement the adapter SPI, canonicalization, checkpoint/ack contract,
  diagnostics, and disabled behavior.
- Run the common conformance suite against each installed adapter.

P7-G depends on P7-B/D but may proceed in parallel with P7-E/F.

### P7-H — operations, observability, and lifecycle integration

- Wire app/worker startup and shutdown, health/capability reporting, observer
  isolation, blocked-group inspection/resume/retire, retention, and documentation.
- Add deployment guidance for local versus cross-process transports and trusted
  private snapshot handling.

### P7-I — independent oracle and adversarial completion audit

- Run the complete evidence ledger, named mutations, provider profiles, restart
  harness, race/repeat/fuzz/goleak, deterministic generation, migration upgrade,
  and clean-module tests.
- Independently inspect that no P8 claim, concrete CDC adapter, exactly-once
  promise, or external-write observation is inferred from interfaces alone.

P7 is complete only after P7-I closes every row in `P7-EVIDENCE.md`.

## 13. Parallelization plan

After P7-A freezes shared types, three independent tracks can work without
editing the same implementation packages:

```text
Track 1: P7-B codec/codegen -----------┐
Track 2: P7-C provider publisher ------+--> P7-E --> P7-F
Track 3: P7-D hub + P7-G CDC harness --┘       \
                                                 P7-H --> P7-I
```

The orchestrator owns ContractIR versions, generated public names, system object
versions, integration merges, and the evidence ledger. Agents receive bounded
package ownership and must return tests plus explicit exclusions. No parallel
track may silently change `PUBLIC-EVENT-ABI.md`.

## 14. Definition-of-down checklist

Before P7 may be called complete:

1. P6-to-P7 migrations preserve every existing V1 fact on both providers.
2. Fact rows remain immutable through claim/retry/ack; only state rows mutate.
3. All data/outbox atomicity evidence from P4 remains green.
4. Every publisher crash point recovers with zero loss and documented possible
   duplicates.
5. Causation-local ordering and the absence of a global-order claim are tested.
6. Generated event identities cover scalar and composite primary keys exactly.
7. Batch and nested mutations publish every committed per-row fact.
8. Every event builds a fresh caller/policy/execution and revocation is observed.
9. Created/updated/delete filtering/entity semantics match section 3.6.
10. Conditional fields, relations, counts, and computed selections use the P5
    execution path and event-local caches.
11. Overflow disconnects; cancellation and shutdown leak no work or goroutines.
12. Sharing never crosses distinct principal snapshots or policy generations.
13. The WebSocket protocol, error, and limit corpus passes without weakening
    ordinary HTTP GraphQL.
14. No-adapter external writes are proved invisible and clearly reported.
15. Every installed CDC adapter passes the common live/restart harness.
16. Observers are bounded, sanitized, and correctness-neutral.
17. SQLite/PostgreSQL live, race, repeat, fuzz, migration, and mutation gates
    pass with zero required skips.
18. `P7-EVIDENCE.md` records the exact commit and commands under test.

Items 1–17 are verified in the current worktree. Item 18 awaits the explicitly
requested P7 commit and its identifier; P8 must not begin before that final
administrative closure.

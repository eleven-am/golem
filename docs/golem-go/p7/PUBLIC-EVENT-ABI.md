# P7 public event, publisher, subscription, and CDC ABI

Status: **frozen contract; implementation and verification complete**

This document fixes the source-level surface P7 must implement. Names and
signatures shown here are normative unless a later committed contract change
updates this document, the Bible conflict table, generated goldens, and the
evidence ledger together.

## 1. Model declaration

Subscriptions are opt-in and attached to the model:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
    return golem.DefineModel(
        golem.GraphQL(
            golem.GraphQLOperations(
                golem.GraphQLFindOne,
                golem.GraphQLFindMany,
                golem.GraphQLCreate,
                golem.GraphQLUpdate,
                golem.GraphQLDelete,
            ),
            golem.GraphQLRoots(golem.GraphQLRootNames{
                FindOne:  "post",
                FindMany: "posts",
                Create:   "createPost",
                Update:   "updatePost",
                Delete:   "deletePost",
                Events:   "postEvents",
            }),
        ),
        golem.Subscriptions[Post](),
    )
}
```

The additions in `go/golem` are:

```go
func Subscriptions[M any]() ModelOption[M]

type GraphQLRootNames struct {
    FindOne, FindMany                         string
    Create, Update, Upsert, Delete           string
    UpdateMany, DeleteMany                   string
    Aggregate, GroupBy, RelationGroupBy      string
    Events                                   string
}
```

`Subscriptions[M]()` defaults to off and simultaneously enables P4 fact capture,
P7 typed event generation, and the P7 GraphQL subscription root. There is no
runtime registration callback and no process-global model map.

The compiler requires an exposed model and an exposed primary key. Composite
key component order is the ModelIR primary-key order. Subscription configuration
changes ContractIR and generated artifacts only; it does not change application
table DDL or the model fingerprint.

## 2. Public event values

Core sealed values live in `go/golem`:

```go
type EventAction string

const (
    EventCreated EventAction = "created"
    EventUpdated EventAction = "updated"
    EventDeleted EventAction = "deleted"
)

type EventID [16]byte
type CausationID [16]byte
type EventSchemaDigest [32]byte

type EventMetadata struct { /* immutable */ }

func (m EventMetadata) EventID() EventID
func (m EventMetadata) Action() EventAction
func (m EventMetadata) CausationID() CausationID
func (m EventMetadata) TransactionOrdinal() uint32
func (m EventMetadata) RecordedAt() time.Time
func (m EventMetadata) GenerationDigest() SchemaDigest
func (m EventMetadata) EventSchemaDigest() (EventSchemaDigest, bool)
func (m EventMetadata) ModelID() ModelID
```

The concrete representation remains private. Zero IDs, zero ordinal, zero time,
unknown action, wrong generation, and unknown model never form a deliverable
event.

For a single-key `Post`, generation emits:

```go
type PostEventIdentity = golem.UUID // exact declared Go scalar type

type PostEvent struct { /* immutable */ }

func (e PostEvent) Metadata() golem.EventMetadata
func (e PostEvent) ID() PostEventIdentity
func (e PostEvent) Entity() (golem.Row[Post], bool)
```

For a compound-primary-key `Friendship(userID, friendID)`, generation emits:

```go
type FriendshipEventIdentity struct { /* immutable */ }

func (id FriendshipEventIdentity) UserID() golem.UUID
func (id FriendshipEventIdentity) FriendID() golem.UUID

type FriendshipEvent struct { /* immutable */ }

func (e FriendshipEvent) Metadata() golem.EventMetadata
func (e FriendshipEvent) ID() FriendshipEventIdentity
func (e FriendshipEvent) Entity() (golem.Row[Friendship], bool)
```

No public constructor accepts a `ModelID`, `FieldID`, raw encoded identity,
delete snapshot, database value, or transport payload. Events are created only
by validated generated/runtime decoding. Returned identity, row, byte, JSON, and
list values obey the existing ownership/copy rules.

`EventMetadata` has a private representation and is materialized only from a
Golem-internal metadata capability protected by Go's `internal` package
boundary. An external lookalike accessor interface is not accepted. Metadata
alone is never a deliverable event: the generated factory accepts only the
sealed validated-event value that also proves the resolved event schema,
ordered identity, and optional entity.

`Entity()` is absent when no entity was requested, when the event is deleted,
or when the typed infrastructure consumer received only the public fact view.
The private delete snapshot has no accessor.

For every subscription-enabled model, the compiler records all locally stored
scalar/enum fields as one canonical private pre-delete inventory. This is
deliberately complete: actor-dependent Go policy factories cannot be statically
reduced to one sound minimal inventory for every future subscriber. Relation
objects and computed values are not captured. Hidden/write-only scalar values
may therefore exist in the trusted fact, but never in this public ABI. Existing
P4 fact-byte limits fail and roll back an oversized delete rather than emitting
an unverifiable partial snapshot.

### 2.1 Generated caller stream

Generation adds a sealed event request and stream to each subscription-enabled
caller model client:

```go
type EventOption[M any] interface { /* sealed */ }

func EventWhere[M any](Predicate[M]) EventOption[M]
func EventSelect[M any](...Field[M]) EventOption[M]

type EventStream[E any] interface {
    Recv(context.Context) (E, error)
    Close() error
}

func (client CallerPostClient[P]) Events(
    ctx context.Context,
    options ...golem.EventOption[Post],
) (golem.EventStream[PostEvent], error)
```

The caller retains only a safely snapshotted principal revalidation input for
this purpose. `Recv` evaluates each notice with a newly resolved actor, policy
set, execution ID, and loaders. The stream has the same filter/entity/delete,
overflow, cancellation, duplicate, and error semantics as GraphQL. There is no
event stream on System, CallerTx, or SystemTx clients and no publish operation.

## 3. Event transport

The narrow infrastructure SPI lives in `go/events`:

```go
package events

type Notice struct { /* immutable, versioned bytes */ }

func (n Notice) EventID() golem.EventID
func (n Notice) GenerationDigest() golem.SchemaDigest
func (n Notice) EventSchemaDigest() golem.EventSchemaDigest
func (n Notice) ModelID() golem.ModelID
func (n Notice) Action() golem.EventAction
func (n Notice) Encoded() []byte

type EventBatch struct { /* immutable, one causation */ }

func (b EventBatch) CausationID() golem.CausationID
func (b EventBatch) Events() []Notice

type Stream interface {
    Recv(context.Context) (Notice, error)
    Close() error
}

type EventTransport interface {
    Publish(context.Context, EventBatch) error
    Subscribe(context.Context, Subscription) (Stream, error)
}

type RuntimeBinding interface {
    DecodeNotice(context.Context, []byte) (Notice, error)
}

type RuntimeBindableTransport interface {
    BindEventRuntime(RuntimeBinding) error
}

type Subscription struct { /* sealed */ }

func (s Subscription) GenerationDigest() golem.SchemaDigest
func (s Subscription) EventSchemaDigest() golem.EventSchemaDigest
func (s Subscription) ModelID() golem.ModelID
```

`Notice.Encoded()` returns a copy. Application code cannot construct a valid
`Notice` or `Subscription`; this prevents a forged model/action/identity from
entering the authorization path. Adapter packages receive constructors only
through conformance-scoped internal capabilities.

The encoded bytes are opaque to ordinary typed and GraphQL consumers, but they
necessarily contain the private delete snapshot used after cross-process
delivery. A configured transport adapter is therefore an explicitly trusted
data-processing boundary. P7 promises that the snapshot is absent from Notice
metadata accessors, ordinary typed Go events, GraphQL payloads, public errors,
and observer records; it does not claim cryptographic secrecy from the
application-installed transport that receives `Encoded()`.

An external/cross-process transport must implement `RuntimeBindableTransport`;
process-local transports may omit it.
During `App.Open`, Golem supplies one schema-bound `RuntimeBinding` before the
App is published. The binding validates bounded `golem.event.v1` bytes against
the active and explicitly registered historical schemas and returns a sealed
`Notice`; it is not a public raw notice constructor. Binding starts no worker,
must be rejected if repeated or conflicting, and is unnecessary for an
in-process transport whose stream already carries sealed notices.

`GenerationDigest` remains the immutable generation recorded by the original
fact. Transport routing uses the separate sealed logical `EventSchemaDigest`
plus `ModelID`, so a pending V2 fact survives a GraphQL-only regeneration when
its event schema is unchanged. The runtime still decodes and verifies the
original generation metadata before authorization and typed delivery.

`Publish` accepts one complete outer-transaction causation. The batch contains
exactly the contiguous ordinals `1..N` in order and cannot be split by an
adapter. It succeeds only after the transport has accepted responsibility under
its documented durability contract. It may return an error after accepting a
prefix or the whole batch, and `Stream.Recv` may redeliver. Retry republishes the
complete batch with identical event IDs/bytes. Ordering is required within that
batch; no order is promised between causations.

The built-in constructor is explicit about its scope:

```go
func NewMemoryTransport(limits MemoryLimits) (EventTransport, error)

type MemoryLimits struct {
    Buffer int
}
```

It is bounded, process-local, non-durable across process exit, and suitable for
development/tests or applications whose publisher and subscribers are in the
same process. Its use is surfaced in capabilities/diagnostics.

## 4. Runtime event configuration

The generated application `Config[P]` adds:

```go
type Config[P any] struct {
    DB                *sqlx.DB
    Provider          golem.Provider
    ReadLimits        runtime.ReadLimits
    MutationLimits    runtime.MutationLimits
    AnalyticsLimits   runtime.AnalyticsLimits
    EventLimits       events.Limits
    EventTransport    events.EventTransport
    EventObserver     events.Observer
    CDCAdapters       []events.CDCAdapter
    ReportEventOperator events.OperatorAudit
    HistoricalEventBundles []golem.SchemaBundle
    ResolvePrincipal  func(context.Context, P) (Actor, error)
    SnapshotPrincipal func(P) (P, error)
    SnapshotActor     func(Actor) (Actor, error)
    AfterCommitError  func(context.Context, golem.AfterCommitFailure)
    AuditPrincipal    func(P) string
    ReportScopedQuery func(context.Context, golem.ScopedAuditRecord)
}
```

Existing fields keep their P3–P6 semantics. `SnapshotPrincipal` transfers
ownership of a mutable principal into a stable revalidation input for a
subscription. If it is nil, subscription start accepts only a deeply immutable
value principal under the same structural rule used for actor ownership.
Ordinary short-lived `ForPrincipal` calls do not begin retaining principals.

`HistoricalEventBundles` supplies generated schema registries required to decode
pending `golem.fact.v1` generations and historical V2 event schemas. Decoding a
historical fact does not make it deliverable: the publisher durably blocks the
causal group before transport/ACK unless its resolved model/event schema is
compatible with the active generated factory. Operator resume only makes it
eligible for this compatibility check again.
The runtime rejects duplicate/conflicting digests and blocks a pending group
whose decoder is absent. New `golem.fact.v2` facts carry an event-schema digest
that is independent of GraphQL-only contract changes.

When no model enables subscriptions, `EventTransport` and `EventObserver` may be
nil and no worker/hub is started. When any model enables subscriptions,
`EventTransport` is required. The observer may be nil; a no-op observer is used.

### 4.1 Limits

```go
type Limits struct {
    ClaimRows                      int
    PublisherConcurrency          int
    LeaseDuration                 time.Duration
    PublishTimeout                time.Duration
    RetryBase                     time.Duration
    RetryCap                      time.Duration
    MaxEncodedEventBytes          int
    SubscriberQueue               int
    HubInputQueue                 int
    EvaluationConcurrency         int
    MaxSubscriptionsPerConnection int
    ConnectionInitBytes           int
    ConnectionInitTimeout         time.Duration
    ShutdownGrace                 time.Duration
    RetentionDeleteRows           int
}
```

Zero fields use the documented library defaults; invalid, negative,
contradictory, or above-hard-maximum values fail `Open` before worker or server
registration.

### 4.2 Publisher lifecycle

The generated application exposes no implicit goroutine from `Open`. Ownership
is explicit:

```go
func (app *App[P]) RunEventPublisher(ctx context.Context) error
func (app *App[P]) EventCapabilities() events.Capabilities
func (app *App[P]) EventOperator() events.Operator
func (app *App[P]) EventLimits() events.Limits
```

`RunEventPublisher` blocks until context cancellation or terminal worker error.
Calling it twice concurrently on the same `App` returns a stable lifecycle
error. Independent applications/workers may run concurrently against the same
database and transport.

`EventCapabilities` is immutable and includes provider, fact/event codec
versions, transport identity/scope, publisher enabled/running state,
subscription-enabled model IDs, installed CDC adapter identities, and
`ExternalWritesObserved`. It contains no secrets or connection data.

`EventLimits` returns the same normalized immutable limits owned by the runtime
and consumed by generated GraphQL/WebSocket integration.

The operator surface is explicitly trusted and event-ID-specific:

```go
type Operator interface {
    Inspect(context.Context, golem.CausationID) (Delivery, error)
    Resume(context.Context, golem.CausationID) error
    Retire(context.Context, golem.CausationID) error
    RunRetention(context.Context, RetentionPolicy) (RetentionResult, error)
}
```

`Operator` is returned from `App`, not `Caller`, `CallerTx`, GraphQL, or a model
client. `Resume` makes a repaired blocked causal group eligible again. `Retire`
never claims successful publication; it records an audited terminal operator
decision. `Inspect` exposes delivery metadata and sanitized failure codes, not
fact bytes or snapshots.

`ReportEventOperator` is required for an event-enabled application. Resume,
retire, and retention report a dedicated immutable `OperatorAuditRecord` for
success, rejection, and failure. Callback panic is isolated from the operator
decision, and records contain only action, outcome, causation (when applicable),
and aggregate retention counts.

## 5. Observer ABI

```go
type Observer interface {
    ObserveEvent(context.Context, Observation)
}

type Observation struct { /* immutable */ }

type ObservationKind string
type Outcome string
type SuppressionReason string
```

The closed observation kinds include publisher claim/attempt/ack/retry/block,
retention, transport receive/reconnect, hub membership, evaluation, delivery,
suppression, overflow, cancellation, CDC receive/ack, and lifecycle failure.
Closed suppression reasons distinguish genuine policy denial (`unauthorized`)
from a delete whose private snapshot or required relation state cannot be
verified (`deletion-unverifiable`). Missing/incomplete delete state is never
misreported as an authorization decision.

Accessors expose stable model ID, action, kind, outcome, suppression reason,
attempt, bounded queue counts, duration, and aggregate count. They do not expose
event payloads, identity values, principal/audit identity, filters, entity rows,
SQL, binds, driver errors, stack traces, or private snapshots. Runtime catches
observer panics and never changes database/transport/subscription state because
of them.

## 6. Generated GraphQL contract

For a scalar-key model:

```graphql
enum GolemEventType {
  CREATED
  UPDATED
  DELETED
}

type PostEvent {
  eventID: UUID!
  causationID: UUID!
  transactionOrdinal: Int!
  recordedAt: DateTime!
  type: GolemEventType!
  id: UUID!
  entity: Post
}

type Subscription {
  postEvents(where: PostWhereInput): PostEvent!
}
```

For a compound-key model:

```graphql
type FriendshipEventIdentity {
  userID: UUID!
  friendID: UUID!
}

type FriendshipEvent {
  eventID: UUID!
  causationID: UUID!
  transactionOrdinal: Int!
  recordedAt: DateTime!
  type: GolemEventType!
  id: FriendshipEventIdentity!
  entity: Friendship
}
```

Identity component order is declared primary-key order. `eventID` is distinct
from `id` and lets an at-least-once consumer deduplicate. `entity` is nullable
for every event and always null for `DELETED`. Private snapshots and raw codec
metadata do not exist in SDL.

Only subscription-enabled, exposed models contribute a field. One application
emits at most one `Subscription` root and one shared `GolemEventType`. Root/type
collisions are compile errors. P5 query/mutation SDL is byte-identical except for
the intentional ContractIR/ABI version and appended P7 types/root.

## 7. GraphQL server configuration and transport

The generated GraphQL surface remains:

```go
type GraphQLConfig[P any] struct {
    PrincipalFromContext func(context.Context) (P, bool)
    Limits               GraphQLLimits
    Introspection        bool
    WebSocketInit        func(context.Context, json.RawMessage) (context.Context, error)
    ReportInternalError  func(context.Context, error)
}

func (app *App[P]) GraphQL(GraphQLConfig[P]) (*GraphQLServer, error)
func (server *GraphQLServer) Handler() http.Handler
func (server *GraphQLServer) SDL() string
func (server *GraphQLServer) ContractFingerprint() golem.SchemaDigest
func (server *GraphQLServer) Shutdown(context.Context) error
```

Event infrastructure comes from the application `Config`, so GraphQL cannot be
wired to a different database, schema generation, transport, or authorization
runtime. `PrincipalFromContext` runs at WebSocket connection initialization and
ordinary HTTP request start. The retained principal is snapshotted before the
first subscription is registered; every event calls application
`ResolvePrincipal` and rebuilds policy again.

`WebSocketInit`, when non-nil, receives the bounded `connection_init` payload and
may return an authenticated derived context. It cannot inject runtime
capabilities. `Shutdown` stops accepting new subscription operations, cancels
active event work, removes hub membership, closes owned streams, and waits until
completion or its context deadline. It does not close an application-owned
shared transport.

`Handler` supports:

- existing bounded POST query/mutation execution;
- HTTP WebSocket upgrade when the negotiated subprotocol is exactly
  `graphql-transport-ws`; and
- protocol messages `connection_init`, `connection_ack`, `ping`, `pong`,
  `subscribe`, `next`, `error`, and `complete` under normalized limits.

One connection may host bounded concurrent subscription operations. Duplicate
operation IDs, multiple initialization, init timeout/size overflow, invalid
message shape, query/mutation sent as a subscription, a subscription document
with more than one root, and unsupported subprotocols close/refuse with stable
sanitized behavior. The obsolete `graphql-ws` protocol and GraphQL-over-SSE are
not part of this ABI.

## 8. Subscription semantics

At operation start:

1. parse, validate, coerce, and limit the document using P5;
2. require exactly one enabled model event root;
3. normalize and clone `where`, selection, fragments, aliases, and directives;
4. classify filter and selected/dependency fields;
5. snapshot the principal safely;
6. create a fresh caller and authorize model read; and
7. register with the model hub only after every prior step succeeds.

For each created/updated event, the runtime performs the equivalent of an exact
generated caller read using:

```text
event after-identity AND normalized subscription where AND fresh read policy
```

The selected entity and computed/relation dependencies use the existing P3/P5
projection engine. A missing, filtered, or invisible row suppresses the event
without exposing which reason publicly. `entity` is encoded only when selected;
otherwise the runtime selects only what authorization requires.

For deletes, any `where` suppresses the event. Without `where`, delivery requires
a sufficient private pre-delete snapshot and fresh instance read authorization.
The snapshot is never encoded and `entity` remains null.

Event delivery is at-least-once. GraphQL clients may receive duplicate
`eventID`s after transport/publisher recovery. They must deduplicate if their
application requires it.

## 9. Stable public errors

P7 adds these public categories without exposing trusted causes:

```text
GOLEM_EVENT_CONFIG
GOLEM_EVENT_CODEC
GOLEM_EVENT_SOURCE_CLOSED
GOLEM_EVENT_TRANSPORT
GOLEM_EVENT_PUBLISHER_RUNNING
GOLEM_EVENT_POISON
GOLEM_SUBSCRIPTION_INVALID
GOLEM_SUBSCRIPTION_OVERFLOW
GOLEM_SUBSCRIPTION_REVALIDATION
GOLEM_SUBSCRIPTION_SOURCE_CLOSED
GOLEM_SUBSCRIPTION_CANCELLED
GOLEM_CDC_INVALID
GOLEM_CDC_UNAVAILABLE
```

GraphQL maps bad documents/variables to existing validation/input codes,
authentication/revalidation to `UNAUTHENTICATED`, authorization suppression to
no frame, overflow to a subscription error/complete, and invariant/transport
failure to sanitized internal/source errors. SQL, driver text, stack traces,
principal values, identities, event bytes, and snapshots never appear in a
public message or WebSocket close reason.

## 10. CDC SPI

CDC is adapter infrastructure, not ordinary application authoring:

```go
type CDCAdapter interface {
    Identity() CDCIdentity
    CorrelatesGolemTransaction(context.Context, CDCCorrelationInput) (bool, error)
    Run(context.Context, CDCEmitter) error
}

type CDCCorrelationInput struct { /* immutable */ }

func (input CDCCorrelationInput) SourceTransactionID() string
func (input CDCCorrelationInput) Cursor() []byte

type CDCEmitter interface {
    Emit(context.Context, CDCBatchInput) error
}

type CDCBatchInput struct {
    SourceTransactionID string
    RecordedAt          time.Time
    Cursor              []byte
    Changes             []CDCChangeInput
}

type CDCChangeInput struct {
    Ordinal uint32
    Model   golem.ModelID
    Action  golem.EventAction
    Before  *golem.RuntimeModelRow
    After   *golem.RuntimeModelRow
}

type CDCIdentity struct {
    Name     string
    Version string
    Provider golem.Provider
}
```

The adapter is a trusted installation boundary, but its input is still cloned
and validated. `SourceTransactionID` and transaction-level `RecordedAt` must be
stable across replay. `RecordedAt` is non-zero UTC at exact microsecond
precision; it is source-log time, not a fresh Golem worker clock reading.
Change ordinals must be exactly `1..N`; `Cursor` is opaque and bounded;
model/action and exact before/after images must agree with the active or
registered historical event schema. Golem derives deterministic event IDs from
adapter identity, source transaction identity, and ordinal, then derives
ordered record identity and the canonical event envelope. Replaying the same
source transaction and ordinal therefore produces both the same event ID and
the same canonical bytes. The adapter cannot supply a GraphQL payload, private
authorization decision, event ID, or encoded event. `Emit` returns only after
transport acceptance. The adapter may persist/advance its adapter-owned
checkpoint only after that success under its documented replay contract.

Correlation is a required adapter-owned capability because only the adapter can
interpret its source transaction identity, cursor, and transaction contents.
Core Golem does not guess a PostgreSQL transaction/WAL mapping, invent a SQLite
transaction identity, or add a second correlation table. The runtime supplies
an immutable owned `CDCCorrelationInput`; a transaction classified as a Golem
write is suppressed before fact encoding or transport publication.

Adapters construct those private images with `golem.RuntimeCDCModelRow`. Calling
that constructor is the adapter's explicit assertion that the cells came from a
complete persisted stored-scalar image. Ordinary `RuntimeModelReadRow` values,
authorized projections, masked rows, relation values/counts, response-path
occurrences, incomplete inventories, and foreign fields are rejected by CDC
encoding. This provenance split is required because public `ReadNull`
intentionally does not reveal whether NULL came from storage or authorization
masking; only the trusted CDC constructor can assert the former.

Generated application configuration accepts a bounded slice:

```go
CDCAdapters []events.CDCAdapter
```

No adapters means `EventCapabilities().ExternalWritesObserved() == false` and
out-of-process writes are intentionally invisible. Installing an adapter makes
no support claim until that concrete adapter passes the common P7 conformance
harness and its provider-specific live/restart evidence.

## 11. What this ABI deliberately does not expose

- no `Publish` method on `Caller`, model clients, or GraphQL;
- no public constructor for an event, notice, subscription, or CDC record;
- no delete-snapshot accessor;
- no raw outbox/delivery table or SQL access;
- no subscriber policy callback or authorization override;
- no unbounded channel or configurable drop-oldest/drop-newest mode;
- no lifetime caller/policy/cache handle;
- no exactly-once flag;
- no generic message payload/topic/queue API;
- no implicit publisher goroutine from `Open`; and
- no promise that external SQL writes are visible without a validated adapter.

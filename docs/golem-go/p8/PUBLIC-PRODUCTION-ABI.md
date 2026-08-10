# P8 public production and release ABI

Status: **controlling ABI; P8-A and P8-B local implementation are complete;
P8 implementation is ongoing**

This document freezes the new public surface P8 is allowed to add. Existing
P1–P7 model, policy, caller, mutation, GraphQL, analytics, and event APIs remain
owned by their phase documents. P8 may connect and observe them; it may not
create alternate operation engines.

## 1. Module and installation

The Go module is:

```text
github.com/eleven-am/golem/go
```

Repository tags for this nested module are:

```text
go/vX.Y.Z
```

Consumers install the command with:

```bash
go install github.com/eleven-am/golem/go/cmd/golem@vX.Y.Z
```

and import public packages without a local `replace` directive:

```go
import (
    "github.com/eleven-am/golem/go/golem"
    "github.com/eleven-am/golem/go/provider/postgresql"
    "github.com/eleven-am/golem/go/provider/sqlite"
)
```

No public example imports a path containing `/internal/`.

## 2. Verified database handle

Package `provider` owns the common handle:

```go
package provider

type Database struct { /* sealed */ }

func (database *Database) Provider() golem.Provider
func (database *Database) Capabilities() Capabilities
func (database *Database) Pool() PoolStatus
func (database *Database) UnsafeSQLX() *sqlx.DB
func (database *Database) Close() error
```

`Database` cannot be constructed by an application. Provider packages create it
only after configuration and capability proof. `UnsafeSQLX` is the one explicit
trusted-infrastructure escape for application composition, separately reviewed
operations, and integration with non-Golem-owned tables. It does not make raw
SQL available through Caller, GraphQL, custom operations, or hooks.

Raw work through `UnsafeSQLX` bypasses Golem authorization, validation, hooks,
transaction composition, loader invalidation, outbox facts, and events. It must
not be presented as an alternative Golem mutation API, and it cannot join a
`Caller.Transaction`. Changing provider-owned driver/session/pool invariants
after opening the handle is unsupported. The generated application restores the
recorded pool profile before re-proving the live provider and schema. Changes
made through the unsafe escape after application opening remain the trusted
infrastructure caller's responsibility.

The opener owns `Close`. Closing is concurrency-safe and idempotent. A generated
application borrows the handle and never closes it. Opening an application with
a nil, closed, or unverified handle fails before the application is published.

### 2.1 Safe capability view

```go
type Capabilities struct { /* immutable */ }

func (value Capabilities) Provider() golem.Provider
func (value Capabilities) ServerVersion() Version
func (value Capabilities) Features() []Feature

type Version struct {
    Major uint32
    Minor uint32
    Patch uint32
}

type Feature string
```

Feature names are closed, versioned diagnostic identifiers. Slices are cloned.
The capability value contains no DSN, host, database name, user, credential,
connection, SQL, provider error, schema object name, or application data.

### 2.2 Pool status

```go
type PoolStatus struct { /* immutable configuration, not live data */ }

func (value PoolStatus) MaximumOpen() int
func (value PoolStatus) MaximumIdle() int
func (value PoolStatus) ConnectionMaximumLifetime() time.Duration
func (value PoolStatus) ConnectionMaximumIdleTime() time.Duration
```

The status reports normalized configuration only. Live pool metrics are emitted
through observability and are not embedded in capability fingerprints.

There is no public `Adopt(*sqlx.DB)` in the first release. A later adoption API
must prove session/driver invariants for every future pooled connection and
cannot be introduced as a convenience wrapper around a one-connection probe.

### 2.3 Closed provider failures

```go
type Code string

const (
    CodeConfig Code = "PROVIDER_CONFIG"
    CodeOpen   Code = "PROVIDER_OPEN"
    CodeClose  Code = "PROVIDER_CLOSE"
)

func CodeOf(err error) (Code, bool)
```

The concrete provider error is private. `CodeOf` recognizes only errors created
by the provider lifecycle. Configuration, open/probe, and close failures expose
a closed code and sanitized message, never a wrapped driver error.

## 3. SQLite opening

Package `provider/sqlite` exposes:

```go
type Config struct {
    DataSourceName string
}

func Open(
    ctx context.Context,
    config Config,
) (*provider.Database, error)
```

SQLite owns:

- the `modernc.org/sqlite` driver;
- named/shared-memory refusal rules;
- foreign-key and busy-timeout pragmas on every connection;
- immediate transaction locking mode;
- verified bounded pool width;
- SQLite 3.38+ capability floor;
- JSON1 and generated-column probes;
- P2 policy functions and exact-value codecs; and
- P6 analytical exactness functions and probes.

An empty DSN, private `:memory:` database, caller-supplied provider-owned pragma,
or incompatible URI is refused. A supported in-memory application uses a named
`file:` URI with shared cache. Provider-owned query parameters are inserted
canonically and cannot be overridden by caller order or casing.

The first release does not expose arbitrary SQLite pool tuning because its
concurrency and transaction semantics are coupled to the verified provider
profile.

## 4. PostgreSQL opening

Package `provider/postgresql` exposes:

```go
type Config struct {
    DataSourceName string
    Pool           PoolConfig
}

type PoolConfig struct {
    MaximumOpen              int
    MaximumIdle              int
    ConnectionMaximumLifetime time.Duration
    ConnectionMaximumIdleTime time.Duration
}

func Open(
    ctx context.Context,
    config Config,
) (*provider.Database, error)
```

Zero pool fields select bounded provider defaults recorded by `Database.Pool`.
Nonzero values are validated against documented hard limits. Negative values,
idle greater than open, unbounded open connections, and nonsensical durations
are refused.

PostgreSQL owns:

- pgx/stdlib driver configuration;
- refusal of an empty DSN and ambient libpq-only configuration;
- refusal of caller `options` and direct overrides of provider-owned session
  settings;
- UTC timezone, ISO/YMD date style, ISO-8601 interval style, and standard string
  session parameters for every connection;
- the PostgreSQL 15+ floor and live feature probes;
- exact policy, JSON/list, relation, advisory-lock, generated-column, and
  analytical capability proofs; and
- connection cleanup on every failed open/probe path.

Public failures never echo the supplied DSN or raw provider error text. Provider
open errors expose a closed failure code and sanitized message; they do not
unwrap to a driver error that could defeat that boundary.

## 5. Generated application opening

Wave ownership remains explicit while P8 is in progress. P8-A froze the
verified `Database` handle, reviewed-migration document, startup preflight, and
ownership semantics in this section. P8-F subsequently completed the explicit
generated-ABI transition from P7's event-only observer to the unified
`Observer observe.Observer` field shown in the final configuration. Generated
configuration no longer retains the superseded `EventObserver events.Observer`
field.

P8 deliberately advances the generated schema ABI to
`SchemaBundleFormatVersion == 2` and the template ABI to `p8-go-abi-v5`.
The generated-manifest format is likewise version 2: each provider inventory
may carry the SHA-256 fingerprint of its canonical reviewed migration manifest,
and that fingerprint participates directly in `GenerationDigest`. The
migration bytes themselves contain no generation stamp, so the generator can
compute this input first and its provisional/final stamping pass has no digest
cycle.
Version 2 binds every provider schema to one immutable reviewed migration
manifest without exposing migration or physical-schema implementation types:

```go
type MigrationManifestDocument struct { /* opaque */ }

func GeneratedMigrationManifestDocument(
    generation SchemaDigest,
    provider Provider,
    canonicalManifest []byte,
) MigrationManifestDocument

func GeneratedProviderSchemaDocumentWithMigration(
    provider Provider,
    systemFingerprint SchemaDigest,
    schema SchemaDocument,
    migration MigrationManifestDocument,
) ProviderSchemaDocument

func (SchemaBundle) MigrationManifest(
    provider Provider,
) ([]byte, error)
```

These constructors are code-generation composition points, not a caller
override in generated `Config`. The opaque document exposes no fields,
constants, identity getters, byte getter, binding getter, or provider-document
migration accessor. The one bundle method takes the selected provider and
returns copy-isolated canonical bytes only after proving the private format,
bundle generation, provider, and binding. The runtime
then strictly decodes and validates the complete
manifest, requires at least one reviewed entry, and proves that its provider,
model head, and physical head match the selected generated bundle. A generated
application with a missing, empty, foreign, malformed, or stale document is
refused during preflight before database work. Production `generate` and
`check` likewise refuse a missing or empty reviewed provider history.

Generated application configuration replaces separate `DB` and `Provider`
fields with one verified handle:

```go
type Config[P any] struct {
    Database               *provider.Database
    ReadLimits             runtime.ReadLimits
    MutationLimits         runtime.MutationLimits
    AnalyticsLimits        runtime.AnalyticsLimits
    EventLimits            events.Limits
    EventTransport         events.EventTransport
    CDCAdapters            []events.CDCAdapter
    Observer               observe.Observer
    ResolvePrincipal       func(context.Context, P) (Actor, error)
    SnapshotPrincipal      func(P) (P, error)
    SnapshotActor          func(Actor) (Actor, error)
    AfterCommitError       func(context.Context, golem.AfterCommitFailure)
    AuditPrincipal         func(P) string
    ReportScopedQuery      func(context.Context, golem.ScopedAuditRecord)
    ReportEventOperator    events.OperatorAudit
    HistoricalEventBundles []golem.SchemaBundle
}

func Open[P any](
    ctx context.Context,
    config Config[P],
) (*App[P], error)
```

`Open` performs no migration and starts no goroutine. It validates:

- provider declaration and live capability proof;
- complete physical and system schema fingerprint;
- exact migration completeness: the runtime reads the ledger object and
  namespace named by the selected physical schema and requires equality with
  the entire reviewed manifest; missing, shorter, longer/ahead, rewritten,
  reordered, failed, running, or pending histories are refused;
- generated bundle, descriptor, binding, GraphQL, analytics, and event digest
  agreement;
- required hook, scoped-read, event, principal snapshot, and observer
  configuration; and
- deployment-profile compatibility.

Lifecycle remains application-owned:

```go
publisherErr := app.RunEventPublisher(publisherCtx)
```

`RunEventPublisher` owns both durable outbox publication and configured CDC
workers, as frozen by P7. The host cancels its context and waits for it before
closing the provider database. `Open` never hides a background publisher,
subscriber consumer, CDC worker, migration, or retry loop.

## 6. Diagnostics commands

### 6.1 Version

```text
golem version [--json]
```

Human output contains the semantic version and commit. JSON is:

```json
{
  "formatVersion": 1,
  "module": "github.com/eleven-am/golem/go",
  "version": "vX.Y.Z",
  "commit": "40 lowercase hex characters",
  "generatorABI": "closed version",
  "runtimeABI": "closed version"
}
```

Development builds use an explicit `devel` version and cannot satisfy a release
publication gate. A module-cache install may report the all-zero 40-hex commit
sentinel when Go build information contains the module version but not its VCS
revision. Release archives and publication gates require a nonzero commit that
agrees with the version tag.

### 6.2 Doctor

```text
golem doctor
  [--schema <pattern>] [--root <function>]
  [--migrations <directory>]
  --provider <sqlite|postgresql> --dsn <value>
  [--json]
```

`doctor` is read-only. It compiles the schema, verifies reviewed migration
history, opens/probes the provider through the same public lifecycle, checks
live migration ledger and physical/system fingerprints, and reports capability
and compatibility state. It never applies a migration, starts application
workers, or changes database state beyond provider-safe temporary capability
probes that are cleaned before return.

JSON is versioned and contains only closed values:

```json
{
  "formatVersion": 1,
  "release": "vX.Y.Z",
  "provider": "postgresql",
  "capabilities": "pass|fail",
  "history": "current|pending|incomplete|invalid",
  "schema": "current|drift|unreachable",
  "generation": "current|incompatible",
  "diagnostics": [
    {"code": "GOLEM_CLOSED_CODE", "severity": "info|warning|error"}
  ]
}
```

The JSON contains no DSN, host, port, database/user name, SQL, schema/table/field
name, source path outside the module, raw error, row count, application value,
or principal information. Event transport, publisher, and CDC configuration are
runtime application inputs and are therefore reported by the opened
application's P7 `EventCapabilities`, not guessed by `doctor` from a schema and
database.

## 7. Observability ABI

Package `observe` exposes one closed record stream:

```go
type Observer interface {
    ObserveGolem(context.Context, Observation)
}

type Observation struct { /* immutable */ }

func (value Observation) Kind() Kind
func (value Observation) Phase() Phase
func (value Observation) Outcome() Outcome
func (value Observation) Reason() Reason
func (value Observation) Provider() golem.Provider
func (value Observation) ModelID() golem.ModelID
func (value Observation) Operation() Operation
func (value Observation) Duration() time.Duration
func (value Observation) StatementCount() int
func (value Observation) Attempt() int
func (value Observation) QueueDepth() int
func (value Observation) QueueLimit() int
func (value Observation) AggregateCount() int64
```

`Kind`, `Phase`, `Outcome`, `Reason`, and `Operation` are closed string types.
Unknown values are rejected before observation. All numeric values are
nonnegative and bounded: duration is at most 24 hours and every count is at
most 2,147,483,647. `ModelID` is a stable opaque identity, not a public
model name.

Observation covers runtime, read, mutation, GraphQL, analytics, hook,
migration, event, subscription, CDC, and shutdown families. Existing event
observations are adapted into this stream; there is no duplicate event metric
engine.

Observer invocation:

- recovers panic;
- cannot replace or wrap the public result;
- cannot hold a transaction, connection, lease, or queue slot;
- receives a context with a bounded observation deadline;
- executes through a bounded dispatcher when an adapter is asynchronous; and
- increments a safe dropped-observation counter when an adapter cannot accept
  work.

The asynchronous boundary is itself public and closed:

```go
const DefaultQueueCapacity = 1024
const MaximumQueueCapacity = 65536

type DispatcherConfig struct {
    QueueCapacity int
}

func NewDispatcher(target Observer, config DispatcherConfig) (*Dispatcher, error)
func (dispatcher *Dispatcher) ObserveGolem(context.Context, Observation)
func (dispatcher *Dispatcher) Dropped() uint64
func (dispatcher *Dispatcher) Shutdown(context.Context) error
```

`QueueCapacity == 0` selects `DefaultQueueCapacity`; every other accepted
capacity is in `[1, MaximumQueueCapacity]`. A dispatcher owns exactly one
worker and `ObserveGolem` only performs a validated, nonblocking bounded-queue
offer. It never creates a goroutine per observation. A full or closed queue
increments `Dropped`. `Shutdown` closes intake immediately and deterministically
drops the queued, not-yet-started records; it waits for the one active callback
only until the caller's context expires. A callback that ignores its 100 ms
observation context can therefore strand at most the dispatcher's one worker,
never an application request or shutdown. Every observation offered after
shutdown is counted as dropped.

Package `observe/slog` maps records to stable structured attributes. Package
`observe/otel` maps them to documented OpenTelemetry metrics and spans. Neither
adapter records SQL statements, bind values, errors, GraphQL documents,
variables, responses, principal/actor values, row data, DSNs, or private event
bytes.

The adapter constructors and lifecycle are:

```go
// package observe/slog
type Config struct {
    Logger        *slog.Logger
    QueueCapacity int
}
func New(Config) (*Adapter, error)
func (*Adapter) ObserveGolem(context.Context, observe.Observation)
func (*Adapter) Dropped() uint64
func (*Adapter) Shutdown(context.Context) error

// package observe/otel
type Config struct {
    MeterProvider  metric.MeterProvider
    TracerProvider trace.TracerProvider
    QueueCapacity  int
}
func New(Config) (*Adapter, error)
func (*Adapter) ObserveGolem(context.Context, observe.Observation)
func (*Adapter) Dropped() uint64
func (*Adapter) Shutdown(context.Context) error
```

Both providers are mandatory in the OTel configuration. The slog adapter uses
the fixed message `golem.observation.v1`. Both adapters emit only the checked
manifest's closed identity/enumeration attributes and nonnegative numeric
values. They never derive an attribute from an error, SQL text, an application
or schema name, a GraphQL value, a principal, a row, a DSN, or event data.

Metric and trace names are versioned in a checked-in manifest. Renaming or
retyping one is a compatibility change.

The same manifest explicitly classifies the provider-independent publisher
retry/block, retention-orchestration, and transport-reconnect paths. Every
other operation must have a dynamic production occurrence on both SQLite and
PostgreSQL; an unlisted provider exemption is a compatibility-test failure.

Runtime observations include reviewed-ledger/schema inspection performed by
application open. The `migration apply` and `doctor` CLI operations expose
their closed deterministic diagnostics instead; they do not accept an
application observer and do not pretend to be runtime operations. Shutdown
records similarly cover the truthful GraphQL HTTP/WebSocket and publisher
lifecycles. There is no `shutdown.application` record because P8 deliberately
does not give the application runtime ownership of the caller's database or an
application-wide `Close` lifecycle.

Hook operations mirror the public hook ABI exactly. An upsert emits the
selected branch's `hook.create` or `hook.update` record; there is no synthetic
`hook.upsert` hook because no such hook can be authored.
An after-commit hook keeps that selected operation and uses the closed
`after_commit` phase; there is likewise no synthetic `hook.after_commit`
operation because after-commit is a phase, not an authored hook operation.

Publisher claims emit three separate depth operations for `pending`, `blocked`,
and `retired` durable delivery rows. Their `AggregateCount` is computed by one
conditional-aggregate statement inside the provider claim transaction. The
claimed batch's queue depth and individual block/retire transitions remain
separate records and are never substituted for backlog depth. The shared SQL
cost is charged once to `event.depth_pending`; the blocked and retired sibling
records have zero statements, so ancestor accounting never triples one query.

## 8. Production example contract

The checked-in external-style example is a complete single application, not a
set of federated services. It registers at least:

```text
User
Session
Post
Comment
Tag
PostTag
```

It includes a composite key, recursive comment relation, unique session token
hash, ordered indexes, model policies, conditional user fields, hooks, custom
query and transaction mutation, computed and batched computed fields,
analytics, event subscriptions, and both provider migrations.

The example exposes:

```text
GET  /health/live
GET  /health/ready
POST /graphql
GET  /graphql  (WebSocket upgrade for graphql-transport-ws)
```

Liveness reports process health only. Readiness fails for a closed database,
schema drift/incomplete migration, required event transport unavailability, or
incompatible generated artifacts. Neither endpoint exposes capability details,
schema names, backlog contents, principal data, or raw errors.

All ordinary model CRUD, authorization, transactions, GraphQL, and events come
from generated Golem code. Handwritten code is limited to models, policies,
hooks, computed/custom functions, authentication/principal resolution,
infrastructure wiring, and HTTP lifecycle.

## 9. Deployment and migration contract

The documented deployment order is:

```text
build immutable application and golem CLI from one version
  -> golem check
  -> backup/restore rehearsal according to provider runbook
  -> golem migration apply as a separate deployment step
  -> golem doctor
  -> start application
  -> readiness passes
  -> start explicitly owned publisher/CDC workers
```

Application startup never auto-applies migrations. A migration that requires an
unsupported semantic cast, backfill, manual companion, extension, or
nontransactional phase is refused with a stable diagnostic. Documentation tells
operators to perform such data work through a separately reviewed deployment
procedure and return the database to the exact expected Golem schema before
starting the new application; Golem does not execute arbitrary SQL disguised as
a migration.

## 10. Compatibility contract

Every release publishes a machine-readable compatibility manifest containing:

```text
module/version/tag/commit
minimum Go version
supported provider/version profiles
public API digest
generated ABI versions
GraphQL ABI version
ModelIR/ContractIR/physical format versions
migration manifest/ledger versions
fact/event/principal snapshot codec versions
supported historical decode versions
required regeneration/migration/operator actions
known intentional boundaries
```

The manifest contains digests and closed identifiers, not source or schema
contents.

Compatibility rules are:

- **patch**: no breaking handwritten Go, generated Go, GraphQL, persisted format,
  reviewed history, or CLI JSON change;
- **minor**: additive API/schema behavior only; required regeneration or
  migration is explicit and old persisted versions remain decoded or fail with
  a documented migration path;
- **major**: breaking changes require an executable migration guide and a
  machine-detectable incompatibility, never silent reinterpretation.

Generated runtime mismatch, unsupported persisted version, or migration history
mismatch fails at generate/check/doctor/open before serving a request.

## 11. Release artifacts

A Go release contains:

- the signed `go/vX.Y.Z` tag;
- resolvable Go module source;
- `golem` CLI archives for declared platforms;
- SHA-256 checksums;
- source and binary SBOMs;
- build provenance tied to the tag commit;
- compatibility manifest;
- changelog, migration guide, known boundaries, and security reporting process;
  and
- exact evidence summary linking the hosted workflow run.

The release workflow creates artifacts once. A retry verifies and reuses
byte-identical artifacts or fails; it does not silently replace an artifact for
an existing tag/version.

The tracked `compatibility/manifest.json` is the canonical development template,
with `development: true`, version `devel`, an empty tag, and a zero commit. It
does not and cannot embed the hash of the commit that contains it. Release
tooling verifies the template through its separately compiled trusted digest,
copies it, and changes only the release object to the candidate version,
`go/vX.Y.Z` tag, and already-resolved tag-target commit. That canonical copy is
the published compatibility-manifest asset; it is not written back into tagged
source. Signed provenance binds the checked-template digest, published-manifest
digest, tag commit, and all released artifact subjects.

## 12. Explicit non-claims

The production ABI does not claim:

- federation or schema stitching;
- MySQL;
- built-in multi-process broker or CDC vendor drivers;
- external-write observation without CDC;
- exactly-once events;
- raw SQL through authorized surfaces;
- automatic production migrations;
- arbitrary semantic migration transforms;
- general to-many relation analytics; or
- queue/render/workflow behavior.

These boundaries appear in quickstart, capability output, deployment docs, and
release notes rather than only in internal architecture documents.

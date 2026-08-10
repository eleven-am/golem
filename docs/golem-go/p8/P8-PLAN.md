# P8 hardening and release plan

Status: **implementation in progress; P8-A and P8-B local implementation are
complete**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 3, 5, 18–23,
and [`PHASE_MAP.md`](../../../go/phase0/PHASE_MAP.md). P1 owns
schema compilation, generation, migrations, and fingerprints. P2 owns policy
semantics and SQL agreement. P3–P7 own the executable read, mutation, GraphQL,
analytics, and event engines. P8 proves, packages, documents, and releases those
engines. It does not replace them or introduce a parallel runtime.

The exact application and operator contract is frozen in
[`PUBLIC-PRODUCTION-ABI.md`](./PUBLIC-PRODUCTION-ABI.md). Completion is governed
by [`P8-EVIDENCE.md`](./P8-EVIDENCE.md). P8 is not complete while any mandatory
ledger row is `PENDING`, `FAIL`, skipped on a required profile, or supported
only by prose.

## 1. Definition of down

P8 is done when an application author who does not have this repository checked
out can, from one released Go module version:

1. install the `golem` command without a `replace` directive;
2. define a complete application containing User, Session, Post, Comment, Tag,
   and PostTag models, model-attached policies and hooks, computed fields, and
   custom GraphQL operations;
3. inspect, generate, check, migrate, and start that application on SQLite or
   PostgreSQL through public provider-owned connection APIs;
4. use the generated caller, transaction, GraphQL, analytics, event, and
   subscription surfaces without importing a Golem `internal` package or
   recreating provider connection invariants;
5. obtain equivalent authorized behavior through every applicable public entry
   point, with no difference in rows, masks, errors, committed changes, facts,
   or event visibility except documented transport encoding;
6. operate the application under the declared single-process and externally
   adapted multi-process deployment profiles with bounded connections,
   goroutines, queues, statements, memory ownership, and shutdown time;
7. diagnose startup, schema, migration, capability, event, and compatibility
   failures without credentials, SQL operands, private dependencies, row data,
   principal data, or private event snapshots appearing in public diagnostics
   or telemetry;
8. upgrade a database and generated application from every frozen supported
   compatibility fixture without data, authorization, migration-history, or
   pending-event loss;
9. follow checked-in quickstart, deployment, migration, authorization,
   observability, failure-recovery, and upgrade documentation that is executed
   by CI; and
10. reproduce the release from a protected `go/vX.Y.Z` tag, including module
    resolution, CLI archives, checksums, software bill of materials, provenance,
    changelog, and all required provider/security evidence.

Completion requires live SQLite and PostgreSQL tests, a clean external consumer
module, cross-entry-point and independent disclosure oracles, subprocess
failure/recovery tests, bounded-load and leak tests, compatibility fixtures,
documentation execution, hosted CI, and release-candidate installation through
the same artifact path used by the final release.

Passing the existing P0–P7 package tests is necessary but not sufficient.

## 2. Exact boundary

### 2.1 Included

- Public provider-owned database opening, capability reporting, ownership, and
  closing for SQLite and PostgreSQL.
- Generated application configuration updated to consume the public verified
  database handle without a separately supplied provider enum.
- No public raw-pool adoption path in the first release. Any later adoption API
  is a separately frozen addition and is admitted only where the provider can
  prove every required invariant for all present and future connections; an
  unverifiable pool is refused.
- `golem version` and `golem doctor` with stable human and JSON output.
- One executable external-style social application containing multiple models,
  relations, sessions, authorization, hooks, computed fields, custom query and
  mutation roots, analytics, events, and subscriptions.
- Cross-entry-point equivalence for generated caller, caller transaction,
  GraphQL, custom GraphQL operations, and events where the operation exists on
  each surface.
- Authorization-disclosure red-team tests across errors, masks, hooks, computed
  fields, custom roots, scoped reads, analytics, events, observers, logs, traces,
  metrics, and operator diagnostics.
- Bounded load, cancellation, slow-client, connection exhaustion, transaction
  contention, publisher outage, crash/restart, and graceful-shutdown tests.
- A closed, transport-neutral observability contract plus maintained `slog` and
  OpenTelemetry adapters. Observer failure and backpressure cannot change
  application correctness.
- Supported-version, semantic-versioning, generated-artifact, GraphQL-contract,
  database-schema, migration-history, fact/event-codec, and upgrade policy.
- Frozen compatibility corpora and provider upgrades from the P7 schema/event
  format into the first released format.
- Go CI, security scanning, artifact reproducibility, release automation, and a
  rollback-safe release checklist.
- Explicit production deployment profiles and runbooks.
- Reconciliation of all controlling Go documentation so no completed phase is
  described as pending and no excluded feature is described as available.

### 2.2 Excluded from P8 completion

- GraphQL federation, schema stitching, entity federation directives, and a
  supergraph router.
- MySQL or a third database provider.
- Queue, render, scheduling, workflow, or arbitrary job execution.
- A new ORM or replacing `sqlx`.
- Automatic production migration during application startup.
- Raw SQL access through caller, GraphQL, custom-operation, hook, analytics, or
  event APIs.
- Exactly-once events, global transaction ordering, event sourcing, temporal
  reconstruction, and generic replay/history APIs.
- Built-in vendor CDC drivers. P7's public CDC SPI, conformance harness, and
  explicit no-adapter behavior remain the contract.
- A mandatory vendor message broker in core. Multi-process event delivery
  requires an installed cross-process transport that passes the P7 transport
  conformance suite; the first core release does not falsely claim one.
- General to-many/mixed relation aggregation, aggregate hooks, custom
  subscriptions, GraphQL live queries, uploads, or other post-v1 feature work.
- Supporting arbitrary application-authored migration SQL. Operations that
  require semantic casts or backfills remain explicit refusals unless a closed,
  typed migration extension is separately specified and evidenced.

These exclusions are public release boundaries, not implied follow-up work
inside P8.

## 3. Decisions frozen before implementation

### 3.1 One composed application is the primary product

One `DefineSchema` may register every model in an application. Generated code
produces one coherent model registry, runtime, caller surface, GraphQL schema,
and event registry. User, Session, Post, Comment, Tag, and PostTag do not need
separate services or GraphQL schemas.

P8 examples and documentation use this composition model. Federation is not
needed to build a complete Golem application. An application that later combines
Golem's HTTP GraphQL endpoint with another system owns that gateway or stitching
layer outside Golem.

### 3.2 Go module and tag identity

The released module remains:

```text
github.com/eleven-am/golem/go
```

Because it is a nested module in repository directory `go`, its repository tags
are `go/vX.Y.Z`. TypeScript release tags and Go release tags are independent.
The installed command remains:

```text
go install github.com/eleven-am/golem/go/cmd/golem@vX.Y.Z
```

Release automation refuses an unprefixed tag, a tag whose version disagrees
with generated release metadata, a dirty source tree, or an artifact built from
a commit other than the tag target.

The checked compatibility manifest is a trusted development template and does
not claim to contain its own commit hash. After resolving and verifying the
signed tag target, release tooling copies that template, changes only its
release identity to version/tag/target commit, and publishes the canonical copy
as an asset. Signed provenance binds both manifest digests, the tag commit, and
the released artifact subjects; the materialized copy is never written back
into the tagged source tree.

The first version number is chosen as an explicit release decision after the
release-candidate evidence passes; the plan does not silently equate the current
TypeScript version with the Go module version.

### 3.3 Provider-owned database lifecycle

Applications no longer reconstruct the behavior currently hidden in
`internal/provider/{sqlite,postgresql}`. Public provider packages return a
sealed verified database handle containing the provider identity, capability
report, and owned `*sqlx.DB`.

- SQLite owns driver selection, DSN pragma injection, transaction locking mode,
  pool width, minimum version, JSON/generated-column checks, policy functions,
  and analytical exactness probes.
- PostgreSQL owns pgx configuration, UTC/date/interval/string session settings,
  minimum version, capability probes, and pool defaults.
- The caller that opens the handle owns `Close`.
- Generated `App.Open` verifies the active schema and capabilities but does not
  apply migrations and does not start background workers.
- Provider identity comes from the verified handle, not a second enum that can
  disagree with it.
- DSNs, passwords, connection strings, and provider error details never appear
  in public reports or telemetry.

The first release has no public pool-adoption API. A later pool-adoption API is
admitted only if it can prove invariants for all present and future pooled
connections. P8 refuses rather than documents an unsafe escape hatch.

### 3.4 Deployment profiles are explicit

The release recognizes these profiles:

1. **Embedded single process** — SQLite or PostgreSQL, one application process,
   built-in bounded memory event transport when subscriptions are enabled.
2. **Database-backed single process** — PostgreSQL, one application process,
   the same event guarantees and an independently managed database.
3. **Adapted multi-process** — PostgreSQL and multiple application processes,
   requiring a conformant cross-process event transport. External SQL writes
   remain invisible unless a conformant CDC adapter is also installed.

Startup capabilities and documentation name the active profile. A process-local
transport never claims multi-process fan-out or durability.

### 3.5 Compatibility is multi-layered

Compatibility is evaluated independently for:

- public handwritten Go APIs;
- generated Go source ABI;
- GraphQL schema and error contract;
- ModelIR, ContractIR, and physical-schema formats;
- reviewed migration manifests, snapshots, and ledger rows;
- fact, event, principal-snapshot, and historical-bundle codecs; and
- CLI command and machine-readable output formats.

Patch releases do not make a source-, generated-, schema-, database-, or codec-
breaking change. Minor releases may add optional API/schema capabilities but do
not reinterpret persisted bytes or reviewed history. A required regeneration,
migration, or operator action is stated in release notes. Breaking changes need
a major version and a tested migration guide.

The initial release freezes a checked-in compatibility corpus. Every subsequent
release must load the corpus and prove either compatibility or an explicitly
versioned refusal and migration path.

### 3.6 Observability is closed and non-authoritative

Instrumentation receives immutable, bounded records containing closed operation,
phase, provider, outcome, reason, model identity, counts, and duration fields.
It never receives:

- SQL text or bind values;
- predicates, selectors, inputs, row values, or masks;
- principal or actor values;
- GraphQL variables or response bodies;
- private dependencies or delete snapshots;
- DSNs, credentials, raw provider errors, or arbitrary exception strings.

Instrumentation is best-effort. A panic, block, exporter outage, or cancellation
inside an observer cannot authorize data, roll back or commit a transaction,
acknowledge an event, retain a connection, or alter the public operation result.
Adapters apply bounded buffering or drop telemetry with their own safe counter;
they never create unbounded work.

### 3.7 Performance gates measure bounds before throughput

P8 does not invent a marketing requests-per-second number. Its hard gates are
semantic resource budgets derived from configured limits and statement plans:

- SQL statement count per accepted operation shape;
- maximum open/in-use connections;
- maximum concurrently executing resolvers, computed batches, event evaluations,
  publisher attempts, and CDC workers;
- maximum queue depth and exact overflow behavior;
- goroutine and heap return-to-baseline after cancellation and shutdown; and
- absence of superlinear statement, goroutine, or retained-memory growth as row,
  relation, subscriber, and batch cardinalities scale within configured limits.

Benchmarks record latency, allocation, and throughput baselines for regression
comparison, but release acceptance is not based on an ungrounded absolute RPS
claim. Any later published capacity number must name hardware, provider,
dataset, concurrency, query shape, and percentile.

### 3.8 Release work cannot weaken security

Quickstart convenience, telemetry, provider wrappers, diagnostics, examples,
and compatibility shims all enter through the same generated identities and
runtime. No P8 package may add a second authorization evaluator, SQL renderer,
GraphQL resolver engine, event decoder, or migration executor.

## 4. Work waves

### P8-A — public provider, startup, and diagnostic ABI

Deliver:

- public `provider`, `provider/sqlite`, and `provider/postgresql` packages;
- sealed verified database handle, safe capability report, explicit ownership,
  and provider-specific open/probe behavior;
- generated application config consuming the handle;
- clean failures for nil, closed, mismatched, unverified, unsupported, or
  capability-incomplete databases;
- `golem version` and `golem doctor`, with versioned JSON output and redaction;
- generator/check diagnostics for unsupported deployment capabilities; and
- external-module compile and startup fixtures.

P8-A is serial because the remaining public work depends on this exact startup
and diagnostic contract.

P8-A freezes only the database-handle, reviewed-migration, startup, and
diagnostic portion of generated configuration. The final
`Observer observe.Observer` field and adaptation of the existing P7
`EventObserver events.Observer` field are owned by P8-F; adding that field is
not retroactively treated as P8-A drift.

### P8-B — executable social application and documentation

Local implementation status (2026-08-09): **complete**. The checked-in clean
nested social module, executable quickstart and snippets, SQLite journey, both
PostgreSQL locale journeys, and backup/drift/restore/pending-event recovery
drills pass. Formal evidence rows 6–7 remain `PENDING` until hosted clean-tag
resolution, required mutation kills, and the independent release audit are
recorded; local completion is not a release claim.

Build one external-style application with User, Session, Post, recursive
Comment, Tag, and PostTag. It must demonstrate:

- exact SQL schema, indexes, defaults, keys, and relations;
- model-attached authorization and conditional fields;
- create/read/update/delete, nested writes, transaction closure, and upsert;
- before, transaction-after, and after-commit hooks;
- generated GraphQL plus a custom query, custom transaction mutation, ordinary
  and batched computed fields;
- analytics and the accepted relation grouping shape;
- events and GraphQL subscriptions; and
- SQLite development plus PostgreSQL deployment.

The quickstart and example commands execute in CI from an empty database. The
example may not import `internal`, use handwritten ordinary CRUD resolvers, or
contain a local replacement for generated authorization, transaction, or event
behavior.

Documentation includes install, concepts, schema authoring, authorization,
hooks, programmatic client, GraphQL, custom operations, analytics, migrations,
events, deployment profiles, observability, operations, security boundaries,
troubleshooting, compatibility, and upgrades.

### P8-C — cross-entry-point conformance

Create an independent operation corpus and compare, where applicable:

```text
Caller
CallerTx
GraphQL generated root
GraphQL custom root using Caller/CallerTx
caller event stream
GraphQL event stream
```

For each operation, compare normalized rows, null/masked/present cells, stable
error code and disclosure category, statement classification, committed state,
hook trace, loader invalidation, fact rows, and event visibility. Transport-only
encoding differences are normalized explicitly.

The oracle cannot calculate expected behavior through production planners,
evaluators, GraphQL lowering, event authorization, or serializers.

### P8-D — disclosure and capability red team

Attack every public boundary with:

- missing versus unauthorized rows and relations;
- conditional fields and computed dependencies;
- aliases, fragments, directives, malformed scalars, nested inputs, custom
  operation arguments, and GraphQL validation errors;
- crafted generated identities, foreign predicates/selectors/inputs, stale
  artifacts, and version mismatches;
- hook errors, panics, retry branches, after-commit failures, and system bypass;
- scoped builder joins and aggregation discharge;
- private delete snapshots, event suppression, observer records, and operator
  diagnostics;
- DSNs with credentials and provider errors containing SQL or data; and
- concurrent principals with colliding public audit identifiers.

The suite compares response body, headers, error values, logs, trace/metric
attributes, observer records, and retained state. Secret canaries must be absent
from every unauthorized channel.

### P8-E — bounded load, failure, and recovery

Exercise both providers with controlled cardinality ramps for rows, relation
depth, selected fields, nested mutations, groups, subscribers, batch size, and
publisher backlog. Prove exact configured bounds and cleanup under:

- client cancellation and deadlines;
- slow and disconnected GraphQL clients;
- provider connection starvation;
- SQLite write contention and PostgreSQL serialization/unique conflicts;
- hook and computed-field latency or panic;
- transport outage and duplicate acceptance windows;
- publisher/CDC worker crash and restart;
- application graceful shutdown and forced subprocess termination; and
- migration interruption at every supported transaction boundary.

Record benchmark baselines and machine metadata. A regression threshold may be
relative to a checked-in baseline only when the benchmark is stable; correctness
and resource-bound gates never depend on a noisy timing percentile.

### P8-F — observability and operations

Deliver the closed observation model and adapters described in the public ABI.
Minimum operational coverage includes:

- runtime open/capability/drift;
- caller and GraphQL operation duration/outcome;
- statement and relation-load counts;
- transaction retry/commit/rollback;
- hook phase and after-commit failure;
- aggregate/scoped-read refusal and limits;
- outbox pending/blocked/retired depth, claim/retry/ack;
- active subscriptions, evaluation, suppression, overflow, and cancellation;
- CDC adapter/checkpoint lifecycle; and
- runtime reviewed-migration inspection state.

Migration apply and doctor remain operator commands with closed deterministic
diagnostic output; they do not accept an application observer and therefore do
not fabricate runtime observation records. Observer-configurable operator
commands are future additive work. Likewise, shutdown observations cover the
truthful GraphQL HTTP/WebSocket and publisher lifecycles; P8 has no
application-owned `Close` lifecycle to observe.

Released SQLite and PostgreSQL coordinators compute pending, blocked, and
retired delivery-row depths with one conditional-aggregate statement inside
the same transaction as each publisher claim. The three status depths are
distinct closed operations; claimed-batch length and individual state
transitions are never presented as backlog depth.

Runbooks name diagnosis and recovery for schema drift, incomplete migration,
blocked outbox causation, transport outage, slow subscribers, CDC failure,
capability refusal, and incompatible generated artifacts.

### P8-G — compatibility and upgrade proof

Freeze release-candidate corpora for:

- public source compilation;
- generated Go and GraphQL artifacts;
- inspect/check/version/doctor JSON;
- SQLite and PostgreSQL physical schemas and migration history;
- data containing every exact logical scalar and composite identity;
- pending, delivered, blocked, and historical-version event facts; and
- principal snapshots and conditional-field behavior.

Upgrade each corpus through the public CLI and provider APIs. Verify application
data, keys, constraints, authorization behavior, pending facts, event IDs,
ledger chain, schema fingerprint, and generated/runtime compatibility. Downgrade
is not implied; rollback means deploying the previous compatible binary before
an incompatible migration or restoring a tested database backup afterward.

Publish a compatibility matrix and migration guide with every release.

### P8-H — CI, supply chain, and release automation

Extend hosted CI to run:

- the complete P0–P8 suite on the declared Go toolchain matrix;
- SQLite and supported PostgreSQL-major provider profiles, including the `C`
  and linguistic collation profiles;
- race, repeat, shuffle, fuzz, mutation, subprocess crash, leak, vet, format,
  vulnerability, and external-module tests;
- executable docs and example smoke tests;
- public API and GraphQL schema compatibility checks; and
- reproducible generation and release builds.

The `go/vX.Y.Z` release workflow verifies the tag, runs required hosted gates,
builds supported CLI archives, emits checksums/SBOM/provenance, verifies
`go install` and module resolution through a clean consumer, creates release
notes from the compatibility manifest, and never publishes from an unverified
moving branch.

### P8-I — independent release audit

An audit package and release-candidate job, which do not reuse production
expectation code, must prove:

- the full external social application on both providers;
- cross-entry-point equivalence;
- disclosure-canary absence;
- bounded resource and recovery properties;
- upgrade compatibility;
- public package/import hygiene;
- documentation command accuracy;
- artifact reproducibility; and
- release installation from the candidate tag/artifact channel.

P8-I closes only after every evidence row and named mutation passes on hosted
infrastructure. Local success alone cannot mark the release phase complete.

## 5. Parallelization and ordering

```text
P8-A public startup/provider ABI
   ├── P8-B example and documentation
   ├── P8-C cross-entry conformance
   ├── P8-D disclosure red team
   ├── P8-E load/failure/recovery
   ├── P8-F observability and operations
   └── P8-G compatibility corpus

P8-H CI/release scaffolding starts alongside P8-A,
then absorbs B–G as their gates stabilize.

P8-B + P8-C + P8-D + P8-E + P8-F + P8-G + P8-H
   └── P8-I independent release audit
```

After P8-A freezes the public lifecycle, B–G can be assigned to separate agents
with non-overlapping primary package ownership. P8-C and P8-D remain independent
of production expectation helpers. P8-H owns workflow files; feature agents add
named commands to the ledger rather than editing release policy concurrently.

## 6. Completion discipline

- Each wave begins with its named tests or explicit evidence skeleton.
- Public API changes update `PUBLIC-PRODUCTION-ABI.md` before implementation.
- Every security or recovery invariant has a named mutation that must be killed.
- No required PostgreSQL profile may pass by skipping.
- Examples and documentation are tested as external consumers.
- Generated files are reproduced, not hand-edited.
- Evidence records exact commit, provider versions/profiles, commands, durations,
  mutation kills, fuzz counts, and artifact digests without credentials.
- Documentation-only claims cannot turn a `PENDING` row into `PASS`.
- The branch is merged, hosted gates pass on the merge candidate, and the release
  candidate is installed through the public artifact path before P8 is marked
  complete.

## 7. End state

After P8, the accepted P0–P8 roadmap is complete. Golem for Go may then be
described as a released model-driven backend framework for SQLite and
PostgreSQL within the documented boundaries. New databases, federation, vendor
transports/CDC, broader analytics, render, queue, or other feature families are
post-roadmap extensions and require their own contracts and evidence; they do
not retroactively reopen P8.

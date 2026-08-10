# Golem for Go production guide

Status: **unreleased P8 working documentation**. This guide describes the
implemented P1–P7 behavior and the production contract P8 is proving. A feature
whose P8 evidence is still pending is not release evidence merely because it is
described here. The mandatory state is tracked in
[`p8/P8-EVIDENCE.md`](./p8/P8-EVIDENCE.md).

## Product boundary

One schema composes a complete application. Golem generates typed model
descriptors, caller/system clients, transaction clients, GraphQL execution,
analytics, event facts, and subscriptions. Application authors retain control
of authentication, principal resolution, domain policies, hooks, custom and
computed functions, HTTP/process lifecycle, secrets, backups, and external
infrastructure.

Golem supports SQLite and PostgreSQL with the same authorization and operation
semantics. MySQL is not supported. The first release is not an ORM for arbitrary
SQL and does not provide federation, a broker, vendor CDC drivers, automatic
production migrations, exactly-once events, or arbitrary migration scripts.

## Schema authoring

Persisted scalar fields always have explicit `db` column names and logical SQL
types. Struct and model specifications carry stable IDs, exact table names,
nullability, defaults, generated/read-only/immutable/hidden exposure, primary
and unique keys, ordered indexes, checks, generated expressions, and relations.
GraphQL names and exposure live in the contract fingerprint, not the persistence
fingerprint, so a transport rename alone does not create a database migration.

The schema root explicitly selects every model. Relation targets and join
models must be selected too; discovery never silently expands the persistence
boundary. See the checked-in social models and
[`p1/01-schema-authoring-and-logical-ir.md`](./p1/01-schema-authoring-and-logical-ir.md)
for the complete tag and typed-spec grammar.

## Authorization and conditional fields

Each model implements `DefinePolicy` against `*golem.Rules[Model]` and the
application actor. Rules are ordered; the last applicable declaration wins.
Row grants and field grants are distinct lenses. A policy can therefore allow
a user row while masking an email field, or grant update reach while permitting
only a named field set.

Predicates are typed model expressions over generated fields and relations.
They are compiled into scoped SQL before pagination and are also used to verify
persisted mutation results. Application code does not receive a loaded row in
`CanRead`, and a custom GraphQL operation cannot substitute a separate policy.

Public missing, policy-invisible, and failed guarded single-row targets share
the documented non-disclosing error category. Conditional GraphQL fields must
be nullable because policy masking is represented as null. Public result state
does not explain whether a null came from storage or policy.

`System` and `SystemTx` bypass caller policies and caller hooks. They retain
schema validation, transactions, facts, invalidation, limits, and provider
verification and belong only in trusted application infrastructure.

## Hooks

Hooks are model methods discovered and bound by generation. Read hooks may
replace a typed immutable request before execution and inspect the authorized
typed result after execution. Mutation hooks have three distinct phases:

- before hooks may validate or replace the typed input;
- transaction-after hooks inspect persisted authorized results inside the same
  transaction and must be safe if the database transaction retries; and
- after-commit hooks perform irreversible external work only after commit.

An application transaction closure is never replayed. An after-commit hook
failure cannot turn committed data into a returned mutation failure; route it
through the configured after-commit failure handler. Hooks receive public
typed requests/results and the execution context. They do not receive raw SQL,
the provider pool, private authorization dependencies, or a way to widen caller
capabilities.

Batch mutations produce one durable fact per affected row but do not run
per-row model hooks. Their exact bounded identity set is captured before the
change, and exceeding the configured bound is an error rather than truncation.

## Programmatic caller and transactions

`App.ForPrincipal` resolves and snapshots the actor for one execution. The
generated `Caller` owns typed clients for each model. The model clients expose
authorized `FindUnique`, `FindFirst`, `FindMany`, `Count`, create/update/delete,
upsert, bounded batch mutation, analytics, scoped queries where enabled, and
events where enabled.

Call `caller.Transaction(ctx, func(*CallerTx[P]) error)` to compose multiple
generated operations atomically. Every nested client in the closure uses the
same transaction and actor snapshot. Returning an error or canceling the
context rolls back. Do not perform irreversible network work in a transaction
closure.

The raw pool escape is deliberately separate:
`provider.Database.UnsafeSQLX()` returns `*sqlx.DB` for reviewed infrastructure
and integration with non-Golem tables. It bypasses every Golem application
guarantee and cannot join `CallerTx`. Changing provider-owned session, pragma,
or pool invariants through it is unsupported.

## GraphQL, custom operations, and computed fields

Generated GraphQL and the programmatic caller execute the same runtime plans.
The endpoint includes exposed model reads and mutations, relation selections,
analytics roots, and generated event subscriptions. Limits bound page sizes,
selection depth, fields, statement size/parameters, resolver concurrency, and
computed batches.

A package-level `DefineGraphQL(*golem.GraphQLSchema)` declares custom queries
and mutations. The resolver's generated `*Caller[P]` argument is the same
authorized caller used by ordinary roots. A custom transaction mutation calls
its `Transaction` method; it does not use raw SQL or a second authorization
implementation.

A model `DefineGraphQL(*golem.GraphQLModel[M])` declares ordinary or batched
computed fields. `golem.Requires` declares private dependencies so policy and
masking are settled before resolver invocation. Dependencies do not become
public merely because the computed output is selected. Loaders have explicit
batch bounds and request-local isolation.

Golem does not generate custom subscriptions. Federation and schema stitching
are outside the first release; a gateway may compose the generated endpoint
externally.

## Analytics and scoped queries

Generated clients expose `Aggregate`, `GroupBy`, and the accepted to-one
`RelationGroupBy` shape. They apply caller read authorization before aggregate
calculation and preserve exact integer/decimal semantics on both providers.
GraphQL group limits are bounded by the schema contract. The programmatic
`GroupBy` API is deliberately not capped by GraphQL's `maxGroups`, but remains
subject to runtime and statement limits.

General to-many or mixed relation traversal in aggregation is not implemented
and is refused. Scoped queries are an explicit per-model capability for closed,
typed joins and expressions. They are not raw SQL: only generated scope fields,
approved joins, predicates, grouping, selections, and ordering can be composed,
and every accepted/refused execution emits the configured safe audit record.

## Migrations and generated artifacts

The reviewed history is part of the generated application identity. Generation
requires a non-empty, provider-specific manifest with exact immutable files.
Startup reads the live ledger from the generated system namespace and refuses a
missing, shorter, ahead, reordered, rewritten, running, failed, or otherwise
different history.

The deployment order is:

```text
build application and CLI from one version
  -> golem check
  -> rehearse provider backup and restore
  -> golem migration apply
  -> golem doctor
  -> start application and require readiness
  -> start explicitly owned publisher/CDC workers
```

`App.Open` never migrates. Keep migration snapshots, manifests, SQL, generated
source, and the generation manifest in version control. Do not edit an applied
migration or generated file. Create a new reviewed migration and regenerate.

## Events, subscriptions, and CDC

Accepted mutations write durable, lossless outbox facts in the same database
transaction. The host explicitly runs `App.RunEventPublisher(ctx)`. Event
delivery is at least once, so consumers deduplicate with the stable event ID.
Fresh authorization is evaluated per subscriber before projection. A slow or
overflowing subscriber follows the configured bounded failure behavior; the
runtime never grows an unbounded queue.

The core memory transport is process-local. It is appropriate for one process,
not multi-process fan-out or durability. Multiple PostgreSQL application
processes require an externally supplied transport that passes Golem's transport
conformance suite.

Writes made outside Golem are invisible unless a conformant CDC adapter is
configured. Core supplies the CDC interface and conformance harness, not vendor
drivers. A CDC adapter feeds the same authorization, filtering, projection,
transport, and subscription path; it does not create a privileged event path.

## Deployment profiles

### SQLite single process

Use a named file or named shared-memory URI. Private `:memory:` and
caller-overridden provider pragmas are refused. The verified provider owns the
driver, foreign-key and busy-timeout settings, immediate transaction locking,
pool width, functions, and capability probes. Run exactly one application
process against the file unless a separately tested topology says otherwise.

### PostgreSQL single process

Use PostgreSQL 15 or newer. The verified provider owns pgx configuration, pool
bounds, UTC and other deterministic session settings, capability probes, and
cleanup. Caller DSN `options` and direct provider-session overrides are refused.

### PostgreSQL multiple processes

Use a conformant cross-process event transport. Add a conformant CDC adapter
only if external SQL writers must appear in subscriptions. Read/mutation SQL
semantics remain portable; event reach depends on explicitly installed
infrastructure capabilities.

## Health, observability, and secrets

Liveness reports process health only. Readiness fails closed for a closed
database, schema or migration mismatch, incompatible generated artifacts, or a
required unavailable event transport. Neither endpoint returns provider,
schema, backlog, principal, capability, or raw error detail.

P8's unified `observe` API and maintained slog/OpenTelemetry adapters are not
release-ready until their mandatory evidence passes. Until then, do not build
an integration against an internal observation type. The frozen output contract
contains only closed operation/phase/outcome/reason identities, stable opaque
model identity, bounded counts, and duration. It never contains SQL, bind
values, predicates, inputs, row data, GraphQL documents/variables/results,
principals, DSNs, credentials, private event snapshots, or arbitrary errors.
Observer failure must never alter application correctness.

Treat DSNs, session tokens, signing keys, and CDC/broker credentials as secrets.
Pass them through a secret manager or protected environment, not command
history, source, migration files, health output, logs, traces, or metrics.

## Failure recovery and troubleshooting

Use `golem version --json` to identify CLI/runtime ABI provenance and `golem
doctor --json` for closed machine-readable startup classification. `doctor` is
read-only and cannot repair history or schema.

- `history=pending`: apply the reviewed migration in a separate deployment
  step, then rerun doctor.
- `history=incomplete` or `invalid`: stop rollout. Restore the exact reviewed
  ledger and database from the rehearsed backup; never edit ledger rows by hand.
- `schema=drift`: stop traffic and compare the database with reviewed generated
  artifacts. Do not let startup silently recreate objects.
- `generation=incompatible`: rebuild and regenerate with one module version;
  do not mix generated source from another ABI.
- provider open/config failure: verify the supported provider/version and
  provider-owned DSN restrictions. Public errors are intentionally redacted.
- publisher outage: keep accepting writes only within the reviewed outbox
  capacity policy, restore the transport, and restart the explicitly owned
  publisher. At-least-once delivery can repeat an event.

On process crash, restart with the same generated artifacts and reviewed
history, run doctor, open the application, and restart the publisher. Never
acknowledge an event before its configured transport has accepted it.

### Backup and restore rehearsal

A backup is not accepted merely because `doctor` passes after restore. Rehearse
with a distinctive managed row written through a generated client and at least
one unacknowledged outbox fact. Snapshot the managed row, exact migration ledger,
outbox identities, and delivery state before backup. After restore, require the
same snapshot byte for byte, run `doctor`, then start the explicit publisher and
prove that the restored pending fact reaches the transport and becomes
delivered.

For SQLite, stop every application/worker owner, checkpoint and close the
verified database, prove there is no nonempty WAL sidecar, and only then copy
the database file. Copying the main file while a writer or WAL remains active
is not a supported backup. For PostgreSQL, use a version-compatible logical or
physical backup tool against a disposable rehearsal database and restore into a
new database; never test recovery by mutating the shared source database.

The checked-in example provides a public-only deterministic rehearsal canary:

```text
GOLEM_PROVIDER=sqlite GOLEM_DATABASE_DSN=file:social.sqlite go run ./cmd/social-recovery-fixture seed
GOLEM_PROVIDER=sqlite GOLEM_DATABASE_DSN=file:social.sqlite go run ./cmd/social-recovery-fixture verify
GOLEM_PROVIDER=sqlite GOLEM_DATABASE_DSN=file:social.sqlite go run ./cmd/social-recovery-fixture drain
```

`seed` writes a fixed User and Post through the generated System client and
leaves its event fact pending. `verify` prints the deterministic managed-row,
ledger, fact, and delivery snapshot without ordinary raw CRUD. `drain` runs the
public publisher through the memory transport and requires the exact Post event.
This fixture is a runbook proof aid, not a substitute for production backup
tooling or application-specific recovery canaries.

## Compatibility and upgrades

Pin one released module version for CLI, generated code, and runtime. Patch
releases do not break handwritten Go, generated Go, GraphQL, persisted codecs,
reviewed migration history, or CLI JSON. Minor releases are additive and state
required regeneration/migration/operator work. Breaking changes require a major
version and an executable migration guide.

For an upgrade:

1. read the release compatibility manifest and migration guide;
2. regenerate and review changes in a clean branch;
3. create and review any required migration;
4. rehearse backup, migration, doctor, application startup, publisher recovery,
   and rollback with production-like data;
5. deploy immutable CLI/application artifacts from the same tag; and
6. retain the prior application artifact and provider backup until the new
   readiness and event backlog are healthy.

Never silently reinterpret an unsupported generated, migration, schema, fact,
event, or principal-snapshot format.

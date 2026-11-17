# Compatibility and adoption prerequisites

## Development handoff — 2026-11-17

This handoff accompanies go/v0.9.0 and client/v0.3.0 and covers the
Golem repository only. Application migration, application builds/tests,
application-baseline collection and deployment are outside this task. The
following maps the implementation plan's Golem slices to local acceptance
evidence; it does not certify unrun deployment measurements.

The implementation task was closed on 2026-11-17 with NAS performance explicitly
unverified, as requested. This does not change the recorded measurement limits
or the reader-first release requirements below.

| Slice | Local implementation and acceptance evidence |
| --- | --- |
| P00 Characterization | Generated 25,000-video query fixture, 3,864 × 1,024-dimensional public vector benchmark, and 30-minute mixed server/SDK workload. Local results and their measurement limits are recorded below and in the semantic guide. Target-storage performance is unverified; application-baseline collection is outside this task. |
| P01 Typed DTOs | [Generated HomeFeed HTTP fixture](../internal/generate/pipeline/object_http_acceptance_test.go), named output/input metadata, generated bindings and client type checks. |
| P02 Rows and inputs | [Caller-hydrated embedded rows and item gates](../internal/generate/pipeline/object_rows_acceptance_test.go), plus [presence and exactly-one inputs](../internal/generate/pipeline/input_http_acceptance_test.go). |
| P03 Scalar sets | [Generated caller read/mask/update acceptance](../internal/generate/pipeline/scalar_sets_acceptance_test.go), provider/evaluator checks and large actor-grant fixtures. |
| P04 Vectors | [Generated vector authorization and transaction acceptance](../internal/generate/pipeline/vector_queries_acceptance_test.go), plus SQLite and PostgreSQL semantic lifecycle fixtures. |
| P05 Policy facts | [Generated read/write equivalence](../internal/generate/pipeline/frames_facts_acceptance_test.go), actor-domain/template fixtures, runtime mutation-history and historical-event acceptances under explicit V4 rehearsal. Automatic writers remain V3. |
| P06 Continuations | [Generated HTTP and actual-client acceptance](../internal/generate/pipeline/continuation_acceptance_test.go), authenticated continuation envelopes and client pagination/type checks. |
| P07 Invalidation | [Generated custom invalidation and actual-client acceptance](../internal/generate/pipeline/invalidation_acceptance_test.go), including scope and dependency authority checks. |
| P08 Recovery | Client recovery-generation/stale-response tests, generated invalidation acceptance and the bounded real-SDK mixed workload. Its time in recovery is reported below; uninterrupted liveness is not claimed. |
| P09 Transformations | [Reviewed data-correction command lifecycle](../cmd/golem/migration_data_correction_test.go), runtime ledger/rollback checks and interrupted migration fixtures. Target-NAS migration duration remains open. |
| P10 Maintenance | [Bounded logical-removal and cleanup fixtures](../internal/generate/pipeline/maintenance_acceptance_test.go), including semantic rollback and retry on SQLite and PostgreSQL. |
| P11 Queue priority | Provider/runtime promotion and fencing checks, plus [reserved admission and three-to-one service opportunities](../internal/queue/worker/priority_test.go) and capped-cohort rotation. |
| P12 Snapshot/restore | Public snapshot, both restore modes, released-archive migration, [process-interruption acceptance](../runtime/sqlite_restore_crash_test.go) and generated browser-incarnation reset. Snapshot/restore are explicitly local APIs; physical filesystem failure and NAS durability are not certified. |
| P13 Compatibility | Actual v0.8.8 codec/archive/queue-refusal fixtures and current-tree mixed-writer rehearsal on both providers. go/v0.9.0 supplies V3/V4 readers with automatic V3 writing; deployed validation must precede later V4 activation. |
| P14 Golem integration handoff | Golem-owned restartable copy, generated application, authentication/session and dependency-selection fixtures. Actual Frames migration and its application equivalence/build/test work are outside this task and are not unfinished Golem work. |

Recorded verification covers the standard Go verification tiers, generated
identity checks, SQLite/PostgreSQL fixtures and the 13-package race tier. The
initial broad run exposed a stale public API record and three oracle expectation
failures. After correcting those records/expectations, the public API check,
all three affected oracle packages, and the complete database tier passed.
The required staged pass executed 47 runtime and four generated parents without
skips; the separate actual-client pass executed three parents across five
provider cases without skips. No production changes followed that broad pass;
the later scratch-directory fixture changes received focused generated and
ownership checks plus format/vet verification.

Client verification passed `check:no-use-effect`, both workspace builds and
both type-checking test commands: 369 core and 41 React tests passed. Five core
HTTP-fixture tests are intentionally gated in the ordinary Social-server run;
the explicit generated-client and mixed-SDK runs supply their separate evidence.
Some original raw logs were in a temporary directory that is no longer present;
the counts here are recorded results, not a claim that those log artifacts are
still available or that a fresh complete run has just occurred.

Before enabling V4 writes, publish and verify a named V3/V4 reader release with
automatic writers still on V3. After V4 writes, rollback must retain V4 readers;
v0.8.8 cannot be the rollback target. Do not infer deployment readiness from the
current-tree two-executable rehearsal. go/v0.9.0 supplies the reader-first
release, not an activated V4 deployment. Target-storage performance and durability remain unverified;
existing NFS support is not being redesigned. Application adoption and baseline
collection are not counted as tasks for the Golem implementation.

## Frames generator dependency prerequisite

`TestFramesGQLGenConsumerMVS` builds the current CLI from source inside a
disposable consumer that pins Frames' gqlgen v0.17.94 and gqlparser v2.5.36.
It inspects a nested DTO/input schema, creates its reviewed initial migration,
generates and checks the application, and compiles every generated package.
The selected generator versions are checked before and after these operations.
Using a CLI built in Golem's own module would miss generator API skew.

This tests the two generator pins under consumer module selection, not Frames'
complete application dependency graph or its production build. Neither Golem's
dependency versions nor Frames' files are changed. The shared subprocess owner
below keeps the check serial and offline; all selected transitive module sources
must already be in the local cache. A cache miss is an incomplete check, not
evidence that generated bindings are incompatible.

The Go CI database tier prepares the unchanged pinned consumer dependencies
before running these offline tests. Its cache preparation uses disposable
module files; it does not normalize the checked-in fixtures or application
dependencies. First generation is followed by the documented `go mod tidy`
step before checking and compiling the generated application.

## Shared-session consumer prerequisite

`TestFreshGeneratedLoginCookieAndSessionRevocation` generates an application
and compiles its consumer against the exact unreplaced PondSocket v0.2.9 pin.
The local SQLite acceptance passes: an HTTP custom login mutation sets the
session cookie through real middleware, and the same cookie authenticates HTTP,
Golem subscriptions and PondSocket connections. Revocation prevents further
private-row delivery, closes both sockets within the configured bounds, and
rejects HTTP and socket reconnects. A separate case checks credential expiry.

Golem uses its documented rolling revalidation or explicit credential deadline.
PondSocket requires an application-owned session cancellation callback that
closes the connection; cancellation of the upgrade request is insufficient.
The disposable session validator demonstrates this integration boundary, not
Frames' production credential verification or session-store migration. CI caches
the test-owned consumer pin separately; Golem's module dependencies are unchanged.

## Local named-fact workload characterization

The opt-in characterization fixtures accept
`GOLEM_CHARACTERIZE_FRAMES_SQLITE_DIRECTORY` as an existing absolute scratch
parent. Only a fresh private database directory is created there; generated
source, binaries and Go caches keep their ordinary locations. Provisioning uses
the single-connection DELETE/FULL profile from the start, and cleanup removes
only that owned child. The option does not verify the physical mount or turn
local measurements into NAS evidence.

The 2026-11-17 attempt on the Blizzard NFS mount stopped at the fresh database's
initial open, before any workload ran. The mount reported NFS v3 with `nolocks`;
a disposable POSIX file-lock probe returned `ENOTSUP`, and macOS's native SQLite
also returned a disk I/O error on a separate fresh database. File synchronization
succeeded. The generated fixture removed its owned database directory and left
the ownership marker unchanged. This result applies to that Mac-mounted path;
it does not establish a failure of Golem's existing `OpenNetworkFilesystemWAL`
profile for a single-process NFS deployment. The 25,000-row and 30-minute NAS
gates remain unrun; their local results below must not be relabeled as NAS results.

`TestFreshGeneratedFrames25000VideoCharacterization` completed its SQLite
characterization with 25,000 independent Media/Video pairs, 15,000 storage
copies and an exact 10,000-row authorized partition. Normal public mutations
produced 65,000 V4 facts in transactions of at most 65 facts and 38,367 evidence
bytes. The fixture declares indexes on Video.media_id and StorageObject.video_id;
its asserted one-child topology uses a relation-fanout bound of one.

| Operation | Local duration |
|---|---:|
| First authorized 100-row page after seeding | 53 ms |
| Authorized count | 238 ms |
| Warm page p50 / p95 / p99 / max, 200 samples | 52 / 63 / 67 / 80 ms |

These development-tree measurements used Go 1.26.10 on darwin/arm64, one
execution thread, nice 10 scheduling priority, normal I/O and a 2 GiB soft Go
heap limit. SQLite used the network-filesystem
durability policy on **local temporary storage**, one connection, DELETE
journaling and FULL synchronization. There was no cache flush, explicit
ANALYZE, publisher or concurrent scan/playback/job workload. These measurements
do not establish cold-cache, NAS, PostgreSQL or production latency, or the
baseline-relative adoption target.

A controlled comparison on the same database and connection alternated the
exact generated primary statement with and without its redundant required-key
null rank. Bindings and all 100 returned rows matched across 20 measured pairs
after two warmup pairs. Primary-statement p95 was 311 ms with the null rank and
5.9 ms without it. Native SQLite plans showed a table scan and temporary sort
for the former, and primary-index order without that sort for the latter.
The current generated query uses the latter form. Its required key retains
conditional mask metadata; the existing planner proves the mask true for every
authorized candidate. The renderer consumes that proof without changing policy
admission or nullable-field ordering. Earlier measurements used background I/O
and nice 19; the paired comparison, rather than a ratio against those older
end-to-end timings, isolates this SQL change.

The exact populated primary provider plan was captured privately after seeding.
The complete public `ExplainFindMany` report still returned `PLAN_TOO_COMPLEX`;
the characterization records that status rather than reporting an empty plan
as success. Its timing and row-equality results do not certify complete public
plan-report coverage. Diagnostic limits remain unchanged.

`TestFreshGeneratedFramesMixedServerContention` passes its 30-minute local SDK
workload. Scanner writes, authorized playback reads, three long-running jobs,
the publisher and real semantic queue processing ran concurrently. Its 6,864
scanner transactions also changed an existing Media title through public
mutations. The 6,310 playback pages had p95/max of 71/534 ms; scanner transaction
p95/max were 20/340 ms. All three jobs completed on attempt one, each with 1,801
persisted lease-expiry advances under the original three-second lease. Each had
7,203 samples; minimum observed remaining lease exceeded 1.9 seconds.

The actual generated GraphQL application, manifest and TypeScript client
converged to the final title after replacement acknowledgements. It recorded
13,727 replacement subscriptions and acknowledgements, and 6,863 HTTP refetches,
below the elapsed-time cooldown bound of 14,408. It spent 246 seconds in
`RECOVERING` over the 1,800-second offer window; this does not establish
uninterrupted liveness. Thirty once-per-minute SDK heap samples ranged from
23 to 74 MiB with the same 256 MiB JavaScript heap cap. Subscription
instrumentation keeps counters rather than retaining every request and callback.

All 6,865 causations reached durable acknowledgement; first-recorded-to-ack
p95/max were 38/260 ms. The deterministic local three-dimensional embedding
provider processed 20,593 inputs, with work overlapping the offered load, and
drained 727 ms after the final successful mutation return. Pool-wide waiting
increased by 24,027 waits and 170.8 seconds across the offer window and drain,
including workers and diagnostics. Aggregate waits and sampled remaining lease
do not measure per-request connection holds, exact commit latency or
renewal-time slack.

The native Media stream requested `RESYNC / DEPENDENCY_CHANGED`. Its one
pre-terminal delivery-lag sample does not establish complete delivery or
disclosure coverage across the gap. Separate exact data, vector authorization,
complete causation inventory and durable acknowledgement assertions pass.

These measurements use the network-filesystem policy on owned **local temporary
storage**, not a NAS or the measured Frames baseline. They establish this bounded
local workload, not the NAS or baseline-relative adoption gate. Earlier attempts
expired a lease at about 25 minutes and exhausted a history-retaining Vitest
worker at about 19 minutes. The earlier lease run used a different scheduling
and I/O profile; the successful rerun alone does not isolate that failure's
cause. Failed-run timing samples are not included in the successful results.

## Released fact codec prerequisite

`TestReleasedV088AndCurrentFactCodecCompatibility` exercises the actual
`github.com/eleven-am/golem/go@v0.8.8` fact decoder against the current checkout's
V3 create-fact metadata and a genuinely sealed V4 causation. The released decoder must accept
and preserve V3 bytes and reject V4 specifically as an unsupported format. Its
own constructor/encoder also produces V3 metadata which the current reader must
accept and preserve byte for byte. The current reader must accept both versions
and validate the complete sealed V4 causation.

The helper imports the released `schematest.NewSubscribedGraph`, not a copied
schema or decoder. The current fixture uses `NewPolicyCaptureGraph`, whose
subscribed Post event shape is compatible with the released fixture. A changed
event shape fails this test instead of pretending the old registry understands
it. No prior-release codec source is copied into this directory.

The nested module pins v0.8.8. Its `go.sum` retains the published release's
dependency checksums plus the cached module's checksums. The parent copies the
module to a temporary directory, so Go's module normalization cannot modify
tracked files. Both modules and their dependencies must already be cached.
The subprocess disables downloads, checksum service access, workspace overrides,
private-module proxy bypasses, and toolchain switching. It runs with `-p=1` and
`GOMAXPROCS=1`, a three-minute process deadline, bounded metadata files, and
bounded diagnostic capture. Do not run it concurrently with another build.

From the `go` directory, with a local Go 1.26 toolchain:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOPRIVATE= GONOPROXY=none GONOSUMDB=none GOVCS='*:off' GOMAXPROCS=1 go test -p=1 -count=1 -timeout=5m ./compatibility -run '^TestReleasedV088AndCurrentFactCodecCompatibility$'
```

The offline acceptance passes locally against cached v0.8.8. It verifies the
create-fact codec boundary, not a complete release matrix. The second reader is
the current unreleased checkout, not a named compatible release.

Before V4 writer activation, ship and verify a named V3/V4 reader release while
automatic writers remain V3. After any V4 commit, the supported rollback target
must retain V4 readers; v0.8.8 is explicitly **not** that target. Historical
schema bundles cannot add a decoder to an old binary. This codec test does not
certify mixed publisher/consumer fleets, writer activation, rollback of a
running deployment, or retention windows. The separate released-backup gate
below covers one explicit archive path. Complete historical consumer and
deployment acceptance remain pending. No writer default changes.

## Writer activation and rollback rehearsal

`TestFreshGeneratedMixedWriterProviderRolloutRollback` generates one application
and compiles two executables from this checkout: a V3 writer with V3/V4 readers,
and the same source with only the automatic writer constant switched to V4.
It verifies pending V3 delivery after upgrade, persisted V4 delivery, and a fresh
V3-writer process draining pending V4 before writing V3 again. All four groups
must reach durable delivered state without changing the generated schema or
database identity. An unused scope declaration enables static capture; ordinary
read policies remain in effect throughout. Named facts are not enabled while
V3 writers coexist.

SQLite uses its supported process-local transport and waits for each application
process to exit before opening the next. That local acceptance passes. PostgreSQL
keeps both subscribers running on real Core NATS and checks deliveries in each
process. That local acceptance also passes with owned PostgreSQL 17.9 and the
exact pinned Core NATS image. Its broker reuses the existing pinned-image and
container-ownership lifecycle.
The database CI tier requires the broker, so missing prerequisites fail the gate.

These executables rehearse writer activation in the current development tree.
They are not two shipped releases or evidence of a deployed rollout. The
automatic writer default remains V3; a named dual-reader release is still needed
before deployment activation, and v0.8.8 remains an unsupported rollback target
after V4 writes. No NAS or production rollout is certified by this test.

## v0.8.8 offline SQLite backup prerequisite

`TestReleasedV088BackupRestore` runs the actual pinned v0.8.8 generated
`p10operations` application through its released runtime and provider. It creates
and updates rows, leaving three V3 facts and their pending deliveries, plus
pending and leased queue jobs. It closes the sole provider, runs the released
`CheckpointForBackup`, and creates a standalone read-only archive. Its original
Model2, Contract7, Physical3 documents, reviewed history and bounded row evidence
come from that executable; they are not regenerated by the current reader.

The current historical registry verifies Contract7 through an independent
frozen vocabulary, normalization and fingerprint owner. Active applications
still require Contract8 and regeneration. Contract formats1–6 and fact codecs
V1/V2 remain retired. Retaining a schema does not add a codec or grant current
hidden-fact scope authority across generations.

The restore test first requires refusal of the old queue layout, then explicitly
upgrades only an extracted copy. Both resume-at-least-once and abandon/resync
must preserve every source job and delivery identity. The test applies a reviewed
Physical3→4 identity migration, publishes with a fresh identity, verifies the
final catalog and exact ledger, and compares model data, facts, versions and
counters with the released archive. Original archive bytes remain unchanged.
A regression that deletes the archived queue is observed to fail before these
checks pass on the normal implementation.

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOPRIVATE= GONOPROXY=none GONOSUMDB=none GOVCS='*:off' GOMAXPROCS=1 go test -p=1 -count=1 -timeout=5m ./compatibility -run '^TestReleasedV088BackupRestore$'
```

This names the tested local archive window: v0.8.8 Contract7/Physical3 with V3
facts and its recognized queue layout. The current target is newly authored,
lowered metadata for offline publication; this test does not claim regenerated
application startup or browser recovery. Those have separate current-runtime
and generated-process gates. It does not ship a reader release, validate arbitrary
older archives, or establish NAS, asset-reconciliation or fleet rollout guarantees.

## SQLite queue startup prerequisite

`TestReleasedV088RefusesPrioritySQLiteQueueBeforeService` uses the same pinned,
offline v0.8.8 module and bounded subprocess owner. The current provider creates
real pending, promoted and leased jobs, then closes the database. The released
provider must open that file but refuse `EnsureSchema` specifically because the
priority-aware `golem_queue_claim` index differs from its released definition.
An unrelated provider/open error does not satisfy this test. A separate fresh
database created by the released provider must pass startup and claim a job,
showing that its ordinary runtime works. The current parent compares every
queue field and the complete SQLite schema before and after the refusal, then
checks that current startup still accepts the database.

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOPRIVATE= GONOPROXY=none GONOSUMDB=none GOVCS='*:off' GOMAXPROCS=1 go test -p=1 -count=1 -timeout=5m ./compatibility -run '^TestReleasedV088RefusesPrioritySQLiteQueueBeforeService$'
```

This is startup refusal for the exact v0.8.8 SQLite executable, not a fence on
already-running old producers or workers. Those must be drained before the
explicit storage upgrade. Arbitrary older releases and mixed-fleet operation
remain separate gates.

Local runtime restart acceptance additionally reopens the same disposable
SQLite file through public provider/runtime startup. The fresh app reads pending
V3 and V4 groups, resolves a non-anchor V4 notice, and publishes valid complete
groups. Missing or malformed V4 companions block the whole affected group while
an independent V3 group still delivers. Default post-restart writes remain V3.

The automatic V4 path is rehearsed by compiling the binary's writer selection
as V4 in a disposable local test run, then restoring V3. SQLite mutation-family,
hook/savepoint, graph-limit rollback and no-template compatibility acceptances
pass without manual sealing. A semantic drain insertion failure also rolls back
the application write, sealed evidence, delivery, semantic shadow and queue job
together, without invoking embeddings or after-commit hooks.

Disposable PostgreSQL 17.9 acceptance verifies the actual Serializable default,
committed before/after reparent graph, refusal of explicit ReadCommitted before
callbacks, and a two-connection conflict retry. The retry discards the failed
attempt's facts and delivery, then seals a graph from the fresh attempt. A
disjoint-write relation-phantom case separately requires a genuine PostgreSQL
serialization refusal and history consistent with the winner after replay. The
fixture checks its private Unix socket and actual data directory before any
schema writes, creates a separate database per test, and removes the owned
cluster afterward. These are local provider checks; they do not ship a reader
release, activate a deployment, or establish fleet, backup or production gates.


## PostgreSQL queue startup prerequisite

`TestReleasedV088RefusesPriorityPostgreSQLQueueBeforeService` performs the same
exact released-executable check on PostgreSQL. It requires both explicit
`GOLEM_POLICY_HISTORY_PG_ROOT` and `GOLEM_POLICY_HISTORY_PG_DSN`; it never selects
a default local server. The existing disposable-database owner validates the
private Unix socket and actual server data directory before creating separate
target and control databases. The child receives only those database identities
in a bounded mode-0600 fixture file through the existing offline harness.

The current provider enqueues normal, promoted and leased jobs in the target.
The actual v0.8.8 provider must admit and claim its own old-layout control, then
refuse the target specifically for the priority-aware claim-index columns.
The parent compares every queue row field and public namespace column, index
and constraint definition before and after refusal, then requires current
startup to accept the target. This is a local executable compatibility check;
it does not exclude already-running old workers or provide an old-fleet fence.
Run the existing bounded offline command with
`-run '^TestReleasedV088RefusesPriorityPostgreSQLQueueBeforeService$'` and the
explicit private-cluster environment supplied by its owner.

The existing `scripts/verify.sh` database and race tiers now supply this owner.
They use `PG_BINDIR`, or binaries found through `pg_config`, to create a bounded,
socket-only PostgreSQL 15+ cluster under a private temporary root. Required
PostgreSQL checks fail when configuration is missing. A complete root/DSN pair
provided by the caller keeps its existing lifecycle; the verifier stops and
removes only a cluster it created itself. These paths have a local lifecycle
smoke check; the complete CI tiers remain separate verification work.

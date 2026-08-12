# Go roadmap execution ledger

Status: **active implementation ledger**.

This file is the durable continuation boundary for the accepted Go roadmap. It
records the product decisions, execution order, current implementation boundary,
and evidence required before any item is called complete. A conversation summary
or an agent's memory is not authority. Resume work by reading this file, the
linked contract for the active item, and the current Git diff.

## Controlling decisions

- The Go implementation is the maintained Golem system. TypeScript parity and
  further TypeScript maintenance are out of scope.
- Requested product work is built as production infrastructure, not as an MVP or
  a disposable validation layer.
- Existing incomplete behavior is corrected before new capabilities are added.
- SQLite is a durable single-instance profile. It uses the embedded process-local
  event transport and never uses NATS for horizontal application fan-out.
- PostgreSQL is the multi-instance profile. Its official cross-process event
  transport is Core NATS, while PostgreSQL remains the durable source of model,
  migration, semantic-index, session, and outbox truth.
- Existing model policies own tenant/security partitioning. No second portable
  tenant invariant is being introduced.
- Client subscription replay is not part of Golem.
- No adjacent authentication/session framework, generic queue, business-logic
  feature, or compatibility fallback is authorized by this plan.
- Each mechanism must name the concrete failure it prevents and have a regression
  test that is observed failing against the defect before it is accepted green.

These decisions supersede earlier discussion that treated client replay or a
separate tenant invariant as possible roadmap items.

## Fixed execution order

| Order | Work | Contract | State |
|---:|---|---|---|
| 0 | Correct query-triggered semantic refresh scope | [`SEMANTIC-INDEXES.md`](./SEMANTIC-INDEXES.md) | Locally complete including the final live pgvector rerun |
| 1 | Complete the public policy testing kit | [`POLICY-TESTING-KIT.md`](./POLICY-TESTING-KIT.md) | Locally complete including the checked-social all-provider race gate |
| 2 | Complete SQLite WAL and safe backup | [`SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md`](./SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md) | Locally complete; documentation recovery journey remains separate |
| 3 | Complete PostgreSQL widening and reviewed backfills | [`SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md`](./SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md) | Locally complete through reviewed physical-v3, VersionedNote publication, and integrated verification |
| 4 | Implement human-readable migration plans | [`HUMAN-READABLE-MIGRATION-PLANS.md`](./HUMAN-READABLE-MIGRATION-PLANS.md) | Complete through the checked-social journey and exact eight-document CLI compatibility inventory |
| 5 | Implement first-class optimistic concurrency | [`OPTIMISTIC-CONCURRENCY.md`](./OPTIMISTIC-CONCURRENCY.md) | Locally complete through coordinated compatibility publication and external generated SQLite/PostgreSQL C/linguistic verification; hosted confirmation remains in order 8 |
| 6 | Implement safe query-plan visibility | [`SAFE-QUERY-PLAN-VISIBILITY.md`](./SAFE-QUERY-PLAN-VISIBILITY.md) | Locally complete through compatibility publication and external generated SQLite/PostgreSQL C/linguistic verification; hosted confirmation remains in order 8 |
| 7 | Implement the PostgreSQL-only NATS transport | [`ROADMAP.md`](./ROADMAP.md) | Locally complete through public compatibility publication and live PostgreSQL C/linguistic outage, duplicate, and no-replay evidence |
| 8 | Release and verify the Go module | Product tests, compatibility checks, and release artifacts | Local all-profile verification complete; the signed tag and hosted publication remain |

The order is deliberate. Human-readable plans depend on a truthful migration
model. NATS is last because it expands the operational topology and cost profile;
it must not distract from single-instance correctness.

## Current checkpoint

Baseline at the start of this execution was branch `codex/semantic-indexes` at
commit `0d9ca9c` (`Prove the kit answers what a real read does`). The tree was
clean.

Orders 0–7 are locally complete. The coordinated Order-7 compatibility
publication is frozen at a reviewed pass-2 manifest and pass-3 fixed point with
its separately compiled trusted digest. Order 8 has crossed its local integrated
release-candidate boundary: the mandatory all-profile run passed 4,836 tests in
142 packages with zero failures and zero skips, including live pgvector, Core
NATS, PostgreSQL C/linguistic, and the external generated-application journeys.
Its structured event stream is bound by SHA-256
`31c5b60954a449ad5d0882a3dca4fcf4e204191798ba1d0cd41ed78833f70957`.
The remaining Order-8 boundary is the signed `go/v*` tag and its hosted release.
The release workflow runs the supported product suite once, builds the release
assets, and publishes them to GitHub.

Order 7 preserves PostgreSQL/outbox durability while adding the maintained Core
NATS cross-process transport; SQLite remains process-local and refuses NATS
before dialing. Subjects route only by deployment-unique prefix, event-schema
digest, and model identity. Generation remains authenticated in the notice, Core
does not replay, duplicate delivery retains the stable event ID, and the broker
must prove the reviewed 2 MiB payload ceiling initially and after reconnect.
Runtime topology/readiness, adapter concurrency, host lifecycle, dependency
license packaging, live harness, and hosted workflow catalogs killed respectively
8, 23, 16, 10, 10, and 15 mutants with no final survivors or invalid results.
The clean external consumer passed outage/reconnect and duplicate/no-replay
scenarios on both PostgreSQL C and linguistic profiles, including the race
detector. Hosted toolchain, provider, and hardening jobs require all four exact
profile identities with immutable image-digest authority and fail-closed cleanup.
The public API corpus is additive and fixed at
`8d0ba09168a461cb54190a8c94e217fc85b3134f988ae552bafaa291e42737d8`;
the canonical checked manifest and separate trust root are fixed at
`02572dc6ca1aedc862acc26523d5c888c592f9efb117f0e6a6f63a9ed4a35561`.
This is local Order-7 completion, not the final Order-8 release-candidate result.

The release-upgrade boundary is executable rather than documentary only. Frozen
v1-to-v3 reviewed migration composition and the immutable P7
ModelIR-v1/ContractIR-v4/Physical-v1 bootstrap support the canonical
`go/v0.0.2`-to-`go/v0.1.0` guide across SQLite and PostgreSQL C/linguistic
profiles. The pre-1.0 release contract permits breaking changes only at a minor
boundary, still requires the exact migration guide, and continues to reject a
breaking patch. The
release tooling verifies signed prior/current tags, ancestry, exact corpus trees,
and guide endpoints/actions; build inventory, checksums, and provenance bind the
detached guide bytes, while publish rejects missing, tampered, or symlinked guide
artifacts. All six release-guide mutants are killed, and release normal, focused
race, vet, and diff gates pass. This proves the release machinery, not completion
of order 8. Order 8 also retains the mandatory hosted PostgreSQL C/linguistic
external generated-application reruns for optimistic concurrency and query-plan
visibility.

Order 1 now carries immutable generation identity on final generated model,
scalar-field, and relation handles through explicit stamped constructors;
bootstrap constructors remain unstamped and cannot cross the policy-test-kit
boundary. The generation, codegen, public-authority, and mutation-catalog
package tests pass, and nine compiling policy mutants have exact patch sites.
Their semantic KILLED run was intentionally serialized behind the physical v2
fixture regeneration; no metadata-only foreign-generation claim is accepted as
completion evidence. The four active runtime applications have now been
regenerated as complete eight-file publications and pass their exact byte-
identical regeneration gates. All nine policy-kit mutants subsequently ran
baseline-first and are killed. Two initial INVALID results were corrected at
the mutation boundary—their replacement bodies had created unused locals—then
rerun as compiling semantic defects and killed. The checked-social external
consumer now uses the appended reviewed physical-v2 migration and atomic
13-file generated publication rather than bypassing canonical history. Its
SQLite external generated-application gate passes under the race detector.
Workflow audit requires the exact PostgreSQL C and linguistic profiles without
skips; that hosted rerun remains explicit order-8 evidence.

The older product roadmap contained one stale topology sentence that described
the NATS adapter as provider-neutral. The completed boundary is PostgreSQL-only:
SQLite uses the embedded process-local transport, and an attempted SQLite/NATS
configuration fails explicitly before broker work.

Preparation for orders 5 and 6 also resolved specification drift. Their
isolated public value/codec boundaries may be built and mutation-tested in
parallel because they do not consume migration, runtime, provider, generated,
or compatibility state; no runtime/codegen/provider integration may advance
out of order. Optimistic `ExpectAbsent` is not an existence
oracle: an authorized existing row conflicts, while a missing or policy-invisible
row remains `NOT_FOUND`. Existing identity-based update facts are not expanded
into historical row snapshots. The generated GraphQL expectation reuses the
existing `BigInt` scalar. ModelIR owns the concurrency identity; ContractIR and
physical snapshots are independently validated projections. Generated batch
surfaces are absent for a versioned model, relation writes require an expectation
for every actual foreign-key owner they mutate, and row/action authorization
precedes the version comparison while hook-transformed changed-field
authorization follows the hook. Transaction-after hook executors require the
same expectation and their generic/batch helpers are not a bypass. A
concurrency-enabled before hook may not retarget the immutable prechecked row;
custom GraphQL and stale/model-erased clients cannot resurrect omitted batch
surfaces. Query-plan explanation reuses its Caller
policy snapshot, invokes only hooks the matching production operation actually
owns, refuses unbounded deferred statement counts, emits one buffered finish
observation after resource release, and requires renderer-owned alias/access
registries—including policy traversal aliases—rather than parsing provider
names.

Order 5 began with a deliberately incomplete but real compiler boundary. The
public expectation tokens are comparable and equality-only, expose no value or
pointer methods, and canonicalize every non-positive input to the invalid zero
state. Freeze can reject that state before database work; after the authorized
row is locked, runtime will compare the claim with `ExpectVersion(observed)` or
`ExpectExisting(observed)` and bind the already-observed version into the CAS.
The method-free public values are now defined facades over a primitive-only
`internal/concurrencyclaim` representation. That internal owner provides the
only validation and discrimination seam the mutation runtime may consume;
external applications cannot import it, and the public value and pointer method
sets remain empty with no JSON, text, or binary protocol. Focused normal and
race tests prove invalid, absent, positive-existing, and `MaxInt64` states
across the facade conversion. Three exact claim mutants—accepting zero,
collapsing absent into invalid, and exposing a public raw-value method—are all
killed baseline-first.
The compiler recognizes only one direct portable manifest-backed
`OptimisticConcurrency` declaration, validates the complete linked
provider-neutral eligibility matrix atomically, and stores the exact stable
FieldID solely in ModelIR. ContractIR now carries a separately copied, validated
projection and compilation rejects missing, orphaned, mismatched, duplicate, or
pre-authored ownership. The complete compiler suite passes. The downstream
physical, runtime, generated-Go, and GraphQL projections are closed, and the
retained persistence plus coordinated compatibility inventories are published
without changing a released format in place.

Current ContractIR is version 6 and canonically includes the validated
optimistic-concurrency FieldID projection. The retained v5 DTO, validator,
normalizer, JSON envelope/duplicate-key decoder, and original-byte fingerprint
owner are independent of mutable current rules and reject the v6 field; current
JSON framing remains private and explicitly non-authoritative, with whole-
CompilationIR agreement as the v6 semantic authority. Only `NewHistorical`
may adapt validated v5 to current in-memory v6 with absent concurrency. Its
provider documents dispatch exactly through frozen physical v1/v2/v3 decoders
and verify both application and system fingerprints. The literal released
p5social ModelIR-v1/ContractIR-v5/physical-v2 bundle loads there while active
startup rejects it. Six compiler mutants and twelve historical-authority mutants
are killed with zero survivors or invalid runs; focused normal/race/vet gates
pass. The coordinated manifest-v2 compatibility publication is complete.

ModelIR persistence is independently closed. Current ModelIR is version 2;
historical v1 has a distinct DTO, validator, normalizer, canonical decoder, and
original-byte fingerprint verifier, and only `NewHistorical` may consume it.
Active registries reject v1, while v1 rejects duplicate, unknown, noncanonical,
and v2 optimistic-concurrency members including explicit `null`. The actual
released social v1 payload decodes and reproduces its published fingerprint.
Upstream pre-v2 types/canonical sources and the retained adaptation are all
digest/line pinned. Normal/race compiler and policy-schema gates, the generation
pipeline, vet, and diff hygiene pass; all four isolated ModelIR mutants are
killed. The complete released persistence bundle is therefore readable without
granting historical documents active runtime authority.

The physical optimistic-concurrency boundary is now closed locally. Current
physical format 3 owns the optional table-level concurrency FieldID, while the
retained physical-v2 normalizer, validator, canonical projection, and migration
planner are independent of mutable current rules. The reviewed v1-to-v2
transition reproduces both immutable checked-social `0003_physical_v2` graphs,
phases, fingerprints, and chain hashes exactly. The reviewed v2-to-v3
transition emits the fixed `AddColumn -> InitializeConcurrencyColumn ->
ValidateConstraint -> AlterColumnNullability -> RecordSchemaVersion` graph and
does not mutate its input snapshots. PostgreSQL overwrites every existing value
with the provider-owned literal `1`, proves the exact `IS DISTINCT FROM 1`
postcondition, and invents no database default. SQLite performs one reviewed
rebuild and copies the literal `1`. Bootstrap independently proves exact
ModelIR/ContractIR/physical agreement and the complete logical eligibility
matrix, including the SQLite `int32`/`int64` storage ambiguity. Full focused
normal and race suites, vet, published-history/full-plan goldens, and all eight
physical-behavior mutants pass. The publication prerequisite is independently
closed as well: physical v3 has its own retained shape, normalizer, validator,
canonical decoder/projection, fingerprints, and planner. Reviewed v3 plan
snapshot facts clone only through that frozen normalizer; independent review
found and removed the last mutable-current normalization shortcut. Five
additional frozen-profile mutants are killed. Both authoritative manifests now
advertise physical v3 with exact `[1,2,3]` retained inventories, bound by the
separately compiled trusted digest.

The runtime compare-and-swap boundary is now closed locally. Creates receive
the provider-owned initial token `1`; update, delete, and both upsert branches
compare only an opaque frozen expectation against one authorized locked
pre-image and bind the observed database token into the atomic predicate.
Stale and exhausted tokens refuse before application Before hooks, while an
engine-owned upsert retry freezes authored/runtime-owned values once, emits one
root observation, preserves attempt ordinals, and never replays a scoped caller
transaction. Expect-absent distinguishes visible conflict from missing or
policy-invisible not-found without leaking the private token. Generic,
model-erased, batch, hook-executor, and existing-row nested paths that cannot
carry an exact expectation fail before hooks or SQL; nested creates remain
valid and initialize the token to `1`. Real two-writer SQLite and PostgreSQL C
and linguistic gates each produce one winner and one conflict with one fact.
The runtime acceptance suite is green normally and under the race detector,
vet and diff hygiene pass, and all 25 isolated compiling runtime mutants are
killed with zero survivors or invalid runs. Those gates also found and closed
a shared SQL precedence defect: every row-policy fragment in mutation
postcondition verification is now parenthesized beneath the persisted primary-
key predicate.

The generated Go optimistic-concurrency surface is now closed locally. For a
versioned model, Caller, System, CallerTx, and SystemTx clients require the
closed expectation token on Update, Delete, and Upsert and route only to the
expectation-aware runtime entry points. The runtime-owned version field has no
authored create/update capability; root batch APIs, batch aliases, and unsafe
existing-row nested operations are absent. Versioned-root relation values are
create-only because the current runtime intentionally refuses relation-bearing
CAS updates; safe nested creation and new-root ownership remain available.
Compiler declaration discovery uses one explicitly non-authoritative permissive
shell before the concurrency declaration is known, while the mandatory final
prospective compilation uses the exact generated ABI and blocks publication on
any mismatched call. Nonversioned model and registry outputs remain byte-stable.
Focused normal/race tests, vet, the publication pipeline, and diff hygiene pass;
all seven isolated generated-Go mutants are killed.

The GraphQL optimistic-concurrency boundary is also locally closed. Authored
inputs omit the runtime-owned token; versioned Update and Delete require a
positive exact `BigInt` version, while Upsert accepts only the closed one-of
existing-version or absent expectation. Versioned batch roots, unsafe nested
existing-row forms, orphan relation helper types, and recursive custom
versioned-UpdateMany arguments are absent or rejected. The released eight-field
runtime request input remains source-compatible, including external unkeyed
literals; a separate additive versioned input/freezer carries only opaque closed
claims. Claims are detached through map, lower, freeze, and public runtime
request layers; the runtime enforces exact operation/model/claim agreement
before projection, hooks, or SQL and preserves the ordinary nonversioned route.
A real SQLite dispatch gate
covers successful CAS, stale conflict, and absent upsert. Full GraphQL and
bridge normal/race tests, vet, formatting, and diff hygiene pass; all sixteen
isolated compiling GraphQL mutants are killed with zero survivors or invalid
runs. The public API comparator is additive, and the coordinated ABI and
compatibility publication is complete.

Order 6's first public-report draft was rejected during independent review
because it invented a public JSON protocol and accepted semantically
contradictory reports. The corrected construction boundary is an internal
validated `queryplanreport` producer converted into exact public opaque facade
types; downstream applications cannot import the producer. The contract now
includes the previously missing deferred-batch capacity and finite per-node
statement-bound accessors. That public/internal core is focused green normally
and under the race detector: it validates the complete node/access/identity
matrix, canonical ordinals and roots, finite aggregate and deferred bounds,
derived warnings, deep-copy immutability, and a private digest without exposing
a public decoder, constructor, or JSON protocol. Its ten baseline-first mutants
are all killed with no survivors or invalid runs; an additional pointer-only
authority-method mutant is also killed by the exact value-and-pointer API
inventory. The unreachable
`PLAN_BOUNDED_REFUSAL` warning was removed after an exact API
test observed it red: boundedness failure returns no report, so only the closed
error classification can own that fact. Renderer-owned alias maps, physical
access identity lookup, bounded provider adapters, Caller orchestration, and
observation are implemented as recorded below. Generated surfaces,
compatibility evidence, the SQLite external application evidence, and the
mandatory live PostgreSQL capture matrix are complete; the hosted external
PostgreSQL application rerun remains order-8 evidence.

The first private mapping seams are now locally green. The exact schema
registry retains provider-scoped table identities, explicit access-object
identities, and name-free ordered primary/unique-key facts so SQLite rowid and
autoindex evidence can be matched without parsing generated names. Seven
registry mutants are killed. Policy relation SQL now records every allocated
traversal alias at the allocation point with only an opaque matcher plus stable
model/relation identities; its omission mutant is killed. These facts are not
yet a report. The ordinary read renderer now adds immutable facts for root,
relation-count, correlated, batch-hydration, cursor, and policy-relation access
aliases while leaving projection-only aliases unregistered. One allocator is
shared by every policy fragment in the exact statement, closing the observed
flat-`golem_p1` ambiguity; all nine read-map mutants are killed. The bounded
provider adapters still have to consume and sanitize the same vocabulary. The
scoped renderer now owns immutable root,
join, occurrence-policy, and field-mask-policy alias facts, shares one allocator
across the exact statement, and kills all eight mapped mutants. Analytics
mapping is in final role review: provider-visible derived aggregate and
materialization aliases must remain structurally distinct from physical access
aliases rather than inheriting a false root-table access claim. That review is
now green: analytics facts carry a closed role for physical access, aggregate,
materialize, or structural aliases, all ten analytics mutants are killed, and
the full analytics package passes normally and under the race detector after
the active fixture regeneration.

Independent review also found that the report producer forced every first
statement to purpose `root`, making the accepted `analytics` and `scoped`
purposes unreachable for their own operations. The producer now binds the
primary purpose to the operation, permits only relation-batch or policy-
hydration statements afterward, and rejects contradictory or repeated primary
purposes. The fail-first regression was observed red and its exact mutant is
killed.

The bounded provider-capture boundary is now locally complete. SQLite uses only
`EXPLAIN QUERY PLAN`; PostgreSQL uses one fixed `EXPLAIN` form with `ANALYZE`,
costs, settings, buffers, WAL, and summary all disabled. Both consume exactly
one renderer-owned comment-free `SELECT`/`WITH` statement on an explicit
caller-owned connection, close all rows before returning, discard raw provider
strings/errors, and map only exact registry/alias facts into stable identities.
PostgreSQL performs a byte/depth/token/duplicate-key preflight before private
JSON decoding, including nested and escaped-equivalent duplicate keys. Derived
analytics aliases remain structural and cannot become physical access claims.
The focused packages pass normally, under the race detector, and in a live
PostgreSQL C profile; all twelve provider-capture mutants are killed. Their
catalog is now part of the shared closed mutation inventory.

Provider-neutral typed assembly is also locally complete. It combines the
sanitized primary provider tree with the already-authorized typed read,
analytics, or scoped plan. Correlated relations remain inside the captured
provider tree; only key-dependent relation batches and private policy
hydrations become deferred statements. Their exact batch capacity and finite
minimum/maximum statement counts come from the production batch renderer.
Explicit `take=0` is finite zero deferred work, while an absent bound refuses
the whole report as `PLAN_UNAVAILABLE`. A narrow immutable cursor walks both
Relations and Hydrations iteratively with hard 256-frame/depth-32 limits, so the
diagnostic path does not invoke recursive deep-copy accessors on adversarial
graphs. All eight typed-builder mutants are killed, including the depth-bound
mutant, and the catalog is aggregated into the shared inventory.

Caller query-plan orchestration now reuses the exact production preparation and
renderer paths. Original typed input is frozen before any before-read hook;
hook output is frozen again and reauthorized, and no after hook, decoder, or
data query is executed. SQLite and PostgreSQL capture adapters consume only the
renderer-owned maps. Rows and the explicit connection are closed before one
buffered finish observation is delivered. Focused normal and race suites pass,
and eight orchestration/policy-role mutants are killed with no survivors or
invalid runs. The shared closed mutation inventory is now exactly 137 labels.
The PostgreSQL parser/adapter gates pass. The focused live gate now runs both C
and linguistic database profiles, proves the planned data statement is not
executed, verifies typed primary-key identity and redaction, and closes every
connection. Both profiles pass normally and under race; mandatory mode fails
when either profile is missing, and workflow audit requires both exact tests.

Generated query-plan methods are now closed locally. Every generated Caller
model client exposes the exact six universal read-only Explain operations;
relation grouping and scoped Explain appear only when the ContractIR model owns
those capabilities. No System, CallerTx, SystemTx, mutation, event, semantic,
or GraphQL authority is emitted. Declaration discovery uses only an in-memory
conditional superset, while the post-Apply shell is ContractIR-exact and the
mandatory prospective compilation proves same-package helper calls. Independent
review added exact body-routing assertions for all eight possible methods before
the long run. Focused normal/race tests, vet, and diff hygiene pass; all sixteen
isolated compiling generated-query-plan mutants are killed with no survivors or
invalid runs. The final compatibility corpus includes the public `queryplan`
package, the active client artifacts are regenerated, and the SQLite external
generated application passes under race. Workflow audit requires the PostgreSQL
C and linguistic profiles without skips; their hosted external rerun remains
order-8 evidence.

Order 4 has now crossed its first serialized integration boundary. A migration
`Plan` carries detached, non-persisted before/after snapshot facts owned by the
existing diff implementation; those facts are deep-copied and do not change the
persisted plan or manifest format. `ValidatePlanShape` is the shared closed gate
for provider identity, snapshot binding, initial classification, deterministic
operation order, phase coverage, transaction modes, and the exact no-change
case. Prospective explanation accepts only a Diff-owned plan whose typed graph
can be reproduced exactly. Reviewed explanation verifies the sealed entry's
self-chain, reconstructed operation graph, approvals, risks, file checksums,
manual companions, and backfill postcondition before producing any report.
Backfill attachment builds and writes that report before canonical publication;
an injected output failure leaves the repository byte-identical. Eight isolated
adapter mutants are killed, and the migration, explanation, workflow-attach,
and CLI-attach gates pass normally and under their focused race runs.

The read-only `golem migration plan` command is now locally complete for both
prospective and reviewed modes. It reuses one all-provider preview with
authoring, validates the complete reviewed history before provider filtering,
and runs prospective generated compilation in an owned workspace outside the
module. Explicit empty selectors refuse; output and diagnostics redact paths,
URIs, and credentials; short writes and cleanup failures refuse; and success,
error, short-write, and panic gates preserve bytes, modes, symlink targets,
lock/WAL/SHM state, and active workspace files. The exact command suite passes
in 120.665 seconds, its five command mutants are all killed, and the focused
explain, vet, and whitespace gates pass. The checked-social external journey and
the exact eight-document CLI compatibility inventory are complete. Physical
fingerprints currently omit some
system/provider transitions. The implementation therefore recognizes no-change
from exact normalized typed-snapshot equality; changing the public meaning of a
physical fingerprint would require its own coordinated persisted-format and
compatibility transition, not an explainer shortcut.

Order 2's implementation is now locally quiescent. The public operation is
`provider/sqlite.CheckpointForBackup(ctx, database)`, where the exact sealed
SQLite handle owns its private data-source identity and must have completed a
successful close. The old unsafe-SQL recovery path and duplicate internal-only
acceptance owner were removed. Before implementation the public regression
failed to compile because the operation and `PROVIDER_MAINTENANCE` code were
absent. The restored implementation passed the exact gate three times, the race
gate, and the focused public provider/handle/SQLite suites. Its compatibility
surface is frozen with the coordinated generation-stamp ABI transition; the
separate documentation recovery journey remains.

Order 3 advances the physical schema and canonical formats from v1 to v2 for
the closed PostgreSQL `varchar(n)` identity. Immutable v1 migration history is
a deliberate released compatibility boundary, not an active-schema fallback.
The existing P7 upgrade test has been observed failing because workflow history
verification re-rendered a frozen PostgreSQL v1 entry with the v2 renderer. The
required correction is byte-exact v1 replay plus one closed reviewed v1-to-v2
edge: SQLite records the format transition without schema DDL; PostgreSQL may
replace legacy bounded `text` only when the exact registered length CHECK proves
the same bound, then drops that CHECK, alters to `varchar(n)`, and records the
new format. Generic `text` to `varchar` is not safe widening, old history is
never rewritten, and provider-upgrade evidence may not bypass public apply by
executing SQL or inserting ledger rows directly.

The exact SQLite historical journey now passes through those boundaries:
public apply installs the frozen v1 history from blank, the compiler authors
the one reviewed v1-to-v2 edge with exact approvals, public apply advances the
current history, and the ledger, application rows, and durable event state are
preserved. The previous direct SQL/ledger installer is gone. PostgreSQL C and
linguistic public-apply profiles remained mandatory before this boundary could
turn locally green. Backfill attach also remained publication-blocked until the
single order-4 report renderer was integrated; no temporary formatter was
accepted. Both requirements have since passed as recorded below.

The first exact PostgreSQL C and linguistic run reached the real transition
and failed identically because PostgreSQL refuses to alter a source column type
while an unchanged stored generated column depends on it. This is not a SQL
ordering workaround. The v1-to-v2 typed DAG must detach and recreate every
affected generated column, together with its own dependent indexes and
constraints, around the source representation change. That derived recreation
is a reviewed rewrite rather than owner-row data loss and must work on the
minimum supported PostgreSQL 15 surface before both profiles can turn green.
PostgreSQL 15 forbids generated expressions from referencing another generated
column, so that shape is rejected during physical validation. The supported
graph covers one or more stored generated columns over ordinary widened source
fields, including each generated column's local dependents and foreign keys in
other tables that reference its key.

After closing the typed detach/recreate graph and the exact registered
PostgreSQL deparse equivalence for `varchar -> text` inside reviewed
expressions, the public P7-to-current PostgreSQL upgrade passed both mandatory
profiles with no skips: C in 68.42 seconds and linguistic in 67.03 seconds
(135.46 seconds total). The run proves the current two-entry journey, native
`varchar` catalog identity, removal of the v1 length CHECK, durable application
rows, event/outbox preservation, and current runtime startup. It is diagnostic
evidence for the current path, not order-3 completion: generated add/remove/
rename regressions and the standalone tagged-v1 validator/normalizer/diff and
provider-history boundaries remain mandatory.

The historical core no longer relabels v1 data and sends it through mutable
current validation/diff logic. `DiffHistorical` now dispatches to an isolated
planner adapted from the exact `go/v0.0.1` source digest
`4d7271550104a57a6f9766bbe9456a5544cdf429eda4540df366550ace572679`,
and v1 semantic validation has its own retained tagged implementation with
source digest
`bf4ef0b2ee7eeaa82ade0ab35a4a548bba580d1780aaee5b4f3a0c79bd75c35b`.
The v1-only decoder, separate reviewed decoder, pre-allocation shape guard,
historical normalization, and shuffled/noncanonical refusal are focused green.
Active provider `RenderInitial`, `ApplyInitial`, and `Introspect` entry points now
refuse v1 before database work. Historical access is restricted to sealed-entry
`RenderMigration` and private reviewed-snapshot introspection tokens that bind
provider, both physical fingerprints, operations, and phases. The tagged v1
renderer preserves released behavior by treating extension metadata as
non-rendered; it cannot invoke current semantic-vector DDL. Source-provenance,
branch-complete v1 plan goldens, closed-validator mutation goldens, and nonzero
current-only-field refusal are focused green. PostgreSQL, SQLite, physical,
migration, and workflow packages pass together; both PostgreSQL profiles and
SQLite have passed the public P7-to-current journey, and reviewed backfill
authoring/apply has passed both PostgreSQL profiles. The order-4 adapter now
renders and writes the shared typed report before backfill publication, and an
injected output refusal proves publication does not begin. Order 3 is therefore
locally complete at its intended production boundary, including the later
checked-social reviewed-v3 and VersionedNote publications. Compatibility and
release-upgrade evidence are frozen only after their reviewed fixed point.

The checked-social physical-v2 serialization boundary is now closed. Migration
`0003_physical_v2` was appended without changing either prior manifest prefix
or any old immutable artifact, and the reviewed plan validates both providers.
Generation published the exact 13-file manifest inventory with digest
`1d6b2165c790a65d42c176050df9cf159e11dfe7aa4387e9d6bc12da3027e541`;
the subsequent check reports no changed or stale files. Both hosts build, all
social packages pass, and the policy-kit external consumer passes under the
race detector across SQLite and both mandatory PostgreSQL profiles.

The four active runtime fixtures (`p5extensions`, `p5social`,
`p5socialactive`, and `p6metrics`) are each an atomic eight-file publication
from the exact custom-lowerer `pipeline.Build` requests in
`runtime/p5_generated_fixture_identity_test.go`; all four active publications
are byte-identical to their current generation requests. Frozen P7 and P7-event
corpora, p7subscription, and stale-generation test fixtures remain
byte-preservation boundaries. Compatibility inventories were frozen only after
every active publication completed.

Order 4 began in an isolated report/encoding boundary. Its exact tests
were observed failing to compile before the types/builders/renderers existed,
then passed normally and under the race detector. The one immutable closed
report now owns copied accessors, canonical provider/warning order, risk counts,
dependencies, capabilities, approvals, reviewed companions/artifacts, bounded
JSON/text renderings, privacy rejection, and exhaustive operation-effect
classification from explicit typed facts. Raw migration `ObjectID` and
`LogicalPath` never enter the report. The subsequent Plan/Diff and
backfill-attach integration is recorded in the current order-4 checkpoint
above; the standalone command and all-provider preview reuse that same report
rather than adding another explanation path.

The isolated report boundary also has executable mutation resistance rather
than assertion-only coverage. Its dedicated baseline-first catalog killed all
22 compiling mutants with zero survivors or invalid runs, including reordered
operations, weakened loss/rewrite/unknown effects, omitted approvals,
dependencies, backfill facts and postconditions, invented downtime/duration
claims, SQL/value/DSN/path/physical-name disclosure, rendering before
validation, text-control line injection, provider filtering before validation,
prospective writes or temp leaks, and unversioned/open JSON. The original 21
mutants passed together; the subsequently added control-injection mutant was
killed independently after its regression was first observed red. Final race
and vet runs passed and left no mutation sandboxes. The adapter integration adds
eight separately isolated and killed graph/validation mutants. The completed
command-level acceptance suite separately owns CLI orchestration and filesystem
preservation evidence.

## Phase completion rules

For every row above:

1. Re-read its source-of-truth contract before changing production code.
2. Inventory every public producer and consumer of the changed contract.
3. Name ownership of the decision, execution, durable truth, retries, and audit.
4. Add an adversarial regression and observe it fail against the defect or
   deliberate mutant before accepting the implementation.
5. Remove replaced paths rather than retaining unrequested compatibility layers.
6. Run focused tests, race tests where concurrency is involved, both PostgreSQL
   collation profiles where provider behavior is involved, and the relevant
   generated external application journey.
7. Update public API, generated ABI, persisted-format, observation, compatibility,
   documentation, and workflow inventories when their surface changes.
8. Record exact evidence and update this ledger before proceeding to the next
   row.

Passing a unit test is not completion when the public CLI/application journey is
missing. A test that constructs a state users cannot create through production
entry points is not product evidence.

## Corrections incorporated into the current execution

The following review findings are now controlling requirements. Their local
implementation status is recorded above; they remain part of final integrated
evidence rather than optional cleanup:

- Policy kit: the generated application journey is mandatory; the
  checked-social integration uses the reviewed v2 publication rather than a
  fixture shortcut.
- SQLite WAL: the supported public backup/checkpoint operation replaces the
  former internal-only/unsafe recovery seam.
- PostgreSQL evolution: the pending-draft and `migration backfill attach`
  authoring path and accepted `varchar` widening edges are required. The frozen
  v1 decoder owns a
  standalone v1 semantic validator rather than relabeling v1 as current and
  calling mutable current validation. Historical decoding is v1-only; active
  provider render/apply/introspection remain current-only; explicit reviewed
  history seams alone may replay v1 and the one v1-to-v2 edge. The generated
  widening DAG must cover added, removed, renamed, and retained generated
  dependents; local keys/checks/indexes and remote foreign keys; and source plus
  generated add/drop ordering. PostgreSQL generated-to-generated references,
  forged expression type/nullability facts, and globally colliding
  operation-addressable IDs fail closed.
- Semantic indexes: query-triggered refresh is selected-index only; explicit
  maintenance remains all-index.

## Resume protocol

At the beginning of every resumed work session:

1. read this file completely;
2. read the active contract completely;
3. inspect `git status`, the current commit, and the complete active diff;
4. verify that this checkpoint still describes the tree; and
5. continue from the first incomplete evidence item, not from remembered chat.

If code and this ledger disagree, stop feature expansion, reconcile the factual
state, and correct this file before continuing.

# Developing Golem

This is the maintainer reference. Application usage and upgrade requirements live
in the [user guide](GUIDE.md); the [root README](../README.md) is the starting point.
Keep one current account of a design here instead of introducing parallel guides
or dated task ledgers. Historical release notes and proposals remain in Git.

## Contents

- [Repository and ownership](#repository)
- [Verification](#verification)
- [Release procedure](#releasing)
- [System-owned fields](#system-fields)
- [Mutation row locks](#row-locks)
- [Continuation envelopes](#continuations)
- [Policy fact authority](#fact-authority)
- [Static policy templates](#scope-contracts)
- [Compatibility procedures](#compatibility)
- [Mixed-load example](#loadmix)

<a id="repository"></a>

## Repository and ownership

| Directory | Responsibility |
| --- | --- |
| `go/` | Go module, CLI, generated application runtime, providers and acceptance tests |
| `client/core/`, `client/react/` | Go protocol client and React hooks; independently published npm workspaces |
| `typescript/` | Prisma/NestJS backend and its npm workspace packages |
| `docs/GUIDE.md` | Authoritative user documentation, including executable examples |
| `scripts/verify.sh`, `.github/workflows/` | Verification tiers and release automation |

The compiler owns schema and contract identities. Generated bindings must match
the application schema and reviewed migration history; change declarations and
regenerate instead of hand-editing `zz_golem_*`. The runtime owns admission,
authorization and transaction behavior. Providers own their SQL and storage
contracts. Browser code consumes the generated manifest rather than guessing
model dependencies, result shapes or commit evidence.

All `go/internal/` packages are private. The exported API is enforced by
`go/internal/publicapi/surface.txt` and `TestPublicSurfaceMatchesItsRecord`.
When an intentional public change occurs, review the reported additions/removals
and update that record in the same change. A generated `surface.actual.txt` is
ignored; it is a comparison artifact, not an alternative specification.

Keep application adoption work outside Golem's task list. Consumer fixtures in
this repository test Golem's compatibility boundaries; they do not certify an
application migration or production deployment.

<a id="verification"></a>

## Verification

Run commands from the repository root unless a command explicitly says otherwise.
Install only the workspaces needed for the change. Avoid concurrent builds on a
developer machine; a focused Go invocation can use `GOMAXPROCS=2 go test -p=1`.

```sh
make help
scripts/verify.sh list
scripts/verify.sh format
```

The normal Go verification tiers are `format`, `vet`, `gate`, `database-free`,
`cli-and-harness`, `database-bound`, the three `oracle-*` tiers and `race`.
`make gate` checks generated drift/byte identity; `make verify` runs the complete
suite and requires the configured PostgreSQL services. `make release-check` adds
the pinned vulnerability scan. These broad gates can compile generated consumers
and start owned fixtures; do not describe a focused run as the full suite.

Go documentation tests in `go/cmd/golem/quickstart_documentation_test.go` extract
individual `<!-- chapter:name -->` sections from `docs/GUIDE.md`. Keep the matching
end markers and runnable file names when editing the quickstart, application,
queue, full-text, semantic and render chapters. Tests migrate, generate, compile
and run those examples; queue also has a PostgreSQL acceptance. Chapters are
isolated because several examples intentionally use the same `notes/` file names.

The formatting step also checks that the guide's declared Go release matches its
installation command and the root README. Change these together for a release.
The nine package READMEs are intentionally short npm entry points; keep detailed
behavior in the guide and use package READMEs for installation and navigation.

`scripts/verify.sh compatibility` reruns the pinned-source consumers with the
same private PostgreSQL fixture owner as `database-bound`. Run
`scripts/verify.sh cache-compatibility` first. This explicit network step prepares
the complete root module graph and the pinned consumers in disposable modules,
then verifies that the root graph is readable offline. CI calls this same owner;
the compatibility test commands remain offline. Set `PG_BINDIR`
when `pg_config` points to client-only tools. CI keeps its PostgreSQL 17 service
profiles and explicitly uses PostgreSQL 18 for the private cluster. The verifier
prints the selected server version before creating that cluster.

For the browser client, install with `npm --prefix client ci --strict-peer-deps`,
then use the workspace build, type-check and test scripts in `client/package.json`.
The shared development tools require Node 22.22.2, 24.15 or later in those major
lines, or Node 26 and later. Public SDKs retain Node >=20.19 with GraphQL 16.
TypeScript 6.0.3 is the newest compatible compiler: gql.tada's language-service
plugin currently accepts TypeScript 5 and 6, so TypeScript 7 is excluded.
For the Prisma backend, use `make ts-install`, `make ts-build` and `make ts-test`.
These are independent workspaces; a client-only change does not require rebuilding
the Prisma backend.

Before the first local `scripts/verify.sh database-bound` or
`scripts/verify.sh oracle-failure-and-event` run, explicitly cache the pinned
Core NATS image with `scripts/verify.sh cache-nats`. The cache action uses the
image declared in the verifier; it does not start a container. It requires an
available local Unix Docker socket. Set `DOCKER_HOST=unix:///absolute/socket/path`
when that socket is not `/var/run/docker.sock`; remote endpoints are refused.

Ordinary verification does not fetch or inspect images in the shell. Its Go
fixtures inspect the exact cached image through a 15-second Docker command
bound and fail required gates when the image or daemon is unavailable. The
explicit cache action can wait on the daemon or network; cancel it if the local
daemon stalls. CI runs it as a separate prerequisite with a five-minute step
deadline, before either NATS-dependent verification tier. It is not an additional
verification matrix tier. PostgreSQL prerequisites and private cluster ownership
remain unchanged.

The database tier first runs the ordinary V3 suite, then a scoped V4 compiler
overlay for the named history/policy-fact runtime tests and four generated
consumers covering authorization equivalence and bound scope/fact templates.
These template consumers require historical route evidence and run in the
mandatory V4 pass. The existing client CI job separately runs
`scripts/verify.sh writer4-client` after installing and checking the locked
client packages. This starts fresh generated applications for continuation,
custom invalidation, and browser recovery after restore on their declared
providers. These required passes reject missing or skipped tests, including
skipped subtests; the checked-in writer default remains V3.

For focused local verification, `scripts/verify.sh writer4-database` runs the
staged database acceptances without repeating the ordinary database tier.
Both explicit writer actions use the existing private PostgreSQL owner. The
client action also requires Node and the installed client dependencies.
Existing Go flags are preserved; an existing compiler overlay is refused rather
than silently replaced. Temporary overlay files are removed on exit.

The staged runtime pass uses temporary stubs for the external test package
when it contains no selected tests, `TestMain`, or `init` functions. This avoids
compiling unrelated generated-application tests for the internal selection;
the ordinary V3 suite still runs them. Finding any of those declarations keeps
the entire external package, including its helpers. The runtime package import
path, required-test inventory, and generated-application checks stay intact.


<a id="releasing"></a>

## Release procedure

Release namespaces are independent: `go/vX.Y.Z` is the Go module,
`client/vX.Y.Z` is the browser client, and plain `vX.Y.Z` belongs to the
Prisma/NestJS packages. A tag in one namespace does not release the others.

1. Update the appropriate package/lockfile versions and guide requirements.
   For Go, use matching CLI/library builds and state any generated-contract,
   migration, outbox, queue-storage or rollback boundary in the upgrade chapter.
2. Run the checks required by that surface. Go tags require the release gate and
   vulnerability scan; client releases require both workspace builds, type checks
   and tests. Preserve signing and repository protections.
3. Review the diff and release-check results. Never move an already-published
   tag to include a later documentation change; use a follow-up commit.
4. Check the corresponding GitHub workflow, Go module resolution and/or npm
   package versions and distribution tags. Workflow definitions in
   `.github/workflows/` are the executable release authority.

Current documented releases are Go 0.9.2 and browser client 0.3.1. The Go release
requires Go 1.26.11 or later. Automatic writers remain V3; staged V4 verification
does not activate V4 in a released application. After a V4 commit, rollback must
retain V4 readers. NAS measurements and production rollout remain unverified.

<a id="system-fields"></a>

## System-owned fields

The former system-fields proposal is implemented. A caller input naming a
`system` field is refused even if a hook would replace its value. Hooks and
system clients can write it through system field handles or `golem.SetCreate`;
`system;immutable` still cannot be rewritten on update. A `readonly` field has
neither create nor set input. Declare `golem.GraphQLHookOwned(...)` when a
required create field is supplied by a hook. Generated declarations execute in
their declared stance; a broader system escape is explicit and observed. Keep
this boundary in the schema and runtime, not in transport-specific checks. See
[field modes](GUIDE.md#application-the-tag-vocabulary) for the current API;
the original decision record remains in Git.

<!-- chapter:row-locks -->
<a id="row-locks"></a>

## Mutation row locks

Internal decision record for maintainers. One current account: correct it in
place when the design changes.

<a id="row-locks-decision"></a>

### Decision

On PostgreSQL every transaction keeps one ordered lock ledger (`Ledger`). Every
row a write reads in order to change it, every foreign-key parent it relies on,
every upsert selector guard and every application lock key is locked through
the ledger, in one total order, before the write's statements run. SQLite runs one writer at a time and takes no
row locks.

<a id="row-locks-why"></a>

### Why

Before go/v0.6.5 each statement locked rows in whatever order its own query
returned them (`SELECT ... FOR UPDATE` in predicate order, cascades in
dependency order, foreign-key checks inside the write). Valid concurrent writes
deadlocked, PostgreSQL detected it only after `deadlock_timeout` (1s), and one
write was aborted with 40P01. Named cases, each with a regression test in
`runtime/postgresql_lock_order_test.go`, `postgresql_parent_lock_test.go`,
`postgresql_lock_upgrade_test.go` and `upsert_guard_order_test.go`:

- two updates linking self-related rows to each other in opposite directions;
- a cascade delete of a parent racing an update that relinked one of its
  children;
- a cascade delete racing a transaction that updated a child and then created
  another;
- two transactions that both reference a parent (KEY SHARE from the foreign
  key) and then both update it (upgrade deadlock);
- an `UpdateMany` racing a link between the same rows;
- a nested upsert racing a root upsert on the same selector.

The cost was not only the abort: every blocked writer waited the full deadlock
timeout behind the victims, so throughput collapsed under contention.

<a id="row-locks-protocol"></a>

### Protocol

1. **Order.** A key is `(model ID, encoded primary identity)`; upsert guards
   (prefix `\x00\x00`) sort first, application lock keys (`\x00\x01`) next,
   and rows (`\x01`) last. `strongestSorted` merges duplicate keys, keeping the
   stronger mode. `plan` decides, from the keys the transaction already holds,
   which keys still need a lock and whether each may wait.
2. **One lock statement shape.** Every row lock is
   `SELECT <columns>, <match>, <ordinal> FROM t WHERE <keys> ORDER BY <ordinal>
   FOR UPDATE` (or `FOR KEY SHARE`, `NOWAIT` where step 3 requires it), one
   statement per run of keys sharing a model, a mode, the wait choice and (for
   a locking read) the reader, chunked to the parameter limit. `WHERE` names only
   primary keys, so every row it finds is locked and returned, and recorded in
   the ledger. A plain `Lock` reads no caller columns and has no predicate.
3. **Never wait out of order.** A key that sorts before the highest key the
   transaction already holds, or an upgrade of a key it holds (KEY SHARE to
   UPDATE), is taken with `NOWAIT`. Lock failure (55P03) is reported as
   `CONFLICT`. Waiting happens only in ledger order, so the waits-for graph has
   no cycle.
4. **Predicate selections: enumerate, then lock and read in one statement**
   (`SelectMatching`). The caller's query enumerates the candidate rows without
   locks. Each candidate the transaction does not already hold is then locked
   by a statement that also returns the caller's columns and evaluates the
   caller's predicate as a column (`<match>`): PostgreSQL re-evaluates both on
   the row version it locked, so the result is the row as locked and whether it
   still matches. Each enumerating statement has its own `Reader` (columns,
   predicate, arguments); the nested set difference enumerates current members
   and each desired target separately, and a row both enumerate is locked by
   the first reader's statement and read again, without waiting, by the
   second's. Rows already held are returned as enumerated: no other
   transaction can have changed them.

   The merged lock-read is used only for a reader proven, from its rendered
   SQL, to read nothing but the target row, and whose own arguments leave room
   in the parameter bound for at least one identity (`Reader.mergeable`). The
   proof is syntactic and closed: after skipping quoted identifiers and
   literals, the columns and predicate may not contain `SELECT`, `FROM`, `JOIN`,
   `TABLE`, `LATERAL` or `VALUES`, and every function call must be a listed
   operator form (`COALESCE`, `CASE`, ...), a `pg_catalog.` function or a type
   cast. Anything else, including a policy or field grant that follows a
   relation (an `EXISTS (SELECT ...)`), takes the fresh-read path: the reader's
   keys are locked with a plain `Lock` in the same ordered round. Once the
   round has taken any lock, every such reader that enumerated rows runs its
   enumeration again as a new statement and keeps the rows the transaction
   holds, including a reader whose rows were all held before the round: any
   lock in the round may have waited, and a grant it depends on may have been
   revoked meanwhile. Why: a statement's subqueries read other tables at the
   snapshot taken when the statement started. If a lock waits, a grant revoked
   by the blocking transaction would be unseen and the write authorized on
   stale data; a new statement after the lock sees it. A merged reader whose
   rows were all held keeps its enumeration: it reads only those rows, and no
   other transaction can change a row this one holds. The parameter fallback keeps
   a selection whose own arguments fill the bound working, as it did before.
5. **Complete selections: enumerate, lock, enumerate again** (`Select`). The
   cascade closure, the root upsert probe and foreign-key parents found through
   a unique key must see every row, including one that started to match after
   enumeration, which a locking read of known keys cannot find. They lock with
   `Lock` and run the enumeration again under the locks.
6. **Foreign-key parents** a write will reference are locked `FOR KEY SHARE`
   (`LockReferences`) before the write, in the same order, so the database's own
   foreign-key check never takes a lock out of order. A parent referenced by
   its primary key is keyed from the written value without a query (mutation
   input is refused when it exceeds the column's declared precision or scale,
   and identity encoding is canonical, so the key equals the row's). A parent
   referenced through another unique key is found by a query, a complete
   selection. A parent the transaction already holds is not locked again.
7. **Upsert guards** are transaction advisory locks ordered before every row: a
   guard is acquired blocking only while the transaction holds no row, and
   otherwise with `pg_try_advisory_xact_lock` (failure is `CONTENTION` →
   `CONFLICT`).
8. **Application lock keys** (`LockKeys` on a transaction, `Session.LockApp`)
   are transaction advisory locks on the two-int4 key formed from the first 64
   bits of SHA-256 of the order string, a key space disjoint from the guards'
   single-bigint keys. Two keys whose digests collide share one advisory lock:
   that can add a wait, or form a cycle that PostgreSQL detects as a deadlock
   and that is reported as `CONFLICT`. The keys of one call are sorted,
   recorded with `Mode=Update`, and taken by `plan`'s rule: blocking with
   `pg_advisory_xact_lock` while nothing held sorts after the key, otherwise
   with `pg_try_advisory_xact_lock` (failure is `CONTENTION` → `CONFLICT`). A
   blocking wait can still deadlock with a wait the ledger does not track, such
   as a unique-index check on a row another transaction inserted; PostgreSQL
   aborts one transaction and the lock call reports `CONFLICT`. SQLite
   validates the keys and takes no lock.
9. **Savepoints.** `Mark` records the ledger length when a savepoint opens;
   `RollbackTo` removes, or restores the previous mode of, every key acquired
   after it, because PostgreSQL releases those locks on rollback to the
   savepoint.

<a id="row-locks-what-a-selection-returns"></a>

### What a selection returns

`SelectMatching` returns the rows this transaction holds `FOR UPDATE` that still
match, each as it was when locked, in enumeration order.

- A row deleted before it was locked is absent: the lock statement does not
  find it. A row changed so it no longer matches is locked but its `<match>`
  column is false, so it is left out. A delete's goal state is already reached;
  an update has nothing to change.
- A row that starts matching after enumeration is not among the keys and is
  not held. This covers `DeleteMany`/`UpdateMany` (root and nested), nested
  relation expansions, and single-target pre-images, on both paths: the merged
  lock-read names only enumerated keys, and the fresh-read path keeps only rows
  the transaction holds. A row that stopped matching is judged on the version
  locked; on the fresh-read path any other table it depends on is read after
  the lock. Including rows that start matching would need
  a second, possibly out-of-order, lock round and could chase concurrent
  inserts without bound.
- The predicate is a column, not part of `WHERE`. In `WHERE`, PostgreSQL would
  lock a row whose new version fails the re-check and not return it, so the
  ledger would not know it holds that lock and could later wait out of order
  while holding it.

`Select` (complete) returns the second enumeration. A row it returns without
holding fails with `ConflictError` (`CONFLICT`): for the cascade closure the
database removes every dependent, so an unlocked one would be deleted without
its event (new dependents can appear only below the first level, under
children not yet locked); for the upsert probe an unseen row would collide with
the create branch. The one exception is a `FOR KEY SHARE` parent key that the
first enumeration requested and that was missing when locked: it is admitted,
and a missing parent is reported by the foreign key itself. A parent key the
second enumeration finds that the first did not (another row took the unique
value) is a `CONFLICT`, since the foreign-key check would lock it out of order.

So a single target that vanished or stopped matching is `NOT_FOUND`, a batch
changes exactly the rows it holds, and the count, hooks, events and cascade
facts are built from those rows. Tests: `runtime/row_lock_ledger_test.go`,
`runtime/row_lock_batch_rule_test.go` (injected through `WithEnumeratedFault`).

<a id="row-locks-under-isolationserializable"></a>

### Under `Isolation(Serializable)`

Everything above assumes READ COMMITTED, the default, where every statement
takes a new snapshot. A transaction opened with
`golem.Isolation(golem.Serializable)` begins with `sql.LevelSerializable` and
keeps one snapshot for its whole life. The ordering rules (steps 1 to 3 and 6
to 9) are unchanged: locks are still taken in ledger order, so the waits-for
graph stays acyclic, and serializable checking adds aborts, not waits. Two
rules of step 4 and of the section above change:

- **Authorization is judged at the transaction snapshot.** The fresh-read
  statement still runs after the lock round, but it reads the snapshot the
  transaction began with, not a fresh one, so a grant revoked by a transaction
  that committed after this one started is not seen. PostgreSQL tracks
  read-write dependencies only between SERIALIZABLE transactions. When the
  revoking transaction is also Serializable, the write is ordered before the
  revocation in a serial order, or, if no such order exists, one transaction
  fails with 40001, reported as `CONFLICT`. When the revoking transaction runs
  at READ COMMITTED, as every default golem transaction and every standalone
  mutation does, it takes no predicate locks: the revocation is not detected,
  and this transaction commits a write authorized on its snapshot. So
  Serializable protects invariants that every participating writer checks
  under Serializable, such as count-check-insert. It does not make
  authorization fresher; under READ COMMITTED a grant is read after every
  lock wait, and that is the stronger rule for revocation.
- **A concurrently changed row is a `CONFLICT`, not a re-check.** When a lock
  statement reaches a row that another transaction updated or deleted and
  committed after this snapshot, PostgreSQL raises 40001 instead of
  re-evaluating the newer version (EvalPlanQual). The `<match>` column is
  never computed on a newer version, and a row deleted after the snapshot is
  not silently absent: the statement fails and golem reports `CONFLICT`.

40001 and 40P01 are classified as `CONFLICT` wherever they surface: at a write
statement, at a read statement, while flushing the outbox, and at COMMIT.
`golem.RetryConflicts(n)` re-runs the whole callback on `CONFLICT`, at most `n`
attempts in all, each in a fresh transaction with a fresh ledger and mutation
state; after-commit hooks run once, after the attempt that commits. Without it
the callback runs exactly once. SQLite already runs one writer at a time
(`BEGIN IMMEDIATE`), which meets Serializable, so the option sends it no level.
REPEATABLE READ is not offered: it has the per-transaction snapshot without the
serializable check, so it could write on a stale grant without an abort even
when every other writer is Serializable, and it does not stop count-check-insert.

Serializable aborts can be false positives. PostgreSQL records what a
serializable transaction read as predicate locks on btree index pages, and
promotes them to relation locks when there are many, so an insert into a page
another serializable transaction read conflicts with it even when the rows are
unrelated. Every golem write also inserts into shared system tables (the event
outbox and delivery rows, and queue jobs when it enqueues), whose indexes
concentrate inserts on the same rightmost pages, and any serializable read of
those tables, or of a hot application index, widens the shared surface. Two
serializable transactions that touch unrelated application rows can therefore
form a read-write cycle through shared pages, and one fails with 40001,
reported as `CONFLICT`. This is inherent to SSI, not a golem fault; callers
that use Serializable should pass `RetryConflicts`.
Tests: `runtime/postgresql_serializable_test.go`,
`runtime/transaction_option_test.go`.

<a id="row-locks-cost"></a>

### Cost

Beyond its enumeration, a merged predicate selection costs one statement; a
fresh-read predicate selection (a policy, grant or predicate that reads another
table, or arguments that fill the parameter bound) and a complete selection
cost two. A single-row update with a single-table policy is 6 statements inside
its transaction; with a relation policy it is 7, as in go/v0.6.6. The counts
are pinned by the PostgreSQL observation oracles (`internal/p8oracle/mutation`,
`internal/p8oracle/load`, single-table policies), by
`runtime/mutation_cascade_cost_test.go`, and, for the fresh-read path, by
`runtime/row_lock_fresh_read_test.go`.

<a id="row-locks-rejected-keep-predicate-order-locks-and-map-40p01-to-conflict"></a>

### Rejected: keep predicate-order locks and map 40P01 to `CONFLICT`

Measured with the contention harness (32 workers, mixed writes on PostgreSQL
17, 20s runs):

| hot rows | v0.6.4 (40P01 → CONFLICT) | ledger |
|---|---|---|
| 10 | 45 ops/s, success p99 9.1s | 1046 ops/s, success p99 299ms |
| 50 | 191 ops/s, success p99 3.4s | 1409 ops/s, success p99 67ms |

Lowering `deadlock_timeout` to 100ms only shortens the stall: 341 ops/s at 10
hot rows, still about 3x worse than the ledger, and it is a server setting
golem cannot own. Uncontended, the ledger as first shipped (go/v0.6.5) cost
about +0.4ms per write (two extra statements: lock and re-read) and about 20%
single-writer throughput (528 → 423 updates/s). Merging the lock and the
re-read (go/v0.6.7) leaves one extra statement per predicate selection whose
reads stay on the target row: a single-row update under a single-table policy
is 6 statements inside its transaction (v0.6.4: 5, v0.6.5: 7), and in alternating runs single-writer update latency fell by about 0.8ms
(p50) with about 12% more updates/s.

<a id="row-locks-known-trade-off"></a>

### Known trade-off

An application transaction that writes rows in an order other than the ledger
order does not wait: it fails fast with `CONFLICT` when another transaction
holds an earlier key. Writers that touch shared rows in a consistent order, or
retry on `CONFLICT`, are unaffected. The GUIDE says this in user terms.

<a id="row-locks-known-gap"></a>

### Known gap

A row whose primary key another transaction changes, and commits, while a lock
statement waits for it is locked but not returned: PostgreSQL re-checks
`WHERE <keys>` on the new version, which no longer matches. The ledger does not
record that lock, so a later wait in the same transaction can be out of order.
Single updates may change a primary key; batch updates may not. Present since
go/v0.6.5, unchanged by the merged lock-read.

The merged lock-read evaluates the target row's own columns on the version it
locked, but a statement cannot see other tables after its start. That is why a
reader that reads any other table, or that cannot be proven not to, uses the
fresh-read path; the price is the second statement go/v0.6.6 paid.
<!-- /chapter:row-locks -->

<!-- chapter:continuations -->
<a id="continuations"></a>

## Continuation envelopes

This package owns the private authenticated envelope used by custom page
execution. It does not authorize data, serialize application arguments, store
rank snapshots, or establish that an application ordering is immutable.
It does not install a pageable operation or expose a public token-signing API.

Registered `PageQuery` execution is owned by `runtime.DispatchCallerPage`.
Application startup requires the exact generated page-resolver inventory and
an explicit `Pages` configuration for schemas declaring pages. This table is
separate from custom mutation/transaction registrations and cannot carry
`Within` grants. The scope callback receives the admitted actor snapshot; the
dispatcher pins that actor in context before scope derivation or resolution.

`custom.PrepareCanonical` bounds declared types and incoming values before
ordinary preparation validates or clones them. The dispatcher binds effective
take plus ordinary arguments, excluding only `after`, and passes the same owned
prepared values to the generated resolver decoder. The resolver receives a
typed `PageRequest` and returns a `PageBatch`, which contains candidate
consumption and private checkpoint bytes rather than a signed token.
Ordinary custom execution also selects this bounded preparation automatically
for registered page declarations; native embeddings cannot reach an unbounded
copy step merely by bypassing the HTTP compiler.

The immutable batch constructor bounds its own copying too. Oversized item
lists/checkpoints retain at most one element/byte beyond the shared protocol
ceiling as an invalid witness. Every such witness exceeds all legal declaration
limits and is rejected before a response can be served. These ceilings have one
Go owner in `internal/pagecontract`, shared by declaration validation, batch
construction and continuation admission; request-specific take remains tighter.

The GraphQL adapter normalizes within the admitted item bound, applies ordinary
caller hydration and DTO row gates, drops unreadable direct-model page items,
and encodes once with a nullable continuation placeholder. Only successful
encoding without field failures can finalize a token. The selection owner then
fills selected `next` aliases in the encoded page. A denied candidate still
advances the private traversal; hydration and encoding failures issue no token.
Hydration matches candidates through a separately validated primary-key
correlation, so private identity cells do not become public filters or selected
fields. Typed before-read hooks retain that correlation through their public
option roundtrip; hook-authored filters and lookup windows still pass normal
authorization and bound checks.

`Bind` hashes length-framed canonical operation inputs: contract, registered
operation identity, ranking revision, data incarnation, arguments and the
admitted actor's durable audience/profile/revision. The operation owner supplies
those canonical bytes. Process-local caller counters and policy-cache keys are
not durable audience identities. A changed data incarnation invalidates tokens
issued before restoration even when the schema and encryption keys survive.

`Begin` fixes the chain's absolute deadline. `Resume` authenticates that scope
and the complete envelope before trusting its private position or candidate
count. `Advance` retains the original deadline and requires positive bounded
candidate consumption and different, nonempty state. Retryable replay is
intentional; each request must still authorize its items. Candidate consumption
includes denied/deleted candidates and is independent of emitted item count.
An empty page can therefore have a continuation. Terminal pages do not mint a
token.

The page dispatcher must enforce its own tighter operation limits and ordering
contract. An opaque position plus an increasing consumption counter cannot
prove an arbitrary keyset advanced. A ranked snapshot needs an immutable order,
a deterministic tie breaker, a validated ordinal and storage whose lifetime is
at least the chain deadline. Keyset traversal needs an immutable ordering and
defined population boundary. Neither may silently regenerate a changed order
when a token is resumed.

AES-256-GCM uses the standard library's random-nonce implementation. Version,
key identifier and scope digest are authenticated additional data. Private
state is encrypted, so hidden candidate identifiers are not disclosed by base64
decoding. The fixed ceilings are 2 KiB state, 8 KiB encoded token, 10,000 consumed
candidates per advancement and eight keys. The deployment explicitly supplies
a whole-second absolute lifetime of at most 24 hours; there is no generated
startup key or default lifetime.

All serving processes must share the configured keyring. Constructing a new
codec with a new active key and retained retiring keys rotates issuance; removing
a retiring key revokes its tokens. Retain decrypt keys through their maximum
token lifetime and rotate before 2^32 encryptions across every instance sharing
the key. A restarted process with the same configuration can resume tokens.
Reducing the configured maximum lifetime also rejects existing envelopes whose
original lifetime exceeds the new maximum. Configuration material is copied
into immutable cipher state.
<!-- /chapter:continuations -->

<!-- chapter:fact-authority -->
<a id="fact-authority"></a>

## Policy fact authority

The internals below include the staged V4 path. Go 0.9.0 ships V3/V4 readers with automatic V3 writing; named-fact runtime admission and V4 writer activation remain deferred.

Named facts inspect physical relationships without granting access to the
related rows. Their observation contract permits conservative source
invalidation, so an author must explicitly authorize the domain in which that
activity may be disclosed.

One immutable `ir.FactScope` belongs to the whole fact-owning policy. Every
resolved fact retains this same scope identity. Effective row and selected-field
conditions intersect the scope **after** ordered rule resolution, for every
caller action. In particular, negative facts, denials and unconditional
fallback grants cannot escape the domain. Raw fact-reference resolution retains
ordinary Boolean meaning; it does not inject a scope underneath negation.

The first release requires configured subscriptions for the owner and every
scope/body participant, including unused definitions. Policy activation checks
this inventory before query or write grants become available, rather than
waiting for a stream to open. Target read grants remain independent: a subscribed
physical dependency may still be unreadable to that caller.

Fact declaration freezing retains authored logical branches until binding;
`true OR relationship` cannot erase an inline participant before observation
admission checks it. Compiled templates independently retain their complete
static inventory through parameter normalization. Template matching compares
normalized copies and returns the authored condition, so additional dead
participants cannot disappear during equivalence checking either. Both use
the ordinary predicate freezer and evaluator; ordinary caller predicate
normalization and the later policy truth normalization keep their existing
semantics.

The shared `policy/capture` validator checks every authored definition against
the registered scope/body projections before normalization, including unused
definitions and dead branches. References retain their complete physical
prefix when coverage is checked. Admission has one aggregate work budget;
delivery reuses the same static checks under its matcher budget and cancellation.
Registered model coverage is distinct from a row's presence as a graph root:
additional captured scalar scopes still use their exact historical row proof.

Scopes support scalar predicates, Boolean composition, explicit constants and
positive correlated `Is`/`Some` witnesses in the predicate IR. Actor grant keys
come from the authoritative actor snapshot;
they are not a cache of current fact truth. An explicit `true` scope authorizes
conservative activity observation throughout the root domain. A missing scope
is invalid. Related authority gates retain a distinct physical boundary during
normalization, query planning and mutation final verification. Negative
relationship predicates remain data/rule truth inside a positive authority
cover; a historical authority claim must not depend on proving absence.

Ordinary runtime admission rejects every named-fact declaration with a
route-evidence error, including scalar, unused and operation-only definitions.
A local body can still be observed through a relationship, so its syntax cannot
establish a safe reader-only exemption. Internal historical admission/planning
accepts facts under the captured writer contract; related scopes also require
registered bindings. Runtime selects this mode only when the actual binary
writer and registered capture inventory both require history. The default
reader-release binary writes V3 and does not activate named-fact policies.

Observation has two separate questions:

- **Authority:** the exact policy scope plus the source route established by
  captured correlation keys.
- **Relevance:** conservative Boolean bounds on whether a dependency could
  affect the source. Unknown relevance may widen work inside the domain; it
  must never widen authority.

Missing data-witness fields therefore retain a scoped route for conservative
recovery. Missing fields in a historical scope remain an error. A negative
relationship quantifier may invert data relevance; it never inverts authority.

The policy scope is applied once when entering a fact region. A nested fact
reference beneath a self-relation still belongs to the original policy root;
it must not rebind that scope to the related row. A related fact-owning row is
also observed independently. Historical planning walks scope relationships
separately from data relationships, with a constant scope-only body and no
unused data-template prerequisite. Scope, data and owner-row roles retain
distinct path identities. Shared scope walks deduplicate only within the same
exact bound scope and ordinary source occurrence, so concurrent loss of data
truth cannot hide a domain change.

A stream keeps its initial observation graph, not an initial Caller execution.
Before a dependency notice can invalidate a fact-owning stream, the runtime
resolves the principal again and fingerprints the current expanded row,
caller/hook filters and complete selected-field masks. Ordinary readable
members and physical members share this freshness boundary. A changed contract
ends the stream with a generic resync, without attributing the signal to hidden
activity. Reopening builds a new graph. Configured periodic revalidation uses
the same comparison, including policies whose fact definitions are unused.
Newly broadened graphs still need reopening, periodic revalidation, expiration
or explicit policy invalidation when no notice reaches the initial graph.

Full selections lift child row policies, selected masks and count policies
through their parent relation, retaining each child's own scope and its local
and ancestor read context. Scope ownership is tracked independently of surviving
fact references. An unused fact-owning child therefore still receives fresh
principal checks and a scope-only historical witness; its unused data bodies do
not become observation dependencies. Lifted questions share the fact expansion
budget, in addition to the observation path limit.

Fresh planning invokes source read-before hooks per dependency notice and per
configured periodic check. This work runs outside the hub lock and follows
both subscriber and hub cancellation. Hooks must tolerate repeated execution;
plan limits and queues bound work, but representative workload measurements
are still required before making latency or capacity claims.

Old and new persisted routes are both needed for deletes and reparenting.
Missing routing or authorization evidence cannot be replaced by a universal
audience. Runtime evidence failures and history compatibility still require
integration acceptance before release. This is not a release claim for the
complete event contract. Historical paths retain every fact-owning scope on
their captured ordinary chain, including positive conjunctive to-one side
witnesses. Each requirement names its exact captured primary identity, complete
scope and occurrence. A non-root captured owner needs present images in both
phases; an explicit root retains its certified absence semantics. Incomplete
evidence remains unknown.

Current scope, ordinary source and local guard probes share one SQL statement.
Each candidate keeps its own conjunction even when probes share execution.
Historical scopes must hold together in the same phase. Generic cleanup may
waive a currently false scope only when this causation certified that scope's
loss; a different scope revoked later cannot borrow the waiver.

Filter and selected-field questions inherit the source row's authority and
ordinary guards. They do not create separate source audiences. Data-fact truth
stays conservatively projected, so a false data fact does not erase authority
or prevent recovery. Identical complete questions reuse one walk. Local data
facts sharing one scope retain their Boolean algebra; conditional or negative
side scope boundaries and distinct-scope alternatives remain explicit coverage
errors. Physical fact/scope traversal never imports target read scopes.

Authority beneath an uncaptured ordinary prefix can move forward only through
matching positive to-one main-path edges. Source and intermediate fact-free
guards remain current; the carried child scope and local guards are certified
at the exact captured cut. An unrecorded source-owned scope or an unrelated
side edge remains unknown. This lets profile- and playlist-owned rows recover
through a recorded Video without treating their ownership as a historical fact.

For example, a copy's before image can name Video V after its last granted
copy is deleted. If another transaction reparents V from Media M1 to M2 before
delivery, joining that old copy with current V invents authority for M2 and
loses the M1 route. Complete same-causation delivery cannot repair this
cross-causation ambiguity.

Generated scope and fact templates now define the actor-independent path and
projection inventory. Bound predicates retain the template and typed argument
provenance; there is no separate author-supplied capture declaration. The
registry fingerprints the complete inventory, including independent authority
branches. Capture is independent of connected subscribers.

The mutation scope owns first-before and final-after row images, including
savepoint rollback. The graph assembler requires a provider-certified snapshot
and explicit complete relationship extents. Outbox ordinals describe declared
graph order, not physical write chronology. Witness reads never become
synthetic updates, and membership truth is never cached.

The staged V4 format stores one private graph anchor and commits it to the
complete ordered causation. Transport carries references rather than the
graph. A consumer recovers and verifies the bounded complete causation from
one database statement, checks exact notice membership and producer schema,
and retains only immutable physical evidence for that notice. A subscriber's
canceled load cannot poison another subscriber's evidence lookup.

Historical conditions use the ordinary policy evaluator with exact producing
descriptors. A missing extent is unknown; partial to-many rows never establish
completeness. A missing to-one extent may be derived only from same-phase
correlation and a declared unique target key, or exact primary-key absence.
Present captured rows and known-absent roots remain different operations.

The V4 consumer discovers owners only in the committed graph. It enumerates
complete recorded sibling routes and certifies the suffix between a stable
cut identity and its exact owner. The current ordinary source prefix stops at
that cut's primary key; it cannot rejoin today's cut-to-owner relationship or
cross another fact-owning policy. A matching suffix is not a historical row
visibility claim or current authorization.

The cut's own ordinary guard is checked now as well as historically. Each
recorded path retains the exact intermediate primary identities; its mutable
ordinary guards become identity-bound current questions. Historical truth and
current guards must belong to that same path. Distinct paths remain alternatives
and cannot borrow each other's successful predicates. Current guards reject
fact-owning boundaries, including sibling branches whose fact nodes disappeared
during Boolean projection.

Current source-prefix, intermediate-guard and explicit owner-scope probes share
one SQL statement per owner. Named-unique and composite cut routes retain the
stable primary identity even when a unique value moves to another row.
Current rule bodies, field masks and fact truth cannot replace the scope fence.
A certified true-to-false scope loss permits generic, unattributed cleanup;
an unchanged historical scope cannot attribute activity after current scope is
revoked. Direct-owner deletion may remove the current source row as well.
An uncaptured source prefix additionally relies on its ordinary source-event
cleanup when that prefix changes; the local SQLite regression verifies this
backstop without disclosing the source to another actor.

Physical dependency delivery always uses the historical handler. Missing
history is refused before any current-target snapshot or live route probe;
there is no V3 physical live-join fallback. Ordinary V3 members keep their
existing judges and dispatch. Historical observation plans refuse `BindChange`
even for a local/empty plan. The default writer remains V3. Controlled
writer-release rehearsals use the native
writer, publisher and subscription factory to verify related-scope gain/loss,
false/unused data facts, actor isolation and delayed data reparenting on SQLite.
The freshly generated six-model Frames kernel fixture also exercises read and
private-write equivalence on SQLite and PostgreSQL, then native hidden-copy
update, deletion and recreation through Media, Video, Episode, WatchProgress
and PlaylistVideo streams. Related filters retain source authority, foreign
rows stay silent, certified losses are unattributed, and delayed reparenting
cannot borrow the new Media's authority. Full application adoption, GraphQL
selection coverage beyond this fixture, remaining route-loss and compatibility
gates are separate release requirements. The same generated application's HTTP
handler checks actor parity and nested progress/playlist ownership. A physical
fact can grant a source row while ordinary `some: {}` still finds no readable
target. Selecting a target model with no read grant retains its ordinary
`FORBIDDEN` / `NOT_GRANTED` refusal.

History finalization has one pre-flush owner after application callbacks and
fact ordering. It uses the existing binding, registry and historical resolver;
it neither opens another transaction nor holds the state mutex across capture
reads. Empty/uncaptured transactions retain V3. A sealed graph is reusable only
while both history and fact epochs match, and a failed required capture cannot
fall back to V3.

The binary's automatic writer format also controls its transaction snapshot
requirement. A V4 writer with registered capture templates selects PostgreSQL
Serializable before beginning any of the six mutation transaction families;
an explicit weaker option is refused before application callbacks. A successful
driver begin is the only issuer of that binding's certificate. Reader-mode V3
and no-template applications retain their previous isolation behavior, including
nil/default driver options. SQLite retains its existing write-transaction
ownership. Driver tests prove requested options and refusal. Disposable
PostgreSQL 17.9 tests additionally check the server isolation level, committed
reparent history, and a real two-connection conflict retry whose replacement
graph uses the fresh attempt's before state. A separate disjoint-write case
reads a complete relation extent while another transaction inserts a member;
the resulting write skew must cause a serialization refusal, and fresh replay
must capture the winner's committed state. Giving a RepeatableRead transaction
a false Serializable certificate makes that regression fail. Broader provider
and deployment gates remain pending.

The same binary writer contract controls producer admission and legacy fact
routing. A V4 writer with capture inventory rejects current `CDCAdapters` at
startup: their changed-row images cannot certify the source transaction's
complete historical relationships. Its physical fact consumers also refuse
retained V3 notices with generic revalidation before decoding images or probing
current routes. They never attribute old activity through today's intermediate
relationships. Ordinary event members, reader-mode V3 and no-template
applications retain their existing compatibility. Activating this contract
requires handling incompatible backlog; it does not reconstruct old evidence.

This explicit authority contract supersedes inference from the upper bound of
the source's data facts and the special repair for shared to-one paths.
<!-- /chapter:fact-authority -->

<!-- chapter:scope-contracts -->
<a id="scope-contracts"></a>

## Static policy templates

The internals below include the staged V4 path. Go 0.9.0 ships V3/V4 readers with automatic V3 writing; named-fact runtime admission and V4 writer activation remain deferred.

`Subscriptions(ScopeTemplate(name, func(args Args) Predicate[Model] { ... }))`
declares one parameterized positive authority expression. The function is
interpreted statically. It contains one return expression using ordinary
predicate constructors, constants, generated field/relation handles and direct
exported argument fields. Application calls, captured values and control flow
are rejected. Argument types are author-owned named structs.

The compiler owns template identity, typed parameter inventory, normalization
and fingerprinting. The fingerprint includes derived capture keys, fields and
correlations. Capture projections are derived from the expression instead of
being authored or persisted separately. Independent existential branches retain
their path occurrences; composite correlation component order remains exact.
Every participating model must enable subscriptions. Unsupported through
relations fail explicitly until complete junction correlation evidence exists.

`FactTemplate(name, scopeName, func(args Args) Predicate[Model] { ... })`
declares a data predicate associated with a scope on the same model. Both
families share parsing, typed parameters, scalar binding and capture projection
owners. Data expressions also admit negation, negative/universal relations,
ordered scalar comparisons and presence tests. Authority expressions retain
their positive relation grammar. JSON/list predicate algebra and implicit
through relations are explicitly unsupported by static templates.

The scope footprint describes authority paths, not arbitrary data-fact paths.
For example, an unrestricted owner scope has no relation footprint even when
its readiness facts inspect hidden children. Complete producer inventory must
also come from static fact templates whose generated bindings are the actual
fact bodies, associated with the policy's one scope binding. Fact fingerprints
include that authority contract. Body projection retains paths in dead branches
and distinguishes repeated relation occurrences. Capture must derive the union
of authority and body footprints, including complete empty extents for negative
questions. Neither fake dead branches in an unrestricted scope nor separately
authored approximate watch predicates establish coverage. Capture activation
must verify registered provenance for every participating physical definition;
inline definitions alone do not prove coverage.

ContractIR v8 carries this metadata and the generated ABI is `p8-go-abi-v8`.
The runtime registry recomputes the contract and exposes immutable copies.
Older generated contracts follow the existing regenerate/drain refusal. A
mixed-version reader/writer and historical-backlog compatibility window remains
a separate acceptance requirement; this version bump does not prove it.

Code generation emits a `<Model>Scopes` namespace. Each method accepts the
author-owned argument type and returns an opaque `golem.BoundFactScope[Model]`
for `rules.BindFactScope(...)`. This value cannot enter caller predicates or
selections. The generated expression uses stable field/relation handles; the
declaration function never executes to construct policy truth.

The matching `<Model>Facts` methods return opaque `golem.BoundFact[Model]`
values. `rules.BindFact(...)` creates a reference owned by that rules builder.
The registry-backed binder verifies the name, template, body, arguments and
exact authority association. The bound value cannot enter caller expressions;
the resulting policy reference follows the existing fact ownership rules.

The public freezer owns exact typed arguments, including empty sets and full
timestamp precision. Frozen policies with template bindings use version 4;
ordinary policies and inline fact scopes retain versions 2 and 3. The runtime
binder checks the template identity, digest, complete parameter inventory and
types, then reconstructs its expression through ordinary scalar binding and
compares normalized executable truth. Attached metadata alone is insufficient.
Scope and fact templates use shared immutable `TemplateArgument` values.
Bound provenance survives policy normalization, fact resolution and authority
gates even when a body becomes constant. Scope provenance also survives when
data definitions are unused.

The registry derives an immutable union of private capture fields across all
registered templates. Scalar SQL projects these fields independently of caller
field grants and public result selections. Batch and cascade images already
contain complete rows. Schemas without templates retain their prior SQL.

Successful write applications feed a transaction-local image journal before
mutation-capable observers or hooks run. Savepoint-owned contributions retain
the first before-image and final after-image, including deletion/recreation;
rolling back a nested write restores the earlier surviving after-image. Known
absence is distinct from an incomplete image. Cascaded changes and actual
model-row version stamps participate; global clock bookkeeping does not.
The journal has a one-MiB scalar payload budget (`policy_history_bytes`) and a
bounded application count. It is cleared on commit or discard.

The private snapshot reader applies the journal to bounded physical equality
queries. Before-state reads remove current matching changed rows and restore
every matching first-before image, including rows whose old correlation no
longer matches current SQL. Final-state reads use final after-images. Missing
fields, changed write epochs and overflow are errors, never empty extents.
Queries use the existing scalar codecs and project only static capture fields;
the overflow row is a sentinel. Scalar payload and graph work are bounded by
`policy_history_bytes` and `policy_history_nodes`. A failed reader cannot be
reused as complete evidence.

Snapshot admission requires an owned captured write transaction. SQLite uses
its held write transaction; PostgreSQL requires an explicitly Serializable
transaction. Default PostgreSQL isolation is refused. These checks do not yet
constitute the full provider/concurrency acceptance matrix.

The registry also retains each template owner's immutable path inventory.
The private graph assembler follows changed images backwards in each phase
to discover affected roots, then walks the declared paths forwards to capture
complete relation extents. Root absence and complete empty extents are explicit;
an unrecorded root or relation remains unknown. Independent predicate branches
share physical rows without filtering their witnesses or multiplying joins.
The ordinary policy evaluator consumes only the condition's finite dependency
tree from this graph. Its work budget includes recursive immutable-record
construction, so deep paths with wide leaves cannot evade the capture bounds.

The shared immutable graph has a canonical version-1 codec. It binds the exact
producer generation, the static capture fingerprint, and an ordered commitment
to the original V3 fact contents and private before-images. V4 facts reference
that graph; only the first fact stores it in a versioned private-evidence
container in the existing outbox. The ordinary V3 row blob remains unchanged
inside that container. The graph and all container overhead count toward the
existing one-MiB combined outbox limit. Sealing rechecks both physical-write and
fact-buffer revisions before staging, and flush refuses later changes.

The publisher validates the whole V4 causation and its unique first-fact graph
before publication. Transport V1 preserves V4 metadata and before-images while
omitting the graph. Model-routed subscribers may never receive the first event,
so consumers must load and validate the graph from the producing outbox; a
missing retained graph cannot become current-state evidence. No new physical
table or independently retained graph is introduced.

Automatic writers still emit V3. The staged V4 acceptance exercises historical consumer lookup, source routing and authority evaluation, including related scopes. This is not public runtime activation: named-fact admission remains deferred in the released writer. No V3 bytes are reinterpreted as historical route evidence.
<!-- /chapter:scope-contracts -->

<!-- chapter:compatibility -->
<a id="compatibility"></a>

## Compatibility procedures

<a id="compatibility-frames-generator-dependency-prerequisite"></a>

### Frames generator dependency prerequisite

`TestGQLGenConsumerMVS` builds the current CLI from source inside a
disposable consumer that pins Frames' gqlgen v0.17.94 and gqlparser v2.5.36.
It inspects a nested DTO/input schema, creates its reviewed initial migration,
generates and checks the application, and compiles every generated package.
The selected generator versions must equal Golem's current module selection
before and after these operations; the lower consumer pins remain unchanged.
Using a CLI built in Golem's own module would miss generator API skew.

This tests the two generator pins under consumer module selection, not Frames'
complete application dependency graph or its production build. Neither Golem's
dependency versions nor Frames' files are changed. The shared subprocess owner
below keeps the check serial and offline; all selected transitive module sources
must already be in the local cache. A cache miss is an incomplete check, not
evidence that generated bindings are incompatible.

The Go CI database tier prepares the unchanged pinned consumer dependencies
before running these offline tests. `scripts/verify.sh cache-compatibility` owns
the shared local/CI preparation, including root graph metadata that a generated
consumer does not necessarily retain after tidy. Its preparation uses disposable
module files; it does not normalize the checked-in fixtures or application
dependencies. First generation is followed by the documented `go mod tidy`
step before checking and compiling the generated application.

<a id="compatibility-shared-session-consumer-prerequisite"></a>

### Shared-session consumer prerequisite

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

<a id="compatibility-released-fact-codec-prerequisite"></a>

### Released fact codec prerequisite

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
create-fact codec boundary, not a complete release matrix. The second reader is the checkout under test. The released go/v0.9.0 dual-reader boundary is described in the [upgrade guide](GUIDE.md#upgrading).

Before later V4 writer activation, deploy and verify the named go/v0.9.0 V3/V4 reader release while automatic writers remain V3. After any V4 commit, the supported rollback target
must retain V4 readers; v0.8.8 is explicitly **not** that target. Historical
schema bundles cannot add a decoder to an old binary. This codec test does not
certify mixed publisher/consumer fleets, writer activation, rollback of a
running deployment, or retention windows. The separate released-backup gate
below covers one explicit archive path. Complete deployed fleet acceptance is separate from this codec fixture. No writer default changes.

<a id="compatibility-writer-activation-and-rollback-rehearsal"></a>

### Writer activation and rollback rehearsal

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
automatic writer default remains V3; the named go/v0.9.0 dual-reader release must be deployed and verified
before later writer activation, and v0.8.8 remains an unsupported rollback target
after V4 writes. No NAS or production rollout is certified by this test.

<a id="compatibility-v088-offline-sqlite-backup-prerequisite"></a>

### v0.8.8 offline SQLite backup prerequisite

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

<a id="compatibility-sqlite-queue-startup-prerequisite"></a>

### SQLite queue startup prerequisite

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


<a id="compatibility-postgresql-queue-startup-prerequisite"></a>

### PostgreSQL queue startup prerequisite

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
<!-- /chapter:compatibility -->

<!-- chapter:loadmix -->
<a id="loadmix"></a>

## Mixed-load example

A load tool for the commit path and the event delivery path. It is a tool, not
a gate: it has no baseline, no thresholds and no release wiring.

- `loadmix/` is a generated golem app whose Article, ArticleAudio, ChatThread,
  ChatMessage and ReadingSession mirror Readable's column shapes, with
  Subscriptions on every model except ReadingSession, which writes no facts and
  is the control.
- `cmd/loadmix` is the driver: open-loop Poisson arrivals through the generated
  mutation API, one principal per simulated user, the event publisher and one
  Article and one ChatMessage subscription per user.

Run from `go/examples/loadmix/` with `GOLEM_TEST_POSTGRES_DSN` set and `make go-build`
done at the repository root:

```
go run ./cmd/loadmix -kind all
go run ./cmd/loadmix -kind steady -users 100
go run ./cmd/loadmix -kind burst
go run ./cmd/loadmix -kind saturation -writers 16
go run ./cmd/loadmix -kind steady -users 10 -warmup 3s -measure 12s
```

`-kind all` runs steady at `-users`, burst at U=100 and saturation. `-json`
prints the full result. The exit code is 1 when any run fails.

A run fails only on correctness: a stream termination (recorded by code, and
the run stops at the first one), a foreign delivery, a duplicate or surplus
delivery, a shortfall between expected and delivered events after a bounded
drain, or a mutation error. Expected events are counted per owner subscription
from successful Article and ChatMessage writes; delivered events are counted by
(subscription, event ID).
<!-- /chapter:loadmix -->

# First-class optimistic concurrency implementation contract

Status: **accepted implementation contract; not shipped**.

Audience: the engineer implementing optimistic concurrency and the reviewer
deciding whether it is complete. This contract covers an explicit version-token
compare-and-swap invariant only.

Implementation handoff rule: every “must” is acceptance criteria. If a mutation
surface cannot carry an exact per-row expectation, omit/refuse that surface for
a concurrency-enabled model in version 1. Do not add silent retries, conflict
merging, history, locks, or business workflow as a substitute.

## 1. Product decision

A model may explicitly declare one stored `int64` concurrency field. Golem owns
its initialization and advancement. Every generated mutation that can overwrite
an existing row must compare the caller's expected version with the current
version in the same database transaction and write statement.

If the version is stale, the mutation changes zero application rows, commits no
facts/events, runs no transaction-after/after-commit hook, and returns the
existing stable public `CONFLICT` code.

This prevents lost updates. It does not decide how an application resolves a
conflict.

## 2. Explicit schema declaration

Add one compile-time model option:

```go
func OptimisticConcurrency[M any](
    field ScalarColumn[M, int64],
) ModelOption[M]
```

Example:

```go
type Post struct {
    ID      golem.UUID `db:"id" golem:"primary;default=uuid"`
    Title   string     `db:"title"`
    Version int64      `db:"version"`
}

func (Post) Define() golem.ModelSpec[Post] {
    return golem.DefineModel(
        golem.OptimisticConcurrency(Posts.Version),
        // existing policy/GraphQL/etc options
    )
}
```

The compiler must require exactly one field that is:

- a stored scalar on the same model;
- logical `int64`;
- non-null;
- not an identity/key component;
- not generated, updated-at, immutable-by-schema, or database-read-only for an
  unrelated reason;
- not given an application-authored default; and
- exposed for ordinary reads unless an existing field policy masks it.

The option is explicit. Golem must not infer concurrency from names such as
`Version`, `Revision`, `UpdatedAt`, or `ETag`.

The field becomes runtime-owned for create/update purposes. It is omitted from
generated create/update/updateMany inputs, cannot be changed by hooks, and
cannot be targeted through ordinary increment/decrement/set operations.

## 3. Token semantics

- The first committed create stores version `1`.
- Every successful mutation that changes a concurrency-enabled row stores
  exactly `old + 1`.
- The expected and stored versions are valid only in `[1, MaxInt64]`.
- Zero, negative, malformed, or overflowed tokens fail before database work
  with `BAD_USER_INPUT`.
- A row at `MaxInt64` cannot be mutated and returns `CONFLICT`; it never wraps.
- A no-op update that is accepted as a mutation still advances the version,
  because it successfully consumed one expected state and may run hooks/facts.
- Version values are model-local opaque concurrency tokens. Applications must
  compare equality only and must not derive time, ordering across records, or
  global sequence semantics from them.

## 4. Generated Go surface

Add closed public expectation values:

```go
type ExistingVersion struct { /* opaque */ }
type ConcurrencyExpectation struct { /* opaque */ }

func ExpectVersion(value int64) ExistingVersion
func ExpectExisting(value int64) ConcurrencyExpectation
func ExpectAbsent() ConcurrencyExpectation
```

Constructors retain invalid values only long enough for the ordinary freeze
boundary to return `BAD_USER_INPUT`; they expose no struct literals or mutable
state.

For a concurrency-enabled model, generated clients have these signatures:

```go
Update(ctx, target, expected ExistingVersion, input, projection...)
Delete(ctx, target, expected ExistingVersion, projection...)
Upsert(ctx, target, expected ConcurrencyExpectation,
       create, update, projection...)
```

The signatures apply equally to `Caller`, `System`, `CallerTx`, and `SystemTx`.
There is no privileged System bypass. A trusted maintenance process reads the
current token and supplies it like every other writer.

Create takes no expectation and initializes version 1.

### 4.1 Upsert is explicit compare-and-swap

For a concurrency-enabled model:

- `ExpectAbsent()` means “create only if no row matches the unique selector.”
  If a row exists—regardless of its version—the result is `CONFLICT`.
- `ExpectExisting(v)` means “update only if an authorized row exists at version
  `v`.” A missing/invisible row remains `NOT_FOUND`; a visible row at another
  version is `CONFLICT`.

This intentionally removes ambiguous classic upsert behavior for versioned
models. The caller states which prior state it observed.

### 4.2 Batch surfaces

`UpdateMany` and `DeleteMany` are not generated for a concurrency-enabled model
in version 1. One scalar expectation cannot safely represent different versions
for multiple rows. Predicate-based nested `updateMany`/`deleteMany` are likewise
omitted when they would mutate versioned rows.

Applications that require bulk compare-and-swap use an explicit transaction
and individual selector/version pairs. A future bounded coordinated batch API
requires a separate proposal.

### 4.3 Nested mutations and relations

Every nested operation that writes a versioned row must carry an exact
expectation for that row:

- nested update/delete use `ExistingVersion`;
- nested upsert uses `ConcurrencyExpectation`;
- connect/disconnect/set operations that update a versioned foreign-key owner
  require that owner's existing version; and
- recursive writes apply the rule independently at every versioned node.

If the generated nested grammar cannot name every affected row and expected
version, that operation is omitted/refused for the versioned relation. It must
not silently mutate without advancing/checking the token.

## 5. Generated GraphQL surface

For concurrency-enabled models:

- create inputs omit the version field;
- update and delete operations require `expectedVersion: Int64!`;
- upsert requires a closed expectation input;
- output/read/filter/order may expose version under ordinary field policy; and
- updateMany/deleteMany roots and nested batch variants are omitted.

The upsert expectation is:

```graphql
input PostConcurrencyExpectationInput {
  version: Int64
  absent: Boolean
}
```

The input object itself is non-null and exactly one member must be supplied:

- `version` with a positive value means `ExpectExisting`;
- `absent: true` means `ExpectAbsent`;
- omitted, both members, `absent: false`, null version, or non-positive version
  is `BAD_USER_INPUT` before SQL.

GraphQL errors use extension code `CONFLICT` and the fixed public message
`mutation conflicted`. They never include current/expected versions, existence,
provider errors, SQL, or row data.

## 6. Authorization and non-disclosure

Concurrency is evaluated only after the normal selector and caller policy have
identified an authorized row.

- missing row and policy-invisible row remain indistinguishable `NOT_FOUND`;
- visible row plus stale version returns `CONFLICT`;
- invalid expectation returns `BAD_USER_INPUT` before SQL;
- a conflict response never returns the current row or current version; and
- field policy may mask the version on reads, although an application that
  expects clients to update must provide some authorized way to obtain it.

The SQL predicate contains selector, policy/guard, and exact version equality.
No preliminary check may replace the final atomic predicate.

## 7. Transaction and hook order

The runtime must preserve existing mutation semantics while minimizing stale
side effects:

1. freeze and validate selector/input/expectation;
2. resolve actor policy and select/lock the authorized pre-image using the
   existing provider transaction strategy;
3. compare the loaded version and refuse stale expectations before invoking the
   application before-hook;
4. invoke the before-hook exactly as existing mutation retry semantics require;
5. revalidate the transformed mutation while retaining the immutable
   expectation;
6. execute one atomic write whose predicate includes the expected version and
   whose assignment advances it;
7. treat a zero-row atomic write after a successful precheck as `CONFLICT`;
8. run transaction-after hook against the row containing the new version;
9. flush facts/outbox in the same transaction; and
10. run after-commit exactly once only after commit.

Hooks may replace ordinary target guards today. On a concurrency-enabled model,
target replacement must preserve the original expectation. No hook receives a
capability to remove, lower, or forge it.

Conflict is an application outcome, not a provider-retry signal. Golem must not
replay an application transaction closure or automatically retry using the
newly observed version.

## 8. Provider execution

### SQLite

Within the existing immediate transaction/serialized-writer boundary, use an
atomic statement equivalent to:

```sql
UPDATE posts
SET title = ?, version = version + 1
WHERE id = ? AND version = ? AND <authorized guard>
RETURNING ...;
```

### PostgreSQL

Within the existing transaction and lock strategy, use the same equality and
increment invariant with `RETURNING`. Do not introduce advisory locks or a
separate version table.

Both providers must return the same row version, public error, hook phases,
fact/event count, causation, and rollback behavior. Provider serialization or
deadlock errors retain the existing bounded retry semantics; a proven stale
version never retries.

## 9. Events, facts, and projections

- A successful create fact/entity contains version 1.
- A successful update/upsert fact/entity contains the advanced version.
- A delete fact preserves the deleted row's last version; deletion does not
  manufacture a post-delete version.
- A conflict emits no fact/event and changes no outbox/delivery row.
- Computed fields and after hooks observe the committed new version.
- Caller and GraphQL return the same version when selected and authorized.

## 10. Schema, migration, and compatibility

The compiler/ContractIR/physical snapshots must record concurrency field
identity. Declaration order or renaming does not change its identity.

Adding optimistic concurrency to an existing model requires one reviewed
migration that:

1. adds the field through the provider's safe staged required-column path;
2. deterministically backfills every existing row to version 1;
3. verifies no invalid/null token remains; and
4. makes the field required/runtime-owned.

This may reuse the reviewed-backfill machinery in
[`SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md`](./SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md),
but must not accept application runtime SQL.

Removing concurrency, changing its field, or changing its type is
`RiskManual`/refused in version 1. Renaming the same stable field is ordinary
reviewed rename behavior.

The feature changes compiler IR, generated Go, GraphQL, physical/migration, and
public compatibility inventories. Bump the relevant ABI/format versions and
provide historical decoders where the compatibility contract requires them.

## 11. Observability and limits

Existing mutation observations must report `outcome=refused`,
`reason=conflict`, and truthful statement aggregation for stale writes. They
must not add version values, raw target predicates, or a high-cardinality
conflict label.

No new worker, queue, cache, goroutine, table, or connection pool is permitted.
Version checks remain inside existing bounded mutation statements and
transactions.

## 12. Mandatory acceptance gates

1. `TestOptimisticConcurrencyDeclarationAndGeneratedSurfaceAreExact`
2. `TestOptimisticConcurrencyCreateUpdateDeleteAndUpsertCAS`
3. `TestOptimisticConcurrencyMissingInvisibleAndStaleAreIndistinguishableAsSpecified`
4. `TestOptimisticConcurrencyConflictRollsBackHooksFactsEventsAndRelations`
5. `TestOptimisticConcurrencyNestedEveryWrittenRowRequiresExpectation`
6. `TestOptimisticConcurrencyBatchBypassSurfacesAreAbsent`
7. `TestOptimisticConcurrencyCallerSystemTransactionAndGraphQLParity`
8. `TestOptimisticConcurrencySQLiteAndPostgreSQLConcurrentWritersHaveOneWinner`
9. `TestOptimisticConcurrencyVersionOverflowAndInvalidInputTouchNoDatabase`
10. `TestOptimisticConcurrencyReviewedUpgradeBackfillsExistingRows`
11. `TestOptimisticConcurrencyObservationCountsAndRedaction`
12. `TestOptimisticConcurrencyExternalGeneratedApplicationRaceOracle`

The concurrent-writer gate launches real competing goroutines/connections with
the same expected version. Exactly one commits; every loser receives
`CONFLICT`; final version advances once; one fact/event exists. Run SQLite,
PostgreSQL C, and PostgreSQL linguistic profiles under `-race` with zero skips
in mandatory mode.

## 13. Mutation resistance

Tests must kill compiling mutants that:

- infer a concurrency field by name;
- expose version in create/update inputs;
- omit the version predicate or increment;
- compare version only before, not in, the write statement;
- let System or a nested/batch path bypass the expectation;
- reveal current version/existence on conflict;
- treat invisible stale rows as conflict rather than not found;
- run before/after hooks or emit a fact on stale refusal;
- wrap `MaxInt64` to a non-positive value;
- retry a conflict with the latest version; or
- let two writers with one expected version both commit.

Compile/baseline failures are invalid mutation evidence.

## 14. Completion definition

This feature is complete only when an explicitly versioned model has one
portable compare-and-swap contract across every remaining generated mutation
surface, exactly one concurrent writer can consume a version, stale writes leak
nothing and commit nothing, and no batch/System/nested path bypasses the token.

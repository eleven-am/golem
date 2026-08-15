# First-class optimistic concurrency implementation contract

Status: **implemented locally; the public claim/declaration, compiler ownership,
ContractIR v6 with frozen v5, physical v3 with frozen v2/provider-owned
initialization, runtime CAS, the generated Go and GraphQL surfaces, conflict
observation, and the mandatory live SQLite/PostgreSQL external race evidence all
exist and pass; integrated release evidence pending**.

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

Constructors retain a single opaque invalid state for non-positive values so
the ordinary freeze boundary can return `BAD_USER_INPUT` before database work;
they expose neither the raw value, a variant discriminator, nor mutable state.
Runtime comparison does not unwrap a token: it compares it with the closed
invalid zero state during freeze and, after reading the authorized locked row,
compares it for equality with `ExpectVersion(observed)` or
`ExpectExisting(observed)`. The already-observed row version, not a value
extracted from the token, is used for the compare-and-swap bind.

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

Transaction-after hook executors are write clients too. Their model-erased
runtime ABI carries no expectation in version 1, so their update, delete,
upsert, update-many, and delete-many operations all fail closed against a
versioned model; they are not an internal authority bypass.

Create takes no expectation and initializes version 1.

### 4.1 Upsert is explicit compare-and-swap

For a concurrency-enabled model:

- `ExpectAbsent()` means “create only if no authorized row matches the unique
  selector.” An authorized existing row—regardless of its version—returns
  `CONFLICT`. A policy-invisible existing row remains indistinguishable from a
  missing row and returns `NOT_FOUND`; this API is not an existence oracle.
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

Generated model-specific batch input and batch hook request/result aliases are
also omitted for a concurrency-enabled model. The framework's generic batch
types remain available for non-versioned models, but a forged/model-erased old
request against a versioned model is refused by the runtime registry boundary.

Applications that require bulk compare-and-swap use an explicit transaction
and individual selector/version pairs. A future bounded coordinated batch API
requires a separate proposal.

### 4.3 Nested mutations and relations

Every nested operation that writes a versioned row must carry an exact
expectation for that row. The generated nested grammar has nowhere to name one,
so version 1 omits every such operation:

- nested update, delete, and upsert against a versioned target are not
  generated;
- connect/disconnect/set operations that would update a versioned foreign-key
  owner are not generated;
- relation values are create-only on a versioned root, so a source-side
  relation change cannot reuse the root expectation; and
- the rule applies independently at every versioned node, so a non-versioned
  parent cannot mutate a versioned target either.

An inverse or to-many `set` that could disconnect unnamed existing owners is
omitted in version 1; it cannot prove expectations for rows absent from its
desired set. Targetless inverse operations are likewise omitted.

If the generated nested grammar cannot name every affected row and expected
version, that operation is omitted/refused for the versioned relation. It must
not silently mutate without advancing/checking the token.

## 5. Generated GraphQL surface

For concurrency-enabled models:

- create inputs omit the version field;
- update and delete operations require `expectedVersion: BigInt!`, reusing the
  existing GraphQL scalar for logical Go `int64` rather than adding an alias;
- upsert requires a closed expectation input;
- output/read/filter/order may expose version under ordinary field policy; and
- updateMany/deleteMany roots and nested batch variants are omitted.

Custom GraphQL operations may not accept a generated `UpdateManyInput` or
`DeleteManyInput` for a concurrency-enabled model. Old generated clients,
generic maps, and model-erased internal requests do not regain the omitted
capability: the binder/runtime registry refuses those forged paths before SQL.

The upsert expectation is:

```graphql
input PostConcurrencyExpectationInput {
  version: BigInt
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
3. apply row/action authorization, compare the loaded version, and refuse stale
   expectations before invoking the application before-hook; changed-field
   authorization remains after the hook because the hook may transform input;
4. invoke the before-hook exactly as existing mutation retry semantics require;
5. revalidate the transformed mutation while retaining the immutable
   expectation;
6. execute one atomic write whose predicate includes the expected version and
   whose assignment advances it;
7. treat a zero-row atomic write after a successful precheck as `CONFLICT`;
8. run transaction-after hook against the row containing the new version;
9. flush facts/outbox in the same transaction; and
10. run after-commit exactly once only after commit.

Hooks may replace ordinary mutation targets today. On a concurrency-enabled
model, the original frozen selector and authorized pre-image identity remain
immutable after the version precheck. A before hook may transform ordinary
input, but a semantically changed target/key is refused during revalidation;
the expectation must never be applied to a different row that happens to carry
the same version. No hook receives a capability to remove, lower, retarget, or
forge the expectation.

Conflict is an application outcome, not a provider-retry signal. Golem must not
replay an application transaction closure or automatically retry using the
newly observed version.

Same-selector Golem upserts use the existing provider selector guard before
the absence proof, so two `ExpectAbsent` upserts are serialized before
`BeforeCreate` on both providers. A plain create or an external database writer
does not own that upsert guard. On PostgreSQL, such a writer may win after a
valid absence proof and after the retry-safe `BeforeCreate` hook has run once.
That losing upsert still returns the closed visible `CONFLICT` or
policy-invisible `NOT_FOUND` result and must emit no transaction-after hook,
after-commit hook, fact, event, or durable write. Golem does not broaden every
ordinary create with advisory locking to erase this mixed-operation edge.

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
- A successful mutation result and every in-transaction/after-commit entity
  produced for that mutation contain the advanced version. The existing durable
  v2 update fact remains identity-based; later event hydration may observe a
  newer committed row version and is not a historical row-snapshot guarantee.
- A delete fact preserves the deleted row's last version; deletion does not
  manufacture a post-delete version.
- A conflict emits no fact/event and changes no outbox/delivery row.
- Computed fields and after hooks observe the committed new version.
- Caller and GraphQL return the same version when selected and authorized.

## 10. Schema, migration, and compatibility

ModelIR owns the concurrency field identity. ContractIR and physical snapshots
carry independently interpretable, validated projections of that same identity;
compilation/bootstrap requires exact agreement and never re-infers three
separate facts. Declaration order or renaming does not change the stable field
identity.

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

The declaration and the generated surface are exact:

1. `TestOptimisticConcurrencyDeclarationAndGeneratedSurfaceAreExact`,
   `TestOptimisticConcurrencyDeclarationPublishesExactModelIRFieldOwner`, and
   `TestApplyRejectsEveryIneligibleConcurrencyFieldFact`

Create, update, delete, and upsert are compare-and-swap, and stale, missing,
and invisible rows stay indistinguishable as specified:

2. `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite` and
   `TestVersionedOptimisticConcurrencySQLIsExactCAS`
3. the visible-conflict and invisible-not-found corpora inside
   `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite` and
   `TestOptimisticConcurrencyPostgreSQLCrossConnectionRaces`

A conflict rolls back hooks, facts, events, and relations:

4. `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite` and
   `TestOptimisticConcurrencyRollbackFailureCannotMasqueradeAsUniqueCollision`
5. `TestOptimisticConcurrencyNestedEveryWrittenRowRequiresExpectation`

No batch, System, custom-GraphQL, or model-erased path bypasses the token:

6. `TestOptimisticConcurrencyModelAuthoredBatchAndUnsafeNestedSurfacesAreAbsent`,
   `TestOptimisticConcurrencyGraphQLSchemaIsExactAndUnsafeSurfacesAreAbsent`,
   and `TestOptimisticConcurrencyCustomUpdateManyInputIsRejectedThroughLists`
7. `TestOptimisticConcurrencyRuntimeABIIsTypedAndExplicit`,
   `TestOptimisticConcurrencyGraphQLClaimsFreezeIntoExactRuntimeRequests`,
   `TestOptimisticConcurrencyCallerGraphQLRuntimeDispatchUsesClosedClaims`, and
   `TestNonVersionedGraphQLRuntimeRejectsForgedConcurrencyClaim`

Concurrent writers have exactly one winner, and invalid or exhausted tokens
change nothing:

8. `TestOptimisticConcurrencyPostgreSQLCrossConnectionRaces`, the competing
   SQLite writers inside
   `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite`, and
   `TestExternalGeneratedApplicationOptimisticConcurrencyRaces`
9. `TestOptimisticConcurrencyExpectationsRetainOnlyClosedImmutableState`,
   `TestConcurrencyExpectationDistinguishesAbsentExistingAndInvalid`,
   `TestOptimisticConcurrencyGraphQLExpectationOneOfRejectsEveryInvalidShape`,
   and the `MaxInt64` and invalid-expectation corpora inside
   `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite`

Reviewed upgrades initialize every existing row, and observation stays truthful
and redacted:

10. `TestOptimisticConcurrencyPhysicalUpgradeHasExactClosedInitializationGraph`,
    `TestPlanIncrementalInitializesEveryConcurrencyValueToExactlyOne`,
    `TestSQLiteIncrementalInitializesConcurrencyWithProviderLiteralOne`, and
    `TestOptimisticConcurrencyCannotAdoptRemoveOrSwitchField`
11. the `outcome=refused`, `reason=conflict`, and statement-count assertions
    inside `TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite`
12. `TestOptimisticConcurrencySQLiteAndPostgreSQLExternalGeneratedApplication`,
    which drives `TestExternalGeneratedApplicationOptimisticConcurrencyRaces`

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

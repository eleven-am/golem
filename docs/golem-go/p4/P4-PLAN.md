# P4 authorized mutations and transactions plan

Status: **complete — all definition-of-done rows and local completion gates pass**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 2, 9, 10, 13,
14, 16–20, and 21. P1 owns schema, identities, generated artifacts, migrations,
and system-schema fingerprints. P2 owns policy resolution, exact condition
evaluation, classification, and provider predicate fragments. P3 owns typed
public projections, exact decoding, caller/system capabilities, and authorized
reads. P4 consumes those contracts and must not recreate them.

The public spelling is fixed by [`PUBLIC-MUTATION-ABI.md`](./PUBLIC-MUTATION-ABI.md).
The executable completion ledger is [`P4-EVIDENCE.md`](./P4-EVIDENCE.md). No P4
status may be changed to complete while any required evidence row is pending.

## 1. Outcome

P4 turns generated, model-typed mutation requests into authorized committed
database changes on SQLite and PostgreSQL. It adds:

- `Create`, `Update`, `Delete`, `Upsert`, `UpdateMany`, and `DeleteMany` to
  generated caller and system model clients;
- generated typed create/update/update-many inputs;
- the accepted nested-write vocabulary;
- caller and system closure transactions whose reads and writes remain bound to
  one `sqlx.Tx`;
- persisted-result create verification and locked before/after update
  verification;
- actual field-diff authorization, including no-op behavior;
- model-attached before, transaction-after, and after-commit hooks;
- bounded deterministic batch mutation sets;
- database-backed upsert branch correctness and bounded retry;
- execution-loader invalidation after successful writes; and
- commit-derived, versioned outbox facts for event-enabled models, including one
  fact per affected top-level batch row and every actual nested row mutation.

P4 is complete only when one generated social application performs the same
authorized mutation corpus on live SQLite, PostgreSQL with `C` collation, and
PostgreSQL with the required linguistic collation profile, including concurrent
multi-connection interference.

## 2. Explicit exclusions

P4 does not claim:

- GraphQL schema, input objects, or resolvers (P5);
- aggregate/group-by or scoped reads (P6);
- outbox publication, subscription hubs, delivery retries, or CDC (P7);
- arbitrary external SQL observation (P7 CDC only);
- top-level `CreateMany`;
- mutation access through the P6 scoped read builder; or
- MySQL.

P4 writes durable outbox facts but does not publish them. Queue remains outside
the Go core product.

## 3. TypeScript compatibility and deliberate Go changes

### 3.1 Compatibility targets

P4 preserves these observable TypeScript outcomes:

- create/update/delete/upsert/update-many/delete-many behavior;
- composite and named unique selectors;
- persisted database defaults and atomic numeric updates being verified from the
  real stored row rather than simulated in Go;
- policy-invisible and missing single-row write targets sharing `NOT_FOUND`;
- write filters being classified as reads before database work;
- update/delete target selection using their own action constraints;
- upsert probing through update reach and running only the chosen branch hooks;
- before hooks transforming or vetoing typed requests;
- field authorization based on actual before/after differences, so a no-op does
  not require permission for an unchanged field;
- recursive nested-write authorization;
- callback transactions; and
- bounded per-row event facts for top-level batches.

### 3.2 Required improvements

P4 deliberately does not reproduce these TypeScript weaknesses:

1. Every caller mutation is transaction-bound, including paths that look
   unconditional. There is no fast path that can escape result verification,
   hooks, outbox facts, or loader invalidation.
2. Upsert correctness does not depend solely on cooperative in-process or
   striped locking. Database uniqueness, transaction isolation, provider locks,
   and bounded whole-attempt retry are authoritative.
3. Nested writes become explicit mutation nodes. They cannot be forwarded as an
   opaque provider payload and verified approximately afterward.
4. Every actual nested create/update/delete can produce its own fact. Nested
   batch changes are not silently absent from event history.
5. SQLite and PostgreSQL SQL are rendered by Golem from validated descriptors;
   there is no Prisma validation fallback and no caller-supplied identifier.
6. After-commit failure is not returned as an ordinary mutation failure after
   data has committed. It is reported through the required application error
   handler described in section 10.4.

## 4. One mutation architecture

The complete caller path is:

```text
generated typed request
  -> run applicable before hook(s)
  -> freeze/clone request values
  -> bind identities and validate exposure/requiredness
  -> classify every caller value-influencing field
  -> expand the complete static nested operation tree
  -> derive action/field/dependency requirements
  -> build deterministic provider-neutral MutationPlan
  -> begin or reuse one execution-bound sqlx.Tx
  -> acquire/lock exact targets and dynamic nested sets
  -> execute typed provider statements
  -> decode exact before/after snapshots
  -> verify row postconditions and actual changed fields
  -> build the public return projection through P3
  -> run transaction-local after hooks
  -> append commit-derived outbox facts
  -> commit
  -> clear execution loaders
  -> run/report after-commit hooks
```

System mutations use the same binder, planner, transaction, provider SQL,
decoding, fact, and invalidation paths. They omit caller policy, classification,
and all caller hooks. A system write is still a Golem write and therefore emits
configured outbox facts.

### 4.1 Package boundaries

P4 adds production packages with closed responsibilities:

```text
golem                         public typed mutation values and hook payloads
internal/mutation/ir          closed provider-neutral request/plan value types
internal/mutation/bind        schema/exposure/identity/value binding
internal/mutation/plan        authorization, touched graph, snapshots, limits
internal/mutation/sql         deterministic SQLite/PostgreSQL statement rendering
internal/mutation/decode      exact before/after/result/outbox value decoding
runtime                       transaction execution, hooks, retry, invalidation
```

The mutation packages consume `policy/ir`, `policy/resolve`, `policy/classify`,
`policy/evaluate`, `policy/sql`, the P1 schema registry, and P3 result planning.
They do not read tags, infer physical names, or maintain a second model registry.

## 5. Mutation IR and touched graph

One immutable `MutationPlan` records, at minimum:

- operation kind and root model identity;
- caller versus system stance;
- the bound unique target and optional guard predicate;
- normalized scalar operations and typed operands;
- every static and dynamically expanded nested node;
- node parent, relation, depth, deterministic ordinal, and branch ownership;
- selecting action and policy constraint for every existing-row node;
- fields named by selectors, filters, and nested filters;
- candidate written fields;
- before-image fields and relation dependencies;
- after-image fields and relation dependencies;
- result projection and private P3 dependencies;
- hook inventory and exact phase order;
- expected identity behavior, including permitted single-row identity changes;
- event/outbox requirements; and
- provider requirements, statement bounds, and retry class.

Runtime identities discovered by a query are appended only to an
execution-local expansion of the frozen plan. They never mutate the reusable
schema or generated descriptors.

The touched graph has stable depth-first ordinals. Those ordinals determine SQL
tie-breakers and deterministic diagnostics. Observable mutation ordering uses
those ordinals except for source dependencies that must exist before their FK-
owning parent insert: the canonical order is root, all pre-parent dependency
subtrees in stable ordinal order, then ordinary subtrees in stable ordinal
order. Replacement graphs inherit the replaced node's global order prefix;
their local ordinals never compete with main-graph ordinals.

## 6. Authorization semantics

### 6.1 Selection and information disclosure

- Update and update-many select existing rows with the update constraint.
- Delete and delete-many select existing rows with the delete constraint.
- Upsert probes with the update constraint. If update has no reach, no existence
  probe runs and the operation proceeds as the missing/create case.
- Caller predicates and unique-selector guards only narrow these constraints.
- A missing single-row target and a policy-invisible target execute the same
  constrained lookup and return the same public `NOT_FOUND`.
- Every selector/filter/nested-filter field is classified with the read lens
  before provider statements. Update/delete fields discharge only when the
  actual selecting constraint proves their read condition.
- An upsert target is classified before the branch is known, but the branch
  probe still uses update reach. This preserves the accepted P2/P3
  classification contract without substituting the read constraint for the
  probe.

### 6.2 Create

Create authorization uses the actual persisted result:

1. Validate the transformed input and resolve runtime-owned values.
2. Preflight that a create policy and every explicitly requested field policy
   exist; conditional policy is not guessed from the input.
3. Execute inside the transaction and load the required persisted after-image,
   including database defaults, generated values, foreign keys, and policy
   dependencies.
4. Evaluate the create row condition against the persisted after-image.
5. Evaluate create field conditions for caller- or hook-authored fields against
   that after-image.
6. Verify every nested node independently.
7. Roll back on any denial.

Database-generated, database-read-only, generated-column, and `updated` values
are not treated as caller-authored fields merely because the database produced
them. They still participate in row and dependency evaluation.

### 6.3 Update

Update authorization uses both truthful images with distinct jobs:

- the locked pre-image must satisfy the update selecting constraint;
- exact logical before/after comparison determines the fields that actually
  changed;
- update field conditions for changed author-writable fields are evaluated
  against the locked pre-image, matching the accepted TypeScript rule meaning;
- a requested assignment that leaves the stored logical value unchanged is a
  no-op and does not require field permission;
- database-generated/read-only changes are excluded from the caller field-diff
  set but remain available to row/dependency verification; and
- the persisted after-image must satisfy the update row condition as a
  postcondition. A transition outside update reach rolls back.

Logical equality is type-directed: bytes compare by content, JSON and scalar
lists by canonical typed structure, Decimal by exact coefficient/scale value,
and temporal values by their declared normalized precision. Driver Go types do
not decide equality.

### 6.4 Delete

Delete locks and hydrates the constrained pre-image, builds any authorized
public return projection and private delete snapshot, executes the delete by the
exact identity, verifies the affected count, runs after hooks, and appends its
fact in the same transaction. There is no after-image.

### 6.5 Returned rows

Single-row mutation results use P3 `Projection[M]` and the same field lens,
dependency hydration, masking, and private stripping as reads. Mutation action
permission does not imply read permission. A never-readable selected field is
refused before mutation; a conditional projected field is finalized against the
persisted create/update row or hydrated delete pre-image.

An omitted projection is a valid empty typed row on the programmatic surface.
P5 will always translate the GraphQL selection set into an explicit projection.

## 7. Inputs and update operations

Generated inputs obey the P1 exposure matrix:

- hidden, read-only, database-read-only, and generated fields have no mutation
  constructor;
- immutable fields are present only in create inputs;
- write-only fields are writable but have no read/filter/projection capability;
- non-null fields without a default are required at bind time;
- defaulted, generated, updated-at, and nullable fields may be omitted;
- nullable fields have an explicit null operation distinct from omission; and
- update-many accepts scalar operations only.

The mandatory portable update vocabulary is:

- `Set` for every writable scalar, enum, JSON, bytes, and scalar-list field;
- `Null` for nullable writable fields;
- `Increment` and `Decrement` for accepted numeric fields; and
- relation operations defined in section 8.

Additional numeric operations or scalar-list append operations are generated
only after a registry entry proves identical validation, overflow/rounding,
SQLite, PostgreSQL, persisted-result, and field-diff behavior. They are not
forwarded opportunistically because one provider accepts syntax.

Mutable values are cloned at request freeze and hook boundaries. Input maps,
slices, JSON, bytes, and nested operation slices cannot be changed concurrently
after execution begins.

Runtime-owned UUID/identity/now/updated values are frozen once for one logical
mutation before an internal retry loop, so a retry does not silently become a
different authored mutation. Database-owned defaults remain database-owned and
only the committed attempt is observable.

## 8. Nested writes

P4 completion covers the closed nested vocabulary:

```text
create             createMany
connect            connectOrCreate
disconnect         set
update              updateMany
upsert              delete             deleteMany
```

Generated relation handles expose only operations legal for their cardinality,
requiredness, direction, field exposure, and available child input. A required
to-one relation has no disconnect operation. A to-one relation never exposes a
list-shaped operation. Invalid operations fail at generation or compilation,
not after partial SQL.

Rules:

1. Every nested filter/selector is bound and classified before its first
   provider statement.
2. Every nested operation expands into the rows whose persisted data or relation
   membership can change.
3. Each row-changing node receives its owning model's create/update/delete
   policy and field checks. A parent grant never grants a child.
4. Connect, disconnect, and set authorize the existing target selected by the
   caller and the actual foreign-key-owning row that changes. The four-action
   policy vocabulary represents relation membership changes as update.
5. `set` computes exact added and removed identity sets inside the transaction.
6. Nested create-many, update-many, and delete-many use the same bounded exact-set
   rules as top-level batches and count toward the transaction touched-row cap.
7. Connect-or-create and nested upsert use the same update-reach probe,
   uniqueness authority, and truthful branch rules as root upsert.
8. The planner uses RelationIR local/remote key order and supports composite
   identities across every nested operation.
9. Cycles and recursion are bounded by depth and total touched rows. No provider
   cascade is treated as an authorized nested write unless the complete affected
   fact set is known from the plan or a provider return set.
10. Hooks run for actual nested create/update/delete nodes. Connect/disconnect
    invoke update hooks only for rows whose persisted foreign-key state changes.

The execution order is deterministic: before hooks and writes use the canonical
order defined above after branch/target expansion; transaction-after hooks run
in reverse canonical order after final-graph verification; facts retain forward
canonical order; after-commit hooks retain the corresponding committed order.

## 9. Batches and identity changes

Top-level update-many and delete-many are always bounded:

1. Classify the caller filter before beginning a transaction.
2. Inside the transaction, select at most `MaxTouchedRows + 1` authorized
   identities in declared primary-key order.
3. Refuse the entire operation if the sentinel exists.
4. Lock or otherwise stabilize that exact set using the provider strategy.
5. Mutate only that captured set, chunking parameters below the provider limit.
6. Verify affected counts and, for updates, load and verify the same after-image
   identity set.
7. Produce one fact per affected row for event-enabled models.

There is no silent truncation and no global child/batch limit applied as if it
were per row.

P4 V1 refuses identity-component changes in update-many and nested update-many.
Single-row update may change an identity component only when the schema exposes
it as mutable. The plan then records ordered before and after identities,
rebinds result lookup to the after identity, verifies referential effects, and
emits both identities in the update fact. Provider cascades whose affected set
cannot be enumerated are rejected for such a mutation.

## 10. Hooks

P4 fills the P1 aliases without renaming them:

```text
CreateHookRequest/Result
UpdateHookRequest/Result
DeleteHookRequest/Result
UpdateManyHookRequest/Result
DeleteManyHookRequest/Result
```

### 10.1 Before

Before hooks receive a mutable request facade over an owned clone. Typed helpers
may set, clear, replace, or append only model-correct generated input values.
After every hook transformation the request is rebound and validated before
authorization. A hook cannot smuggle an unknown field, forbidden nested
operation, or cross-model identity into the planner.

### 10.2 Transaction-after

After hooks run after persisted-result and public-result verification but before
outbox append and commit. They receive immutable typed before/after snapshots,
the public result, count/identity information where applicable, and a
transaction-bound opaque executor available through typed `golem.Hook*` helper
functions. Hook-initiated Golem writes use the same actor, policies, `sqlx.Tx`,
limits, invalidation buffer, and fact buffer. They cannot access the base DB or
arbitrary SQL.

### 10.3 Upsert and retries

There is no upsert hook. Each attempt runs only the selected create or update
branch hooks. Before and transaction-after hooks may repeat when an
engine-owned upsert attempt is retried and therefore must not perform
irreversible external work. Only the committed branch's after-commit hook runs.

An application-owned transaction closure is never automatically replayed. An
upsert conflict inside it returns `CONFLICT` rather than rerunning arbitrary
application code.

### 10.4 After commit

After-commit hooks run only after a successful outermost commit and never for a
savepoint rollback, failed retry, denial, provider error, or cancelled
transaction. They have no transaction executor.

Returning a normal mutation error after commit would invite the caller to retry
an already committed write. Therefore after-commit hook errors are delivered to:

```go
AfterCommitError func(context.Context, golem.AfterCommitFailure)
```

The generated application refuses to open when after-commit hooks exist and the
handler is nil. The committed mutation return remains successful. Reliable
external delivery belongs in the transactional outbox/P7; after-commit hooks
are explicitly best-effort application effects.

System operations bypass before, transaction-after, and after-commit hooks.

## 11. Transactions and connection ownership

Generated caller and system transaction closures receive clients backed only by
one `*sqlx.Tx`. Reads reuse the P3 planner/decoder with that transaction as their
executor. Mutations reuse the same transaction without beginning, committing,
or rolling it back individually.

- Tx clients do not expose `Transaction`, `System`, or a base DB handle.
- Runtime prepared operations carry an unforgeable executor/transaction identity.
- Relation loaders and hook helpers use the transaction-bound executor.
- Any internal fallback to `App.database` while a transaction identity is active
  is a hard invariant error and a named mutation-test failure.
- A callback error, panic, cancellation, hook error, policy denial, fact limit,
  or provider error rolls back the outer transaction and discards all buffered
  after-commit work.
- After the outer commit, all execution-local loaders are cleared once before
  the caller can perform another operation.

P4 does not claim it can prevent application code from deliberately closing over
an unrelated caller and starting a separate operation. It guarantees that every
operation invoked through the supplied Tx client and every engine-owned nested
operation cannot escape.

## 12. Upsert and concurrency

Upsert uses a canonical typed token over model identity, selector identity, and
ordered exact selector values. Raw selector values are never persisted in guard
state or diagnostics.

For an engine-owned upsert attempt:

1. Bind/classify before the transaction.
2. Begin the provider transaction.
3. Acquire the provider's database-backed selector guard as the first statement.
4. Probe under the update constraint and lock an accessible existing row.
5. Run exactly the update branch if found; otherwise run create.
6. On a retryable serialization, deadlock, busy, or uniqueness interference,
   roll back the complete attempt and retry from step 2 up to the configured
   bound.
7. If no truthful authorized branch commits, return stable `CONFLICT` without
   raw provider detail.

PostgreSQL uses a transaction-scoped advisory/row-lock strategy plus unique
constraints. SQLite uses a provider-verified immediate write transaction and a
bounded guard row strategy plus unique constraints. The concrete strategy is a
provider capability, not public API.

An existing row outside update reach is treated as missing: no unrestricted
existence query runs. A create collision is reported as `CONFLICT`, exactly as a
missing-row create can conflict with concurrent or other unique state. The
operation never falls through to an unauthorized update branch.

## 13. Outbox facts and invalidation

P4 adds the versioned `SystemOutbox` V1 physical object to both provider system
schemas, renderers, migration/introspection/verification paths, and system
fingerprints. A subscription-enabled model implies event capture.

Each committed fact contains:

- versioned fact and codec identity;
- globally unique event ID;
- generation/schema fingerprint;
- model identity and created/updated/deleted action;
- ordered exact before/after identity as applicable;
- transaction causation ID and deterministic ordinal;
- exact typed scalar metadata required by P7; and
- a private pre-delete snapshot sufficient for later delete authorization when
  configured.

Facts are encoded without float64 or lossy JSON boundaries. Data changes and
facts share the same transaction. Rolled-back attempts leave neither. Exceeding
fact count or encoded-byte limits rolls back rather than dropping facts.

P4 does not claim delivery. P7 owns leasing, publication, retries,
deduplication, subscriptions, and cleanup.

Every successful caller or system write marks the execution dirty. On outermost
commit the runtime clears all execution-local loaders and decision data whose
answers may depend on database state. It does not attempt guessed per-key
invalidation. On rollback the pre-transaction caches remain valid and no
after-write clear is published.

## 14. Limits

The public `runtime.MutationLimits` is copied and normalized during `Open`:

| Limit | Zero-value default and portable hard ceiling |
| --- | ---: |
| nested mutation depth | 5 |
| touched rows per top-level mutation | 1,000 |
| committed facts per outer transaction | 1,000 |
| encoded outbox bytes per outer transaction | 1 MiB |
| parameters per statement | 999 |
| engine-owned upsert attempts | 3 |

Applications may lower but not raise these P4 portable ceilings. Negative
values and raised ceilings fail at `Open`. Every exact boundary and boundary+1
has a live provider test. P8 may raise a ceiling only with new load, failure,
and provider evidence plus a contract change.

## 15. Stable errors

P4 reuses P3 errors and adds `CONFLICT`:

- invalid/missing inputs, illegal nested shapes, limit overflow, declared
  value/domain violations, and before/transaction-hook vetoes:
  `BAD_USER_INPUT`;
- absent and policy-invisible single targets: `NOT_FOUND`;
- unresolved principal: `UNAUTHENTICATED`;
- action or field denial: `FORBIDDEN`;
- exhausted upsert retry, serialization/deadlock interference, uniqueness, or a
  changed captured batch identity set: `CONFLICT`.

Messages contain logical model, field, and operation information only. SQL,
driver messages, physical names, constraint names, selector values, hidden-row
existence, policy structure, and stack/file data remain trusted causes only.

## 16. Work waves

### P4-A — contracts and closed mutation IR

Freeze the public ABI, input capabilities, target shape, hook payloads,
`MutationLimits`, errors, exact value operations, internal MutationIR, plan
invariants, event-fact codec boundary, and provider transaction interfaces.

Gate: compile-pass/fail fixtures prove exposure modes, cross-model assignments,
illegal relation operations, target ownership, and transaction capability
separation before an executor exists.

### P4-B — generator and application surface

Emit mutation-aware per-field/per-relation handles, input aliases, caller/system
model methods, caller/system Tx clients, configuration forwarding, hook bridges,
and deterministic manifest inventory.

Gate: a fresh external module compiles every accepted method and fails every
forged/disabled/illegal method at compile time where Go can express the
distinction.

### P4-C — binder, classification, and static touched graph

Bind values and identities, enforce exposure/requiredness, classify every root
and nested filter/selector, derive row/field constraints and dependencies,
expand the static nested graph, and enforce pre-transaction limits.

Gate: spies prove refused requests open no transaction and issue no SQL,
including all eleven nested kinds and two-hop filters.

### P4-D — provider transaction and single-row kernel

Implement transaction-bound exact create/update/delete statements, target
locking, before/after decoding, create/postcondition verification, exact field
diffs, public P3 result projection, and stable error translation for SQLite and
PostgreSQL.

Gate: live single-row oracle agreement on both providers, including defaults,
generated values, atomic increments, no-ops, identity changes, null/JSON/list/
Decimal/BigInt/time/bytes values, and missing/invisible equality.

### P4-E — nested mutation kernel

Implement every accepted nested operation, dynamic target expansion, composite
relations, deterministic ordering, recursive hooks, row/field verification, and
rollback completeness.

Gate: the complete social graph mutates through every relation direction on both
providers, with a denial injected at every depth and no partial data/fact state.

### P4-F — bounded batches

Implement deterministic exact-set update-many/delete-many and nested batch
variants, provider chunking, count/set verification, identity-change refusal,
limits, and per-row facts.

Gate: exact boundary and overflow corpora plus concurrent interference on both
providers.

### P4-G — upsert and retry

Implement canonical selector guards, provider locking, update-reach probing,
truthful branch pipelines, bounded engine-owned retry, transaction-closure
conflict behavior, and branch hook/fact semantics.

Gate: same-key multi-connection/process tests, hidden-existing/missing equality,
alternate-selector and external interference, retry exhaustion, and exactly one
committed branch fact.

### P4-H — hooks, outbox, and invalidation integration

Fill every mutation hook shell, implement transaction-bound hook helpers,
after-commit error reporting, SystemOutbox V1 migrations/codecs, nested/batch
facts, and execution-wide loader invalidation.

Gate: failpoint tests at every write/hook/fact/commit boundary prove rollback,
commit truth, no premature after-commit effect, and correct read-after-write
behavior through both caller and system Tx clients.

### P4-I — independent oracle and audit

Run an independent plain-Go mutation oracle against live SQLite and both
PostgreSQL profiles. Run concurrency, named mutations, full race, repeat,
shuffle, vet, format, deterministic generation/SQL, and fresh-module acceptance.
Reconcile all evidence without claiming P5–P8.

## 17. Safe parallelization

After P4-A freezes types and invariants, these tracks can proceed concurrently:

```text
generator/API              binder/planner             fact codec/system schema
     |                          |                              |
     +--------------------------+------------------------------+
                                |
                  provider single-row execution
                     /                       \
              nested/batches             upsert/retry
                     \                       /
                      hooks/outbox/invalidation
                                |
                         oracle/final audit
```

Provider-specific SQLite and PostgreSQL render/execution work may also run in
parallel once one provider-neutral plan shape is frozen. Agents must not invent
separate semantic plans for the two providers.

## 18. Definition of done

P4 is complete only when all of the following are `PASS` in
[`P4-EVIDENCE.md`](./P4-EVIDENCE.md):

1. every generated caller/system mutation and transaction method executes from
   a fresh external module;
2. generated inputs expose exactly the legal field/relation operations;
3. every caller-influencing selector/filter/nested-filter is classified before
   transaction or SQL;
4. create verifies persisted rows, explicit fields, defaults, and nested effects
   and rolls back on denial;
5. update/delete use constrained locked targets and missing/invisible outcomes
   are indistinguishable;
6. exact before/after diffs authorize only actually changed fields and preserve
   no-op behavior;
7. every accepted nested operation authorizes and verifies the complete touched
   graph, with composite identities and deterministic hooks/facts;
8. batch mutations are bounded, deterministic, atomic, and emit one fact per
   affected row without silent truncation;
9. upsert uses update reach, commits one truthful branch, survives required
   interference, and exhausts retries as stable `CONFLICT`;
10. caller/system transaction clients keep every read, write, nested operation,
    loader, and hook on the supplied `sqlx.Tx`;
11. before, transaction-after, and after-commit hooks obey transformation,
    ordering, retry, rollback, system-bypass, and error-reporting contracts;
12. data/outbox atomicity, exact codecs, nested facts, batch facts, and
    transaction ordinals pass on both providers;
13. successful writes clear execution loaders through every entry point while
    rollback does not publish a write invalidation;
14. all exact types, stable errors, limits, generated artifacts, SQL text, binds,
    and fact bytes are deterministic across both providers; and
15. the independent oracle, multi-connection/process concurrency, named
    mutation, race, repeat/shuffle, vet, formatting, and CI-equivalent gates
    pass.

## 19. No-deviation rule

Implementation may split work differently, but it may not weaken or silently
reinterpret the public ABI, transaction boundary, authorization image, nested
touched graph, provider equality, fact truth, retry semantics, or definition of
done in this plan. A required semantic change must amend this plan, the Bible if
needed, and the affected evidence row before implementation proceeds.

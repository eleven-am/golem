# P2 policy kernel execution plan

Status: **controlling implementation plan; P2-B through P2-F are implemented;
P2 as a whole is not complete**

Authority: [`../BIBLE.md`](../BIBLE.md) is authoritative. The detailed operator,
policy-resolution, and classification chapters remain normative after applying
the Bible's resolutions. [`OPERATOR-ABI.md`](./OPERATOR-ABI.md) is the accepted
P2-A typed baseline, not the complete P2 surface. The frozen P2-B contracts are
[`PUBLIC-ABI.md`](./PUBLIC-ABI.md), [`INTERNAL-IR.md`](./INTERNAL-IR.md), and
[`PROVIDER-AGREEMENT.md`](./PROVIDER-AGREEMENT.md).

## 1. Outcome

P2 turns model-attached Go policy methods into a fresh immutable policy for one
actor, derives exact row and field constraints, classifies field access, evaluates
conditions in memory, and renders equivalent parameterized SQLite and PostgreSQL
predicates.

P2 is complete only when all of the following are true:

1. The complete P2 authoring surface required by the Bible compiles, including
   ordered grants, denials, and field-scoped rules.
2. Every accepted predicate is a validated immutable typed tree containing only
   generated `ModelID`, `FieldID`, and `RelationID` identities and canonical typed
   values.
3. Predicate construction, policy freezing, normalization, and canonical encoding
   are deterministic and fail closed.
4. Row and field lenses agree with an independent first-matching-rule oracle for
   every generated rule chain in the Phase 0 corpus.
5. Classification produces `always`, `conditional`, or `never`, deterministic
   requirements, a merged relation dependency tree, and conservative semantic
   discharge.
6. Every accepted operator returns the same row identities in the Go evaluator,
   SQLite, and PostgreSQL, including null, Unicode, exact numeric, list, JSON, and
   relation cases.
7. Every rendered authorization predicate is two-valued: SQL `TRUE` is true and
   both SQL `FALSE` and SQL `NULL` are false.
8. Values are parameters and physical identifiers come only from validated P1
   logical and physical descriptors.
9. Provider or storage limitations fail during generation, policy freeze, startup,
   or planning. No unsupported predicate is dropped, approximated, or evaluated
   after pagination.
10. The P2 acceptance and named-mutation suites pass, including live SQLite and
    PostgreSQL agreement.

A public type shell, generated method, unit-only SQL string, or skipped live
PostgreSQL suite does not satisfy this definition of done.

## 2. Starting point

### 2.1 Complete inputs from P1

P2 may rely on these P1 outputs:

- normalized `ModelIR` and `ContractIR`;
- stable model, field, key, relation, and enum identities;
- exact logical types, nullability, relations, and provider capabilities;
- validated SQLite and PostgreSQL physical schemas;
- generated typed model/field/relation descriptors;
- generated model-attached policy bindings;
- canonical schema documents, fingerprints, and generation digest; and
- the `golem inspect` social-network fixture.

P2 must consume those artifacts. It must not reconstruct schema facts with
reflection, table introspection, Go field names, or caller-provided strings.

### 2.2 Implemented P2 foundation

Commit `7ec8b5e` provides the narrow typed baseline, and the current P2-B/P2-C
worktree extends it with:

- equality, ordered, text, bytes, list, opaque, and nullable scalar handles;
- to-one and to-many relation handles;
- typed logical combinators;
- deterministic logical-type-to-handle generation;
- bootstrap type-check failures for invalid handle methods; and
- a dual-provider generated social-style capability fixture;
- real immutable public predicates and ordered frozen policies;
- model-wide grants/denials and field-scoped grants/denials;
- exact public values and sealed, copy-isolated frozen views;
- a closed internal policy IR, canonical encoders, and fingerprints;
- a validated immutable runtime schema registry and public-to-internal binder;
- deterministic conservative condition normalization; and
- separate newest-first row and field rule resolution.

Advanced list, insensitive-text, and JSON policy handles remain deliberately
closed in generated application code until their evaluator/SQLite/PostgreSQL
agreement cells pass.

### 2.3 What is still absent

The repository does not yet contain:

- SQLite or PostgreSQL policy SQL rendering;
- provider agreement fixtures; or
- execution-scoped policy-set construction.

The Phase 0 package is an oracle and design fixture. P2 must not rename it into
production or retain its string model/field identities, `any` operands,
`reflect.DeepEqual`, or simplified operator inventory.

## 3. Contract reconciliation before implementation

P2-A deliberately froze a smaller ABI. That smaller ABI cannot be mistaken for
the Bible's completed P2 contract. The first P2 implementation change must close
the following gaps and update `OPERATOR-ABI.md` before runtime code is accepted.

### 3.1 Rule surface

The final P2 `Rules[M]` surface contains model-wide ordered grants and denials:

```go
CanRead(Predicate[M])       CannotRead(Predicate[M])
CanCreate(Predicate[M])     CannotCreate(Predicate[M])
CanUpdate(Predicate[M])     CannotUpdate(Predicate[M])
CanDelete(Predicate[M])     CannotDelete(Predicate[M])
```

It also contains typed field-scoped rules for actions with field semantics:

```go
CanReadFields(Predicate[M], first Field[M], rest ...Field[M])
CannotReadFields(Predicate[M], first Field[M], rest ...Field[M])
CanCreateFields(Predicate[M], first Field[M], rest ...Field[M])
CannotCreateFields(Predicate[M], first Field[M], rest ...Field[M])
CanUpdateFields(Predicate[M], first Field[M], rest ...Field[M])
CannotUpdateFields(Predicate[M], first Field[M], rest ...Field[M])
```

`Field[M]` is sealed to generated scalar and relation handles and exposes no
string identity. Requiring `first` makes an empty field rule unrepresentable.
Delete remains model-wide because delete has no field data authorization.

Declaration order is semantic. The latest applicable rule wins. Public
`All[M]()` is the authoring spelling for an unconditional rule; freeze records it
as the internal unconditional form so an empty predicate and an unconditional
predicate cannot be confused.

### 3.2 Operator surface

The completed P2 matrix is the Bible's accepted matrix, not only the P2-A subset.
It includes:

- scalar equality, inequality, membership, ordering, and text operations;
- explicit sensitive and ASCII-insensitive string comparison modes where the
  declared provider set has an agreement-proved renderer;
- explicit null presence operations;
- exact scalar-list operations;
- to-one existence/`is`/`isNot` and to-many `some`/`every`/`none`;
- the accepted JSON path, null-sentinel, guarded scalar, string, and array
  operations from `01-operators.md`; and
- logical constants and combinators.

The public Go spelling for comparison modes and JSON paths is frozen in
`PUBLIC-ABI.md`; its positive and negative fixtures are mandatory.

The initial P2 vocabulary is portable or closed. Advanced text, list, and JSON
handles are not emitted until their complete method family passes the evaluator,
SQLite, and PostgreSQL agreement gate. A future provider-specific extension needs
a distinct capability-bearing handle and operator identity; it cannot silently
broaden a portable method.

### 3.3 Relation existence

`ToOne.IsNull` and `ToOne.IsNotNull` mean absence or presence of a related row.
They render through correlated `EXISTS`/`NOT EXISTS`, including for a relation
declared required. They are not reduced from field nullability alone. This follows
the detailed operator contract and remains correct in the presence of drift or a
dangling reference.

### 3.4 Policy conditions versus caller relation filters

An authored policy condition is trusted authorization logic and is not itself
field-classified. Its relation node evaluates the authored nested predicate and
does not recursively inject another model's row policy.

Later operation planners still enforce the Bible's every-hop invariant: a
caller-authored relation filter or selected relation supplies the target model's
fresh row constraint separately. The P2 SQL renderer therefore accepts an explicit
related constraint from its caller; it never performs hidden recursive policy
lookup while walking an authored predicate.

This split prevents recursive policy construction while keeping P3's relation-hop
authorization mandatory and visible in the plan.

### 3.5 P2/P3 classification seam

P2 owns classification algorithms and typed classification inputs. P3, P4, P5,
and P6 own the operation trees whose positions must be collected.

P2 therefore exposes an internal typed request carrying:

- owning `ModelID`;
- exact `FieldID` uses;
- use kind;
- read, create, update, or delete action;
- the statement's selecting constraint; and
- the model's read constraint.

The classifier never accepts a field name. Missing or mismatched identities
refuse. Later phases must prove with collector-spy and disclosure tests that every
operation position calls this P2 seam.

## 4. Target architecture

### 4.1 Public package boundary

`go/golem` owns only the author-facing and generated ABI:

- typed field and relation handles;
- typed `Predicate[M]` builders;
- typed `Rules[M]` builders;
- exact public values (`UUID`, `Decimal`, `Date`, `Time`, canonical JSON/list);
- immutable `FrozenPredicate`/`FrozenPolicy` views with copy-isolated getters; and
- generated policy bindings.

Application code cannot construct a predicate from IDs, operator strings, raw SQL,
or arbitrary nodes. Predicate internals remain unexported. Read-only frozen views
exist solely so the internal kernel can validate and lower generated values.

### 4.2 Internal packages

The intended ownership is:

```text
go/golem                         typed authoring + opaque frozen values
go/internal/policy/ir           closed non-generic condition/rule/value IR
go/internal/policy/bind         frozen-public-value -> validated internal IR
go/internal/policy/operator     registry, validation, evaluation contracts
go/internal/policy/normalize    canonical two-valued-safe normalization
go/internal/policy/resolve      ordered row and field lenses
go/internal/policy/dependency   ordered local requirements + merged hydration tree
go/internal/policy/evaluate     immutable loaded records + one exact evaluator
go/internal/policy/imply        canonical conservative structural implication
go/internal/policy/classify     typed requests, access, dependencies, discharge
go/internal/policy/sql          safe traversal and provider renderer contract
go/internal/provider/sqlite     SQLite operator fragments and binding codecs
go/internal/provider/postgresql PostgreSQL operator fragments and binding codecs
go/internal/policy/oracle       shared agreement corpus and test harness only
```

Packages may be merged when the dependency graph remains one-directional, but
ownership may not be duplicated. In particular, providers do not implement their
own rule resolution or evaluator.

### 4.3 Internal predicate IR

The production condition tree is closed and non-generic. Every node contains only
the fields required by its kind:

- root `ModelID`;
- node kind and stable operator identity/version;
- optional `FieldID` or `RelationID`;
- canonical typed operand or operand list;
- ordered child nodes; and
- derived provider capability requirements.

Operands are not `any`. The value union records its exact logical kind and
canonical representation. Bytes are copied. Lists are ordered and copied. JSON is
canonical and decoded with exact numbers. Date/time values are normalized to the
declared precision; `time.Time` loses monotonic data and is compared by instant.
Non-finite floats are rejected before a predicate freezes.

The binder validates every ID, root/child model transition, logical type, enum
label, nullability operation, arity, and provider requirement against P1 IR.

### 4.4 Operator registry

There is one registry entry per stable operator identity. An entry owns:

- accepted field/value kinds;
- arity and empty-operand semantics;
- null truth table;
- canonical operand validation;
- Go evaluation;
- SQLite rendering capability;
- PostgreSQL rendering capability;
- parameter encoding; and
- required provider/storage capabilities.

An operator is not public until all applicable cells are implemented and exercised
by the agreement corpus. Renderers may format provider SQL differently but cannot
define operator meaning.

### 4.5 SQL compiler contract

The compiler receives a validated condition, root model, root SQL alias, logical
model registry, selected physical schema, and deterministic alias allocator. It
returns an immutable fragment and ordered argument list.

- No API accepts a caller identifier or raw SQL fragment.
- Fields resolve through `FieldID` to a physical column.
- Relations resolve through `RelationID`, local/remote field pairs, and physical
  tables.
- Relation predicates use correlated `EXISTS`; never joins.
- Every leaf and composition is forced to two-valued truth.
- `Every` searches for a related row whose child is not true.
- LIKE operands escape `%`, `_`, and `\` before binding.
- SQLite and PostgreSQL own placeholder and expression syntax independently.
- Alias and bind order are deterministic under repeated and shuffled builds.

P2 returns predicate fragments only. P3 owns complete `SELECT`, projection,
ordering, pagination, decoding, and execution.

## 5. Work waves

Each wave has one acceptance gate. A later wave may not compensate for an earlier
failed gate.

### P2-A — typed baseline

Status: **complete in `7ec8b5e`**.

Gate: generated handles compile with the narrow method sets and invalid basic
methods fail bootstrap type checking on both-provider fixtures.

### P2-B — public contract and representation foundation

Status: **implemented locally; acceptance gate passes**.

Work:

1. Implement the frozen `PUBLIC-ABI.md` rule, field, comparison-mode, and JSON
   syntax without widening any closed capability cell.
2. Add the sealed copy-isolated public frozen predicate/policy views required by
   `INTERNAL-IR.md`.
3. Add field-rule methods and generated sealed `Field[M]` identity access.
4. Change relation handles and generated constructors to carry both endpoint
   `FieldID` and `RelationID`.
5. Implement exact public value constructors, parsers, canonical encoders, and
   copy isolation.
6. Extend positive/negative code-generation fixtures for every method family and
   provider capability.
7. Stop emitting list, insensitive, and JSON policy handles until the corresponding
   agreement gate opens them; keep schema-expression capability intact.
8. Bump the generated template ABI.

Gate:

- every accepted authoring example compiles;
- every forbidden cross-model, cross-kind, empty-field, or unavailable-provider
  example fails at the earliest possible boundary; and
- construction tests prove mutation of input slices/bytes cannot mutate a frozen
  predicate.

### P2-C — validation, normalization, and canonical identity

Status: **implemented locally; acceptance gate passes**.

Work:

1. Implement a bounds-checked `physical.CanonicalDecode`, with exact re-encoding,
   validation, trailing-data rejection, and fingerprint checks.
2. Decode each `SchemaBundle` once into an immutable ID-keyed runtime registry.
3. Implement the closed internal policy IR and binder.
4. Validate shape, model ownership, relation transitions, values, operators, and
   provider capabilities.
5. Normalize constants, empty combinators, associative nesting, identities, and
   duplicate branches.
6. Preserve rule order while canonicalizing conditions.
7. Implement stable canonical encoding and fingerprinting.

Forbidden rewrites include unproved De Morgan transformations, nullable comparison
rewrites, or relation-quantifier rewrites.

Gate: the embedded physical document decodes, validates, fingerprints, and
re-encodes byte-identically; canonical policy bytes are identical under repeated
construction and permitted commutative shuffles; malformed schema/ID/value
fixtures all fail closed with stable error codes.

### P2-D — ordered rule kernel

Status: **implemented locally; acceptance gate passes**.

Work:

1. Freeze immutable ordered model rules per actor.
2. Implement newest-first applicability.
3. Implement row-chain selection and `RowConstraint`.
4. Implement field-chain selection and `FieldCondition` separately.
5. Derive the action gate only from `RowConstraint == None`.
6. Preserve first-seen field dependency order.

Gate: exhaustive rule chains of length up to three over the Phase 0 alphabet agree
row-for-row with an independent direct first-match evaluator for both row and field
lenses. Every named mutation in `02-policy-resolution.md` makes a named test fail.

### P2-E — evaluator and dependency collection

Status: **implemented locally; acceptance gate passes**.

Work:

1. Implement all registry operators in the Go evaluator.
2. Distinguish missing dependency data from a genuine null/empty relation.
3. Collect local required fields without descending through a relation.
4. Build and recursively merge relation dependency trees.
5. Refuse evaluation when required data is absent; never treat an unloaded relation
   as empty.

Gate: evaluator unit/property tests cover nulls, empty operands, quantifier vacuity,
exact values, JSON absent versus JSON null, and missing-dependency refusal.

### P2-F — implication and classification

Status: **implemented locally; acceptance gate passes**.

Work:

1. Implement canonical structural equality without `reflect.DeepEqual`.
2. Implement conservative conjunct/disjunction implication rules plus a bounded
   exact propositional fallback over canonical opaque leaves. Oversized proofs
   and unsatisfiable-selector vacuity fail closed.
3. Implement `always`, `conditional`, and `never` classification.
4. Compute semantic discharge as
   `Implies(selectingConstraint, fieldCondition)`.
5. Pin the sibling field-scoped grant regression.
6. Return deterministic requirements and merged dependencies.

Gate: the classification oracle and the positive/negative implication corpus pass;
the always-true and always-false implication mutants both fail tests; the kernel
exports no string-keyed field API.

### P2-G — SQLite and PostgreSQL rendering

Status: **shared compiler contract implemented; provider leaf renderers remain in
progress**.

Work:

1. Implement the safe provider-neutral SQL walk and deterministic alias allocator.
2. Implement SQLite fragments and parameter codecs.
3. Implement PostgreSQL fragments and parameter codecs.
4. Implement composite relation correlation and recursive relation conditions.
5. Implement exact string collation, null, list, and JSON capability behavior.
6. Return stable unsupported errors before statement execution.

Gate: renderer goldens prove descriptor-only identifiers, parameter-only values,
stable aliases/binds, correlated `EXISTS`, and two-valued fragments for every
registry entry.

The shared compiler additionally requires the exact bound model fingerprint and
an immutable runtime-probed capability proof for the active provider. The
reserved `golem_p` alias namespace prevents correlated-subquery capture. Raw
provider leaves are not hidden behind a compiler-added `COALESCE`; each renderer
must itself satisfy the measurable two-valued contract.

### P2-H — live agreement oracle

Work:

1. Build one canonical social-policy dataset containing nulls, empty/non-empty
   lists, recursive comments, composite relations, Unicode boundaries, exact
   numerics, JSON shapes, and dangling-relation probes.
2. Evaluate the same frozen conditions in memory.
3. Execute the same conditions against migrated SQLite.
4. Execute them against PostgreSQL 15+ through `GOLEM_TEST_POSTGRES_DSN`.
5. Compare selected stable row identities and separately count SQL unknown results.
6. Run provider-specific cases only when the schema explicitly declares that
   provider and capability.

Gate: all three engines select identical identities for the portable matrix; every
authorization predicate has zero SQL-unknown rows; every named operator mutation
from `01-operators.md` makes at least one named test fail.

The normal local suite may skip PostgreSQL when its DSN is absent. The P2 completion
and release gate may not: CI must provision PostgreSQL 15+ and run the live profile.

### P2-I — generated-binding and startup integration

Work:

1. Make generated policy factories build and freeze real policy values.
2. Build one fresh actor-specific policy set per explicit execution input.
3. Validate generation/schema fingerprints and provider capabilities before use.
4. Enable each previously closed generated handle only after its complete P2-H
   agreement inventory passes, with a template ABI bump when generated types change.
5. Add deterministic inspect output for operator requirements and attached policy
   inventory without executing application policy code during generation.
6. Prove no actor-specific policy result is stored in global or engine state.

Gate: two concurrent actors repeatedly build different policies without leakage;
mixed fingerprints and unsupported operators fail before provider execution; race
tests pass.

### P2-J — final audit

Work:

1. Run every P2 acceptance and named-mutation obligation.
2. Audit every P2 Bible invariant and record its test owner.
3. Remove or clearly quarantine Phase 0 production lookalikes.
4. Update examples and status documents without claiming P3 reads or P4 writes.
5. Record intentional provider extensions and remaining later-phase work.

Gate: the definition of done in section 1 is evidenced line-by-line and the full
test, vet, formatting, race, deterministic, SQLite-live, and PostgreSQL-live suites
pass.

## 6. Parallel execution map

After P2-B freezes the public and internal contracts, work can be delegated without
overlapping ownership:

```text
P2-B contract/value layer
        |
        v
P2-C internal IR + normalization
   |             |                |
   v             v                v
P2-D rules   P2-E evaluator   agreement-fixture scaffolding
   |             |
   +------v------+
          P2-F classification
                 |
          P2-G SQL interface
             /          \
     SQLite renderer   PostgreSQL renderer
             \          /
              P2-H live oracle
                    |
              P2-I integration
                    |
              P2-J final audit
```

Safe parallel ownership after the core IR freezes:

- agent 1: rule resolution and independent oracle;
- agent 2: evaluator and canonical value corpus;
- agent 3: provider agreement fixture and SQL-renderer contract;
- root: contract enforcement, integration review, and cross-track invariants.

SQLite and PostgreSQL renderers can proceed in parallel only after the shared SQL
renderer contract and operator semantics are fixed. Agents may not independently
invent operator semantics or public methods.

## 7. Required acceptance inventory

At minimum, the plan produces named suites for:

- compile-time method capability and model ownership;
- frozen-value immutability and canonical encoding;
- normalization identities and forbidden rewrites;
- direct-rule-decision versus row/field lens agreement;
- every `02-policy-resolution.md` mutation;
- implication positive, negative, sibling-grant, and time-instant cases;
- deterministic requirement and dependency ordering;
- scalar/list/JSON/relation evaluator truth tables;
- SQLite renderer goldens and live results;
- PostgreSQL renderer goldens and live results;
- portable three-engine identity agreement;
- SQL unknown-count equals zero;
- provider capability rejection;
- parameter injection and identifier-forgery refusal;
- generated-binding freshness and cross-actor isolation; and
- race and shuffle determinism.

The test corpus stores expected row identities and booleans, not only expected SQL
text or error messages. Security tests assert observable refusal and non-execution;
message assertions are separate ergonomics tests.

## 8. Explicit P2 exclusions

P2 does not implement:

- complete read statements, projections, ordering, cursors, pagination, relation
  loading, result decoding, or masking application (P3);
- mutation execution, field diffs, hooks, transactions, upsert, or outbox facts
  (P4);
- GraphQL schema or resolvers (P5);
- aggregate/group-by operation planning (P6); or
- event delivery, subscriptions, or CDC (P7).

P2 does provide the row constraints, field conditions, classifications,
dependencies, evaluator, and SQL fragments those phases must consume. Later phases
may not reimplement them.

## 9. Stop conditions

Implementation stops and the contract is amended before proceeding if:

- a public method would have different meaning on SQLite and PostgreSQL without an
  explicit provider capability;
- evaluator and either provider cannot be made to agree exactly;
- a value cannot be represented canonically without loss;
- an operation would require caller-provided SQL or string field identities;
- a normalization or implication rule cannot be proved conservative;
- relation-policy recursion appears implicitly in the renderer; or
- a live provider test is unavailable at the claimed completion gate.

The response to a stop condition is a documented contract decision and a failing
fixture, never an approximation or silent deferral.

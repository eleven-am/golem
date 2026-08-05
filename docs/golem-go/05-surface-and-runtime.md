# 05 — Surface and Runtime

> **Specification status:** detailed supporting research. The merged
> [`BIBLE.md`](./BIBLE.md) is authoritative, especially for model-attached
> authoring, explicit system capability, per-event policy freshness, durable
> outbox delivery, and the CDC boundary.

**Scope.** What a caller can ask golem for, and how one request is served safely.
Two halves: the *surface* (§1–§2, §6 in part) — the operations and input types
generated for every model — and the *runtime* (§3–§5, §7) — the lifecycle of a
single request, the batching that happens inside it, the subscriptions that
outlive it, and the errors it can answer with.

**Audience.** An implementer who has never seen golem and will never read the
TypeScript. Everything needed is here. Where the TypeScript did something
non-obvious, this document says what breaks if you do the obvious thing instead.

**Depends on.** Spec 01 (the condition tree — the data type that expresses a
`where`, which is the same type a policy condition is written in). Spec 02–04 for
the datamodel, the policy, and the statement compiler. This document names the
seams; it does not restate them.

**Fixed decisions this document assumes.**

- Conditions are data. Nodes carry their own model.
- Go, generics, Postgres-first, zero runtime reflection, code generated from Go
  structs.
- The Engine is long-lived and holds **no** per-request state. The policy is
  built **per request** by a function the Engine holds; the result lives in the
  request's `context.Context` alongside a batch-loader registry.
- A context with no scope is **refused**. It is never treated as unrestricted.
- Two-valued operators. **A filter is a read.**

---

## 1. The operation set

### 1.1 Two surfaces, one engine

Golem generates two callable surfaces over the same engine. Both are typed, both
are generated from the Go model structs, and both enforce identically.

- **Transport surface.** The GraphQL schema (or equivalent) exposed to remote
  callers. Narrower on purpose: it omits arguments a remote caller has no
  business driving.
- **Programmatic surface.** A per-scope typed client the application's own code
  calls — `golem.For(ctx).Posts.FindMany(...)`. Wider: it carries `cursor`,
  `distinct`, projections, `findFirst`, `count`, and interactive transactions.

Every enforcement rule in this document applies to both. A rule that only holds
on one surface is a hole, because the programmatic surface is reachable from any
custom resolver.

### 1.2 Names

For model `M` with a plural form `P` (English pluralisation; if the plural equals
the singular, append `List`):

| Operation | Transport field | Programmatic method |
| --- | --- | --- |
| findOne | `m` (lower-camel `M`) | `M.FindOne` |
| findMany | `p` (lower-camel `P`) | `M.FindMany` |
| findFirst | *(none)* | `M.FindFirst` |
| count | *(none)* | `M.Count` |
| aggregate | `pAggregate` | `M.Aggregate` |
| groupBy | `pGrouped` | `M.GroupBy` |
| relationGroupBy | `pRelationGrouped` | `M.RelationGroupBy` |
| create | `createM` | `M.Create` |
| update | `updateM` | `M.Update` |
| upsert | `upsertM` | `M.Upsert` |
| delete | `deleteM` | `M.Delete` |
| updateMany | `updateManyP` | `M.UpdateMany` |
| deleteMany | `deleteManyP` | `M.DeleteMany` |
| subscribe | `mEvents` | `M.Events` |

Keep these names. Downstream applications have them compiled into queries.

### 1.3 Queries

**findOne** — `where: MWhereUnique!` → `M` (nullable).
Reads exactly one row selected by a unique selector. Under a row policy the
selector is merged with the read constraint and the read becomes a
constrained first-match rather than a unique lookup; a row that exists but is
beyond the caller's reach answers exactly as a missing row does (§7.4).

**findMany** — `where: MWhere`, `orderBy: [MOrderBy!]`, `take: Int`, `skip: Int`
→ `[M!]!` (non-null list of non-null items).
The programmatic form additionally takes `cursor: MWhereUnique` and
`distinct: [MField!]`. `take` may be negative on the programmatic surface,
meaning *the last N in the given order*; the compiler refuses a negative `take`
with no order (§2.4). `maxTake` (§1.7) is compared against `|take|`.

**findFirst** *(programmatic only)* — same arguments as findMany → `M` (nullable).

**count** *(programmatic only)* — `where: MWhere` → `Int`.

**aggregate** — `where: MWhere`, `measures: MMeasures` → `MAggregate!`.
Returns `{ count, countBy, sum, avg, min, max }`; every member nullable, present
only for the measures asked for (§2.6).

**groupBy** — `where: MWhere`, `by: [MGroupField!]!`, `measures: MMeasures`,
`having: MHaving`, `orderBy: MGroupOrderBy`, `take: Int`, `skip: Int`
→ `[MGroup!]!`, each `{ key: MGroupKey!, count, countBy, sum, avg, min, max }`.
Requires at least one grouping key. `take`/`skip` must be non-negative integers
here (unlike findMany). Cardinality is bounded by `maxGroups` (§1.7): an explicit
`take` above the bound is a validation error before the statement runs; with no
explicit `take` the engine asks for `maxGroups+1` and refuses if more came back,
naming the bound and telling the caller to narrow or pass a `take`. When a `take`
is in force and no `orderBy` was given, the engine orders by the `by` fields
ascending so the page is deterministic.

**relationGroupBy** — generated only for models with configured
`relationDimensions`. Same argument shape as groupBy over a dimension enum that
mixes local dimensions with the configured relation dimensions, and a
`GroupOrderBy` that additionally accepts `key`. Constraints, all enforced before
any statement: `by` must be non-empty, must not repeat a key, every key must be a
configured dimension, and at least one key must be a *relation* dimension;
`take ≤ maxGroups`; every measure named in `measures`, `having` or `orderBy` must
be a configured measure and must be of a type the aggregate accepts (sum/avg:
numeric only; min/max: numeric, `DateTime` or `String`). Execution is two-phase —
a root grouping, then a bounded walk along the configured to-one path — and the
complete intermediate set is checked against `maxIntermediateGroups` *before*
`having`, ordering and pagination are applied. Every model reached along the path
gets its own row policy applied independently; a target that is absent or
policy-invisible contributes nothing (inner-join semantics).

### 1.4 Mutations

**create** — `data: MCreate!` → `M!`
**update** — `where: MWhereUnique!`, `data: MUpdate!` → `M!`
**upsert** — `where: MWhereUnique!`, `create: MCreate!`, `update: MUpdate!` → `M!`
**delete** — `where: MWhereUnique!` → `M!`
**updateMany** — `where: MWhere`, `data: MUpdateMany!` → `BatchPayload!`
**deleteMany** — `where: MWhere` → `BatchPayload!`

`BatchPayload` is `{ count: Int! }` and is shared by every model.

Notes that are not obvious:

- **`upsert` is not a primitive.** It probes for an existing row using the
  **update** constraint, then delegates to `update` or `create`. It therefore
  runs the *branch's* hooks, emits the *branch's* event, and produces the
  *branch's* errors. There is no `before upsert` hook (§6.2). When a policy is in
  force the whole probe-then-branch sequence must be serialised (§1.6) or two
  concurrent upserts both take the create branch and one lies about having
  created the row.
- **A write clears the request's batch caches** when it completes (§4.5). This is
  the engine's job, not the transport resolver's.
- `updateMany`/`deleteMany` disclose by *count* what a read could not disclose by
  *value*. Their `where` is classified as a read (§2.9). A caller who may update
  but may not read cannot filter them at all — every field, `id` included, is
  refused.

### 1.5 Subscriptions

Generated per model only when subscriptions are enabled for it.

`mEvents(where: MWhere): MEvent!` where

```
MEvent { type: GolemEventType!, id: <identity>!, entity: M }
GolemEventType = CREATED | UPDATED | DELETED
```

`<identity>` is the primary-key scalar for a single-key model, and a
model-specific non-null object with the key components **in declared order** for
a composite key. `entity` is nullable and is populated only when the caller
selected it. Semantics, delivery rules and teardown are §5.

Build-time requirements: the model must have a primary key; an event bus must be
configured. Either missing is a startup failure naming the model.

### 1.6 The reserved upsert guard

Policy-aware upsert serialises on a bounded striped guard table
(`_golem_upsert_guard`, 4096 stripes by default, configurable). The stripe is
derived from a hash of the model plus a canonical token of the unique selector;
no selector value is stored. The guard acquisition must be the **first**
statement on the engine-owned transaction. Its guarantee is cooperative: writers
that do not go through golem, and differently-addressed selectors, are outside
it. Absence of the table is a startup failure, not a first-upsert failure.

### 1.7 Opting out

Configuration is per model with process-wide defaults. A model may:

- **Disappear entirely** — excluded from the datamodel golem generates over. No
  types, no operations, no delegate.
- **Restrict its operations** — an allowlist over
  `{findOne, findMany, create, update, upsert, delete, updateMany, deleteMany}`.
  Anything not listed generates nothing. An unknown name is a startup failure.
- **Enable/disable subscriptions** (default off).
- **Enable/disable aggregations** (default off), or configure them:
  `dimensions` (allowlist of groupable fields), `measures` (allowlist),
  `relationDimensions`, `maxGroups`, `maxIntermediateGroups`.
- **Cap page size** — `maxTake`.
- **Classify its fields** — `hidden`, `readOnly`, `writeOnly`, `immutable`.

Field access modes and their build-time rules:

| Mode | Meaning | Refused when |
| --- | --- | --- |
| `hidden` | absent from output, from every input, from filters and from aggregates | the field is a primary-key component |
| `readOnly` | readable, never writable | — |
| `writeOnly` | writable, never readable | the field is a primary-key component, a relation, or database-read-only |
| `immutable` | writable on create, not on update | — |

At most one mode per field, with the single exception that `immutable` and
`writeOnly` may be combined. Any other pair is a startup failure naming the model,
the field and the modes. A configured field name that is not a field of the model
is a startup failure.

Process-wide defaults carry `subscriptions`, `aggregations`, `operations`,
`maxTake`, `maxGroups`, `maxIntermediateGroups`, `maxDepth` (default 5),
`checkWriteResults`, `checkReadFields`, `upsertGuardStripes`. Both result-checking
flags default to *on* whenever a policy is configured.

### 1.8 Extensions

**Computed fields** add a field to a model's output type (§6.3).
**Custom operations** add a root query or mutation. A custom operation whose name
collides with a generated one is a startup failure. Custom operations run inside
the request scope like everything else; they get no exemption from §3.

### 1.9 Startup validation, in full

Every one of these must fail at process start, naming the model and field, never
at first traffic:

- a configured field name that is not a field of the model;
- a hidden or write-only primary-key component;
- a write-only relation or write-only database-read-only field;
- conflicting access modes;
- an unknown operation name;
- a computed field whose name collides with a model field, or that requires a
  field the model does not have, or that targets a model golem does not generate;
- a custom operation whose name collides with a generated root field;
- a subscribable model with no primary key;
- subscriptions enabled with no event bus;
- an input type that would have no writable fields (adjust field access or
  disable the operation);
- a relation aggregation whose path reaches a model golem does not generate, or
  whose keys/dimensions/measures include a hidden or write-only field;
- a compound unique selector declared twice under the same name;
- result-checking enabled while the policy provider cannot answer the questions
  result-checking asks.

---

## 2. The input types

In Go these are **generated structs**, not a dynamic schema. Three rules govern
the translation and they matter more than the individual shapes:

1. **Absent must be distinguishable from zero.** `take: 0` is not `take` unset;
   `equals: ""` is not "no equals". Use pointers, or a generated
   `Opt[T]{ Set bool; Value T }`. Never a bare `int`/`string`.
2. **Order-bearing input must be a slice.** Go maps do not preserve order and
   iterate randomly. `orderBy` is a slice. So is a multi-key `AND`.
3. **Illegal states should be unrepresentable where cheap.** A to-one relation
   entry has no `Where` field at all, because a `where` on a to-one is always an
   error (§2.8). This is free correctness; take it.

### 2.1 The where input is the condition tree

`MWhere` **is** the condition tree from spec 01, specialised to `M`. This is not a
resemblance — it is the same type. A policy condition, a caller's filter, and a
nested relation filter are all values of this type, and the engine combines them
by conjunction:

```
merge(where, constraint):
    constraint absent      -> where
    where absent or null   -> constraint
    otherwise              -> AND[where, constraint]
```

Nothing else. There is no other way a policy narrows a read.

Shape:

```go
type PostWhere struct {
    AND []PostWhere
    OR  []PostWhere
    NOT []PostWhere

    ID        *StringFilter
    Title     *StringFilter
    Published *BoolFilter
    Views     *IntFilter
    CreatedAt *TimeFilter
    Status    *PostStatusEnumFilter

    Author   *UserRelationFilter   // to-one
    Comments *CommentListFilter    // to-many
}
```

`AND`, `OR`, `NOT` each take a list of the same type. Semantics are the ordinary
two-valued ones: `AND` of an empty list is true, `OR` of an empty list is false,
`NOT` is a conjunction of negations. Two-valued means there is no third outcome
for a null comparison — this is the invariant the whole policy layer rests on and
it must hold identically in the compiler and in any in-memory evaluation.

### 2.2 Scalar filters

One filter type per scalar type, shared across every model that uses it.

Every filter carries `equals`, `in`, `notIn`, `not`. Beyond that:

| Scalar | Extra operators |
| --- | --- |
| `Boolean` | none |
| enum | none |
| `Int`, `Float`, `BigInt`, `Decimal`, `DateTime` | `lt`, `lte`, `gt`, `gte` |
| `String` | `lt`, `lte`, `gt`, `gte`, `contains`, `startsWith`, `endsWith` |

`in`/`notIn` take a list of non-null values. `not` takes a bare value (negated
equality), not a nested filter.

String matching carries a `mode` where the policy language supports it; the
compiler and any in-memory evaluator must agree on case sensitivity exactly, or
the policy means one thing in SQL and another in a check.

Every operator here is exact and renderable. An operator the compiler cannot
render exactly is **refused when the policy is built**, not approximated at query
time. Approximation in this position is a privilege escalation.

### 2.3 Relation filters

A to-one relation accepts the bare nested shape (shorthand for `is`), plus `is`
and `isNot`. A to-many relation accepts `some`, `every`, `none`. Each takes a
`Where` **of the related model**.

```go
type UserRelationFilter struct {
    Is    *UserWhere
    IsNot *UserWhere
}

type CommentListFilter struct {
    Some  *CommentWhere
    Every *CommentWhere
    None  *CommentWhere
}
```

The critical rule: a field named across a relation is classified against the
**related** model. `{ author: { is: { phone: … } } }` is governed by the rule that
hides `User.phone`, not by any rule about `Post`. Field references are collected
at every depth — through `AND`/`OR`/`NOT`, through the operators on a field, and
through every relation hop — and every collected reference must be readable
(§2.9).

A filter key that names no field on the model is **refused**, not passed through.
This is golem's own check against the model metadata. It is easy to skip because
a permissive policy answers "readable" for any string it is handed, so an unknown
key appears to be safe; it then reaches the query layer and is stopped there, and
your fail-closed behaviour is borrowed rather than owned. Own it.

### 2.4 orderBy, take, skip, cursor, distinct

**orderBy** is a slice of single-direction entries. Direction is `asc | desc`.

```go
type PostOrderBy struct {
    Field     PostSortableField
    Direction SortOrder
    Nulls     NullsOrder   // if the provider is configured for it
}
```

Generating one struct with an optional direction per field and passing a slice of
those also works and matches the wire format more closely; either is acceptable,
but the *slice* is not optional. A map here silently randomises the sort.

There is also a relevance ordering for full-text providers, which names the
fields it searches. It must name them explicitly — an empty or non-string field
list is a validation error — and every named field is a field reference subject
to §2.9. This position is easy to step over in a collector; stepping over it
leaves a hole exactly the size of a hidden column.

**take** — signed. Positive is the first N; negative is the last N relative to the
given order. A negative `take` with no order the query defines is a validation
error, because "last N of nothing" is not a thing. `|take|` is compared against
`maxTake`. On groupBy and relationGroupBy, `take` must be a non-negative integer.

**skip** — non-negative integer, always.

**cursor** — a `MWhereUnique` value. Positions the page at that row.
Every field it names is a field reference subject to §2.9. A compound cursor is
accepted in its nested selector shape; a name that is no field and no selector on
the model is a validation error.

**distinct** — a list of field names. Every one is a field reference subject to
§2.9. It is a weaker disclosure than the others — no operator compares a hidden
value against anything the caller supplies, so there is no character-by-character
recovery — but returning one row per distinct value hands back the partition that
a hidden field induces over rows the caller narrowed by fields it *can* read.
Narrow to two rows, count one back, and you have learned their hidden values are
equal. Refuse it in the same place and by the same rule.

### 2.5 Unique selectors

`MWhereUnique` carries:

- one optional field per visible scalar that is an id or is unique;
- one optional nested selector per multi-column primary key and per multi-column
  unique index, provided every component is visible. The selector's name is the
  index's declared name, or its component names joined with `_`. Its components
  are all non-null.

```go
type PostTagWhereUnique struct {
    PostID_TagID *PostTagPostIDTagIDCompound
}
type PostTagPostIDTagIDCompound struct { PostID string; TagID string }
```

Two selectors resolving to the same name is a startup failure.

A unique `where` may carry ordinary filter keys beside the unique field. That is
why `update`, `delete` and `upsert` classify their `where` like a read does: a
unique selector plus a filter on a hidden column, run repeatedly, is a search, and
found-versus-not-found is the oracle.

Internally the engine flattens a compound selector to its component equalities
before merging a policy constraint, so the merged condition is an ordinary
conjunction over columns and no code downstream has to understand the nested
selector shape. Keep the nested shape at the boundary, flatten immediately behind
it.

### 2.6 Aggregate inputs and outputs

**Measures.** Requesting measures is by field *enum*, not by boolean map:

```go
type PostMeasures struct {
    Count       *bool
    CountFields []PostCountField
    Sum         []PostMeasureField
    Avg         []PostMeasureField
    Min         []PostOrderableField
    Max         []PostOrderableField
}
```

`sum`/`avg` accept numeric fields (`Int`, `Float`, `BigInt`, `Decimal`).
`min`/`max` additionally accept `DateTime` and `String` — hence a *separate*
orderable enum. On a model whose aggregatable columns are all numeric the two
enums coincide and only one is generated. In Go this is two named types; make
sure the generator collapses them when they are equal, or callers see a pointless
distinction.

`count` alone counts rows. `countFields` counts non-null values per field. Both
together produce a total plus per-field counts.

**Outputs.**

```
MAggregate { count: Int, countBy: MCountValues, sum: MSumValues,
             avg: MAvgValues, min: MMinValues, max: MMaxValues }
MGroup     { key: MGroupKey!, ...the same measure members }
```

Result types are deliberately not the column types:

- `sum` over an `Int` column widens, because a sum overflows a 32-bit result even
  when no source value does. Widen it, in both the output type and the `having`
  input type.
- `avg` is floating point, except over `Decimal`, where it stays `Decimal`.
  Decimal averages must reproduce the provider's own result, scale included —
  reconstruct them from a sum and a non-null count, and do the division in the
  provider's semantics, not in a generic decimal library's default rounding.

**having** — filters over the aggregates, not over the columns:

```go
type PostHaving struct {
    Count *IntFilter
    Sum   *PostSumFilter   // field -> filter of the *widened* type
    Avg   *PostAvgFilter
    Min   *PostMinFilter
    Max   *PostMaxFilter
}
```

**group orderBy** — `count`, plus a per-measure-kind field→direction map, plus
`key` for relation grouping. Every field it names is a field reference subject to
§2.9, and a refusal here carries the *validation* code rather than the forbidden
code (§7.3).

### 2.7 Write inputs

**Create.** One field per writable field. Writable means: not database-read-only,
not `hidden`, not `readOnly`. A scalar is **required** in the create input exactly
when the column is required *and* has no default *and* is not an updated-at
column. Everything else is optional.

**Update.** Writable minus `immutable`. Every field optional.

**UpdateMany.** Writable minus `immutable`, **scalars only** — no relation
envelopes. Every field optional.

An input that would end up with no fields at all is a startup failure telling the
operator to adjust field access or disable the operation. Do not emit an empty
struct; a caller cannot express anything with it and the failure would surface as
a confusing runtime error.

**Nested writes.** A relation field in a create/update input becomes an envelope
naming the *back relation*, so the type is unambiguous when two relations point
at the same model:

| Position | Envelope members |
| --- | --- |
| create, to-many | `create[]`, `connect[]`, `connectOrCreate[]` |
| create, to-one | `create`, `connectOrCreate`, `connect` (whole envelope required if the relation is required) |
| update, to-many | `update[]`, `upsert[]`, `connectOrCreate[]`, `connect[]`, `disconnect[]`, `delete[]` |
| update, to-one required | `update`, `upsert`, `connectOrCreate`, `connect` |
| update, to-one optional | the above plus `disconnect: bool`, `delete: bool` |

`connectOrCreate` is `{ where: TargetWhereUnique!, create: TargetCreateWithout…! }`.
`update[]` entries are `{ where: TargetWhereUnique!, data: TargetUpdateWithout…! }`.
`upsert[]` entries carry `where`, `update` and `create`.
The `…Without…` variants are the ordinary create/update inputs with the back
relation removed; if removing it leaves nothing writable, that variant is not
generated and the members that need it are omitted from the envelope.

Nested writes are authorized model-by-model before anything runs: each nested
operation maps to an action (`create`/`createMany`/`connectOrCreate` → create;
`connect`/`disconnect`/`set`/`update`/`updateMany`/`upsert` → update;
`delete`/`deleteMany` → delete), the caller is authorized for that action on the
*target* model, and every `where` inside a nested operation is classified as a
read (§2.9). This last part is the sharpest one: row verification runs *after* the
statement, inside the transaction, so a matching row the caller may not write
becomes a rollback rather than a change — which tells the caller their filter
matched and costs them nothing. That is a free oracle. Classify the nested `where`
before the statement.

### 2.8 Projections and the relation entry

A read carries a projection: which scalars to return and which relations to
traverse. On the transport surface it is derived from the caller's selection set.
On the programmatic surface it is `select` / `include` / `omit`, generated per
model. `select` and `omit` together is a validation error, at the root and at
every relation entry.

A **relation entry** inside a projection is where the nested-filter oracle lives.
It carries its own arguments:

```go
type PostsRelationEntry struct {      // Post is a to-MANY relation of the parent
    Where    *PostWhere
    OrderBy  []PostOrderBy
    Take     *int
    Skip     *int
    Cursor   *PostWhereUnique
    Distinct []PostField
    Select   *PostSelect
    Include  *PostInclude
    Omit     *PostOmit
}

type AuthorRelationEntry struct {     // User is a to-ONE relation of the parent
    Select  *UserSelect
    Include *UserInclude
    Omit    *UserOmit
}
```

The rules:

1. **A to-one relation entry has no `Where`, `OrderBy`, `Take`, `Skip`, `Cursor`
   or `Distinct`.** Generate the field away. Narrowing a to-one by a `where` is
   rejected by the storage layer rather than filtering, so accepting it either
   errors at runtime or — worse, if you "helpfully" apply it in memory — produces
   a different answer than the same condition would produce elsewhere.
2. **Every filter position in a relation entry is classified against the model
   that OWNS the field**, at every depth. `select: { posts: { where: { secretNote: … } } }`
   is refused by the rule that hides `Post.secretNote` — not by any rule about the
   model being queried. The same applies to the entry's `orderBy`, `cursor` and
   `distinct`, and to the `where` of a relation counted under `_count`.
3. **The row policy for the target model is merged into the entry.** For a
   to-many relation it is ANDed into the entry's `where`. For a to-one relation
   there is no `where` to merge into, so the constraint's own columns are
   hydrated into the projection and the row is checked after it is fetched; the
   hydrated columns are stripped from the result before it is returned. If the
   policy provider cannot answer an instance check, traversing that relation is
   **forbidden** — it is not silently allowed.
4. **A projection is prepared for writes too.** The tree a `create`, `update` or
   `delete` returns is walked and classified exactly like a read's, before the
   write runs.
5. **Depth is bounded** by `maxDepth`, counting each relation hop, `_count`
   entries included. Exceeding it is a validation error naming the depth and the
   bound.
6. **Where the statement is compiled**, the nested `where` is rendered against the
   column itself rather than against the masked projection, and the
   classification happens before anything is compiled. A mask applied to a column
   the read is `distinct` on is handed back to the in-memory path instead of
   rendered, because the database deduplicates on the value and masking it in SQL
   first would drop rows the un-masked read keeps.

**Relation counts.** `_count` on a model with visible to-many relations selects
per-relation counts. Only to-many relations may be counted. A count entry accepts
`where` and nothing else. The read policy for the counted model is merged into
that `where`. A count of a relation of a relation is not compiled — it falls back.

### 2.9 One classification rule, everywhere

> **A filter is a read.** Every field referenced by a `where`, an `orderBy`, a
> `cursor`, a `distinct`, a `having`, an aggregate `orderBy`, a measure, or a
> grouping key must be readable by the caller — at the root, inside a relation
> entry in a projection, inside a `_count` entry, and inside the filter a batch or
> nested write selects rows with.

A field the caller may **never** read is refused. A field readable only
**conditionally** is refused *unless* the query constraint already discharges the
condition — which is the case whenever the row policy alone decides readability.
A field the caller may **always** read is unaffected everywhere.

Discharge is decided against the constraint that **selects the rows**, not against
the read constraint. If a caller is selecting rows for an update, the question is
whether the update constraint implies the read constraint; if it does not, the
update could reach a row the caller cannot read and the filter would disclose it.

Why this rule exists, in one sentence: masking the projection was never enough,
because a caller who sees `phone: null` on every row can write
`where: { phone: { startsWith: "+44" } }` and read the value out of which rows come
back, one character at a time.

---

## 3. The request lifecycle

### 3.1 The shape

```go
type Engine struct {
    models    ModelIndex             // immutable, built once
    config    Config                 // immutable
    policyFor func(context.Context, Principal) (Policy, error)   // a FUNCTION
    db        *sql.DB
    hooks     HookIndex              // immutable
    hub       *SubscriptionHubs      // per-model event fan-out, no request state
}
```

There is no `policy` field. There is no `loaders` field. There is no `ctx` field.
There is no `principal` field.

```go
// Begin establishes a request scope. It is the ONLY way to get one.
func (e *Engine) Begin(ctx context.Context, p Principal) (context.Context, error)
```

`Begin` builds the policy **once** for this request by calling `policyFor`,
creates a fresh loader registry, and returns a derived context carrying both
under an unexported key. It returns an error if the policy cannot be built; the
request fails, it does not proceed unrestricted.

```go
type scopeKey struct{}   // unexported: nothing outside the package can forge one

type scope struct {
    principal Principal
    policy    Policy          // built once, memoized per (action, model[, field])
    loaders   *registry       // per-request, per-execution
    execution executionID     // §4.2
    id        requestID       // identity of THIS request
}

func scopeFrom(ctx context.Context) (*scope, error) {
    s, ok := ctx.Value(scopeKey{}).(*scope)
    if !ok || s == nil {
        return nil, fmt.Errorf("%w: golem was called with a context that carries no request scope", ErrNoScope)
    }
    return s, nil
}
```

**Every** engine entry point begins with `scopeFrom(ctx)` and returns its error.
No entry point has a variant that skips it. This includes the programmatic
client, custom operations, batched computed field resolvers, subscription
evaluation, and the raw scoped-query escape hatch.

### 3.2 The steps

1. **Request arrives.** Transport-level concerns only.
2. **The application establishes caller identity.** Golem does not authenticate.
   It receives a `Principal` — an opaque value the policy function understands.
3. **The engine begins a scope.** `ctx, err := engine.Begin(ctx, principal)`.
   The policy is built here, once. The loader registry is created here, empty.
   The scope's lifetime is the derived context's lifetime.
4. **Handlers and resolvers run** with that context. Every golem call reads the
   scope out of it. Policy decisions are memoized inside the scope, keyed by
   `(action, model)`, `(action, model, field)` and `(action, model, sorted field
   list)` — so one request asks the policy provider once per distinct question,
   and a *different* request asks again.
5. **The scope dies with the request.** The context is cancelled; the registry
   becomes unreachable; the memoized decisions go with it. A goroutine that
   outlives the request and still holds the context finds it cancelled, and every
   loader operation checks `ctx.Err()` before doing work.

### 3.3 The failure this prevents

In the TypeScript implementation the request context was a plain object handed to
golem by the GraphQL server. The server was commonly configured with a **static**
context object:

```js
GraphQLModule.forRoot({ context: { pubSub } })     // an object, not a function
```

The GraphQL integration attached the first request's `req` to that object and
handed **the same object** to every request afterwards. The consequences,
compounding:

- `ctx.req` kept the first caller forever, so **every later caller executed with
  the first caller's identity**;
- the memoized policy decisions, keyed by the context object, were shared across
  every caller;
- the per-request batch caches, also keyed by the context object, were shared
  across every caller and never cleared.

The symptom is the worst kind: request #1 is correct. A smoke test passes. The
deploy is green. Request #2 is served as request #1's user.

0.6.0 made it fail loudly rather than run: a middleware establishes a per-request
boundary in async-local storage; the first boundary that sees a given context
object is recorded against it; a context that shows up under a *different*
boundary throws, naming the misconfiguration and the fix. A context that is
deliberately shared opts out with an explicit marker.

That is a detector bolted onto a design that permitted the bug. Go should not
need one.

### 3.4 The Go hazards, and how they are forbidden structurally

| Hazard | Why it is the same bug | Structural refusal |
| --- | --- | --- |
| A package-level loader or registry (`var loaders = newRegistry()`) | one cache for the whole process; caller A's rows served to caller B | the registry type is unexported and has no package-level instance; the only constructor is called from `Begin`; a `gochecknoglobals` lint runs over the runtime package with no exceptions |
| A policy cached on the Engine (`e.policy`, or a `sync.Once` around `policyFor`) | every caller after the first runs as the first | `Engine` has no field of the policy type; a compile-time assertion plus a structure test (§8) enforces it |
| Storing the scope, the ctx, or the principal in a long-lived struct | the value outlives the request that produced it | `scope` is unexported and only reachable through `scopeFrom`; nothing exported returns it |
| A `Policy` value passed as a function argument through the call graph | it can be captured by anything | the policy travels **only** in the context; no exported function takes a `Policy` parameter |
| A background goroutine using the request context after the handler returns | it holds the scope alive and may act with a stale identity | every loader and every statement checks `ctx.Err()` first; the scope's registry refuses work after close with `ErrScopeClosed` |
| A "default" or "system" scope for internal callers | it is an unrestricted scope with a friendly name | there is no such constructor; internal callers call `Begin` with an explicit system principal, which the policy function must explicitly grant |

Two more rules that are cheap and close the remaining gap:

- **`Engine` and `scope` must not be copied.** Embed `noCopy` and vet catches it.
- **`Begin` must be idempotent-safe but not nestable.** Calling `Begin` on a
  context that already carries a scope with a *different* principal is a
  programming error and must return an error, not silently shadow. (Deriving a
  new *execution* within the same scope is a different operation — §4.2 — and is
  allowed.)

---

## 4. Batching

### 4.1 What a loader is

A loader is a long-lived, immutable **descriptor**: a name, a batch function, an
optional key-normalisation function, a maximum batch size. It is created once, at
init, and registered.

```go
type LoaderSpec[K comparable, V any, A any] struct {
    Name         string
    Load         func(ctx context.Context, keys []K, args A) ([]V, error)  // or map form
    CacheKey     func(K) any
    MaxBatchSize int
}

func NewLoader[K comparable, V any, A any](spec LoaderSpec[K, V, A]) *Loader[K, V, A]
func (l *Loader[K, V, A]) Load(ctx context.Context, key K, args A) (V, bool, error)
```

**The `Loader` value holds no results.** Its caches live in the registry inside the
request scope, found by the loader's own identity. If you put a `map[K]V` on the
`Loader` struct you have written the package-level-cache hazard with extra steps.

### 4.2 The key hierarchy

A cached value is found by, in order:

```
request scope  ->  execution  ->  loader identity  ->  argument token  ->  key
```

**Request scope.** From the context. No scope, no loader — `Load` returns
`ErrNoScope`. This is the same refusal as everywhere else.

**Execution.** A request may contain several independent executions. In a plain
HTTP or GraphQL request there is exactly one, and the scope's initial execution
serves it. A **subscription** is the case that makes this key mandatory:

> A subscription's context lives for the whole connection. Without a per-event
> execution key, event 500 is served event 1's cached value.

The subscriber's context is created once, at subscribe time, and survives until
the client disconnects — minutes, hours. Every event after the first is a fresh
question about a row that has, by definition, just changed. Serving it from the
cache the first event populated means the subscription reports the *old* value,
forever, and looks like it is working.

```go
// WithExecution derives a child scope sharing the request's policy and principal
// but with a fresh execution — and therefore fresh loader caches.
func WithExecution(ctx context.Context) (context.Context, error)
```

Each subscription event derives one. So does any place the runtime decides two
pieces of work must not share a cache.

Make this an **explicit token the runtime owns**, not a repurposed field of some
executor's data structure. The TypeScript keyed on the identity of the GraphQL
`rootValue` object, which happened to be per-event for subscriptions and happened
to be absent for ordinary requests. It worked, and it worked for a reason nobody
writing a resolver could see. An explicit `executionID` on the scope costs one
field and removes the entire class of "it broke when we changed the executor".

**Loader identity.** The pointer/handle of the `Loader` descriptor.

**Argument token.** §4.3.

**Key.** The caller's key, normalised through `CacheKey` if one is given.

### 4.3 The argument-key rule

Arguments are part of the key because two callers asking the same key with
different arguments are asking different questions. The token must be
**injective**: distinct argument values must produce distinct tokens.

Serialisation is a canonical, **type-tagged**, key-order-independent encoding:

- `nil`, `bool`, string, signed/unsigned integers, big integers;
- floats, with `NaN`, `+Inf`, `-Inf` and `-0` each distinguished from every other
  value and from each other;
- `time.Time` — encoded in a fixed representation; strip the monotonic reading and
  normalise the location, or two clocks-equal times token differently;
- `[]byte` — base64;
- decimals — their exact string form;
- slices — element order preserved, with a distinct marker for a nil slice versus
  an empty one;
- maps with string keys and plain structs — keys sorted, so key order does not
  change the token.

Type tags are not optional. Without them the string `"1"` and the number `1`
collide, and so do the slice `[1]` and the value `1`.

**Anything that cannot be serialised injectively must fail loudly**, naming the
loader and the path within the argument:

```
batch loader Post.readingTime cannot tell one batch from another by argument
opts.formatter: a func value cannot be serialised exactly; batch arguments must
be built from canonical scalar values, times, bytes, decimals, slices and plain
structs
```

Refuse: functions, channels, unsafe pointers, cyclic structures, interface values
whose dynamic type is not in the list, and types carrying unexported state golem
cannot see. Detect the last one at **generation/registration** time where the type
is statically known, so the failure lands in a build and not in production.

The two wrong ways out, both of which look reasonable:

- **`fmt.Sprintf("%v", args)`** — not injective. `%v` of `struct{A,B string}{"x","yz"}`
  and `{"xy","z"}` are both `{x yz}` versus `{xy z}`… and for maps, slices of
  interfaces, and pointer-containing structs it collapses distinct values freely.
  A collision here serves **one caller's batch to another caller**. That is the
  bug this whole document exists to prevent, arriving through the cache key.
- **The pointer address (`%p`)** — every call is a fresh batch, so batching stops
  working silently; and worse, Go reuses freed addresses, so two *different*
  argument values can share an address at different times and collide anyway.

Injective or error. There is no third option.

### 4.4 The batch function contract

- It is given a non-empty slice of distinct keys and must answer for **exactly**
  those keys — either a slice aligned by index and of equal length, or a map keyed
  the same way the keys identify (`CacheKey` applied).
- A wrong-length slice is an error naming both counts.
- A map containing a key that was not asked for is an error naming the count and
  the first offender: the keys it answers with must identify the same way as the
  keys it is given.
- A key with no answer yields the zero value and `found == false`. It is not an
  error.
- A nil/zero key short-circuits to `not found` without reaching the loader at all.
- `MaxBatchSize` defaults to the compiler's batch chunk size; a batch larger than
  it is split.

**Scheduling.** Go has no microtask queue, so a batch window does not appear for
free the way it does in JavaScript. The runtime must own the flush signal
explicitly — the executor collects `Load` calls while it resolves one level of the
result tree, then flushes. Do **not** use a timer: it makes latency
non-deterministic and makes tests flaky. Whatever you choose, choose it once,
centrally, and note that every correctness rule in this section (keying,
clearing, injectivity) is independent of it.

### 4.5 Clearing: all, or nothing

**A write through golem clears the current execution's caches.** A read after a
write in the same request must observe the write. This is the engine's
responsibility — it happens when the write completes, inside the engine, for
every write on every surface. If clearing lives in the transport resolver, a
write issued from application code through the programmatic client leaves stale
values behind and the next read in that same request serves them. (This is
precisely the gap in the TypeScript: clearing is done by a wrapper around the
generated GraphQL write resolvers, and a write through the context-bound client
does not clear.)

**Per-key invalidation must not be offered.** Not as an option, not as an
optimisation, not behind a flag.

Golem cannot know which keys a write invalidated:

- A **create** produces a row that no key mentions yet, and it can change the
  answer for *any* key. `postsByAuthor(a)` gets longer. `postCountByOrg(o)` gets
  bigger. `topPostThisWeek()` may become a different row entirely. There is no key
  derivable from the created row that covers those.
- An **update** can move a row *between* keys — change `authorId` and two keys are
  now wrong, only one of which appears anywhere in the statement.
- A **delete** removes a row from every aggregate over it.
- A loader may be keyed by something with no relationship to any column at all —
  a tenant, a page number, a search term.

So per-key clearing **under-invalidates**, and it does so silently: the request
returns a plausible answer that is simply out of date, and there is no error to
notice. It reintroduces exactly the read-after-write bug that clearing exists to
prevent, in the subset of cases where a key was missed — which is the subset
nobody tests.

Structurally: the registry exposes `ClearAll()` and nothing else. There is no
exported `Clear(key)`, no exported `Prime(key, value)` (which is per-key
invalidation wearing a hat), and no exported accessor that reaches an individual
loader's cache map.

If clearing everything after every write is too expensive for a specific
workload, the answer is fewer writes per request or a narrower loader, not a
narrower invalidation.

---

## 5. Subscriptions

### 5.1 The Go shape

```go
func (s *subscription[T]) run(ctx context.Context, events <-chan Event, out chan<- T) error {
    defer close(out)
    defer s.hub.remove(s)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        case ev, ok := <-events:
            if !ok {
                return ErrEventSourceClosed
            }

            evalCtx, err := WithExecution(ctx)          // fresh caches, fresh policy
            if err != nil {
                return err
            }
            value, deliver, err := s.evaluate(evalCtx, ev)   // cancellable
            if err != nil {
                return err
            }
            if !deliver {
                s.observer.suppressed(ev, s.reason)
                continue
            }

            select {
            case out <- value:
            case <-ctx.Done():
                return ctx.Err()
            default:
                return ErrSubscriptionOverflow            // never drop silently
            }
        }
    }
}
```

Four things in that loop are load-bearing: `ctx.Done()` in the *same* select as
the event channel; the evaluation taking a cancellable context; the send being
itself guarded by `ctx.Done()`; and the deferred deregistration running before
the channel close.

### 5.2 The defect this closes by construction

The TypeScript subscription was an **async generator**. An async generator parked
at an `await` cannot honour `return()` — the return is queued behind the in-flight
await and only runs when it settles.

A subscription that *filtered* its events did exactly that: on every event it ran
a database query to decide whether the row still matched the subscriber's `where`
and was still readable by them. So when a client disconnected mid-evaluation, the
generator did not unwind. It finished the query, pulled the next event, ran
another query, and kept going — **querying the database on behalf of a client
that had left**, and holding its context (and therefore its policy, its identity
and its caches) alive for as long as events kept arriving.

Two failures at once: a resource leak that grows with churn, and a live reference
to a departed caller's authorization state.

Go closes it by construction. `select` on `ctx.Done()` is evaluated on every
iteration, and the evaluation itself takes the context and is cancelled with it.
There is no parked-await state that outlives a cancellation, provided the
evaluation actually threads the context down to the query. Thread it.

### 5.3 Per-event context derivation

Each event gets a derived context that is:

- **Fresh in policy.** Authorization is re-evaluated for this event against the
  subscriber's identity as it is *now*, not reused from subscribe time. A
  long-lived connection must not keep delivering rows a revoked caller may no
  longer read. The policy provider exposes a "fresh view" hook for exactly this;
  where it does, the per-event context uses it.
- **Fresh in execution.** A new execution ID, so batch caches from event *n* never
  answer event *n+1* (§4.2).
- **Derived from the subscriber's context**, so cancelling the connection cancels
  the event's work.

### 5.4 Delivery rules

At subscribe time, before the first event: authorize `read` on the model. A caller
who may not read the model is refused immediately, not at the first event.

Per event:

- **DELETED.** A deleted row cannot be re-queried, so it cannot be filtered or
  authorized by re-reading. Therefore: a subscription that carries a `where`
  suppresses deletions entirely (reason `deletion-filter`). Under a policy, a
  deletion is delivered only if a **pre-delete snapshot** is available *and* an
  instance check against it passes; otherwise it is suppressed
  (`deletion-unverifiable` / `authorization`). A delivered deletion carries
  `entity: null` always — the snapshot is used to authorize, never exposed.
- **CREATED / UPDATED.** Re-read the row by its identity conjoined with the
  subscriber's `where`, under the fresh policy, selecting exactly the entity
  projection the subscriber asked for (or just the primary key if they asked for
  none). Absent → suppress, with reason `filter` if a `where` was in play and
  `authorization` if a policy was. Present → deliver, with `entity` populated only
  if it was requested.
- **The fast path.** No `where`, no entity projection, no policy → deliver the
  identity without reading anything.
- **A malformed identity** on the event → suppress (`invalid-identity`), do not
  crash the connection.

### 5.5 Fan-out, backpressure and teardown

- **One reference-counted hub per model per schema**, not one event-source
  iterator per subscriber. The hub opens the source when the first subscriber
  arrives and stops it when the last leaves.
- **Bounded per-subscriber queue**, default 64. A full queue **disconnects that
  subscriber** with a named error (`GOLEM_SUBSCRIPTION_OVERFLOW`). Events are
  never silently dropped. A slow consumer is a problem the consumer must be told
  about.
- **Evaluation is deduplicated only within one event**, and only across
  subscribers sharing *both* the same scope identity *and* the same evaluation key
  — the canonical token of (`where`, entity projection). **Never across
  callers.** Two subscribers with identical filters and different principals get
  two evaluations. Sharing one would deliver one caller's rows to the other,
  which is the same wrong answer §8 exists to prevent, arriving through a
  different door.
- **Teardown guarantees**, all required:
  1. When the context is done, the goroutine returns without starting another
     evaluation.
  2. It deregisters from the hub **before** returning.
  3. It closes the output channel exactly once, and it is the only closer.
  4. No in-flight evaluation may send on a closed channel — hence the guarded
     send.
  5. When the last subscriber leaves, the hub stops the shared source.
  6. Tests assert no leaked goroutines (`goleak`) after teardown.
- **Observability.** Expose: active subscriber count, events received, evaluations
  performed with latency, events delivered, events suppressed **by reason**, queue
  depth against capacity, and overflow disconnects. Observer callbacks must never
  affect correctness or availability — a panicking observer is swallowed.
- **Event payloads** must be wire-safe for big integers, decimals, times, bytes,
  composite identities, deletion snapshots and batch envelopes, and the format
  must be versioned.

---

## 6. Hooks and computed fields

### 6.1 The middleware shape

Match Storm's shape:

```go
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)
type Middleware[Req, Res any] func(next Handler[Req, Res]) Handler[Req, Res]
```

Middleware is registered per `(model, operation)` and composed in registration
order, outermost first. Work before the `next` call is a "before hook"; work after
it is an "after hook". One mechanism, not two, and strictly more expressive than
the before/after pair it replaces — a middleware can wrap the call in a
transaction, a timer, a retry, or a recover.

The operations that carry hooks:

```
findOne  findFirst  findMany  create  update  delete  updateMany  deleteMany
```

**`upsert` has no hooks of its own.** It resolves to a create or an update and
runs *that branch's* hooks. Callers find this surprising; document it at the
registration site.

**Middleware is trusted, in-process code, and is not a policy boundary.** It runs
*outside* the engine's authorization, because it wraps the engine call. A
middleware that returns without calling `next` has executed nothing, and no policy
was consulted because nothing happened. That is intentional and must be stated,
not discovered.

A middleware **may** rewrite the request before calling `next`. Widening a `where`
gains nothing: the policy constraint is conjoined *after*, inside `next`. Narrowing
works. This is the correct asymmetry and it should be tested.

### 6.2 Where hooks sit relative to authorization and the statement

For one operation, in order:

1. **Middleware, entering.** Sees and may rewrite the request.
2. **Filter classification.** Every field referenced by `where`, `orderBy`,
   `cursor`, `distinct` — at every depth, through every relation — is classified
   as a read. Unreadable → forbidden. (§2.9)
3. **Model authorization.** The caller is authorized for the action on the model.
   For a write with nested data, every nested target model is authorized for its
   own action, and every nested `where` is classified.
4. **Projection preparation.** The read tree is walked: nested filters classified,
   masks planned, instance checks planned, policy constraints merged into relation
   entries, hydration columns injected, depth checked.
5. **Row constraint.** `constrain(action, model)` for this caller, conjoined with
   the request's `where`.
6. **The statement.** Compiled SQL where possible; a documented fallback
   otherwise, with the reason emitted for observability.
7. **Result verification** (writes, when enabled). Inside the transaction, the
   before and after rows are checked against the policy. A failure rolls the whole
   thing back.
8. **Masking and stripping.** Field masks applied row by row; injected hydration
   columns removed; to-one instance checks applied.
9. **Events**, published after commit and discarded on rollback.
10. **Cache clearing** for writes (§4.5).
11. **Middleware, returning.** Sees the finished, masked result.

Step 8 before step 11 matters for computed fields — see below.

### 6.3 Computed fields

A computed field declares:

```go
type ComputedField[P any, A any, R any] struct {
    Model    string
    Name     string
    Requires []Field        // fields of the model the resolver reads
    Args     A              // optional
    Resolve  func(ctx context.Context, parent P, args A) (R, error)
}
```

`Requires` is how the read fetches what the resolver needs: when the computed
field is selected, its required columns are added to the projection. Rules:

- A required name that is not a field of the model is a **startup failure**.
- A computed field whose name collides with a model field is a **startup
  failure**.
- **Required columns are classified and masked like any other selected scalar.**
  A computed field is not a laundering route around field-level policy: if the
  caller may not read `User.salary`, then `Requires: [salary]` does not deliver it.
- **Therefore the resolver must handle a masked dependency.** Masking happens at
  step 8, before the computed field resolves at step 11, so a masked dependency
  arrives as its zero value / nil. The resolver must distinguish "the caller may
  not see this" from "the row has no value", and it must not treat nil as a bug.
  Give the generated parent type a maskable wrapper (`Null[T]` or a pointer plus a
  `Masked` bit) so the distinction is expressible rather than guessed. This is the
  single most likely place for an implementer to write a correct-looking resolver
  that leaks or panics.

### 6.4 Batched computed fields

A batched computed field resolves N parents in one call:

```go
type BatchedComputedField[P any, K comparable, A any, R any] struct {
    Model    string
    Name     string
    Requires []Field
    Key      func(parent P) (K, bool)   // or a named field of the parent
    Load     func(ctx context.Context, keys []K, args A) ([]R, error)
    CacheKey func(K) any
    MaxBatchSize int
}
```

- If `Key` names a parent **field**, that field is added to `Requires`
  automatically. Forgetting it otherwise produces a resolver that keys on a column
  the read did not fetch.
- The loader is a §4 loader: scoped, execution-keyed, argument-tokenised, cleared
  wholesale on write.
- The field's `args` participate in the argument token, so two callers asking the
  same key with different args get different batches — and args that cannot be
  tokenised injectively **fail loudly** (§4.3).

**The rebindable-parameter rule.** A batched resolver is invoked with
`(ctx, keys, args)` — a *slice* of keys, not one parent. It must therefore be a
plain function or method with exactly that signature, registered directly.

> A batched resolver must not take rebindable parameters — parameters supplied by
> a dependency-injection or parameter-decoration mechanism that can reorder or
> substitute arguments by position.

The TypeScript hit this exactly: the framework let a resolver method annotate its
parameters (`@Parent()`, `@Context()`, `@Info()`, `@Args()`) and rewrote the
argument positions accordingly. On a batched method that rewrite silently remaps
`(keys, ctx, args)` into something else — the method receives a single parent
where it expects a slice of keys — and the failure is a type confusion at runtime,
not a compile error. The fix was to detect the annotations at registration and
refuse, naming the decorators used and explaining the calling convention.

In Go, generics make the signature checkable at compile time. So:

- registration takes the typed function directly; there is **no** `any`-typed or
  variadic registration path for a batched resolver;
- batched resolvers are **excluded** from any parameter-injection wrapper the
  surrounding framework offers;
- if such a wrapper can be detected at registration, detecting one is a startup
  failure that names the field and states the calling convention.

---

## 7. Errors

### 7.1 The taxonomy as Go values

Five codes. No more, and every failure maps to one.

```go
type Code string

const (
    CodeForbidden       Code = "FORBIDDEN"
    CodeUnauthenticated Code = "UNAUTHENTICATED"
    CodeNotFound        Code = "NOT_FOUND"
    CodeConflict        Code = "CONFLICT"
    CodeValidation      Code = "BAD_USER_INPUT"
)

var (
    ErrForbidden       = &Error{Code: CodeForbidden}
    ErrUnauthenticated = &Error{Code: CodeUnauthenticated}
    ErrNotFound        = &Error{Code: CodeNotFound}
    ErrConflict        = &Error{Code: CodeConflict}
    ErrValidation      = &Error{Code: CodeValidation}
)

type Error struct {
    Code   Code
    Model  string   // set whenever known
    Field  string   // set on a field-level refusal
    Op     string   // the operation
    msg    string
    err    error    // wrapped
}

func (e *Error) Error() string  { … }
func (e *Error) Unwrap() error  { return e.err }
func (e *Error) Is(target error) bool   // matches on Code, so errors.Is(err, ErrForbidden) works
```

Callers match with `errors.Is` on the sentinel and read `Code` for transport
mapping. The transport codes are exactly the strings above; downstream
applications match on them today.

### 7.2 Which situation produces which code

**FORBIDDEN**
- the policy denies the action on the model;
- a `where`, `orderBy`, `cursor` or `distinct` references a field the caller may
  not read (root, relation entry, `_count` entry, or a nested write's filter);
- a projection selects a field the caller may never read;
- a relation traversal whose to-one constraint cannot be hydrated safely, or whose
  constraint requires an instance check the provider cannot perform;
- a computed field whose authorization dependencies cannot be hydrated exactly;
- **a call with no request scope** (§7.5).

**UNAUTHENTICATED**
- no caller identity is present where the policy requires one.

**NOT_FOUND**
- a unique target does not exist, **or** exists beyond the caller's reach — the
  two are indistinguishable by construction (§7.4).

**CONFLICT**
- a unique constraint violation;
- an upsert-guard snapshot conflict (retry only by repeating the whole
  transaction, and only where that is safe).

**BAD_USER_INPUT (validation)**
- a negative or non-integer `skip`; a non-integer `take`; a negative `take` with no
  order;
- `take` above `maxTake`, or above `maxGroups`, or a groupBy that matched more
  groups than the bound;
- `groupBy` with no keys; a repeated key; an unconfigured dimension or measure; an
  aggregate applied to a type it does not accept; an unsupported `having` operator;
- a filter/order/cursor/distinct key that names no field on the model;
- `select` and `omit` together, at the root or at any relation entry;
- depth above `maxDepth`;
- a relation-constraint violation reported by the database;
- **a refusal over a measure, a grouping key, a `having`, or an aggregate
  `orderBy`** — these keep the validation code even though the reason is
  readability. Refusals over `where`/`orderBy`/`cursor`/`distinct` are FORBIDDEN.
  Preserve the split; applications distinguish them.

### 7.3 What a refusal must say

A refusal names the **field** and the **model**, and when readability is
conditional it names **what readability depends on**:

```
Cannot filter or order by field "phone" on User: readability depends on id,
which the query constraint does not discharge
```

```
Cannot read field "phone" on User
```

```
Cannot group or aggregate field "salary" on Employee
```

The verb varies by position — "filter or order by", "read", "aggregate", "group or
aggregate" — and it is the verb that tells the operator *where* to look. Keep them.

**Never include:** the SQL, a file path, a line number, a stack frame, the value
being filtered on, or the driver's raw message. Map the driver's SQLSTATE to a
code and emit a fixed message naming the model only. (The TypeScript had to
sanitise its ORM's exception text by stripping anything containing a path
separator — that is a symptom of surfacing the wrong thing in the first place.)

### 7.4 Indistinguishable outcomes

These pairs must be indistinguishable to an unauthorised caller — in code, in
message, and in observable work done:

1. **findOne / update / delete against a row beyond reach ≡ against a missing
   row.** Both answer `NOT_FOUND` with the message `<Model> not found`. Both do
   exactly one constrained probe, so the latency does not separate them.

2. **Upsert against a row beyond reach ≡ upsert against a missing row.** This is
   the sharpest case and the easiest to get wrong.
   The probe for the existing row uses the **update** constraint. If the caller
   may not update the model at all, the probe is **skipped** and treated as "not
   found" — the engine does not run a read to discover whether the row exists,
   because the answer would leak. Either way the upsert then takes the **create**
   branch and answers exactly as it would for a genuinely absent row: a successful
   create if creation is permitted, a `FORBIDDEN` on create if it is not, a
   `CONFLICT` if the create then collides with the very row the caller could not
   see. Nothing in any of those outcomes reveals whether the row existed.
   The guard (§1.6) is acquired **before** the probe, so the branch decision is
   serialised and two concurrent upserts cannot both take the create branch.

3. **A masked field ≡ a null field.** A denied field returns null. It is not
   omitted from the response, it does not error, and it does not null its parent,
   its list, its relation, its alias, or a subscription payload. This forces a
   representation choice: with field checks enabled, **every visible scalar and
   enum output is nullable regardless of database requiredness**. Generate them as
   pointers or `Null[T]`. Identities stay non-null. Input requiredness is
   unchanged. Downstream code that assumed a selected scalar could never be null
   must be updated — this is a known, documented break.

4. **A batch write's count ≡ nothing.** `updateMany`/`deleteMany` disclose by
   count, so their `where` is classified as a read; a caller granted `update` but
   not `read` cannot filter at all. Do not soften this by allowing `id`: an `id`
   filter plus a count is a membership oracle.

5. **A nested write's filter ≡ nothing.** Row verification runs after the
   statement, inside the transaction, so a matching row the caller may not write
   becomes a rollback — which is a *distinguishable* outcome from a filter that
   matched nothing, and costs the attacker nothing. Classify the nested filter
   before the statement so both cases refuse identically.

### 7.5 The no-scope error

A call with no request scope is a **server** fault, not a caller fault. It must:

- fail closed, mapping to `FORBIDDEN` at the boundary;
- be distinguishable internally by a dedicated sentinel wrapping the forbidden
  error, so it can be alerted on;
- touch the database **zero** times;
- be logged at error level with the operation and model, because it means a code
  path exists that does not go through `Begin`.

```go
var ErrNoScope = &Error{Code: CodeForbidden, msg: "no request scope in context"}
```

It is never, under any configuration, treated as an unrestricted call.

---

## 8. Acceptance criteria

Each criterion is stated as a **mutation**: a specific wrong change an implementer
might plausibly make. The suite is adequate only if introducing the mutation makes
a *named* test fail. Verify this by actually applying each mutation and watching
the test go red — a criterion whose test still passes under the mutation is not a
criterion.

All tests run under `-race`. All tests that create goroutines assert none leak.

### M1 — `CachePolicyOnEngine`

*The mutation:* memoize the built policy on the `Engine` (a field, a `sync.Once`,
or a map keyed by anything longer-lived than a request), so it is built once per
process rather than once per request.

*Must fail:*

- **`TestCrossCaller_ConcurrentSameKeysDifferentAnswers`** (§8.6) — the required
  wrong-answer test.
- **`TestCrossCaller_SequentialDifferentPrincipals`** — request 1 as Alice, then
  request 2 as Bob on the same Engine; Bob must not see Alice's row. Catches the
  mutation without concurrency, so it fails deterministically.
- **`TestEngineHoldsNoPolicy`** — a structure test that walks the `Engine` type
  (reflection is fine in a *test*; the runtime rule is about the runtime) and
  fails if any field, transitively, is of a type implementing `Policy`, is a
  `sync.Once`, or is a map/pointer that could hold one. It names the offending
  field path.

### M2 — `DropExecutionKeyFromLoader`

*The mutation:* key loader caches by request scope and arguments only, dropping
the execution level.

*Must fail:*

- **`TestSubscription_SecondEventSeesNewValue`** — one connection; a subscription
  whose entity projection includes a **batched** computed field. Publish event 1 →
  assert value `A`. Mutate the underlying data. Publish event 2 → assert value `B`.
  Under the mutation, event 2 returns `A`. Extend to event 500 with a loop to make
  the "forever" character of the bug explicit.
- **`TestRequest_TwoExecutionsDoNotShareCaches`** — one request containing two
  independent executions with a write between them; the second must observe the
  write.
- **`TestExecutionKeyIsExplicit`** — asserts `WithExecution` produces a distinct
  execution ID and that the ID is a first-class field of the scope, not derived
  from any executor-owned value. (Guards against re-introducing the
  incidental-identity keying.)

### M3 — `MakeLoaderInvalidationPerKey`

*The mutation:* replace the post-write `ClearAll` with a per-key `Clear(k)` for
keys the write "obviously" touched, and export `Clear`.

*Must fail:*

- **`TestReadAfterCreate_UnmentionedKey`** — the decisive one. A loader
  `postsByAuthor(authorID)`. In one request: read author A's posts (2 rows,
  cached) → create a post for author A → read author A's posts again. Must be 3.
  Under the mutation, whichever key the implementation derives from the created
  row (its own `id`, most likely) is cleared and `postsByAuthor(A)` still answers
  2.
- **`TestReadAfterCreate_AggregateKey`** — a loader keyed by something with no
  column to derive from at all: `postCountForOrg(orgID)`, or
  `topPostThisWeek()` with no key. A create changes the answer and no per-key
  scheme can know.
- **`TestReadAfterUpdate_KeyMigration`** — update a post's `authorId` from A to B;
  both `postsByAuthor(A)` and `postsByAuthor(B)` must be correct afterwards.
- **`TestRegistryExposesNoPerKeyClear`** — a structure test asserting the registry
  and loader types export no `Clear`, no `Prime`, and no accessor returning a
  mutable cache.
- **`TestWriteThroughProgrammaticClientClears`** — the write is issued through the
  programmatic client, not a transport resolver. Under an implementation that
  clears in the resolver wrapper, this fails. (This is a real gap in the
  TypeScript; it must not be ported.)

### M4 — `TreatContextWithNoScopeAsUnrestricted`

*The mutation:* when `scopeFrom` finds no scope, proceed with no policy.

*Must fail:*

- **`TestNoScope_EveryEntryPointRefuses`** — a table test over **every** exported
  entry point (`FindOne`, `FindMany`, `FindFirst`, `Count`, `Aggregate`,
  `GroupBy`, `RelationGroupBy`, `Create`, `Update`, `Upsert`, `Delete`,
  `UpdateMany`, `DeleteMany`, `Events`, the scoped/raw escape hatch, and a
  batched computed field's `Load`) called with `context.Background()`. Each must
  return an error satisfying `errors.Is(err, ErrNoScope)` and
  `errors.Is(err, ErrForbidden)`.
- **`TestNoScope_TouchesNoDatabase`** — the same table with a spy driver that
  fails the test if it receives *any* statement. Zero, not "only reads".
- **`TestEntryPointCoverage`** — enumerates the exported methods of the
  programmatic client and fails if any is absent from the table above. Without
  this, a new operation added later silently escapes the check. This test is what
  makes M4 hold over time.

### M5 — `LetSubscriptionIgnoreCtxDone`

*The mutation:* drop `ctx.Done()` from the subscription's select (or stop passing
the context into the evaluation, which is the same mutation wearing a disguise).

*Must fail:*

- **`TestSubscription_CancelDuringEvaluationUnwinds`** — a subscription whose
  evaluation blocks on a channel the test controls. Cancel the client context
  while an evaluation is in flight, then release it. Assert, in order: the
  goroutine returns within a bounded deadline; no *further* evaluation is started
  (an evaluation counter must not advance after the cancel); the hub reports zero
  subscribers; the output channel is closed exactly once.
- **`TestSubscription_NoLeakAfterDisconnect`** — `goleak` at test end, with a
  source that keeps producing events after the client leaves. Under the mutation
  the reader goroutine keeps consuming and keeps querying.
- **`TestSubscription_NoQueryAfterDisconnect`** — the spy driver must record zero
  statements after the cancel completes.
- **`TestSubscription_HubStopsSourceOnLastLeave`** — the shared source's stop is
  observed.

### 8.6 The cross-caller test, specified

This is the test that carries M1, and it is easy to write in a form that proves
nothing. It must construct a case where sharing gives the **wrong** answer, not
merely a different one — two concurrent requests, **different callers**, the
**same keys**, and **different correct results**.

*Fixture.*

```
model Document { id, orgID, ownerID, title }
rows: d1 { org: "org1", owner: "alice" }
      d2 { org: "org1", owner: "bob"   }

policy: can read Document where ownerID == principal.ID
```

*The keys are identical.* Both requests call the same loader with the same key —
`documentsByOrg("org1")` — and both call `Load(docByID, "d1")` and
`Load(docByID, "d2")`.

*The correct answers differ.*

| | Alice | Bob |
| --- | --- | --- |
| `documentsByOrg("org1")` | `[d1]` | `[d2]` |
| `docByID("d1")` | `d1` | not found |
| `docByID("d2")` | not found | `d2` |

*Procedure.*

1. Start both requests concurrently, each with its own `Begin`.
2. Force a deterministic interleaving with a test hook — block request A inside
   its loader dispatch until request B has enqueued its keys, then release. **Do
   not use `time.Sleep`.** A sleep-based interleaving makes the test flaky in the
   direction of passing.
3. Assert Alice's response contains exactly `d1` and **never** `d2`; Bob's
   contains exactly `d2` and **never** `d1`.
4. Run the whole thing again with the roles reversed, so the test cannot pass by
   accident because the caller asserted happens to be the one that ran first.
5. Run under `-race`.

*Why the assertion is the wrong-answer assertion.* Under a shared policy or a
shared loader cache, one caller receives the other caller's row. The test does not
say "the two responses differed" or "the cache was hit N times" — it says a
specific caller received a specific document they must never see. That is the only
formulation that a plausible sharing bug cannot slip past.

*Also assert scope death.* After both requests complete, each scope's registry
must report itself closed, and a `Load` on the retained context must return
`ErrScopeClosed` — not a stale value.

### 8.7 Further mutations

Named the same way; each needs at least one failing test.

- **`SerialiseLoaderArgsWithFmt`** — replace the canonical token with `%v`.
  Must fail: a table of argument pairs that `%v` collapses (`{"x","yz"}` vs
  `{"xy","z"}`; `"1"` vs `1`; a nil slice vs an empty slice; two maps differing
  only in key order must *not* differ) asserting distinct tokens for distinct
  values and equal tokens for equal values. Plus: a loader argument containing a
  func must return an error naming the loader and the argument path, not a token.
- **`AllowRelationWhereOnToOne`** — add `Where` to the to-one relation entry type.
  Must fail: the generated to-one entry type has no such field (a compile-level
  assertion in a generator golden test).
- **`SkipFilterClassificationInsideProjection`** — classify only the root filter.
  Must fail: `select: { posts: { where: { secretNote: { startsWith: "x" } } } }`
  under a rule hiding `Post.secretNote` must be FORBIDDEN, naming the field and
  `Post`. Repeat for the entry's `orderBy`, `cursor`, `distinct`, and for a
  `_count` entry's `where`.
- **`DedupeSubscriptionEvaluationAcrossCallers`** — key the per-event evaluation
  cache by the evaluation key alone. Must fail: two subscribers with identical
  `where` and identical projection but different principals; each must receive
  only their own rows, and the evaluation counter must show two evaluations, not
  one.
- **`MakeUpsertProbeUseReadConstraint`** — probe with the read constraint instead
  of the update constraint, or run a read to discover whether the row exists when
  the caller may not update. Must fail: upsert against an existing-but-unreachable
  row must be byte-identical in outcome — same code, same message — to upsert
  against a nonexistent id, across all three sub-cases (create permitted, create
  forbidden, create collides).
- **`ClassifyBatchWhereAsWriteOnly`** — stop treating `updateMany`/`deleteMany`
  filters as reads. Must fail: a caller with `update` but not `read` must be
  refused on *every* filter field including `id`.
- **`ReturnMaskedFieldAsOmitted`** — omit denied fields instead of nulling them.
  Must fail: the response shape must be identical whether the field is denied or
  genuinely null, and the parent, its list and the subscription payload must stay
  non-null.

---

## 9. Open questions and known divergences

Flagged for whoever owns the Go design; each is a place where the TypeScript's
behaviour and its documented intent do not fully line up, or where a decision has
to be made rather than ported.

1. **The transport `where` is scalar-only; the engine's is not.** The generated
   GraphQL `WhereInput` skips every relation and list field, so a remote caller
   cannot express `{ author: { is: { … } } }` at all — yet the engine, the policy
   condition language and the field-reference collector all handle relation
   filters at every depth, and the release notes describe filtering posts by
   `author.phone` as a case the classifier covers (reachable only through the
   programmatic client or the raw escape hatch). **Decide:** does the Go transport
   surface expose relation filters? This document specifies the *type* as the full
   condition tree and leaves the transport projection as a decision. Whichever way
   it goes, the classifier must cover relation filters — it already has to, for
   policy conditions and for the programmatic surface.

2. **`cursor` and `distinct` are programmatic-only.** They are absent from the
   generated `findMany` field but present on the engine, fully compiled, and
   explicitly classified. The migration guidance warns callers who "paginate or
   deduplicate" that they may now be refused — which they can only be through the
   programmatic surface. **Decide** whether Go's transport exposes them.

3. **`findFirst` and `count` have no transport field** but do have hooks and full
   engine support. Same decision.

4. **The execution key was incidental.** The TypeScript derived it from the
   identity of the GraphQL executor's `rootValue`, which happens to be per-event
   for subscriptions and happens to be absent for ordinary requests. It is correct
   by coincidence of the executor's behaviour. §4.2 makes it an explicit,
   runtime-owned token; do not reproduce the coincidence.

5. **Cache clearing lives in the wrong layer.** In the TypeScript, `clearBatchCaches`
   is invoked by a wrapper around the *generated GraphQL write resolvers* only. A
   write issued through the context-bound programmatic client does not clear, so a
   read-after-write in the same request through that client can serve stale
   batched values. §4.5 puts clearing in the engine. Test M3's
   `TestWriteThroughProgrammaticClientClears` covers it.

6. **The shared-context guard has gaps.** It is applied at root query/mutation
   resolvers and at batched computed fields, but the subscription path bypasses it
   (the subscribe branch is not wrapped) and the engine's own entry points do not
   check it — so a service calling the context-bound client directly is unguarded.
   Go does not need the guard at all if §3.4 is followed, but the gaps are worth
   knowing: they show which paths a bolted-on detector tends to miss, and those are
   the same paths M4's coverage test must enumerate.

7. **The policy-decision memo was keyed by the context object**, so under the
   static-context bug the memo was shared too — one bug amplifying another. In Go
   the memo lives in the scope and dies with it; there is nothing to key on that
   outlives the request.

8. **Batch scheduling has no free window in Go.** JavaScript's microtask queue
   gives a batch boundary for nothing. Go has no equivalent signal, and the choice
   (executor-driven flush vs. timer) affects latency and test determinism, not
   correctness. Make it once, centrally, and document it. §4.4 recommends
   executor-driven and warns against timers.

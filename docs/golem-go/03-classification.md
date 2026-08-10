# 03 — Classification: refusing a query that names a field the caller may not read

> **Specification status:** detailed supporting research. The merged
> [`BIBLE.md`](./BIBLE.md) is authoritative, including typed model/field
> identities and the classified positions exposed by the final Go surfaces.

Status: specification for the Go port. Normative. Derived from the 0.6.x TypeScript
implementation, which is the ground truth for behaviour.

This document specifies **field classification**: the check that refuses a statement
because its filter, ordering, cursor, distinct list or row selector *names* a field the
caller is not allowed to read.

It is the security core of golem. Everything else in the engine is a correctness
concern; this is the part that stops a caller reading data out of the shape of an
answer rather than out of the answer itself.

Three invariants from the rest of the design are load-bearing here and are restated
because the reasoning below depends on them:

- **Every operator renders two-valued SQL.** No operator returns `UNKNOWN`. A row
  either matches or it does not.
- **A FILTER IS A READ.** Naming a field in a `where` interrogates that field's stored
  value. There is no sense in which it is "not reading" it.
- **Discharge is decided against the rows the statement selects**, not against some
  other constraint that happens to be available.

---

## 1. The attack

### 1.1 Why masking the projection is not enough

The obvious model of field-level authorization is: decide, per field, whether the
caller may see the value, and null it out in the response if not. Golem does that —
it is the masking pass, specified elsewhere. It is not sufficient, and an implementer
who stops there has built a system that leaks every field it pretends to hide.

Consider a `User` model with a `phone` column, and a policy that says a caller may
read `User.phone` only on their own row. A moderator queries every user and gets:

```json
[{ "email": "ada@example.com", "phone": null },
 { "email": "roy@example.com", "phone": null }]
```

The masking worked. `phone` is hidden. Now the moderator writes a different query:

```
users(where: { phone: { startsWith: "+" } }) { email }
```

Nothing in the *response* contains a phone number. Every field in the projection is
one the caller may read. But the statement asked the database a question about
`phone`, and the database answered — not in a column, but in **which rows came
back**. That is the value oracle.

### 1.2 Recovering a hidden string, one character at a time

The channel is one bit per query: *did this row survive the filter*. `startsWith`
turns one bit into a full string, because a prefix predicate lets the attacker search
position by position rather than guess the whole value.

Target: `roy@example.com`'s phone number, a 12-character string over the alphabet
`{+, 0..9}` (11 symbols). The attacker may read `User.email` on every row, so they
can pin the query to exactly one row and read the answer off row membership.

```
Query  1: where: { email: "roy@example.com", phone: { startsWith: "+" } }  ->  1 row   ✓ first char is '+'
Query  2: where: { email: "roy@example.com", phone: { startsWith: "+0" } } ->  0 rows  ✗
Query  3: where: { email: "roy@example.com", phone: { startsWith: "+1" } } ->  0 rows  ✗
...
Query  6: where: { email: "roy@example.com", phone: { startsWith: "+4" } } ->  1 row   ✓ second char is '4'
Query  7: where: { email: "roy@example.com", phone: { startsWith: "+40" } }->  0 rows  ✗
...
Query 12: where: { email: "roy@example.com", phone: { startsWith: "+44" } }->  1 row   ✓ third char is '4'
```

Each position costs at most 11 queries and on average about 6. Twelve positions is
roughly **70 queries** to recover a value the system reported as `null` on every read.
Against a `String` column over a larger alphabet the same search costs more queries
and still finishes in seconds. `contains`, `endsWith`, `lt`/`gt` (binary search — 7
queries per character over a 128-symbol alphabet, better than linear), `in`, and
`equals` against a candidate list are all the same attack with different arithmetic.

The two-valued-operator invariant is what makes this clean: there is no `UNKNOWN`
outcome the engine could hide behind, and no NULL-semantics cleverness that blunts it.
The only defence is to **refuse the query before it runs**.

Note what is *not* required for the attack: no unusual permission, no direct SQL, no
knowledge of the schema beyond a field name. The attacker only needs one readable
field to pin rows with (very often the primary key) and one hidden field to interrogate.

### 1.3 The ordering channel

`orderBy` is the same oracle with a different readout. Order the caller's readable
rows by the hidden column and read back a readable field per row: the response is the
**ranking of the hidden values**. Combined with `take`/`skip` this is a comparison
oracle — sort by `phone`, page one row at a time, and the sequence of emails is the
sort order of the phone numbers. Insert a known row (if the caller may create) and its
position in the ranking bounds the hidden values above and below it.

### 1.4 The cursor channel

`cursor` names a row by field value. Whether the page starts, and where it starts
within the ordering, discloses whether a row holds that value and where it sits.

### 1.5 The distinct channel

`distinct: ['phone']` returns one row per distinct value of `phone` among the matching
rows. The values are still masked. The **count** is not: narrow to two rows the caller
may read, ask for `distinct` on the hidden field, and a count of 1 says *these two rows
hold the same hidden value* while a count of 2 says they differ. For a low-cardinality
field (a status, a tier, a tenant) the partition induced over rows is very close to the
value itself.

This channel is weaker than §1.2 — no operator compares the hidden value against
anything the caller supplies, so there is no character-by-character recovery — but it
is the same class of disclosure and is refused in the same place.

### 1.6 The batch-write variant: the count is the channel

`updateMany` and `deleteMany` return `{ count: N }`. That is the disclosure.

```
updateMany(where: { phone: { startsWith: "+44" } }, data: { name: "x" })  ->  { count: 3 }
updateMany(where: { phone: { startsWith: "+45" } }, data: { name: "x" })  ->  { count: 0 }
```

Identical search to §1.2, over a statement that returns no rows at all. An
implementation that classifies read filters but treats a batch write's `where` as
"just a write" has closed the front door and left the count channel wide open.

This one has a second edge, specified in §5: the count ranges over the rows the *write*
constraint selects, which may be a wider set than the rows the caller may read.

### 1.7 The nested-write variant: the rollback is free

This is the sharpest of the three, and the one an implementer is most likely to miss.

Golem verifies writes **after** the statement, inside a transaction: it runs the
mutation, checks the resulting rows against the policy, and rolls back if any row was
not permitted. That is a sound design for *integrity* — no forbidden row is ever
committed.

It is a disclosure disaster for *confidentiality*, because a rollback is an observable
outcome that costs the attacker nothing:

```
post.update({
  where: { id: <a post the caller may update> },
  data:  { readingSessions: { deleteMany: { note: { startsWith: "secret-b" } } } },
})
```

Walk the two cases with `note` unreadable and the child rows unwritable:

- **The prefix matches a child row.** The nested `deleteMany` selects it, the row check
  runs, the row fails the policy, the transaction rolls back, the caller gets an error.
  Nothing was deleted.
- **The prefix matches nothing.** The nested `deleteMany` selects zero rows, no row
  check fails, the statement commits. The caller gets a success.

Success versus error is one clean bit per query, the database is left byte-identical
in both branches, and the attacker pays nothing — no row written, no audit trail of a
change, no rate limit tripped by damage. The TypeScript implementation was driven this
way as a prefix search that **recovered a hidden string in 155 queries with no row ever
written**.

The lesson generalises: *any post-hoc check that turns a match into a distinguishable
outcome is an oracle*. Post-hoc verification protects integrity. Only pre-flight
classification protects confidentiality.

### 1.8 The unique-selector and upsert variants

`update`, `delete` and `upsert` take a *unique* `where`. That looks like it cannot
carry an oracle — you name one row by its key. But the selector may carry ordinary
filters **beside** the unique field:

```
post.update({ where: { id: "p1", secret: { startsWith: "a" } }, data: { ... } })
```

Not-found versus success is the same one bit, and the same prefix search. The unique
key merely pins the row the way `email` did in §1.2.

`upsert` adds its own channel. It probes for an existing row and then chooses a branch.
The branch is observable (a create emits a create event and returns a fresh row; an
update returns the existing one). Since an upsert's `where` is a unique selector, the
branch is an **existence oracle over unique keys** — which, when the unique key is an
email, is account enumeration. The probe must therefore be classified **before** it
runs, not after the branch is chosen.

---

## 2. Every position that must be classified

Classification is exhaustive over the positions listed below. A position that is
missed is not a partial defence; it is a complete bypass, because the attacker picks
the position.

Classification always runs **before any statement is issued to the database and before
any SQL is compiled**. A refusal must be observable as "nothing happened", not as "the
statement ran and then we complained".

### 2.1 Classified positions

| # | Position | Where it appears | Disclosure channel | Selecting action (§5) |
|---|---|---|---|---|
| P1 | `Where` at the root of a read | `FindOne`, `FindFirst`, `FindMany`, `Count`, `Aggregate`, `GroupBy` | which rows / how many rows come back | `read` |
| P2 | `OrderBy` at the root of a read | `FindFirst`, `FindMany`, `Aggregate`, `GroupBy` | the ranking of rows (§1.3) | `read` |
| P3 | `Cursor` at the root of a read | `FindFirst`, `FindMany`, `Aggregate` | whether the cursor row exists and where it sits (§1.4) | `read` |
| P4 | `Distinct` at the root of a read | `FindFirst`, `FindMany` | the row count = the partition the field induces (§1.5) | `read` |
| P5 | `Where` / `OrderBy` / `Cursor` / `Distinct` **inside a relation entry of a projection**, at every depth | `select`/`include` trees on any read **and on the tree a `Create`, `Update` or `Delete` returns** | membership and ordering of the nested array | `read` (see §5.6) |
| P6 | `Where` inside a **relation-count** entry | `_count: { select: { posts: { where: … } } }` | the count of the nested relation | `read` |
| P7 | `Where` a **batch write** selects rows with | `UpdateMany`, `DeleteMany` | the returned `count` (§1.6) | `update` / `delete` |
| P8 | Unique selector of a **single-row write** | `Update`, `Delete` | not-found versus success (§1.8) | `update` / `delete` |
| P9 | `Where` a **nested write** selects rows with, at every depth inside `data` | nested `update`, `updateMany`, `upsert`, `delete`, `deleteMany`, `connect`, `disconnect`, `set`, `connectOrCreate` | the rollback / row-check outcome (§1.7) | `read` (see §5.6) |
| P10 | `Where` an **upsert** probes with | `Upsert`, before the branch is chosen | which branch was taken (§1.8) | `read` (see §5.6) |
| P11 | Aggregate measure fields, grouping keys, `having`, aggregate `orderBy` | `Aggregate`, `GroupBy` | the measure itself | `read` |

Notes on the table:

- **P5 applies to writes too.** `Create`, `Update` and `Delete` accept a projection for
  the row they return. That projection can carry relation entries with their own
  filters. Classify it exactly as a read's projection.
- **P9's operation table is fixed** and must be reproduced exactly. For each nested
  write kind, the filter position is:

  | nested kind | filter lives in |
  |---|---|
  | `create` | *none* |
  | `createMany` | *none* |
  | `connectOrCreate` | its `where` |
  | `connect` | the payload itself |
  | `disconnect` | the payload itself |
  | `set` | the payload itself (may be a list) |
  | `update` | its `where` |
  | `updateMany` | its `where` |
  | `upsert` | its `where` |
  | `delete` | the payload itself |
  | `deleteMany` | the payload itself |

  `create` and `createMany` select no existing rows, so they carry no oracle and are
  correctly skipped. Every other kind selects rows and must be classified against the
  **related** model.
- **P11 is specified in the aggregation document**, not here, but it shares this
  document's collector and refusal rule. Its refusals carry a *validation* error code
  rather than a *forbidden* one, for backwards compatibility. See §4.4.

### 2.2 What is NOT classified, and why that is sound

| Not classified | Why sound |
|---|---|
| The scalar **values** the caller supplies in a filter | They are the caller's own input. |
| **Json path segments** inside a Json filter (`{ meta: { path: ["a","b"], equals: 1 } }`) | `path` names keys inside a document, not fields on a model. The *column* `meta` is classified; that is the authorization boundary. Golem does not do sub-document field authorization. |
| **Scalar-list operator payloads** (`has`, `hasEvery`, `hasSome`, `isEmpty`) | Same: the *column* is classified, the payload is caller-supplied values. |
| The `data` payload of any write | Writing a field is a write. It is governed by write authorization and post-write row verification. Writing a field you cannot read is not a read and discloses nothing. |
| `create` / `createMany` payloads inside nested writes | They select no rows (§2.1). |
| **Projection scalars** (`select`, `include`, `omit`) | Classified by a *different* pass — the projection/masking pass — with different semantics: `never` refuses, `conditional` masks row by row. Do not route them through this collector. |
| `take`, `skip` | Carry no field names. |
| The **authorization constraint the engine itself injects** | It is the policy, not caller input. Classifying it would have the policy refuse itself. It is merged into the statement *after* classification. |
| `_all` inside a `_count` selection | Not a field name. |

The general rule: classify **field names the caller chose to put in a row-selecting or
row-ordering position**. Everything else is either caller-supplied data, or the
engine's own policy.

---

## 3. The collector

The collector walks a condition tree (and the ordering, cursor and distinct
structures) and produces the set of `(model, field)` pairs the statement names.

### 3.1 The Go advantage: nodes carry their own model

In the TypeScript implementation, a condition is a plain object, so the collector has
to walk **model metadata alongside the input** to work out which model owns each key.
At `{ author: { is: { phone: { startsWith: "+44" } } } }` on model `Post`, the walker
must look up `Post.author`, discover it is a relation to `User`, switch the current
model to `User`, and only then attribute `phone`.

Getting that wrong is not a crash. It is a **silent authorization bypass**:
`phone` gets classified against `Post`, `Post` has no field called `phone`, the
classifier is asked about a field that does not exist, no rule matches, and — depending
on the policy engine — the answer comes back permissive. The query runs. The oracle is
open. This is the single most likely way to build a broken classifier.

In Go this failure is **structurally impossible**, and the specification depends on
that. Conditions are data, and *every condition node carries the model it was built
against*, stamped by the generated typed column that built it:

```go
type Condition interface{ conditionNode() }

// A predicate on one scalar column.
type FieldCondition struct {
    Model string   // stamped by the generated column; never inferred
    Field string
    Op    Operator
    Value any
}

// AND / OR / NOT over sub-conditions.
type LogicalCondition struct {
    Op       LogicalOp // OpAnd | OpOr | OpNot
    Operands []Condition
}

// A quantified hop across a relation.
type RelationCondition struct {
    Model      string     // the model that owns the relation field
    Field      string     // the relation field's name on Model
    Target     string     // the related model
    Quantifier Quantifier // QIs | QIsNot | QSome | QEvery | QNone
    Inner      Condition  // built against Target; its nodes carry Target
}

// A full-text relevance ordering. The field names live in the payload,
// not as the node's key. See §3.6.
type RelevanceCondition struct {
    Model  string
    Fields []string
    Search string
    Sort   SortOrder
}

// A compound unique selector, expanded at construction. See §3.7.
type UniqueSelector struct {
    Model      string
    Components []FieldCondition // one per constituent column
}
```

Because `FieldCondition.Model` is set by the typed column at construction time, the
collector never infers ownership, never consults metadata, and cannot misattribute.
The whole collector reduces to a recursive walk that reads a field off each node.

### 3.2 The output type

```go
// FieldReferences maps model name -> set of field names named by the statement.
type FieldReferences map[string]map[string]struct{}

func (r FieldReferences) Add(model, field string) {
    fields, ok := r[model]
    if !ok {
        fields = map[string]struct{}{}
        r[model] = fields
    }
    fields[field] = struct{}{}
}
```

Order is not semantically significant, but implementations MUST iterate models and
fields in a **deterministic order** (sorted) so that refusal messages are stable and
tests are not flaky. Sorting also gives a stable cache key for the classifier
memoisation (§4.5).

### 3.3 The walk

```go
func CollectCondition(into FieldReferences, c Condition) {
    switch n := c.(type) {
    case nil:
        return

    case *FieldCondition:
        into.Add(n.Model, n.Field)

    case *LogicalCondition:
        for _, operand := range n.Operands {
            CollectCondition(into, operand)
        }

    case *RelationCondition:
        into.Add(n.Model, n.Field)   // the relation field itself, on the parent
        CollectCondition(into, n.Inner) // nodes inside carry n.Target

    case *RelevanceCondition:
        for _, name := range n.Fields {
            into.Add(n.Model, name)
        }

    case *UniqueSelector:
        for i := range n.Components {
            into.Add(n.Components[i].Model, n.Components[i].Field)
        }
    }
}
```

That is the whole thing. Every rule below is a clarification of one of these cases.

### 3.4 Logical nodes

`AND`, `OR` and `NOT` are traversed identically — every branch is collected, with no
short-circuiting and no attempt to reason about satisfiability. A field named only in
a branch that can never be true is still refused. That is deliberate: it is
conservative, and evaluating branch reachability would itself be a source of bugs.

`NOT` deserves an explicit statement because it is tempting to skip: `NOT { phone:
{ startsWith: "+44" } }` is exactly as good an oracle as the positive form (the answer
is simply inverted). Collect it.

Empty operand lists are legal and contribute nothing.

### 3.5 Relation quantifiers

A relation hop contributes **two** references, and both matter:

1. `(parent model, relation field name)` — the caller named `Post.author`, and a
   policy may hide the relation field itself.
2. every reference inside `Inner`, which carry the **target** model.

All five quantifiers are traversed identically: `is`, `isNot`, `some`, `every`, `none`.
There is no "safe" quantifier. `none` is a negated `some` and leaks the same bit.

Two shapes that the TypeScript handles and Go must preserve semantically:

- **The bare to-one shorthand.** `{ author: { phone: … } }` with no quantifier key is
  a to-one condition on the related model. In Go the generated typed column produces a
  `RelationCondition` with an explicit quantifier, so the shorthand disappears at the
  API surface — but if the Go engine also accepts an untyped/dynamic condition form
  (e.g. decoded from GraphQL), that decoder MUST produce the same
  `RelationCondition` and MUST NOT treat the inner keys as fields of the parent.
- **Sibling keys beside a quantifier.** In the TypeScript, `{ post: { is: { title: "x" },
  secret: "y" } }` is legal input and `secret` must be classified against `Post`, not
  dropped and not attributed to the parent. Any dynamic decoder MUST fold such sibling
  keys into a condition on the **target** model.

### 3.6 Full-text relevance: field names inside a value

This is the one shape where the field names are not the keys of the structure. It is
easy to miss, and the TypeScript collector originally **stepped over it entirely**,
leaving a hole: an ordering by relevance over a hidden column ranked rows by that
column's contents and disclosed the ranking (§1.3), with no classification at all.

The shape is an `orderBy` entry:

```
orderBy: [{ _relevance: { fields: ["title", "body"], search: "x", sort: "asc" } }]
```

Rules:

1. The names in `Fields` are model fields on the node's own model. Collect **every**
   one of them.
2. `Fields` may be given as a single name or a list; normalise to a list.
3. An empty `Fields`, or any entry that is not a string, is a **validation** error, not
   a refusal:
   `Cannot order <Model> by relevance without naming the fields it searches`.
   Do not silently permit a relevance ordering that names nothing — that is how the
   hole reappears.
4. `sort` and `search` carry no field names.

### 3.7 Compound unique selectors

Prisma-style unique selectors may name a *compound* index by its generated name, with
the constituent values nested inside:

```
where: { email_tenantId: { email: "a@b.c", tenantId: "t1" } }
```

`email_tenantId` is not a column. The selector expands to its **constituents**, and
each constituent is collected against the owning model:

- A compound **primary key** selector expands to the primary-key columns in declared
  order.
- A compound **unique index** selector expands to that index's columns.
- A single-column unique selector (`{ email: … }`) is an ordinary field reference.

In Go this expansion happens at construction (the `UniqueSelector` node carries
`Components`), so the collector never has to consult an index of compound names.
Any dynamic decoder MUST perform the expansion before handing the selector to the
collector, and MUST refuse a name that matches neither a field nor a known compound
selector (§3.9).

Two consequences worth stating:

- The selector of an `Update`/`Delete`/`Upsert` may carry **ordinary filters beside**
  the unique component. Those are collected too — that is precisely the P8 oracle.
- `Distinct` does **not** accept compound selector names. A distinct entry must name a
  real scalar field.

### 3.8 Scalar-list and Json filters

For a field whose kind is not a relation, the collector adds the field and **does not
descend into the operator payload**. This covers:

- comparison operators: `equals`, `not`, `in`, `notIn`, `lt`, `lte`, `gt`, `gte`,
  `contains`, `startsWith`, `endsWith`, `mode`
- scalar-list operators: `has`, `hasEvery`, `hasSome`, `isEmpty`
- Json operators: `path`, `equals`, `string_contains`, `string_starts_with`,
  `array_contains`, …

Rationale: the payload contains caller-supplied values and document paths, never model
field names. Descending into it would collect garbage (`"path"`, `"mode"`) and, under a
permissive blanket policy, would then be *answered* rather than rejected — noise at
best, and at worst a validation error on a legal query.

The inverse mistake is worse: skipping the field because "it's only a list operator".
`{ tags: { has: "secret-tag" } }` is a membership oracle over `tags` and must be
collected.

### 3.9 Unknown keys are refused, not passed through

Any key in a filter, ordering, cursor or distinct position that names **no field on the
model** is a **validation** error. It must not be forwarded to the query layer.

Messages (preserve the shape; the operator needs the key and the model):

- filter / ordering: `Cannot filter or order by "<key>" on <Model>: no such field`
- cursor: `Cannot page from "<key>" on <Model>: no such field`
- distinct: `Cannot read <Model> distinct on <json-quoted name>: no such field`

Why this is a security rule and not a tidiness rule: under a blanket grant
(`can('read', 'Post')` with no field list), a policy engine typically answers
"always readable" for **any string it is handed**, including a misspelling. So an
unknown key sails through classification. In the TypeScript this was previously caught
downstream by the query layer's own validation — a fail-closed behaviour golem was
*borrowing*, not owning. Owning it matters because a Go engine that compiles its own
SQL has no such downstream backstop.

In Go, a statically-typed condition tree makes this unreachable through the generated
API. It remains reachable through any dynamic decoder (GraphQL input, raw request
JSON), and the check MUST be implemented there.

### 3.10 Aggregate measure shapes

`Aggregate` and `GroupBy` place field names under measure keys (`_sum`, `_avg`,
`_min`, `_max`, `_count`) in two structurally different places, and the two are
collected by **different rules**. Getting them the same way is a bug in one direction or
the other.

1. **As a measure selection on the request** (`_sum: { views: true }`). Collect the keys
   whose value is literally `true`, against the request's model. `_count: true` (a bare
   boolean rather than a selection) contributes nothing, and the pseudo-field `_all`
   inside a `_count` selection is dropped.

2. **As a measure key encountered inside a `where`, `orderBy` or `having` tree**
   (`orderBy: { _sum: { views: "asc" } }`, `having: { _count: { id: { gt: 1 } } }`).
   Here the payload's values are sort directions or nested predicates, not `true`, so
   collect **every key of the payload except `_all`**, regardless of its value.

Grouping keys (`by: [...]`) are collected directly, one reference per entry.

---

## 4. The refusal rule

### 4.1 Inputs

For each model in the collected references, ask the authorization provider to classify
that model's referenced fields for the **`read`** action:

```go
type FieldAccess int
const (
    AccessNever FieldAccess = iota
    AccessConditional
    AccessAlways
)

type FieldClassification struct {
    Access                 FieldAccess
    Requires               []string // field names the readability decision depends on
    Dependencies           DependencyTree
    DischargedByConstraint bool     // the row constraint alone decides readability
}

type Classifier interface {
    ClassifyFields(ctx context.Context, action Action, model string, fields []string) (map[string]FieldClassification, error)
}
```

Always classify for `read`, in every position — including the `where` of an
`updateMany`. **A filter is a read.** The action being *performed* is irrelevant to
whether naming the field interrogates it.

### 4.2 The decision, per field

```
Access == AccessAlways                         -> permit
Access == AccessNever                          -> REFUSE
Access == AccessConditional
    && DischargedByConstraint
    && selectedRowsStayReadable(...)  (§5)     -> permit
Access == AccessConditional, otherwise         -> REFUSE
no classification entry returned for the field -> REFUSE
```

The last line is not an edge case; write it deliberately. **The classification path
fails closed.** If the provider returns a map with no entry for a field the statement
named, that is a refusal.

> Note the deliberate asymmetry with the projection pass: there, a *missing* entry is
> treated as permitted (the field is simply projected and, if conditional, masked).
> Here a missing entry is a refusal. Do not "unify" the two — the projection can fall
> back on masking, the filter cannot.

`Dependencies` plays no part in this decision; it is used by the projection pass to
hydrate the columns a per-row check needs. Only `Access`, `Requires` and
`DischargedByConstraint` matter here.

### 4.3 The refusal message

A bare `FORBIDDEN` is useless. An operator staring at a forty-column model cannot act
on it, and the application developer whose legitimate query broke cannot fix it. The
message MUST name **the field**, **the model**, and, where the field is conditional,
**what its readability depends on**.

Two shapes, exactly:

```
Cannot <verb> field "<field>" on <Model>
```

```
Cannot <verb> field "<field>" on <Model>: readability depends on <r1, r2, …>, which the query constraint does not discharge
```

The second form is used when the classification carries a non-empty `Requires`; the
list is the `Requires` entries joined with `", "` in the order the provider returned
them. The first form is used otherwise (`AccessNever`, or conditional with no stated
requirements, or a missing entry).

`<verb>` is determined by the position:

| positions | verb |
|---|---|
| P1–P10 (filters, ordering, cursor, distinct, row selectors) | `filter or order by` |
| grouping keys / `having` / group `orderBy` | `group or aggregate` |
| aggregate measures | `aggregate` |

Refusal is **fail-fast**: the first offending field raises. Do not accumulate. Do not
report the full set of offending fields — that is itself a small disclosure about which
fields exist and how they are classified, and it costs the operator nothing to fix them
one at a time. (This also means the deterministic iteration order of §3.2 decides
*which* field is named; hence the requirement that it be deterministic.)

### 4.4 Error codes

| position | error kind |
|---|---|
| P1–P10 (filter, ordering, cursor, distinct, row selectors) | **Forbidden** (`FORBIDDEN`) |
| P11 (measures, grouping keys, `having`, aggregate `orderBy`) | **Validation** (`BAD_USER_INPUT`) |
| unknown key / malformed relevance / bad distinct name (§3.9, §3.6) | **Validation** (`BAD_USER_INPUT`) |

The aggregate positions carry a validation code for compatibility with the pre-0.6
behaviour, where they were already refused as bad input. Do not "fix" this to
`FORBIDDEN` in the port; downstream applications match on the code.

### 4.5 Engagement and memoisation

- The whole classification pass is gated on a **`CheckReadFields`** engine option. When
  off, no position is classified and unknown keys are passed downstream. This exists so
  applications that predate field-level policy are not broken by an upgrade; it is a
  documented footgun, not a security control.
- It is also gated on the provider actually implementing `ClassifyFields`. A provider
  without it classifies nothing.
- `ClassifyFields` results MUST be memoised **per request context**, keyed by
  `(action, model, sorted field list)`. A single read can reach the same model at
  several depths and the classifier may be expensive. Never memoise across contexts —
  the classification is per caller.
- The constraint lookups used by §5 are memoised the same way, keyed by
  `(action, model)`.

### 4.6 Ordering relative to everything else

Classification runs:

- **before** the statement is issued,
- **before** any SQL is compiled (a compiled read must emit no compilation event at all
  when its filter is refused),
- **before** an `Upsert` probes for an existing row,
- **before** the constraint is merged into the `where`,
- and, for a write, **before** any transaction is opened.

A refusal must be indistinguishable from "the engine never touched the database",
because §1.7 shows that any observable side effect of having tried is itself a channel.

---

## 5. Discharge against the selecting constraint

### 5.1 What "conditional, discharged by the constraint" means

A field is `conditional` when the policy grants it under a row condition — "you may
read `Post.title` on published posts". The condition is about *rows*, not about the
field.

Now: if the statement is **already narrowed to exactly those rows**, then naming the
field in the filter discloses nothing new. Every row the filter can interrogate is a
row whose `title` the caller may read anyway. The condition is *discharged* by the
constraint, and the filter is permitted.

`DischargedByConstraint` is the provider's assertion that the row constraint alone
decides this field's readability — i.e. there is no additional per-field condition
beyond the row policy. In the common case (a policy with row conditions and **no field
lists**), every field is conditional-and-discharged, and this whole mechanism is a
no-op. Classification only bites for applications that write **field-scoped** rules.

### 5.2 The 0.6.0 correction

The subtle part, and the reason this section exists.

Classification asks about **reading** — always, in every position. That is correct and
must not change. But *which rows the statement can interrogate* is not decided by the
read constraint. It is decided by the constraint attached to the action that **selects
the rows the statement touches**:

- a read selects with the **read** constraint,
- an `Update`/`UpdateMany` selects with the **update** constraint,
- a `Delete`/`DeleteMany` selects with the **delete** constraint.

Consider a policy: *read `Post` where `published`; update every `Post`.*

```
updateMany(where: { title: { startsWith: "Draft" } }, data: { published: false })
```

`title` is conditional on `published`, and the read constraint (`published = true`)
would discharge it. But this statement does not select with the read constraint. It
selects with the update constraint, which is *everything*. The returned count ranges
over unpublished posts — rows whose `title` the caller may not read. The count is a
prefix oracle over exactly the values the policy hides.

So the rule is:

> **Discharge holds only if the SELECTING constraint implies the READ constraint.**

Formally: let `S = constrain(selectingAction, model, ctx)` and
`R = constrain("read", model, ctx)`. Discharge is permitted iff `S ⟹ R`, i.e. every
row the statement can touch is a row the caller may read.

### 5.3 The check

```go
type SelectedRows struct {
    Model  string
    Action Action // the action whose constraint selects the rows
}

func selectedRowsStayReadable(ctx context.Context, p Provider, model string, sel *SelectedRows) (bool, error) {
    if sel == nil || sel.Action == ActionRead || sel.Model != model {
        return true, nil
    }
    selecting, err := p.Constrain(ctx, sel.Action, model)
    if err != nil { return false, err }
    readable, err := p.Constrain(ctx, ActionRead, model)
    if err != nil { return false, err }
    return ConstraintImplies(selecting, readable), nil
}
```

Three early-outs, each with a reason:

- `sel == nil` — the caller did not declare a selecting action, so the position
  discharges against the read constraint (see §5.6).
- `Action == read` — a read selects with the read constraint, and a constraint always
  implies itself. Reads are untouched by this rule.
- `sel.Model != model` — the reference is on a **different** model than the one the
  statement selects rows of, reached through a relation hop. No statement narrows the
  interrogated rows of a related model to a write constraint, so that model discharges
  against its own read constraint. See §5.6.

The result is computed at most once per model per refusal pass (lazily, only when a
conditional-and-discharged field is actually encountered) and reused for the remaining
fields of that model.

If `Constrain` itself refuses — the caller has no grant at all for the selecting action
— that refusal propagates as a forbidden error. This is sound: a caller who cannot
perform the write cannot be harmed by learning that it cannot perform the write, and it
would have been refused a step later regardless.

### 5.4 Which action each position declares

| position | declares |
|---|---|
| any read (`FindOne`, `FindFirst`, `FindMany`, `Count`, `Aggregate`, `GroupBy`) | nothing (read) |
| `Update`, `UpdateMany` root `where` | `update` |
| `Delete`, `DeleteMany` root `where` | `delete` |
| projection relation entries (P5, P6) | nothing (read) |
| nested-write filters (P9) | nothing (read) |
| `Upsert` probe `where` (P10) | nothing (read) |

### 5.5 The write-only consequence

An ability that grants `update` (or `delete`) but **no `read` at all** cannot filter an
`UpdateMany` or `DeleteMany` by anything — not even by the primary key.

This is not reached through §5.2 at all; it is reached through §4.2. With no read grant,
the classifier returns `never` (or nothing) for every field, and every field is refused
before discharge is consulted.

It is correct, not a bug, and worth defending explicitly because it will be reported as
one:

> A `where` is a read. It interrogates the database and answers through the count and
> through which rows changed. A caller who may not read the model must be told nothing
> by one — including "yes, a row with this id exists".

An application that wants a write-only ability to address rows must either grant a
narrow `read` alongside it, or use an addressing path that does not interrogate
(the engine's own constraint-merged single-row write against a key the caller already
holds).

### 5.6 The three positions that still discharge against the read constraint

These are deliberate and each has a reason. They must be preserved.

1. **A field reached through a relation** (`{ author: { is: { phone: … } } }`). It is
   classified against the *related* model, and no statement narrows the interrogated
   rows of the related model to a write constraint. There is no "selecting constraint"
   for `User` in a `Post` update. It discharges against `User`'s read constraint.
   (This is the `sel.Model != model` early-out.)

2. **A filter nested inside `data`** (P9). It selects the *children of whatever parent
   row the statement matched* — a set no single constraint describes. Discharging
   against the child model's read constraint is the available approximation.

3. **The `where` an `Upsert` probes with** (P10). It is classified *before* the branch
   is chosen, so the selecting action is not yet known; it is classified against the
   read constraint. Note that the probe itself runs under the **update** constraint
   (specified in the upsert document), so an ability whose update reach exceeds its read
   reach can still name a conditionally-readable field there. This is a known, narrow
   residual and is documented as such rather than silently accepted.

---

## 6. The implication check

`ConstraintImplies(selecting, required) bool` decides whether the selecting constraint
entails the required one.

### 6.1 The overriding requirement

The check is **conservative**. It may under-approximate and refuse a statement it could
in principle have permitted. It must **never** over-approximate and permit one it could
not prove. Every rule below is a truth-preserving inference; when no rule applies, the
answer is `false`.

This bears repeating because the natural instinct when a legitimate query gets refused
is to loosen a rule. Loosening a rule here reopens §1.6. Add a *new* truth-preserving
rule instead.

Constraints here are the opaque policy objects the provider returns. The check reasons
about their **structure**, not their semantics: it knows `AND`, `OR` and structural
equality, and nothing about what `{ published: true }` means.

### 6.2 Conjunct extraction

Both sides are first flattened to a list of conjuncts. `conjuncts(c)` is:

| input | conjuncts |
|---|---|
| `nil` / absent | `[]` |
| a list | concatenation of `conjuncts` of each element |
| a non-object scalar | `[c]` |
| an object | for each key/value: if the key is `AND`, splice in `conjuncts(value)`; otherwise emit the single-entry object `{key: value}` |

So `{}` and `{AND: []}` both flatten to the empty list, `{a: 1, b: 2}` flattens to
`[{a:1}, {b:2}]`, and `{AND: [{a:1}, {OR: [x, y]}]}` flattens to
`[{a:1}, {OR:[x,y]}]`.

Note that `OR` and `NOT` are **not** flattened — they stay as single opaque conjuncts,
to be handled by the disjunction rules.

### 6.3 The top-level rule

```
ConstraintImplies(selecting, required):
    needed := conjuncts(required)
    if len(needed) == 0 { return true }        // nothing is required; anything implies it
    held := conjuncts(selecting)
    return every need in needed is conjunctImplied(held, need)
```

Truth-preserving because `S ⟹ (r₁ ∧ r₂ ∧ …)` exactly when `S ⟹ rᵢ` for every `i`,
and because an empty requirement is `true`, which everything implies.

Note the asymmetry: an **empty required** is vacuously satisfied; an **empty selecting**
satisfies nothing (unless the requirement is also empty). That is the correct
direction — an unconstrained write against a constrained read must refuse.

### 6.4 The three conjunct rules

`conjunctImplied(held, need)` returns true if **any** of the following holds. They are
evaluated in this order and the first match wins.

**Rule 1 — an identical held conjunct.**
```
∃ h ∈ held : sameConstraint(h, need)
```
Truth-preserving: `(… ∧ h ∧ …) ⟹ h`.

**Rule 2 — the requirement is a disjunction and the selection implies one branch.**
If `need` is a single-key object whose key is `OR`:
```
∃ b ∈ branches(need) : ConstraintImplies({AND: held}, b)
```
Truth-preserving: if `S ⟹ b` and `b ⟹ (b ∨ …)` then `S ⟹ need`. Note the full
recursion — the branch is compared against the *whole* held conjunction, not a single
conjunct, so a branch may be met by a combination of held conjuncts.

If `need` is an `OR` and no branch is implied, `conjunctImplied` returns **false
immediately**; it does not fall through to Rule 3. That is conservative and therefore
sound, and it is the pinned TypeScript behaviour.

**Rule 3 — a held disjunction all of whose branches imply the requirement.**
If `need` is not an `OR`:
```
∃ h ∈ held : h is a single-key {OR: alts}
             ∧ len(alts) > 0
             ∧ ∀ a ∈ alts : ConstraintImplies(a, need)
```
Truth-preserving: `(a₁ ∨ a₂) ⟹ need` exactly when `a₁ ⟹ need` and `a₂ ⟹ need`. The
`len(alts) > 0` guard matters: an empty `OR` is `false`, which implies everything
vacuously, and permitting on that basis would be a soundness hole created by a
degenerate policy.

If none of the three matches, the answer is `false` and the statement is refused.

### 6.5 Structural equality

`sameConstraint(a, b)` is deep structural equality with these rules:

- identical references / values are equal;
- timestamps compare by instant, not by representation (the TypeScript special-cases
  `Date`; in Go, `time.Time` MUST be compared with `.Equal`, never with `==` or
  `reflect.DeepEqual`, because a monotonic-clock reading or a differing `*Location` for
  the same instant would make equal constraints compare unequal and refuse a legitimate
  query);
- lists compare **positionally**: same length, element-wise equal;
- maps/objects compare by key set and per-key value; **key order is irrelevant**;
- anything else compares by value.

Consequence to be aware of: `{OR: [a, b]}` and `{OR: [b, a]}` are *not* `sameConstraint`.
Rule 1 will miss them; Rules 2 and 3 usually recover the common cases. This is
conservative and acceptable. Do **not** "fix" it by sorting branch lists unless you can
define a total order on constraints that is stable across runs — an unstable order would
make refusals nondeterministic, which is far worse.

In Go, avoid `reflect.DeepEqual` for this function. It gets `time.Time` wrong, and it
gets typed-nil and numeric-type differences wrong (`int64(1)` vs `float64(1)` decoded
from JSON). Write the comparison explicitly over the constraint representation.

### 6.6 Why the `OR` shapes matter in practice

This is the part that looks like over-engineering until you look at a real policy
builder's output.

Ability builders accumulate grants and emit a **disjunction of the matching rules**,
even when there is only one. So a policy that reads as "read published posts" comes
back as:

```
read:   { OR: [ { published: true } ] }
```

and "update published posts" comes back as:

```
update: { OR: [ { published: true } ] }
```

Rule 1 handles that pair. Now add a second read grant — "or posts you authored":

```
read:   { OR: [ { published: true }, { authorId: "u1" } ] }
update: { OR: [ { authorId: "u1" } ] }
```

The write reach is a *subset* of the read reach and the statement is safe, but the two
constraints are not structurally equal. A naive equality check refuses it. That is not a
theoretical regression — it is **the common case**, because read grants accumulate
faster than write grants in every real policy. An implementation with only Rule 1
would refuse nearly every legitimate conditional filter on a write, the feature would be
reported as broken, and the pressure to "just return true" would be immediate. That is
how §1.6 gets reopened.

Rules 2 and 3 exist to make the common single- and multi-branch `OR` comparisons work
without weakening anything.

---

## 7. Worked examples

Throughout: model `Post`; `title` is `conditional`, `requires: ["published"]`,
`dischargedByConstraint: true`. The statement is
`updateMany(where: { title: { startsWith: "a" } }, data: { … })`, which declares
`SelectedRows{Model: "Post", Action: update}`.

### 7.1 Matched reaches — PERMIT

```
read:   { OR: [ { published: true } ] }
update: { OR: [ { published: true } ] }
```

`needed = [{OR:[{published:true}]}]`, `held = [{OR:[{published:true}]}]`.
Rule 1 matches (structurally identical). `S ⟹ R`. Discharge holds, the filter is
permitted, the statement runs.

### 7.2 Write wider than read — REFUSE

```
read:   { OR: [ { published: true } ] }
update: {}
```

`needed = [{OR:[{published:true}]}]`, `held = []`.
Rule 1: no held conjuncts. Rule 2: `need` is an `OR`; its only branch `{published:true}`
is tested against `{AND: []}` → `needed=[{published:true}]`, `held=[]` → no rule matches
→ false. So Rule 2 fails, and Rule 2 does not fall through. `ConstraintImplies` is
false.

Refused:

```
Cannot filter or order by field "title" on Post: readability depends on published,
which the query constraint does not discharge
```

This is §1.6 exactly: the count would have ranged over unpublished posts.

The same holds when the write constraint is **absent** (`nil`, no update rule
conditions at all) rather than `{}` — both flatten to zero conjuncts.

### 7.3 Write narrower than read — PERMIT

```
read:   { OR: [ { published: true } ] }
update: { AND: [ { OR: [ { published: true } ] }, { authorId: "u1" } ] }
```

`held = [{OR:[{published:true}]}, {authorId:"u1"}]` after `AND`-flattening.
`needed = [{OR:[{published:true}]}]`. Rule 1 matches on the first held conjunct.
Permitted — narrowing the write further can only shrink the touched set.

### 7.4 Write is one branch of the read — PERMIT

```
read:   { OR: [ { published: true }, { authorId: "u1" } ] }
delete: { OR: [ { authorId: "u1" } ] }
```

`needed = [{OR:[{published:true},{authorId:"u1"}]}]`, `held = [{OR:[{authorId:"u1"}]}]`.

- Rule 1: not structurally equal.
- Rule 2: `need` is an `OR`. Branch `{published:true}` against `{AND: held}` → Rule 3
  asks whether every alternative of the held `OR` implies `{published:true}`;
  `{authorId:"u1"}` does not → false. Branch `{authorId:"u1"}` against `{AND: held}` →
  Rule 3: the held `OR` has one alternative `{authorId:"u1"}`, and
  `ConstraintImplies({authorId:"u1"}, {authorId:"u1"})` is true by Rule 1 → true.
- One branch implied → Rule 2 succeeds.

Permitted. This is the case §6.6 is about, and the case a naive equality check kills.

### 7.5 A relation hop inside a batch-write filter — PERMIT (discharged against the related model's read reach)

```
updateMany(model: Post, where: { author: { is: { email: "a@b.c" } } }, …)
read(Post):   { OR: [ { published: true } ] }
update(Post): {}
```

References collected: `(Post, author)` and `(User, email)`. For the `User` entry, the
declared `SelectedRows.Model` is `Post` ≠ `User`, so `selectedRowsStayReadable` returns
true at the early-out and `User.email` discharges against `User`'s own read constraint.
Permitted, per §5.6(1).

Note that `(Post, author)` is still classified against `Post`, with the update
constraint as its selecting constraint — so a policy that hides the relation field
itself still refuses.

### 7.6 A write-only ability — REFUSE EVERY FIELD, INCLUDING THE PRIMARY KEY

```
grants: update Post, delete Post.  No read grant of any kind.
updateMany(where: { id: "p1" }, data: { published: true })
```

The classifier is asked to classify `["id"]` on `Post` for `read`. No read rule matches,
so the answer is `never` (or an empty map — both refuse, §4.2).

```
Cannot filter or order by field "id" on Post
```

**This is correct.** The reasoning, spelled out because it will be challenged:

- `where: { id: "p1" }` asks the database *does a row with this id exist, and does the
  caller's write constraint reach it*, and answers through `{ count: 1 }` versus
  `{ count: 0 }`.
- That is an existence oracle over the primary key — enumeration, when ids are
  meaningful (sequential ids, ids derived from an email, tenant ids).
- The caller has no `read` grant on `Post`. The policy's plain meaning is *this caller
  learns nothing about Post rows*. Answering the existence question contradicts it.
- There is no "but it's only the id" exemption, because the id is exactly what an
  enumerator wants.

Discharge (§5) never enters into it: the field is `never`, not `conditional`, so §4.2
refuses before §5 is consulted.

### 7.7 A read filtering on the same conditional field — PERMIT

```
read:   { OR: [ { published: true } ] }
update: {}
findMany(where: { title: { startsWith: "a" } })
```

The statement declares no selecting action, so `selectedRowsStayReadable` returns true
at the `Action == read` early-out. A read selects with the read constraint, which
discharges itself. Permitted — and note that this is permitted in the *same* engine and
*same* policy where §7.2 is refused. The difference is entirely which rows the
statement can touch.

---

## 8. Acceptance criteria

### 8.1 The testing rule that matters most

**Tests MUST assert on DISCLOSURE, not on error text.**

A test written like this is nearly worthless:

```
expect(query).rejects.toThrow('Cannot filter or order by field "note" on User')
```

It is satisfied by an implementation that classifies the field against **the wrong
model** and merely formats the right string — for instance one that walks the input
without tracking the relation hop, attributes `note` to the parent model, finds no such
field there, and refuses for a completely different reason while printing the model name
it happens to have in hand. That implementation refuses this query and **permits** the
next one. This exact weakness was found in the TypeScript version's own test suite and
fixed; do not reintroduce it in the port.

The test must instead assert that a **matching probe and a missing probe are
indistinguishable**.

### 8.2 The probe helper

```go
type disclosure struct {
    Answered bool
    Payload  string // canonical rendering of the result; empty unless Answered
}

func probe(run func() (any, error)) disclosure {
    result, err := run()
    if err != nil {
        return disclosure{}          // every failure collapses to one value
    }
    return disclosure{Answered: true, Payload: canonical(result)}
}
```

Points of the design, all deliberate:

- **Every error collapses to the same value.** The helper does not record the error
  type, code or message. If it did, the test could pass while the two probes produced
  *different* errors — which is itself a channel.
- **`canonical`** renders the payload deterministically (sorted keys, stable number
  formatting) so that two structurally identical answers compare equal.
- For batch writes, the payload is the **count**. For reads, the **row set**. For
  upserts, the **branch outcome**.

Each security test then does:

```go
hit  := probe(func() (any, error) { return attack("secret-b") })  // prefix that MATCHES a hidden value
miss := probe(func() (any, error) { return attack("zzz") })       // prefix that matches NOTHING

require.Equal(t, disclosure{}, hit)   // it refused
require.Equal(t, hit, miss)           // and the two are indistinguishable
```

Both assertions are required:

- `hit == disclosure{}` pins that the engine **refused** rather than answering both
  identically by coincidence of the data.
- `hit == miss` pins **indistinguishability**, which is the actual security property.

Write probes MUST additionally assert **no side effect**: the row count, and the
specific rows' values, are unchanged after both probes. §1.7's rollback channel is
invisible to the return value alone.

The helper must be used with a prefix that genuinely matches seeded data (`hit`) and one
that genuinely matches nothing (`miss`). A test where both prefixes miss proves nothing.

### 8.3 A second, separate assertion: the classifier spy

Disclosure tests prove the *behaviour*. They do not prove the field was classified
against the right model — an engine that refuses everything passes every disclosure
test. So each position also needs a white-box assertion on the classifier call:

```go
require.Contains(t, spy.Calls, classifyCall{
    Action: ActionRead, Model: "Post", Fields: []string{"note"},
})
```

with a companion **positive** test proving a readable field in the same position is
answered (otherwise "refuse everything" passes). The three together — disclosure,
classifier-spy, and positive answer — are what pin the behaviour.

Message-shape assertions are legitimate as **ergonomics** tests. Keep them in a
separate, explicitly-named group, and never let a message assertion stand in for a
disclosure assertion.

### 8.4 Named mutations

Each mutation below MUST cause at least one named test to fail. A port is not complete
until every one has been introduced deliberately and observed to fail.

**M1 — Misattribute a relation hop.**
Change the collector so a `RelationCondition`'s inner references are added under the
*parent* model instead of the target.

Must fail:
- disclosure test: filtering `Post` by `{ author: { is: { phone: startsWith } } }`, hit
  and miss must be indistinguishable and both refused;
- classifier-spy test: `ClassifyFields` must have been called with model `User` and
  field `phone`;
- the same at two relation hops (`User → posts → readingSessions → note`);
- the same for all five quantifiers (`is`, `isNot`, `some`, `every`, `none`) and for
  sibling keys beside a quantifier.

Note that a message-only test would *pass* under this mutation if the message is
formatted from a variable that happens to hold the right name. That is the point.

**M2 — Skip the `distinct` position.**
Remove `distinct` from the collected clauses.

Must fail:
- disclosure test: two reads narrowed to two rows each, one pair sharing a hidden
  value and one pair differing, `distinct` on the hidden field; the returned row counts
  must be indistinguishable and both refused;
- the same on `FindFirst`;
- the same for a `distinct` inside a relation entry of a projection, classified against
  the owning model;
- positive: `distinct` over an always-readable field still answers, and `distinct` over
  a conditional-and-discharged field still answers.

**M3 — Skip the nested-write position.**
Make the nested-write walker classify nothing (or drop one row of the nested-kind
table, e.g. `set` or `connectOrCreate`).

Must fail:
- disclosure test per nested kind (`update`, `updateMany`, `upsert`, `delete`,
  `deleteMany`, `connect`, `disconnect`, `set`, `connectOrCreate`): hit and miss
  indistinguishable, both refused, **and the database unchanged after both**;
- the same two relations deep;
- the same where the nested filter itself hops a relation;
- positive: a nested write filtered by a readable field still reaches the row checks
  (it may then fail on the row policy — that is the correct, different outcome).

**M4 — Discharge against the read constraint instead of the selecting one.**
Change `selectedRowsStayReadable` to always compare the read constraint with itself
(or ignore the declared `SelectedRows`).

Must fail:
- disclosure test with `read = published only`, `update = everything`: an `updateMany`
  filtered by a conditional field, hit and miss indistinguishable and both refused;
- the same for `deleteMany`, and for a single `Update` whose selector carries an
  ordinary filter;
- positive: matched reaches still run; write-narrower-than-read still runs; write-is-one-
  branch-of-read still runs; a plain read on the same field still runs.

**M5 — Make the implication check return `true` unconditionally.**
Replace `ConstraintImplies` with `return true`.

Must fail: the same disclosure tests as M4 (the write-wider-than-read cases), which is
the point — M5 and M4 are different ways to reach the same hole, and both must be
caught by behaviour, not by a unit test of the checker alone.

Add the inverse mutation, **M5′ — return `false` unconditionally**: the positive tests
of M4 must fail. Without M5′ the suite would accept a check that simply refuses
everything.

**M6 — Fail open on a missing classification entry.**
Change §4.2 so a field with no entry in the returned map is permitted.

Must fail: a disclosure test whose provider returns a classification map that omits the
queried field entirely (a realistic provider bug); the query must still be refused.

**M7 — Skip the `_relevance` payload.**
Make the collector treat `_relevance` as an opaque ordering key.

Must fail:
- disclosure test: `orderBy: [{ _relevance: { fields: ["secret"], … } }]` must be
  refused;
- validation test: a relevance ordering naming **no** fields must be a validation error,
  not a silent permit;
- positive: relevance over readable fields still orders.

**M8 — Pass unknown keys through.**
Remove the "no such field" checks.

Must fail: validation tests for an unknown key in `where`, in an `OR` branch, in
`orderBy`, in `cursor`, in `distinct`, in a relation-hopped filter, and in a nested-write
filter. Each must raise a validation error and must not reach the query layer.

**M9 — Classify after the statement instead of before.**
Move the classification call to after execution.

Must fail:
- the "no side effect" half of every write disclosure test;
- the compiled-read test asserting that a refused nested filter emits **no compilation
  event at all**;
- the upsert test asserting the probe never ran.

**M10 — Drop the unique-selector position.**
Stop classifying the `where` of `Update`, `Delete` and `Upsert`.

Must fail: disclosure tests for `update`/`delete`/`upsert` whose unique selector carries
an ordinary filter beside the unique field (`{ id: "p1", secret: { startsWith: "a" } }`),
and the upsert existence-oracle test in which an upsert against a taken unique key and
an upsert against a free unique key must produce the same outcome for a caller who may
not update the existing row.

**M11 — Widen `sameConstraint` with `reflect.DeepEqual`.**
Must fail: a discharge test whose constraints carry a timestamp produced through two
different paths (one with a monotonic reading, one round-tripped through the provider),
which must still compare equal and permit.

### 8.5 Coverage matrix

Every cell must have at least one disclosure test:

| position | never-readable | conditional, undischarged | conditional, discharged (positive) | always (positive) |
|---|---|---|---|---|
| root `where` | ✓ | ✓ | ✓ | ✓ |
| root `orderBy` | ✓ | ✓ | ✓ | ✓ |
| root `cursor` | ✓ | — | — | ✓ |
| root `distinct` | ✓ | ✓ | ✓ | ✓ |
| relation-entry `where`/`orderBy`/`cursor`/`distinct` | ✓ | ✓ | ✓ | ✓ |
| relation-count `where` | ✓ | — | — | ✓ |
| relation hop (all five quantifiers) | ✓ | — | ✓ | ✓ |
| `updateMany` / `deleteMany` `where` | ✓ | ✓ | ✓ | ✓ |
| `update` / `delete` / `upsert` selector | ✓ | ✓ | — | ✓ |
| nested write (each of the nine kinds) | ✓ | — | — | ✓ |
| write projection (`create`/`update`/`delete` return tree) | ✓ | — | — | ✓ |
| `_relevance` | ✓ | — | — | ✓ |
| feature switch off | permits everything (documented footgun) | | | |

---

## 9. Summary for the implementer

1. A filter is a read. Classify for `read`, always, everywhere.
2. Collect `(model, field)` from every row-selecting and row-ordering position. In Go
   the nodes carry their model, so the collector is a plain recursive walk and
   misattribution is impossible — but only if you never re-derive the model from a
   metadata lookup.
3. `always` permits, `never` refuses, missing refuses, `conditional` refuses unless
   discharged.
4. Discharge means the constraint that **selects the rows the statement touches**
   implies the **read** constraint.
5. The implication check is conservative: prove it or refuse it. Never loosen it to make
   a query pass; add a truth-preserving rule instead.
6. Refuse before anything runs, before anything compiles, before any transaction opens.
7. Name the field, the model, and the dependency in the message.
8. Test disclosure, not error strings.

# 04 — Statement Shape: how a read becomes SQL

> **Specification status:** detailed supporting research. The merged
> [`BIBLE.md`](./BIBLE.md) is authoritative, especially for SQLite/PostgreSQL
> parity, exact-value decoding, bounded relation loading, and refusal instead of
> an unspecified fallback.

Status: specification, Go target, Postgres-first.
Ground truth: the TypeScript implementation at `typescript/packages/core/src` and
`typescript/packages/policy/src`, released as 0.6.1.

This document specifies the compiled read path: the thing that turns a request for
rows into SQL, folds the caller's filter and the authorization policy into the same
statement, aggregates relations, masks columns the caller may read only sometimes,
and decodes the result back into an object graph.

golem's central claim is **one statement per request, with permissions inside it**.
This document is that claim made precise, including the two places it is
deliberately relaxed and exactly why.

You should not need to read the TypeScript. Where the TypeScript does something
non-obvious, this document says what breaks if you do the obvious thing instead.

---

## 0. Vocabulary and standing invariants

| Term | Meaning |
| --- | --- |
| **read** | A `findMany` / `findFirst` / `findUnique` request: a root model, a projection, an optional filter, ordering, and pagination. |
| **projection** | What the caller asked for: scalar columns, relations, relation counts. Expressed as `select` (allow-list), or `include` + `omit` (all scalars, minus omissions, plus relations). |
| **row predicate** | The boolean condition deciding whether a row is visible. It is the conjunction of the caller's `where` and the model's read **policy constraint**. |
| **policy constraint** | A condition tree the authorization provider returns for `(action=read, model, caller)`. Data, not a closure. See doc 02. |
| **field policy** | A per-column decision: always readable, never readable, or readable **on some rows** (conditional). A conditional field carries its own condition tree. |
| **mask** | The SQL that renders a conditional field: the true value where the condition holds, `NULL` elsewhere. |
| **withhold** | Not projecting a column at all, and refusing any statement that references it. |
| **defer** | Golem could not render a mask in SQL, so it fetches the true value and nulls it in memory, and *reports* that it did. |
| **plan** | The immutable description of the statement(s) to run, plus how to decode the rows. Built once, executed once. |
| **fallback** | The planner refusing to compile this request at all, with a reason and a human-readable detail. The caller runs it some other way. |

Three invariants hold everywhere in this document and are not negotiable:

1. **Two-valued operators.** Every comparison golem emits is null-safe: it is `true`
   or `false`, never `unknown`. Equality renders as `IS NOT DISTINCT FROM` on
   Postgres and `IS` on SQLite. A `NOT` over a predicate therefore means what it
   says. Nothing downstream — a mask, an `EXISTS`, a `CASE` — has to reason about
   three-valued logic.
2. **A filter is a read.** If the caller filters, orders, or de-duplicates on a
   column, the caller has read that column, whatever the projection says. Golem
   refuses the whole request (a forbidden error, not a fallback) when a caller
   filters or orders on a column that is only conditionally readable, unless the
   field's condition is *discharged by the row predicate* — implied by it, so the
   field is readable on every row the read can return. This refusal happens before
   planning. Consequences of this rule show up in §6.
3. **Loading strategy never changes an answer.** §4 introduces a second statement
   for some relations. It changes how many statements reach the database and
   nothing else. Any behaviour that differs between the strategies is a bug, and
   §9 names the mutation that must catch it.

Postgres is the target. A dialect seam exists (`Dialect` below); SQLite is the other
implementation behind it and each place where SQLite forced a different decision is
called out, because those places are where a Go implementation is most likely to
"simplify" something load-bearing.

```go
type Dialect interface {
    Name() string                              // "postgres" | "sqlite"
    Placeholder(position int) string           // "$1"    | "?"
    QuoteIdent(s string) string                // "x"     | "x"
    NullSafeEquality() string                  // "IS NOT DISTINCT FROM" | "IS"
    BinaryCollation() string                   // `"C"`   | "BINARY"
    JSONArrayAgg(inner, alias string) string   // §2
    JSONObject(inner, alias string) string     // §2
}
```

---

## 1. The shape of a generated read

### 1.1 The example schema

Everything below uses this schema. It is the live oracle fixture, reduced.

```
users     (user_id PK, name, tenant_id)
profiles  (profile_id PK, bio, user_id UNIQUE → users.user_id)
posts     (post_id PK, title, author_id → users.user_id, published, views, secret_note)
           INDEX posts_author_id_idx (author_id)
metrics   (metric_id PK, label, owner_id → users.user_id, note, rank_value,
           score NUMERIC, hits BIGINT, ratio, active, recorded_at)
           -- no index on owner_id
```

Note the asymmetry that drives §4: `posts.author_id` is indexed, `metrics.owner_id`
is not.

### 1.2 The request

```
findMany User
  where:   { tenantId: 1 }                     // caller
  policy:  User → { tenantId: 1 }              // authorization provider, read/User
  orderBy: [{ name: 'asc' }]
  take:    20
  select:
    id, name
    profile: { select: { id, bio } }                      // to-one
    posts:   { select: { id, title, secretNote },         // to-many, indexed FK
               where: { published: true },                //   caller filter + Post policy
               orderBy: [{ views: 'desc' }],
               take: 5, skip: 10 }
    metrics: { select: { id, label } }                    // to-many, UNindexed FK
  field policy:
    Post.secretNote → conditional on { published: true }
```

### 1.3 The statement golem generates (Postgres)

```sql
select
  "t0"."user_id" as "id",
  "t0"."name"    as "name",
  cast((select to_json(obj) from (
      select "t1"."profile_id" as "id",
             "t1"."bio"        as "bio"
      from "profiles" as "t1"
      where "t1"."user_id" = "t0"."user_id"
  ) as obj) as text) as "profile",
  cast((select coalesce(json_agg(agg), '[]') from (
      select "t2"."post_id" as "id",
             "t2"."title"   as "title",
             case when ("t2"."published" IS NOT DISTINCT FROM $1)
                  then "t2"."secret_note" else null end as "secretNote"
      from "posts" as "t2"
      where ("t2"."published" IS NOT DISTINCT FROM $2)
        and "t2"."author_id" = "t0"."user_id"
      order by "t2"."views" desc
      limit $3 offset $4
  ) as agg) as text) as "posts"
from "users" as "t0"
where ("t0"."tenant_id" IS NOT DISTINCT FROM $5)
order by "t0"."name" asc
limit $6
```

Parameters, in bind order: `[true, true, 5, 10, 1, 20]`.

Then, once, keyed by the parent ids the first statement returned:

```sql
select "t3"."metric_id" as "id",
       "t3"."label"     as "label",
       "t3"."owner_id"  as "ownerId"
from "metrics" as "t3"
where "t3"."owner_id" in ($1, $2, /* … up to 900 */)
```

`ownerId` is the injected correlation key. The caller did not ask for it. It is
aliased under its ordinary model field name — not a decorated one — and recorded in
the batch's `Drop` list so it is deleted from each child row after the children have
been attached. The parent side needed no injection here because the caller already
selected `User.id`; had it not, `"t0"."user_id" as "id"` would have been added to
the root projection and to the root's `Drop` list. **Injection is idempotent**: if
the column is already projected under that name, nothing is added and nothing is
dropped. Emitting it twice is a duplicate output column, which the decoder cannot
tell from a projection bug.

Two statements, not five. The count is bounded by the number of *batched relation
levels*, never by the number of rows. §4 says which relations are batched and §5
says how the second statement works.

| Fragment | What it is | Why it is shaped that way |
| --- | --- | --- |
| `"users" as "t0"` | The root table under the reserved root alias. | The root alias is fixed so a policy predicate can be rendered against it without knowing the projection. |
| `"t0"."user_id" as "id"` | A scalar column, physical name → logical name. | Every projected column is aliased to its **model field name**, so decoding never consults the schema mapping again. A model that carries no physical name is a fallback, never a guess. |
| `"t3"."owner_id" as "ownerId"` (second statement) | A *correlation key* the caller did not ask for. | Added because `metrics` is batched and must be re-attached to its parent by key. Injected keys are recorded in the plan's `Drop` list and deleted from the decoded row before the caller sees it. See §5.4. |
| `case when (…) then "t2"."secret_note" else null end` | The mask. | The value never leaves the database on a row where the caller may not read it. One construction site renders this for every depth and both loading strategies (§6). |
| `(select to_json(obj) from (…) as obj)` | A to-one relation. | A correlated scalar subquery. Zero matching rows ⇒ the subquery yields no row ⇒ SQL `NULL` ⇒ decodes to a nil relation, which is the right answer for a to-one. |
| `(select coalesce(json_agg(agg), '[]') from (…) as agg)` | A to-many relation. | Aggregate with no `GROUP BY` ⇒ always exactly one row ⇒ never a missing row, but `json_agg` over zero rows is `NULL`, so the `coalesce` is mandatory (§2.2). |
| `cast(… as text)` around the whole aggregate | Hand the driver a string, not JSON. | golem parses the JSON itself. Left as `json`, `pgx` decodes numbers with its own rules and the numeric-type guarantees of §7 are lost before golem sees them. |
| `"t2"."author_id" = "t0"."user_id"` | The correlation. | Added *after* the relation's own `where`, so bind order is: relation projection, relation filter, correlation, paging. |
| `("t2"."published" IS NOT DISTINCT FROM $2)` inside the subquery | The **related model's** policy, folded in. | A relation is a read of the related model. Its policy belongs inside its subquery, not in the outer `WHERE`, where it would filter parents. |
| `limit $3 offset $4` inside the subquery | Per-parent paging. | A correlated subquery is evaluated once per parent row, so `LIMIT` there is naturally per parent. §5.3 is about preserving that when the relation is batched instead. |
| `("t0"."tenant_id" IS NOT DISTINCT FROM $5)` | Caller `where` ∧ root policy, one predicate. | Merged as `{AND: [where, constraint]}` before rendering, so a policy can never be short-circuited by the shape of a caller filter. |
| `limit $6` | Root paging. | Pushed into SQL **unless** the read is `distinct` (§1.6). |

### 1.4 Alias allocation

Aliases must be a deterministic, collision-free function of the final projection.
Two statements built from the same request must be textually identical, so that
statement text can be logged, diffed, and cached.

| Alias | Namespace | Allocation |
| --- | --- | --- |
| `t0` | root | Reserved. Never allocated to anything else. |
| `t1`, `t2`, … | relation subqueries and relation-count subqueries | One shared counter starting at 1, incremented in **pre-order depth-first** traversal of the projection, in projection key order. A relation takes its alias *before* its children are planned, so a parent's number is always lower than its descendants'. |
| `o1`, `o2`, … | join aliases introduced by `orderBy` through a relation | The **same** counter as `t*`. `t` and `o` share a numbering sequence; do not give them separate counters, or a log line stops identifying a subquery uniquely. |
| `c0` | the cursor anchor subquery | Fixed. There is at most one. |
| `t0_1`, `t0_2`, `t2_1`, … | relation hops *inside a rendered condition* (policy or mask) | `<owning alias>_<hop depth>`. The underscore is what keeps this namespace disjoint from `t1`/`t2`. Verify this in your alias allocator; it is the only thing preventing a policy's `EXISTS` from capturing a projection subquery's alias. |
| `agg`, `obj` | the derived table inside a JSON aggregate | Fixed names in the TypeScript. **In Go, allocate `a<n>` from the same counter instead.** The fixed names work only because each is bound in its own subquery; a unique name removes the reasoning step entirely. |
| `g0`, `g1`, … | the inner alias of a *scoped* derived table | §8. Positional, by root index. |

**Replanning resets the counter.** §6.5 describes a case where the planner
discovers that some columns it planned are no longer needed and replans. When that
happens, the alias counter is reset to 1 and the *whole* node tree is planned again.
Do not try to patch the first plan. Aliases must be a function of the final
projection only.

### 1.5 The plan

The planner is pure: metadata in, plan out, no database access. The plan is what the
executor and the decoder both read.

```go
type Plan struct {
    SQL        string
    Params     []any
    Reversed   bool               // reverse the root rows after everything else (§1.7)
    Columns    []Column           // projected scalars, in select order
    Counts     []CountColumn      // relation counts, decoded out of `_count$<rel>`
    Relations  []NestedProjection // JSON-aggregated relations and how to decode them
    Batches    []Batch            // relations loaded by a second statement (§5)
    Drop       []string           // injected columns to delete after decoding
    Distinct   *DistinctPlan      // in-memory de-duplication (§1.6)
    Masked     []string           // dotted paths masked in SQL — for observability
    Deferred   []DeferredMask     // masks that could not be rendered (§6.4)
}
```

`Masked` and `Deferred` are not decoration. They are the read's own account of where
authorization was enforced, and the acceptance tests in §9 assert on them.

### 1.6 `distinct` is not `SELECT DISTINCT`

When the request carries `distinct: [fields…]`, golem does **not** emit
`SELECT DISTINCT` or `DISTINCT ON`. It:

1. emits the statement with the distinct keys added to the projection (injected and
   added to `Drop` if the caller did not ask for them),
2. does **not** push `LIMIT`/`OFFSET` into SQL,
3. de-duplicates in memory, keeping the **first** row of each key tuple in the order
   the statement returned,
4. then applies `OFFSET` and `LIMIT` to the de-duplicated list.

Pushing `LIMIT` into SQL here is wrong: the engine would apply it before
de-duplication and the caller would get fewer than `take` rows. This ordering is
also why a masked column can never be a distinct key (§6.4).

### 1.7 Reversal

A negative `take` means "the last *n* in this order". golem flips every `ORDER BY`
direction in SQL, applies `LIMIT |take|`, and reverses the decoded rows in memory
afterwards. A negative `take` with no order at all is a fallback, not a guess; a
paged relation with no order borrows the related model's primary key as an implied
ascending order, and if it has none, that is a fallback too.

`NULLS FIRST` / `NULLS LAST` is emitted as an additional leading order term of the
form `case when <col> is null then 0 else 1 end` (values swapped for the other
direction), and the choice is *also* flipped under reversal. Do not emit the
dialect's `NULLS FIRST` keyword directly: SQLite has no such clause, and the seam is
not worth splitting for one term.

---

## 2. Relations as correlated subqueries

Every relation in the projection becomes a **correlated scalar subquery in the
select list** of its parent. Never a join in the parent's `FROM`.

### 2.1 To-many

```sql
(select coalesce(json_agg(agg), '[]')
   from (
     select <projected columns of the child, masked>,
            <nested relation aggregates>
     from <child table> as <child alias>
     where (<child model's row predicate>)
       and <child alias>.<fk> = <parent alias>.<pk>      -- one AND per key column
     order by <child order terms>
     limit <n> offset <m>
   ) as agg)
```

On SQLite the aggregate is `coalesce(json_group_array(json_object('k', "agg"."k", …)), '[]')`,
which enumerates the keys explicitly and therefore has a hard ceiling: a stock SQLite
build guarantees `json_object` only 126 arguments, so **63 projected entries**
(columns plus nested relations) per relation level. Exceeding it is a fallback, not a
truncation. Postgres has no equivalent limit because `json_agg(agg)` aggregates the
whole derived-table row and takes the aliases as keys.

`jsonb_agg` / `to_jsonb` are acceptable substitutes for `json_agg` / `to_json` on
Postgres, with one caveat that turns out to be a feature: `jsonb` normalizes numeric
literals and de-duplicates and reorders keys. De-duplication and reordering are
harmless — aliases are unique within a level and decoding is by name — but the
numeric normalization means that **if you use `jsonb` the text cast of §7 is not an
optimization, it is the only thing standing between a `bigint` and a silently
reformatted number**. Specify one and hold it; do not let it vary by code path.

### 2.2 Why the `coalesce` is not optional

`json_agg` over zero rows returns SQL `NULL`, not `'[]'`. The subquery still returns
exactly one row — it is an aggregate with no `GROUP BY` — so the parent gets a
column, and that column is `NULL`.

Without the `coalesce`, a parent with no children decodes to a *null relation*
rather than an *empty list*. In Go that is the difference between a `nil` slice and
a `[]T{}`, which is observable: `encoding/json` renders them as `null` and `[]`, a
GraphQL non-null list field errors on one and not the other, and `for range` over
either is fine so the bug does not surface until it reaches a client.

Do not fix this in the decoder ("if it is not an array, use an empty slice"). A
decoder that repairs `NULL` cannot distinguish "no children" from "the relation
column is missing from the result set because the plan and the statement disagree" —
which is exactly the class of bug the acceptance tests in §9 exist to catch. Put the
`coalesce` in the statement, inside the subquery, and let a `NULL` to-many stay an
error.

### 2.3 To-one

```sql
(select to_json(obj)
   from (
     select <projected columns of the child, masked>,
            <nested relation aggregates>
     from <child table> as <child alias>
     where <child alias>.<pk> = <parent alias>.<fk>
   ) as obj)
```

No aggregate, so zero matching rows means the subquery produces no row and the
scalar subquery is `NULL`. That is correct: a to-one with no partner *is* null. No
`coalesce`.

A to-one relation may **not** carry a `where` in the projection. That is a fallback,
because the upstream API rejects it rather than treating it as a filter, and
compiling it would make golem answer a question the reference implementation refuses
to answer. A to-one whose related model carries a *conditional row policy* is handled
elsewhere: the policy cannot go in a `where` here, so the read tree records a
post-hoc check and the relation is nulled in memory for rows the policy denies. If
the authorization provider cannot answer per-instance questions at all, the whole
traversal is refused.

### 2.4 Nesting

A relation's own `where`, `orderBy`, `take` and `skip` nest inside *its* subquery
and nowhere else:

- `where` → the subquery's `WHERE`, added **before** the correlation predicates.
- `orderBy` → the subquery's `ORDER BY`, over the child alias. Ordering a relation
  *by a further relation* is a fallback: golem compiles relation-ordering only at the
  top level of a read.
- `take` / `skip` → the subquery's `LIMIT` / `OFFSET`, which a correlated subquery
  evaluates once per parent, so they are per-parent for free.
- On SQLite, `OFFSET` without `LIMIT` is a syntax error; emit `limit -1` first. This
  is the dialect seam earning its keep — do not let it leak into the planner.

Nested relations recurse: a relation inside a relation is another correlated
subquery in the inner subquery's select list, and its JSON lands nested inside the
parent's JSON.

### 2.5 Relation counts

`_count: { select: { posts: true } }` becomes:

```sql
(select count(*) from "posts" as "t4"
  where "t4"."author_id" = "t0"."user_id" and (<optional per-count where>)) as "_count$posts"
```

Constraints:
- Only at the **top level** of a read. A count of a relation of a relation is a fallback.
- Only on to-many relations.
- The result column is `_count$<relationName>`. If that exceeds **63 bytes**,
  fall back: Postgres silently truncates longer identifiers and two counts would
  collide into one column. The same 63-byte check applies to aggregate measure
  aliases (§7.4) and to group-by key names.

Decode `count(*)` as §7.2 specifies — it is a Postgres `bigint`, not an `int`.

---

## 3. Why correlated `EXISTS`, not a join

This section is about **relation filters**: a condition on the parent that talks
about a related row, either in the caller's `where` or in a policy constraint.
`{ author: { is: { name: 'Ada' } } }`, `{ posts: { some: { published: true } } }`.

golem renders every one of them as a correlated `EXISTS`:

```sql
EXISTS (SELECT 1 FROM "users" AS "t0_1"
         WHERE "t0_1"."user_id" = "t0"."author_id"
           AND (<the inner condition>))
```

and a negative one (`none`, `isNot`, a hop under `NOT`) as `NOT (EXISTS (…))`. The
hop alias is `<owning alias>_<depth>`, so hops nest without ever colliding with the
projection aliases of §1.4.

**This is a correctness property, not a performance preference.**

### 3.1 The failure

A join filters the row set *before* the surrounding boolean expression is evaluated.
A disjunction evaluated after a join can never rescue a row the join already
removed.

Make `posts.author_id` nullable and take this policy:

```
Post read policy: { OR: [ { author: { is: { name: 'Ada' } } },
                          { published: true } ] }
```

with rows:

| post_id | author_id | published |
| --- | --- | --- |
| 1 | 1 (Ada) | false |
| 2 | NULL | **true** |

The correct answer is **both rows**. Post 2 has no author, so the first branch is
false for it, but it is published, so the second branch grants it.

Written as a join:

```sql
-- WRONG
select p.* from posts p
  join users u on u.user_id = p.author_id
 where u.name IS NOT DISTINCT FROM 'Ada' or p.published IS NOT DISTINCT FROM true
```

`p.author_id` is `NULL` for post 2, the join condition is not satisfied, and post 2
is gone before `or p.published` is ever considered. The statement returns post 1
only. A published post has been hidden from a caller the policy grants it to.

Written as `EXISTS`:

```sql
select p.* from posts p
 where EXISTS (select 1 from users u
                where u.user_id = p.author_id
                  and u.name IS NOT DISTINCT FROM 'Ada')
    or p.published IS NOT DISTINCT FROM true
```

Post 2 stays in the row set, the `EXISTS` evaluates to `false` for it, the `or`
branch evaluates to `true`, the row is granted. Two rows, correct.

### 3.2 The `LEFT JOIN` is not a fix

Switching to `LEFT JOIN` preserves post 2, and it breaks two other things:

- **Cardinality.** A `LEFT JOIN` onto a to-many multiplies the parent row by the
  number of matching children. `LIMIT 20` no longer means twenty parents. `count(*)`
  over the result no longer counts parents. Adding `DISTINCT` to repair the count
  changes which rows `LIMIT` keeps and interacts with the ordering.
- **Negation.** `NOT (u.name IS NOT DISTINCT FROM 'Ada')` over a left-joined row
  whose right side is entirely null is not the same predicate as "there is no
  related user named Ada". The anti-join formulation (`LEFT JOIN … WHERE u.pk IS
  NULL`) expresses only the *unconditional* case; it cannot express "no related row
  satisfying P" without a second correlated condition, at which point you have
  written the `EXISTS` anyway.

`EXISTS` has none of these problems because it is a *predicate on the parent row*.
It composes with `AND`, `OR` and `NOT` the same way any other predicate does, and it
does not touch cardinality. That is the whole argument. Any performance benefit of a
join is irrelevant if it answers a different question.

### 3.3 Consequences for the implementation

- A relation filter is compiled by the **condition renderer** (doc 02/03), which
  produces a predicate node. The statement planner in this document splices that
  node into a `WHERE` and never rewrites it into a join.
- The only joins golem's read path ever emits are the `o*` aliases from §1.4 —
  `LEFT JOIN`s introduced by `orderBy` through a *to-one* relation, and a grouped
  derived table for `orderBy: { rel: { _count: 'asc' } }`. Those are joins on a
  to-one (cardinality-preserving) and on a pre-aggregated single-row-per-key derived
  table (cardinality-preserving), used only in `ORDER BY`, never to filter. The
  `_count` term additionally orders on `coalesce(<count>, 0)` so a parent with no
  children sorts as zero rather than as null.
- Ordering by a relation is only compiled at the **top level**. Inside a relation
  subquery it is a fallback.

---

## 4. The loading-strategy chooser

A correlated subquery is evaluated **once per parent row**. When the child's foreign
key is indexed, that is an index seek per parent — cheap and bounded. When it is
not, it is a **full scan of the child table per parent row**: a 20-row page over a
million-row child table is twenty million-row scans in one statement, and it will
look to an operator like the database has hung.

Foreign keys are not indexed by default in most schema toolchains. So golem reads
index metadata and, when the key it would correlate on carries no usable index,
loads that relation with a **second statement keyed by the parents** instead —
bounded by relation *level*, not by row.

### 4.1 The index predicate

```go
// EqualityIndexed reports whether an equality lookup on model.field can use an index.
func EqualityIndexed(m *Model, field string) bool {
    for _, ix := range m.Indexes {
        if ix.Kind != IndexFullText && len(ix.Fields) > 0 && ix.Fields[0] == field {
            return true
        }
    }
    if m.PrimaryKey != nil && len(m.PrimaryKey.Fields) > 0 && m.PrimaryKey.Fields[0] == field {
        return true
    }
    for _, u := range m.UniqueIndexes {
        if len(u.Fields) > 0 && u.Fields[0] == field {
            return true
        }
    }
    f := m.Field(field)
    return f != nil && (f.IsID || f.IsUnique)
}
```

Only the **leading** column of a composite index counts: an index on `(a, b)`
accelerates an equality lookup on `a` and does not accelerate one on `b`. A
full-text index does not serve an equality lookup and is excluded.

This is metadata the Go generator must emit from struct tags — see doc 05. Zero
runtime reflection: indexes are as much part of the generated model description as
columns are. **A generator that omits indexes silently degrades every read to the
batched path**, because `EqualityIndexed` then returns false for everything. Make
"indexes present" an assertion of the generated-code test suite.

### 4.2 The decision rule

For each relation, in the order the projection names it:

```
shape(relation, parentShape) =
    "json"  if parentShape == "json"
    "json"  if !relation.IsList
    "json"  if len(relation.Correlation) != 1
    "json"  if kindOf(childKeyColumn) != Plain      // see §7.1
    "json"  if EqualityIndexed(childModel, childKeyField)
    "row"   otherwise
```

The root node's shape is `"row"`.

In words: a relation is batched (`"row"`) only when **all** of the following hold.

1. **It is to-many.** A to-one correlated subquery evaluates once per parent and
   returns at most one row; there is nothing to batch and an `IN`-keyed second
   statement would not be cheaper.
2. **It correlates on exactly one column.** A multi-column key would need a
   row-constructor `IN`, whose index behaviour is engine-specific and which SQLite
   does not support in the same form.
3. **The key column is a "plain" scalar** — `String`, `Int`, `Float`, or an enum.
   Not `BigInt`, `Decimal`, `DateTime`, or `Boolean`. Those either lose precision or
   change representation on the way to being a bind parameter and back, and the
   batched path re-attaches children to parents by comparing key *values* in memory
   (§5.4). A key that does not round-trip exactly cannot be compared exactly.
4. **The foreign key is not equality-indexed.** This is the actual trigger. An
   indexed key stays in the single statement.
5. **Its parent is itself row-shaped.** Once a relation is inside a JSON aggregate,
   its children are inside that JSON too. You cannot key a separate statement off
   rows that only exist inside a `json_agg`. So batching is available only along an
   unbroken chain of row-shaped nodes from the root.

Nothing else — not `take`, not `where`, not depth — influences the choice.

### 4.3 What this changes and what it does not

It changes **the number of statements**. It never changes an answer. The batched
statement projects the same columns, applies the same relation `where` and the same
related-model policy, renders the same masks, and orders by the same terms. The only
thing it cannot carry is `LIMIT`/`OFFSET`, which §5.3 handles.

State this to operators plainly, as the release notes do: **if you want the
single-statement shape, index the foreign key.** golem will use it, immediately,
with no configuration.

### 4.4 The cost you are accepting

The batched statement fetches **every** child of every parent in the chunk and slices
per parent in memory (§5.3). For a relation with `take: 5` over 900 parents each
having 10 000 children, that is nine million rows fetched to return four and a half
thousand. This is the same thing the reference implementation does, and it is the
price of not scanning the child table once per parent. It is another reason to say
"index the foreign key" loudly.

---

## 5. The batched path in detail

### 5.1 The plan node

```go
type Batch struct {
    Path       string             // dotted path, e.g. "metrics" or "metrics.tags"
    Name       string             // the relation field name on the parent
    ParentKey  string             // model field name on the parent, e.g. "id"
    ChildKey   string             // model field name on the child,  e.g. "ownerId"
    Relations  []NestedProjection // JSON relations nested under this batched child
    Batches    []Batch            // further batched relations under this one
    Drop       []string           // injected columns to delete from the child rows
    Limit      *int
    Offset     *int
    Reversed   bool
    Build      func(keys []any) (sql string, params []any)
}
```

`Build` renders the child `SELECT` — the same child `SELECT` the correlated form
would have used, projection, masks, relation `where`, related-model policy and
`ORDER BY` included — with the correlation replaced by `<child alias>.<fk> IN (…)`
and **no `LIMIT` and no `OFFSET`**.

### 5.2 Chunking, and why it is not optional

```go
const BatchChunk = 900
```

Parent keys are split into runs of at most `BatchChunk` and one statement is issued
per run. `ceil(len(keys) / 900)` statements per batched relation level.

**The ceilings.** Postgres's extended query protocol encodes the parameter count as
a signed 16-bit integer: **65 535 bind parameters per statement**, hard. SQLite's
`SQLITE_MAX_VARIABLE_NUMBER` defaults to **999** in builds before 3.32 and **32 766**
after; both are still in the field. 900 clears the lowest of these with about 99
parameters of headroom for the relation's own filter and policy parameters, which
are bound in the same statement. Do not raise it to "just under 65 535" because
Postgres is the target: the constant is shared, and the headroom is what makes it
safe to add a policy predicate to a batched relation without recomputing anything.

**This bug shipped in TypeScript and survived, because every fixture was small.**
Unchunked, the implementation put every parent key into one `IN` list. Every test
had a handful of parents, so the list never approached any ceiling, and the whole
test suite passed against a live database while the code was one large page away
from a hard driver error in production. The lesson is a testing lesson: the chunking
tests seed **33 000** parents for SQLite and **70 000** for Postgres — deliberately
past each engine's real ceiling — precisely so that a regression is a failure rather
than a fixture that happens to be small. Port those magnitudes, not just the logic.

Two short-circuits, both before any statement is issued:
- If no parent has a non-null key, issue nothing and attach empty lists.
- If `Limit` is `0`, issue nothing and attach empty lists.

### 5.3 Limit, offset and reversal apply PER PARENT

The batched statement has no `LIMIT`. Applying the relation's `take` across the
batch would return `take` children *in total*, distributed by whatever order the
engine returned, instead of `take` children *for each parent*. It is not an
approximation of the right answer, it is a different answer, and with `skip` it is a
different answer that looks plausible.

So: fetch all children for the chunk, ordered by the relation's own `ORDER BY`, then
per parent:

```go
func sliceFor(rows []Row, limit, offset *int, reversed bool) []Row {
    start := 0
    if offset != nil { start = *offset }
    if start > len(rows) { start = len(rows) }
    end := len(rows)
    if limit != nil && start+*limit < end { end = start + *limit }
    out := append([]Row(nil), rows[start:end]...)
    if reversed { reverse(out) }
    return out
}
```

Order of operations, and it matters: **offset, then limit, then reverse.** Reversal
last is what makes a negative `take` mean "the last *n*", because the statement
already flipped the `ORDER BY` directions (§1.7).

The chunk boundary must be invisible. A parent that happens to be the 901st parent —
the first of the second chunk — must get exactly the same children as the first
parent of the first chunk would with the same data. Its slice is computed from its
own bucket, never from a running counter across the batch. §9's `BATCH_WIDE_LIMIT`
mutation is exactly this.

### 5.4 Attaching children to parents

Children are grouped by the **value** of `ChildKey`, and each parent claims the
bucket matching the value of its `ParentKey`.

Comparison is by a **canonical value identity**, not by Go `==` on `any` and not by
string formatting. Two values are the same key iff they are the same type-and-value:

```go
func keyIdentity(v any) string {
    switch x := v.(type) {
    case nil:              return "n"
    case time.Time:        return "d" + strconv.FormatInt(x.UnixMilli(), 10)
    case int64:            return "i" + strconv.FormatInt(x, 10)
    case float64:          return "f" + strconv.FormatFloat(x, 'g', -1, 64)
    case bool:             if x { return "b1" }; return "b0"
    case string:           return "s" + x
    default:               return "o" + fmt.Sprint(x)
    }
}
```

The type tag prevents the integer `1` and the string `"1"` from colliding, which is
reachable whenever a schema has an `Int` key on one side of a relation and the
driver hands back a numeric string on the other. In Go, normalize driver output to
canonical types *before* computing identity; do not let `int32` and `int64` produce
different tags for the same key.

**A parent with no children gets an empty, non-nil list.** Never `nil`, never a
missing field, for the same reason the `coalesce` of §2.2 is mandatory. A parent
whose key is `NULL` also gets an empty list: its key was never sent to the database
(null keys are excluded from the `IN` list), and no child can match it because
`x IN (…)` is never true for `NULL x`.

Deduplicate the keys before binding them. Many parents share a foreign key value;
binding it once per parent burns the parameter budget and multiplies the chunk count
for no benefit.

### 5.5 Ordering of work

For one batched relation:

1. Collect distinct non-null parent keys, in parent order.
2. For each chunk, render and run the child statement; accumulate rows.
3. Decode the JSON relations nested inside those child rows (§7).
4. **Recurse**: run this relation's own nested batches against the child rows.
5. Group the child rows by `ChildKey`.
6. Strip the child rows' injected `Drop` columns.
7. Slice per parent and attach.

Steps 4 and 5 are in that order because a nested batch keys off a column of the
child rows, which step 6 is about to delete. Step 6 before step 5 would delete the
grouping key. Step 6 before step 4 would delete a nested batch's parent key. This
ordering is fragile-looking and is load-bearing; write the test that fixes it.

At the root, the same sequence runs after the root statement: decode counts, decode
JSON relations, apply `distinct` (§1.6), run the batches, strip the root's injected
columns, then reverse if `Reversed`.

---

## 6. Masking

A field the caller may read only on some rows is masked **in the statement**, at
every depth and on both loading strategies.

### 6.1 The form

```sql
case when (<the field's condition, rendered against this node's alias>)
     then <the column reference, with any type cast from §7 applied>
     else null end as "<field name>"
```

The condition is rendered with **`absent` = deny-all**: if the condition tree is
empty or missing, the mask renders as `false` and the column is null everywhere.
This is the opposite of the row predicate, where an absent constraint means
grant-all. An absent *row* constraint means "the provider has nothing to say about
which rows"; an absent *field* condition on a field the provider has already
declared conditional means "the provider said it depends and then did not say on
what", and that must never be read as a grant. In practice golem does not even
render it — see `unconstrained` in §6.4.

Each masked column gets **its own** condition. Two masked columns on the same model
never share a `CASE`.

The condition may itself hop relations, in which case it renders as the correlated
`EXISTS` of §3 against the node's alias — inside the projection, inside whatever
subquery this node is.

### 6.2 One construction site

There is exactly one function that renders a projected column:

```go
func (b *builder) column(alias string, col PlannedColumn, mask *Expr) Expr
```

and exactly one function that builds a child `SELECT`:

```go
func (b *builder) child(rel PlannedRelation) SelectBuilder  // projection + masks + relation where
```

`child` is called by **both** the correlated-aggregate path (which wraps it in
`json_agg` and the correlation predicates) and the batched path (which wraps it in
`IN (…)`). Neither path may project a column any other way.

This is not tidiness. The two strategies are chosen by an *index* — a property of
the physical schema that changes when someone runs a migration. If masking lived in
the aggregate builder only, then adding an index to a foreign key would move a
relation from the batched path to the correlated path and the mask would appear;
**dropping** an index would move it the other way and the mask would silently
disappear, exposing a column to a caller who may not read it, with no code change
and no test failure. A DBA's routine index change would be a privilege escalation.
One construction site makes that state unreachable rather than merely untested.

§9's `MASK_ONE_STRATEGY` mutation is the test that this holds.

### 6.3 What a mask does and does not do

A mask nulls **output**. It does not restrict the column anywhere else in the
statement: `WHERE`, `ORDER BY`, the correlation predicate and de-duplication all
still see the true value. That is intentional and it matches the reference
implementation, which fetches the true value and nulls it in memory — ordering there
is by the true value too.

It is also why invariant 2 ("a filter is a read") is enforced *before* planning: a
caller who could filter on a masked column could binary-search the value it is not
allowed to read. golem refuses such a request outright with a forbidden error. A
masked column is filterable only when its condition is **discharged by the row
predicate** — implied by it, so every row the read can return satisfies the mask —
at which point the value is readable on every returned row and filtering leaks
nothing.

### 6.4 The deferral rules, exhaustively

When golem cannot render a mask in the statement, it **defers**: it projects the
true value, nulls it in memory after decoding, and records a `DeferredMask` on the
plan. Deferral is a correctness-preserving retreat, never a silent one.

```go
type DeferredMask struct {
    Path   string  // "the read" for the root, else the dotted relation path
    Field  string
    Reason DeferralReason
    Detail string  // human-readable, names the model, field and why
}
```

The complete set of reasons, in the order the planner checks them:

| Reason | Condition | Why it cannot be rendered there |
| --- | --- | --- |
| `relation` | The mask's path names a node the compiled read does not reach. | There is no statement, no alias and no rows for that path. Nothing to render against. |
| `unconstrained` | The provider classified the field as conditional but handed over no condition (nil, or an empty tree). | An absent condition must never be read as a grant. Golem will not render `false` and call it a mask either, because the field may still be readable per-row via the provider's instance check — so the value is fetched and the provider is asked row by row. |
| `unprojected` | The field is masked but the compiled projection has no column for it. | Nothing to wrap. (Reachable when the caller's `select` does not name it but a mask check does.) |
| `correlated` | The field carries a key a batch correlates on: either the parent key of a batched child of this node, or the child key of the batch this node *is*. | §5.4 attaches children to parents by comparing key values. A masked key is `NULL` for some rows, all such rows collapse into one bucket, and children attach to the wrong parents — or to none. The key must be whole while the batch runs. Golem masks it **after** the batch has been attached, in memory. |
| `distinct` | A **root-level** field the read is distinct on. | §1.6 de-duplicates on the value. The engine — and the reference implementation — de-duplicate on the *true* value before the mask would null it. Masking first would collapse every unreadable row into a single `NULL` group and drop rows the caller is entitled to. Only reachable at all when the mask is discharged (§6.3); otherwise the request was already refused. |
| `decoder` | Engine-specific. On SQLite: the masked column is not `String`, `Float` or `BigInt`. | SQLite carries a *declared type* alongside a value, and wrapping a column in a `CASE` expression erases it. A client that decodes by declared type then reads an `Int` as something else. Measured: `String`, `Float` and `BigInt` survive the wrapper; `Int`, `Boolean`, `DateTime`, `Decimal` and enums do not. **Postgres has no equivalent limitation** — a `CASE` over a typed column keeps the column's type — so this reason never fires on the Postgres path. Keep the check behind the dialect seam; do not delete it because Postgres does not need it. |
| `unrenderable` | Rendering the condition raised an unsupported-condition error. | The condition uses an operator the SQL renderer does not implement. Note this is *not* a fallback of the whole read: only this one mask retreats. |

Deferral is per field. A read may mask three columns in SQL and defer a fourth, and
the plan reports both lists.

### 6.5 Hydration, and the replanning step

To null a deferred mask in memory, golem must have fetched whatever the condition
reads. The read tree therefore **injects** extra columns into the projection —
"hydration" — for example projecting `posts.published` so that a deferred mask on
`posts.secretNote` can be evaluated per row.

When the mask turns out to be renderable in SQL, that hydration is dead weight and
leaks a column the caller did not ask for into the result. So the planner:

1. plans the node tree,
2. plans the masks,
3. computes which injected columns are now unnecessary — an injected column may be
   dropped only if **every** mask that needed it was rendered in SQL,
4. if anything can be dropped, prunes the projection, **resets the alias counter**,
   replans the node tree from scratch, and replans the masks.

The "every mask that needed it" condition is the subtle one. Two masks on the same
model can share a hydration column; if one renders and the other defers, the column
stays. Getting this wrong produces a mask that evaluates against a column that is
not there, which in Go is a zero value — that is, a mask that quietly denies (or
quietly grants) instead of failing.

The replan is a full replan. Do not attempt to patch aliases.

### 6.6 Withholding is stronger than masking

Masking and withholding are not two flavours of the same thing.

- **Mask**: the column is in the statement. Its value is nulled in the projection.
  It remains filterable, orderable and comparable in every other clause. A caller
  who can reach those clauses can still learn the value.
- **Withhold**: the column is **not in the statement at all**, and any statement
  that references it is **refused** with a forbidden error, in every clause,
  including inside a CTE body, inside a subquery in `FROM`, inside a correlated
  subquery, and inside a join condition.

Withholding is what §8's scoped builder does for a field the caller may never read,
and for a field whose mask cannot be rendered and is not discharged by the row
predicate. It is strictly stronger, and it is the right default whenever the caller
gets to write the query, because a mask's guarantee depends on the caller not being
able to write a `WHERE` — and in the escape hatch, the caller can.

---

## 7. Type traps

### 7.1 JSON aggregation destroys numeric types

A relation's columns travel through a JSON document. JSON numbers are IEEE-754
doubles in every parser that does not go out of its way. A 64-bit integer
round-tripped through JSON loses precision above 2^53 — `9007199254740993` becomes
`9007199254740992` — and a fixed-point decimal becomes a binary float, so
`0.000000000000000001` becomes `1e-18` and `1234567890.1234567890123` loses its tail.
Neither raises an error. The value is simply wrong, and it is wrong in the direction
that looks fine in a test with small numbers.

**The cast-to-text rule.** Every column that will travel inside a JSON aggregate is
classified, and the classification decides both the SQL and the decode:

| Model type | Kind | SQL inside the aggregate | Decode in Go |
| --- | --- | --- | --- |
| `String`, `Int`, `Float`, enum | `Plain` | the column reference | as-is from the JSON value |
| `BigInt` | `BigInt` | **`cast(<col> as text)`** | `strconv.ParseInt(s, 10, 64)` — or a big.Int if the schema exceeds int64 |
| `Decimal` | `Decimal` | **`cast(<col> as text)`** | the project's exact-decimal type, constructed **from the string**, never via float64 |
| `DateTime` | `DateTime` | the column reference | §7.3 |
| `Boolean` | `Boolean` | the column reference | true JSON bool, or `1`/`0` from SQLite |
| anything else (`Json`, `Bytes`, list columns, unknown) | — | **fallback** | — |

`Int` is safe uncast because it is 32-bit. `Float` is safe uncast because it is
already a double and JSON round-trips doubles exactly. `BigInt` and `Decimal` are
not, and are the whole point of the rule.

**The rule applies only to columns inside a JSON aggregate.** A column projected at
the root, or in a batched child statement, is not cast: it comes back through the
driver's typed protocol, which already carries `bigint` and `numeric` losslessly.
Casting there would be a pessimization and would force a string decode where the
driver already did the work. So the classification is a property of the *node's
shape*, computed once when the node is planned, and stored on the plan — never
recomputed at decode time.

The mask (§6) applies the cast **inside** the `CASE`, around the column reference:
`case when (<condition>) then cast(<col> as text) else null end`. That order is
fixed by the specification, not chosen per call site — the cast belongs to the
*column* (decided when the column is planned, from its type and its node's shape),
the mask belongs to the *field policy* (applied afterwards), and there is one
function that composes them in that order. Both orders would evaluate the same on
Postgres, which is exactly why the order must be written down: otherwise two
implementations produce different statement text for the same request and §9.3's
determinism check has nothing to compare.

**If you use `jsonb`**, this rule is doubly load-bearing: `jsonb` normalizes numeric
literals as it stores them, so a `numeric` that reaches `jsonb_agg` uncast has
already been through a canonicalization before your decoder sees it.

### 7.2 Integers that do not fit

`count(*)` on Postgres is `bigint`. `sum(int)` on Postgres is `bigint`. Both are
routinely decoded into a 64-bit Go integer without trouble — but the reference
implementation surfaces a specific error when the value exceeds what a
double-precision consumer can hold, and the oracle requires golem to raise the same
error with the same message naming the same column and value.

Specify a single `decodeSafeInteger(value any, column string) (int64, error)`:
accept `int64`, `int32`, a numeric string, or a `big.Int`; parse exactly; and if the
magnitude exceeds `2^53 - 1`, return the precision error naming the *result column
alias* (`_sum$rank_value`, `_count$posts`) and the exact decimal text of the value.
Do not round. Do not clamp. Do not silently widen to `big.Int` on a code path whose
contract says integer — the caller asked for a type that cannot hold the answer and
must be told.

The measured case: a `bigint` column summed past `2^53`. Both the compiled path and
the reference implementation must fail, with the same error class, code, column name
and value in the message.

### 7.3 DateTime

Normalize on decode, one function, used by both the relation decoder and the
aggregate decoder:

- already a `time.Time` → use it,
- an integer → milliseconds since epoch,
- a string → if it has no `T`, replace the first space with `T`; if it has **no
  zone suffix** (no trailing `Z`, `z`, or `±HH:MM` / `±HHMM`), **append `Z`**, then
  parse as RFC 3339.

The "append `Z`" step encodes a decision: a naive timestamp is UTC. Postgres
`timestamptz` comes back zoned and never takes this branch; SQLite and Postgres
`timestamp` (without time zone) do. If you drop the step, a naive timestamp is
parsed in the process's local zone and the answer changes with `TZ`, which is a bug
that only reproduces on machines configured differently from yours. Preserve
milliseconds; the oracle asserts to the millisecond.

### 7.4 Aggregate decoding, and the divergences that were measured

Aggregates (`_count`, `_sum`, `_avg`, `_min`, `_max`, with optional `by` grouping)
compile to a single statement:

```sql
select "t0"."owner_id" as "ownerId",             -- one per group-by key
       count(*)                 as "_count$_all",
       count("t0"."note")       as "_count$note",
       cast(sum("t0"."hits") as text) as "_sum$hits",
       avg("t0"."rank_value")   as "_avg$rank_value",
       min("t0"."recorded_at")  as "_min$recorded_at"
from "metrics" as "t0"
where (<caller where AND policy constraint>)
group by "t0"."owner_id"
having (<having, over the same measure expressions>)
order by …
limit … offset …
```

Rules:
- Alias is `<group>$<physical column>`, or `_count$_all` for `count(*)`. Over 63
  bytes ⇒ fallback (§2.5).
- The same `cast(… as text)` rule as §7.1, driven by the *decode kind* of the
  measure: cast when the decode is `BigInt` or `Decimal`.
- `having` is rendered over the **measure expressions**, not over column references,
  and the supported filters are the ordered set `equals, not, in, notIn, lt, lte,
  gt, gte`. An empty `in` renders as `1 = 0`, an empty `notIn` as `1 = 1`, and an
  empty `AND`/`OR` group as `1 = 1` / `1 = 0`. Anything else is a fallback.
- An **ungrouped** aggregate with `take`, `skip`, `orderBy` or `cursor` is a
  fallback: the reference implementation fences that off with a subquery and golem
  does not compile the fence.

The measured divergences, and the decode each demands:

| Measure | Column type | Postgres returns | SQLite returns | Decode kind |
| --- | --- | --- | --- | --- |
| `_count` (any) | — | `bigint` | integer | **safe integer** (§7.2) |
| `_sum` | `Int` | **`bigint`** — widened | integer | **safe integer**. This is the trap: summing a 32-bit column produces a 64-bit result that can exceed the safe range, and it must raise, not truncate. |
| `_sum` | `BigInt` | `numeric` | integer | big integer, via the text cast |
| `_sum` | `Float` | `double precision` | real | float64 |
| `_sum` | `Decimal` | `numeric` | real (!) | exact decimal, via the text cast |
| `_avg` | `Int` | **`numeric`** — not a float; arrives as a string | real | **float64, parsed from the string**. `avg` over an integer never returns an integer; a decoder that assumes the measure's type follows the column's type reads `2.3333…` as `2`. |
| `_avg` | `BigInt` | `numeric` | real | float64, parsed from the string |
| `_avg` | `Float` | `double precision` | real | float64 |
| `_avg` | `Decimal` | `numeric` | real | exact decimal, via the text cast |
| `_min` / `_max` | `DateTime` | `timestamp[tz]`, driver-decoded | **a string** | §7.3 normalization. This is why the datetime decoder must accept both. |
| `_min` / `_max` | `String` | text | text | string |
| `_min` / `_max` | `Int` / `Float` / `BigInt` / `Decimal` | as the column | as the column | as the corresponding kind above |
| `_min` / `_max` | `Boolean` | — | — | **fallback** — the reference implementation does not offer it |

Two consequences for Go:

- **The decode kind is a function of `(measure group, column type)`, not of the
  column type alone.** Compute it in the planner, store it on the plan, and have the
  decoder do nothing but follow it. A decoder that inspects the returned value's
  dynamic type will get `_avg` over `Int` right on SQLite and wrong on Postgres.
- **`_avg` is compared with a tolerance, everywhere.** Postgres computes `avg` in
  arbitrary precision and SQLite in binary floating point; they differ in the last
  bits. The oracle asserts equality of averages within a **relative tolerance of
  1e-9**, and any comparison you write in Go must do the same. Exact equality on an
  average across engines is a flaky test waiting to happen.

### 7.5 Decoding a relation

`Plain` values pass through. `nil` decodes to the field's null representation. A
to-many whose JSON is not an array is an **error**, not an empty list (§2.2). A
relation's `Reversed` flag reverses the decoded slice after decoding, applying the
same offset/limit/reverse discipline as §5.3.

---

## 8. The scoped escape hatch

Some queries golem will not generate: analytical rollups, window functions,
multi-pass CTEs. The escape hatch lets a developer write one — **rooted at a derived
table golem constructed**, with the row predicate and the field policy already
applied.

### 8.1 The root

For each root the developer declares, golem builds:

```sql
(select "g0"."user_id" as "id",
        "g0"."name"    as "name",
        case when (<field condition>) then "g0"."email" else null end as "email"
   from "users" as "g0"
  where (<row predicate>)) as "u"
```

- `g<i>` is the inner alias, positional. The outer alias is the developer's.
- The projection is **every non-relation field of the model**, minus hidden fields,
  minus fields the caller may never read, minus fields whose mask cannot be rendered
  and is not discharged by the row predicate. Conditional fields whose mask renders
  appear wrapped in a `CASE`.
- The predicate uses `absent` = **deny-all when the constraint is explicitly null**,
  grant-all when it is merely absent. An explicit null means "the provider denies";
  an absent constraint means "the provider has no opinion".
- If the projection would be empty, refuse the whole query. A derived table with no
  columns is not a useful escape hatch, and returning one invites a `select *` that
  would resolve to nothing and look like a data problem.
- Every column not projected is recorded as **withheld**, with the reason, keyed by
  the outer alias.

The developer then builds a `SELECT` over those roots. Joins between roots are
allowed (each root carries its own predicate). Everything else is audited.

### 8.2 Go: make a forged root unconstructable, and keep the audit

The TypeScript guarantee is an AST walk that compares table references **by node
identity** — the exact object golem created — plus structural checks. It works, and
it is the only option in a language where any object can be reconstructed.

In Go, do better *and also* keep the audit:

```go
package golem

// ScopedRoot is the only thing a scoped query may read from.
type ScopedRoot struct {
    alias string
    seal  *rootSeal   // unexported field, unexported pointee type
}

type rootSeal struct {
    owner *registry   // identity: the registry that built this root
    sql   string      // the derived table, as golem rendered it
}

func (r ScopedRoot) sealedBy(reg *registry) bool {
    return r.seal != nil && r.seal.owner == reg
}
```

Two properties, and both are needed.

- **A populated root cannot be forged.** `seal`'s field is unexported and its
  pointee type is unexported, so no package outside `golem` can write a
  `ScopedRoot` literal with a seal, cannot fill one by embedding, and cannot
  produce one by conversion from a look-alike struct — Go forbids conversion
  between struct types whose unexported fields come from different packages.
- **The zero value is still reachable**, because `var r golem.ScopedRoot` compiles
  anywhere. So the seal must also be **checked**: the builder verifies
  `sealedBy(itsRegistry)` for every root it is handed and refuses otherwise. The
  pointer comparison also pins the root to *this* query's registry, so a root
  borrowed from another scoped query — built for a different caller, with a
  different policy — is refused too.

The builder accepts `ScopedRoot`, a concrete type, not an interface, so there is no
implementation to substitute. Together, a forged root either does not compile or is
refused before any SQL is produced. That closes the class of attack the identity walk
exists to catch, most of it at compile time and the remainder at a single check.

**Keep the structural audit anyway.** The seal proves the *root* is golem's. It
proves nothing about what the rest of the query does: a genuine root can appear
alongside a raw fragment, a union with an unscoped select, a CTE that reads the
physical table directly, or a correlated subquery over an unscoped table. Those are
not forgeries; they are perfectly ordinary query construction that happens to reach
data the policy never approved. The seal and the audit cover disjoint failure modes.
Run both.

### 8.3 What must be refused, and why each check is load-bearing

Run the checks in this order; each assumes the previous ones passed.

| # | Refuse | Why it is load-bearing |
| --- | --- | --- |
| 1 | **Anything that is not a single `SELECT`.** | Every later check assumes a select-shaped tree. A non-select is unanalyzable and there is no reason a read should be one. |
| 2 | **Any set operation** — `UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT`, and the `ALL` variants — including when the *operand is itself scoped*. | The operand of a set operation is a second query whose `FROM` the outer checks do not reach. Even a scoped operand is refused, because allowing it means the audit has to prove operand equivalence, and "this one happens to be safe" is not a rule you can enforce on the next one. |
| 3 | **Any data-modifying node**, anywhere, including inside a CTE. | On Postgres a data-modifying CTE (`WITH x AS (DELETE … RETURNING …) SELECT …`) *is* a `SELECT` statement. It passes check 1. A read path that can delete is not a read path. Also refuse a CTE whose body is not a `SELECT`. |
| 4 | **A CTE named after a physical table of any model, or after a scoped root's alias** (case-insensitively). | Name shadowing. A CTE named `users` shadows the table its own body reads: SQLite rejects it as a circular reference, Postgres resolves it the other way, and the same query means two different things on two engines. A CTE named after a scoped root makes a reference ambiguous depending on where it stands. |
| 5 | **A `FROM` or `JOIN` that is not a sealed root, a subquery over one, or a CTE this query declares.** Also refuse an empty `FROM`. | This is the core check: the policy predicate lives in the root's derived table, so anything read from somewhere else carries no predicate. An emptied `FROM` is refused explicitly because it is what an attacker reaches for after failing to add one. |
| 6 | **Raw SQL the developer wrote, anywhere.** | A raw fragment is opaque to every other check. It can name a table, reference a withheld column, or open a subquery, and none of the walks can see inside it. Golem's *own* raw fragments — the ones it built for the root — are exempt by identity, and the walk stops descending at them. |
| 7 | **A read of a table name, in any `FROM` or `JOIN` at any depth**, unless that name is visible at that point as a CTE or a scoped alias. | Check 5 covers the top level; this one covers subqueries, correlated subqueries and CTE bodies. Visibility is scoped correctly: a non-recursive CTE may read siblings declared **before** it and not after; a recursive one may read itself. Getting the visibility rule wrong either breaks legitimate multi-pass queries or lets a CTE read a table by name. |
| 8 | **Any table reference to a name that was never declared, and any schema qualification at all.** | Schema qualification (`other.users`) is a table reference the name-based checks would not recognize; refuse it outright rather than resolving search paths. This is the belt to check 7's braces, catching table nodes in clauses the `FROM`/`JOIN` walk does not visit. |
| 9 | **Any reference to a withheld column**, qualified by the alias of the root that withheld it — in the projection, the predicate, `ORDER BY`, `GROUP BY`, a join condition, a CTE body, a subquery in `FROM`, or a correlated subquery. | This is the enforcement of §6.6. The column is absent from the derived table, so most references would error anyway — but the error would be a SQL error the developer might "fix" by reaching around the root. Refusing explicitly, with a **forbidden** error naming the alias, the column and the reason, makes the boundary legible. Note the failure class: withheld-column access is *forbidden*, not *invalid*. |
| 10 | **A builder that did not come from golem**, or a callback that returns something that is not the builder golem handed it. | Everything above analyzes the tree the builder produced. If the developer returns a different object, none of it applied. |

Two further notes:

- **Plugin and transform hooks must be audited after they run, not before.** A
  plugin that rewrites the tree — replacing the scoped root with the base table, or
  rebuilding the root without its predicate, or adding a CTE over a raw table — is
  the exact attack. Audit the final node, once, immediately before compiling it.
- **Do not rely on the type system to enforce any of this.** In TypeScript the
  builder type removes the escape methods, and the red-team suite calls every one of
  them through a cast to prove the type is a hint and the audit is the boundary. In
  Go the equivalent is a narrowed interface, which a type assertion re-widens. Same
  conclusion: **the audit is the boundary.** Keep the narrowed surface for
  discoverability; test every removed method through the widening.

Methods that legitimately do not defeat the root — clearing the outer `WHERE`,
`SELECT`, `LIMIT` or `ORDER BY`, a call-through combinator, a plugin that adds
nothing refusable — must keep working. Clearing the outer `WHERE` is safe precisely
because the predicate is not there; it is inside the derived table.

---

## 9. Acceptance criteria

### 9.1 The oracle

Everything below is anchored by one test, and it is the reason this specification
can be checked at all:

> **The compiled path and an independent reference implementation must return
> identical decoded object graphs for the same request against the same live
> database.**

Identical means:

- the same values,
- the same **types** — a big integer is a big integer and not a float, an exact
  decimal is an exact decimal, a timestamp is a timestamp to the millisecond,
- the same **keys present**, including keys whose value is null (a null column must
  be *present and null*, not absent),
- the same **empty-versus-nil** distinction for every to-many,
- the same ordering, including null placement, which is whatever the engine does.

Compare structurally, by walking both graphs and comparing a type-tagged rendering
of every leaf, and *then* compare the values directly. The type-tagged walk is what
catches "the numbers match but one is a float".

The reference implementation must be **structurally different**, not a refactor of
the compiled path: the naive shape — one statement per relation level, no folding,
policy evaluated the long way — and ideally an existing library rather than
something written alongside. That is what makes the oracle meaningful. Two
implementations that share a planner drift together and the test passes while both
are wrong. The TypeScript oracle runs golem's compiled path against Prisma, on a
live Postgres and a live SQLite, over a seeded fixture containing precisely the
values that break naive implementations: a `BigInt` above and below 2^53, a
23-significant-digit decimal, a decimal of 1e-18, timestamps with milliseconds,
nulls in every nullable column, an empty relation, and a relation with no matching
parent.

The oracle also asserts on the **plan**, not only the answer: which relations were
batched, how many statements ran, which paths were masked, which were deferred and
with what reason. A read that returns the right answer by the wrong strategy is a
regression.

### 9.2 The mutations

Each of these is a change an implementer might plausibly make. For each, the named
test must fail. If it passes, the test does not exist yet.

| Mutation | The change | What must fail |
| --- | --- | --- |
| **`MASK_ONE_STRATEGY`** | Render the mask in the correlated-aggregate path only, leaving the batched child `SELECT` unmasked (or vice versa). | A read of a to-many whose foreign key is **unindexed**, projecting a masked column, must return the masked value nulled on the rows where the condition is false. With the mutation the batched statement returns true values and the oracle diverges. Add the mirror test on an **indexed** foreign key so that whichever half is mutated, one fails. Also assert the second statement's text contains the `case when`. |
| **`NO_CHUNK`** | Put every parent key into one `IN` list. | A batched relation over **70 000** parents on Postgres and **33 000** on SQLite must succeed and return the correct children for the first, a mid-chunk, a chunk-boundary and the last parent. With the mutation the driver refuses the statement. A test with 100 parents will not catch this — that is the actual historical failure. Assert the statement count equals `1 + ceil(parents / 900)`. |
| **`BATCH_WIDE_LIMIT`** | Apply the relation's `take`/`skip` across the whole batch, or push a `LIMIT` into the batched statement. | A batched relation with `take: 2` over 950 parents, each with 3 children, must return exactly 2 children **per parent** — total 1900 — and the parent at index 900 (first of the second chunk) must get the same *relative* children as the parent at index 0. With the mutation the total is 2, or the second chunk's parents come back empty. Add the `skip: 1, take: -2` variant to pin offset/limit/reverse ordering. |
| **`JOIN_FOR_RELATION_FILTER`** | Compile a relation filter as a join instead of a correlated `EXISTS`. | §3.1: a nullable foreign key, a policy of the form `OR: [ {relation hop}, {local column} ]`, and a row whose foreign key is null but whose local column satisfies the other branch. That row must be returned. With the mutation it is not. Add a to-many variant asserting `LIMIT n` returns n **parents**, which the join formulation breaks by multiplying rows. |
| **`NO_TEXT_CAST_BIGINT`** | Project a `BigInt` (or `Decimal`) inside a JSON aggregate without `cast(… as text)`. | A relation carrying `hits = 9007199254740993` and `-9007199254740993`, and a decimal of `1234567890.1234567890123`, must decode exactly and with the same type as the reference implementation. With the mutation the values come back off by one and the decimal loses its tail — **and the test only catches it if the fixture holds values above 2^53**, which is why the fixture does. |
| **`DROP_COALESCE`** | Remove the `coalesce(…, '[]')` from the to-many aggregate. | A parent with no children must decode to an empty non-nil list. With the mutation it decodes to nil (and, if the decoder "repairs" it, the test must instead assert that the raw JSON column is `'[]'`). |
| **`MASK_THE_DISTINCT_KEY`** | Render the mask on a column the read is `distinct` on, instead of deferring. | A read `distinct` on a discharged-mask column must return the same rows as the reference. With the mutation every row failing the condition collapses into one `NULL` group and rows disappear. The plan must report the field as deferred with reason `distinct`. |
| **`MASK_THE_BATCH_KEY`** | Render the mask on a column that carries a batch's correlation key, instead of deferring. | A read whose parent key (or child key) is itself a masked field must attach children to the correct parents, and must null the key afterwards. With the mutation every parent whose key masks to null claims the same bucket. Plan must report reason `correlated`. |
| **`IGNORE_INDEX_METADATA`** | Have the generator omit indexes, or have `EqualityIndexed` return a constant. | The plan for an **indexed** foreign key must be a single statement with no batches; the plan for an **unindexed** one must be two statements. Both answers must remain identical to the reference either way — that is the point of "loading strategy never changes an answer" — so this test asserts on the **plan**, not the rows. |
| **`FORGE_SCOPED_ROOT`** | Skip the seal check; or hand the builder a zero-valued `ScopedRoot`, a root borrowed from another scoped query, or reach the base table through a raw fragment / union / CTE / correlated subquery / schema qualification. | Must not compile (the unexported seal) or must be refused (the seal check, then the audit). Port the red-team suite wholesale: one test per refusal in §8.3, plus the zero-value and borrowed-root cases, each asserting the *error class* — validation versus forbidden — not just that something failed. |
| **`TYPE_IS_THE_BOUNDARY`** | Enforce the escape-hatch restrictions only in the narrowed builder type. | Every removed method, called through a widening type assertion, must still be refused at audit time. |

### 9.3 Determinism

Two additional properties, cheap to test and easy to lose:

- **Statement text is a pure function of the request and the metadata.** Build the
  same plan twice; assert byte-identical SQL and identical parameter order. This is
  what makes the replan of §6.5 safe and statement logs comparable.
- **Bind order is projection, then filter, then correlation, then paging**, depth
  first. Assert the parameter slice of the §1.3 example is exactly
  `[true, true, 5, 10, 1, 20]`. A change here is invisible until a driver-side
  prepared-statement cache disagrees with a log line.

---

## Appendix A — Fallback reasons

When the planner cannot compile a request it returns a fallback: a reason and a
detail naming the model, the field and what about it could not be compiled. The
reason is machine-readable; the detail is for a human reading a log at 3am. The
set:

`client`, `provider`, `relation`, `projection`, `cursor`, `decoder`, `distinct`,
`where`, `orderBy`, `take`, `measure`, `group`, `having`.

A fallback is not a failure. It means this request runs some other way. But every
fallback is a statement that did not get compiled, so the observability event must
carry both the reason and the detail, and the acceptance suite should assert the
*reason* for each known fallback case — a fallback drifting from `orderBy` to
`projection` is a planner regression even though the answer is unchanged.

## Appendix B — Cursors

A `cursor` renders as a disjunction of prefix comparisons over the order terms, with
the anchor values fetched by correlated scalar subqueries over the same table under
alias `c0`:

```sql
(   ("t0"."a" = (select "c0"."a" from "tbl" as "c0" where ("c0"."id") = ($1))
 and "t0"."b" >= (select "c0"."b" from "tbl" as "c0" where ("c0"."id") = ($1)))
 or ("t0"."a" >  (select "c0"."a" from "tbl" as "c0" where ("c0"."id") = ($1))))
```

The last term of the deepest branch uses `>=` (or `<=`), so the cursor row is
included; every shallower branch uses the strict comparison. Directions flip under
reversal.

Two refusals, both correctness:

- **The order must not leave the cursor's table.** If any order term references a
  joined alias, fall back — the anchor subquery only knows the root table.
- **The order must not include a nullable column.** The reference implementation's
  own cursor predicate keeps rows on *both* sides of the cursor when a null is
  involved and settles the page by locating the cursor row among the rows it read
  back. golem's predicate does not, so on a nullable column the two would page
  differently. Fall back rather than diverge.

Compound unique selectors are expanded into their component columns before binding.

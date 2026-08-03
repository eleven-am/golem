# Release notes — next major

Golem generates the SQL for the queries it generates, and enforces
permissions with its own semantics rather than Prisma's.

Prisma keeps the schema, the migrations, the generated types, the
connection, and the client you write yourself. It is no longer in the path
of a query golem generates.

---

## Breaking

### Abilities built with `createAbility` fail to start

`GolemAuthorizationAdapter` now refuses an ability factory whose conditions
matcher disagrees with golem's operator table, and `createAbility` from
`@eleven-am/authorizer/prisma` is one such factory: it is backed by
`@casl/prisma`, whose matcher is precisely what golem replaces.

**If you do not set `abilityFactory`, nothing changes** — golem installs its
own. If you set it explicitly, switch to `createGolemAbility`:

```diff
- import { createAbility } from '@eleven-am/authorizer/prisma';
+ import { createGolemAbility } from '@eleven-am/golem-authorizer';

  abilityFactory() {
-   return new AbilityBuilder(createAbility);
+   return new AbilityBuilder(createGolemAbility);
  }
```

The failure is at startup, names the operator that diverged, and cannot be
reached in production without being seen in a deploy.

### `min` and `max` arguments change type on some models

`min` and `max` now accept `DateTime` and `String` columns, not only
numeric ones, so they reference a new `<Model>OrderableField` enum where
they used to reference `<Model>MeasureField`. `sum` and `avg` are unchanged.

This happens **only** on models whose aggregatable columns are not all
numeric. A model with a numeric-only `measures` allowlist emits no new enum
and its schema is unchanged.

Queries passing `min`/`max` as **literals** keep working. Queries passing
them through a **typed GraphQL variable** fail validation, because GraphQL
has no enum subtyping and a variable's type must match the position exactly:

```graphql
# no longer valid where the model gained orderable measures
query ($f: [PostMeasureField!]) { postsAggregate(measures: { min: $f }) { … } }
```

Re-run GraphQL codegen and update any variable declarations.

### Policy conditions are validated, and unsupported operators are refused

Conditions are Prisma's `WhereInput` — scalars, string operators with
`mode`, to-one via the bare shorthand or `is`/`isNot`, to-many via `some`,
`every` and `none`, scalar lists, and Json. Anything golem cannot render
exactly is refused when the ability is built rather than approximated.

A rule that previously passed a condition golem could not express now fails
on the first request from a user whose rules include it, naming the rule and
the operator.

### The Node floor is `>=20.19`

Kysely is ESM-only, and `require` of an ESM module is unflagged from Node
20.19. Every published package declares it.

### A read may no longer filter or order by a field it may not read

Field-level authorization used to govern the projection only. A caller who
saw `phone: null` on every row could still write
`users(where: { phone: { startsWith: "+44" } })`, and the rows that came back
disclosed the value — `startsWith` turning it into a character-by-character
search. `aggregate` counted over the same hidden values.

`where`, `orderBy` and `cursor` are now classified the same way a measure
already was, on `findOne`, `findFirst`, `findMany`, `count`, `aggregate` and
`groupBy`. A field the caller can never read is refused; a field readable
only conditionally is refused unless the query constraint already discharges
the condition, which is the case whenever the row policy alone decides
readability. Fields the caller may always read are unaffected.

Field references are collected at every depth — through `AND`, `OR` and
`NOT`, through the operators on a field, and through relation filters.
A field named across a relation, as in `{ author: { is: { phone: … } } }`,
is classified against the **related** model, so a rule that hides
`User.phone` also blocks filtering posts by it.

Refusals report `FORBIDDEN` and name the field and model:

```
Cannot filter or order by field "phone" on User: readability depends on id,
which the query constraint does not discharge
```

This only reaches an application that writes **field-scoped** CASL rules
(`can('read', 'User', ['phone'], …)` or `cannot('read', 'User', ['phone'])`).
An ability with row conditions but no field lists classifies every field as
discharged by the constraint and is unaffected. Refusals over a measure,
a grouping key, `having` or an aggregate `orderBy` keep the
`BAD_USER_INPUT` code they already had.

The same classification applies inside a projection. A relation entry in a
`select` or `include` tree — its `where`, its `orderBy`, its `cursor`, and
the `where` of a relation counted under `_count` — is classified at every
depth against the model that **owns** the field, so
`select: { posts: { where: { secretNote: … } } }` is refused by the rule
that hides `Post.secretNote` rather than by a rule about the model being
queried. A projection is prepared for writes too, so a nested filter in the
tree a `create`, `update` or `delete` returns is classified alongside it.
Where the statement is compiled rather than handed to Prisma, that nested
`where` is rendered against the column itself rather than against the masked
projection, and the classification happens before anything is compiled.

Writes classify the filter they select rows with, at the root and nested
inside `data`. `updateMany` and `deleteMany` disclosed by count what a read
could not disclose by value; their `where` is now classified like a read's,
and so is the `where` of a nested `update`, `updateMany`, `upsert`,
`delete`, `deleteMany`, `connect`, `disconnect`, `set` and
`connectOrCreate`. The nested case was the sharper one: row verification
runs **after** the statement, inside the transaction, so a matching row the
caller may not write became a rollback rather than a change — telling the
caller their filter matched and costing them nothing. Driven as a prefix
search that recovered a hidden string in 155 queries with no row ever
written. `update`, `delete` and `upsert` are classified too: a unique
`where` may carry ordinary filters beside the unique field, and
not-found-versus-success makes that the same search, so `upsert` is checked
before it probes for the existing row. A field the caller may always read
stays usable in every one of these positions.

A filter key that names no field on the model is now refused rather than
passed along. Under a blanket `can('read', 'Post')`, CASL answers `always`
for any string it is handed, so an unknown key used to reach the query layer
and be stopped there — the fail-closed behaviour was the query layer's,
borrowed. It is now golem's own, checked against the model metadata, and it
covers the fields named inside an `orderBy: { _relevance: { fields: […] } }`,
which the collector previously stepped over entirely.

`distinct` remains unclassified: returning one row per distinct value
discloses the partition a hidden field induces over rows the caller can
otherwise select, and combined with the right to insert rows it degrades to
testing a whole value for equality. It is not a prefix search — no operator
compares the hidden value against attacker input. Two smaller edges: a batch
write discharges a conditional field against the **read** constraint while
it selects rows with the write constraint, so an ability whose write reach
exceeds its read reach can still count over the difference; and an ability
that grants `update` without `read` can no longer filter an `updateMany` or
`deleteMany` at all, since every field classifies as unreadable.

Reach is worth stating plainly, because the generated GraphQL API is not the
whole threat surface and in these cases is not the threat surface at all.
Relation fields in the generated schema take no arguments and `WhereInput`
omits relations, so a nested `where`/`orderBy`/`cursor`, a relation filter,
`distinct` and `cursor` cannot be written through GraphQL. They are reachable
through the generated programmatic client and through the engine directly,
which is where these classifications earn their place. Compiled reads are the
mirror image: only GraphQL asks for one, and the generated client hard-codes
`compiled: false`, so a compiled nested filter is reachable only by calling
the engine yourself.

### A GraphQL context reused across requests is refused

`@nestjs/apollo` accepts a static object as `GraphQLModule`'s `context`. It
attaches the first request's `req` to that object and then hands the same
object — still carrying the first caller's `req` — to every request that
follows. Every later caller is served with the first caller's identity, and
everything keyed on the context stops meaning "this request". The failure
was silent: requests kept answering, with the wrong caller's rows.

Golem now registers a middleware that stamps each HTTP request, and every
generated root resolver and batched computed field verifies that the
context it is handed belongs to the request being served. A context that
carries another request's `req`, or one context object observed across two
requests, fails the operation with an error naming the fix. The boundary is
observed from the Nest request lifecycle, not inferred from resolver
arguments, so graphql-ws multiplexing, long-lived subscriptions, and
batched HTTP requests are untouched: their operations run either inside
the one request they belong to or outside any HTTP request, where the
check stands down.

The fix is one line — `context` must be a function, so each request builds
its own object:

```diff
- context: {},
+ context: ({ req }) => ({ req }),
```

An application that genuinely means to share one context across requests
can say so: mark the shared object with `[golemSharedContext]: true`, using
the `golemSharedContext` symbol exported from `@eleven-am/golem` (it is
`Symbol.for('@eleven-am/golem.shared-context')`, so no import is required).
The check leaves a marked context alone, and what that sharing does to
authorization and caching becomes that application's own trade-off.

### MySQL is not supported

The dialect rendered but nothing ever executed it against a MySQL server. It
is removed rather than left as a claim the project cannot stand behind.
SQLite and Postgres are supported and both are tested against live servers,
Postgres under both linguistic and byte-order collation.

---

## Added

### `ctx.$scoped(model)`

A Kysely query builder rooted at a model with the policy predicate already
applied, for analytical reads the generated surface cannot express — window
functions, CTEs, `COUNT(DISTINCT)`, expression grouping.

```ts
const rows = await ctx.$scoped('Play')
  .select(['trackId'])
  .select((eb) => eb.fn.min('playedAt').over((ob) => ob.partitionBy('trackId')).as('firstPlay'))
  .execute();
```

Golem compiles it, checks the compiled tree, and executes it through Prisma.
Every table reference must be a scoped root golem itself constructed; joins,
set operations, CTEs, raw fragments and plugin transforms are refused. The
type hides the escapes, but the check on the tree is the guarantee — the
type is convenience.

The scoped root carries field policy as well as the row predicate, on every
root a join carries and not only the first. A column the caller may never
read is absent from the derived table golem roots the query at, and naming
it anyway — in a projection, a `where`, an `order by`, anywhere in the tree
— is refused with `FORBIDDEN` naming the root and the column. A column
readable only on some rows is projected as its condition: a `case`
expression handing back the value on the rows that satisfy it and null on
the rest, so filtering or ordering by it reaches the null rather than the
value. Columns the caller may always read are projected as they were.

Two limits carry over from the masked projection. A conditional field the
provider hands golem no renderable condition for is withheld entirely
rather than trusted; and on sqlite only `String`, `Float` and `BigInt`
columns are masked, because a case expression strips the declared type
sqlite hands Prisma to decode the value by — any other conditional column
is projected plainly when the row constraint already discharges its
condition and withheld when it does not.

A scoped request carrying no context is not a way around this: golem
classifies against the absent context rather than reading the absence as a
grant.

### Aggregations

`min` and `max` over `DateTime` and `String`. Per-field counts through
`measures.countFields`, returned as `countBy` alongside the row total.

### `@map` and `@@map`

Physical table and column names are carried on the generated datamodel, so
golem targets the mapped names.

---

## How a generated read runs now

Golem builds the statement and Prisma executes it. A read reaching
relations is one statement, with each relation a correlated subquery — so
where Prisma issued one query per relation level, there is now one.

With one exception, and it is deliberate. A correlated subquery runs once
per parent row: an index seek per parent when the relation's foreign key is
indexed, a full scan per parent when it is not. Prisma does not index
foreign keys unless asked. So golem reads the foreign key's indexes from
the datamodel and, when it finds none, reads the children in a second
statement keyed by the parents instead — bounded by relation level, not by
row, which is what Prisma has always done.

Nothing about this changes an answer. It changes how many statements
reach the database, and for an unindexed foreign key it is the difference
between one scan and one per row.

**If you want the single-statement shape, index the foreign key.** Golem
will use it.

A field the caller may read only on some rows is masked in the statement
that reads it, at every depth and on both shapes: inside the correlated
subquery when the foreign key is indexed, and inside the second statement
when it is not. The value never leaves the database on either. A field
whose condition golem cannot render there — one carrying a key a batch
correlates on, one the read is distinct on, or a column sqlite hands back
under another type once it is wrapped in a case expression — is fetched and
nulled in memory as before, and the read reports it as deferred.

Developer-written `ctx.post.findMany` is unaffected and stays on Prisma.
Golem compiles the queries golem writes.

---

## Changed behaviour

These are fixes, and they are behaviour changes.

- `contains` and `startsWith` against a null field previously **threw** in
  `@casl/prisma`'s matcher, reachable from any `ability.can` on a row with a
  null column. They are a non-match now.
- `lt` matched a null field through JavaScript coercion while `gte` did not.
  Null never matches a comparison now, in either direction.
- String comparison is byte-ordered on every engine. Prisma emits a bare
  comparison and lets the server's collation decide, which on a
  linguistic-collation Postgres is **more permissive than the rule intends**.
  `mode: 'insensitive'` is the explicit opt-in, as in Prisma.
- An insensitive `equals` escapes its operand. Prisma renders it as a bare
  `ILIKE`, so `{ equals: '100%', mode: 'insensitive' }` is a wildcard match
  there. This is the one place golem deliberately does not match Prisma:
  a policy author writing `100%` means the literal string.

---

## Notes

**Json path filters cannot use an ordinary index.** Only containment can.
Every path comparison is a sequential scan unless an expression index exists
for that exact path, and a policy predicate runs on every read of the model.
Prefer a real column for anything a policy filters on.

**`LEAST` and `GREATEST` treat a null argument differently on each engine.**
This bites the ordinary way of clamping a ratio, because the guard against
dividing by zero produces the null:

```sql
LEAST(AVG(observed) / NULLIF(AVG(expected), 0), 4)
```

When the divisor is zero, `NULLIF` makes it null, the division is null, and
then Postgres's `LEAST` ignores the null and returns `4` while SQLite's
`min()` propagates it and returns null. Same query, same rows, two answers.

Write the guard explicitly if you need one answer:

```sql
CASE WHEN AVG(expected) = 0 THEN 4
     ELSE MIN(AVG(observed) / AVG(expected), 4) END
```

This is a difference between the engines rather than anything golem does —
a hand-written version of the same query hits it too — but a scoped
analytical query is exactly where it shows up.

**In-memory and database answers can differ on null.** Golem's evaluator and
its SQL agree with each other — that is enforced by an oracle that runs both
against SQLite and both Postgres collations — but neither is obliged to
agree with Prisma's own filter semantics, which differ on nulls in ways this
repository measures and records.

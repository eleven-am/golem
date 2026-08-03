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

Filtering a **nested** relation inside `select`/`include`, and the `where` of
`updateMany`/`deleteMany`, are not yet classified. The nested case is the
sharper of the two now that the compiled statement renders that `where`
against the column itself rather than against the masked projection:
`select: { posts: { where: { secretNote: … } } }` still probes a value the
same filter at the root of the read is refused. Classifying it is the next
step, and until it lands a field-scoped rule is enforced at the root of a
read and in a scoped query, not inside a nested relation's own `where` or
`orderBy`.

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

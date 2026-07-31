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

**In-memory and database answers can differ on null.** Golem's evaluator and
its SQL agree with each other — that is enforced by an oracle that runs both
against SQLite and both Postgres collations — but neither is obliged to
agree with Prisma's own filter semantics, which differ on nulls in ways this
repository measures and records.

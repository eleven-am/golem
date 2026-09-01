# Release notes — 0.6.1

These are the release notes for the TypeScript/NestJS packages. They do not
announce a Go module release. For Go module status, follow published `go/v*`
releases. In particular, the Go implementation does not claim MySQL, federation,
automatic production migration, raw SQL through authorized surfaces, built-in
multi-process event transport or vendor CDC drivers, or observation of external
writes without a conformant CDC adapter.

Golem generates the SQL for the queries it generates, and enforces
permissions with its own semantics rather than Prisma's.

Prisma keeps the schema, the migrations, the generated types, the
connection, and the client you write yourself. It is no longer in the path
of a query golem generates.

## Completed hardening work

### Context-aware upsert is serialized

Policy-aware upserts now acquire a bounded striped guard before probing the update-visible branch. The guard key is a SHA-256 hash of the model plus a typed canonical selector, mapped into 4,096 stripes by default; no selector value is stored. Engine-owned transactions acquire it as their first statement, then run exactly one create/update pipeline with exactly that branch's hooks and commit-buffered event. Concurrent participating PostgreSQL clients converge on one row and one truthful create event. SQLite snapshot conflicts are translated to stable `CONFLICT`.

This requires the reserved `_golem_upsert_guard` table. Provider Prisma/SQL examples ship under `typescript/packages/core/prisma`; authorization-enabled Nest applications validate it at startup. The guarantee is cooperative: external/plain Prisma writers and differently addressed selectors remain outside it.

### Subscription fan-out is bounded and observable

One reference-counted local hub per model/schema replaces one event-bus iterator per subscriber. Subscriber queues default to 64; overflow disconnects with `GOLEM_SUBSCRIPTION_OVERFLOW` and never silently drops. Evaluation is deduplicated only inside one event for the identical context object plus canonical filter/selection, never across users. Fresh authorization still runs per event. `GolemSubscriptionObserver` exposes connection counts, receive/evaluate/deliver/suppress counters, latency, queue depth, and overflow.

The event wire format is versioned and JSON-safe for BigInt, Decimal, Date, bytes, composite identities, deletion snapshots, and batch envelopes.

### Composite GraphQL CRUD and event identities

Named and unnamed compound `@@id`/`@@unique` selectors now retain Prisma's nested accessor shape throughout find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete. Fallback selections fetch every primary-key component. Hidden or write-only key components fail schema construction.

Composite models may subscribe. Their event `id` is a model-specific non-null object in declared primary-key order; single-key models retain the existing scalar schema. Filtered delivery and deletion snapshots use scalar conjunctions internally and remain policy scoped.

### Masked scalar outputs are truthfully nullable

When authorization and `checkReadFields` are enabled, visible scalar/enum output fields are nullable regardless of database requiredness. A denied required column can become `null` without nulling its parent object, list, relation, alias, or subscription payload. Input requiredness and relation-list structure are unchanged; identities remain non-null.

### Top-level batch writes emit bounded per-row events

`updateMany` and `deleteMany` now emit deterministic per-row events through GraphQL, `forContext`, plain generated delegates, and generated interactive transactions for subscribable models. Defaults are 1,000 rows and 1 MiB encoded payload, configurable with `batchEvents`. Over-limit work rejects before mutation, never truncates, and primary-key updates are refused. Delete batches snapshot and delete exact identities, verify the count, and roll back on a mismatch. Events publish after commit and disappear on rollback.

This is in-process commit-aware delivery, not an outbox: nested batches, out-of-process writes, and a crash between database commit and publication remain outside the contract.

### Configured relation dimensions

`relationGroupBy()` and the separate `<models>RelationGrouped` GraphQL field provide bounded two-phase aggregation over one explicit forward to-one path. Root and every reached model receive independent row policy. Every participating key/dimension/measure must be readable. Policy-invisible targets use inner-join semantics. Averages merge sum/non-null-count components, Decimal sums avoid Decimal.js operation rounding, and Decimal averages reproduce live SQLite/PostgreSQL provider results (including PostgreSQL native scale). Final having/order/pagination runs only after the complete intermediate set is checked against `maxIntermediateGroups`. Ordinary Prisma-shaped `groupBy` and its uncapped programmatic posture are unchanged. `$scoped()` remains the escape hatch.

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

`distinct` is classified too, by the same rule. It disclosed nothing by
value and everything by count: returning one row per distinct value hands
back the partition a hidden field induces over rows the caller narrows with
fields it may read, and for a low-cardinality field the partition is close
to the value. Narrowing to two rows and counting one back said their hidden
values are equal. It is weaker than the positions above — no operator
compares the hidden value against anything the caller supplies, so there is
no character-by-character recovery — but it is the same disclosure and it is
now refused in the same place: on `findMany` and `findFirst` at the root,
and on a relation entry in a `select` or `include` tree, classified against
the model that owns the field. A name that is no field on the model is
refused rather than passed along, as it is for a `where`, an `orderBy` and a
`cursor`.

A field readable only conditionally whose condition the query constraint
already discharges stays usable, here as everywhere. Such a field is still
masked row by row, and where the statement is compiled the mask on a column
the read is distinct on is handed back to the in-memory path rather than
rendered — the database deduplicates on the value, and masking it in SQL
first would drop rows Prisma keeps.

Discharge is decided against the constraint that **selects the rows the
statement can touch**, not against the read constraint unconditionally.
`update`, `updateMany`, `delete` and `deleteMany` select with the write
constraint, so the write constraint is what has to imply the read constraint
before a conditionally-readable field may be named in their `where`. An
ability whose write reach exceeds its read reach — reads `Post` only where
`published`, updates every `Post` — is refused there, because the count such
a statement reports ranges over rows the caller may not read. Where the write
reach is the read reach, one branch of it, or narrower still, the filter is
answered exactly as before, and reads are untouched: a read selects with the
read constraint, which discharges itself.

An ability that grants `update` without `read` cannot filter an `updateMany`
or a `deleteMany` at all, not even by `id`. That is deliberate. A `where` is
a read — it interrogates the database and answers through the count and
through which rows changed — so a caller who may not read the model is told
nothing by one.

Three positions still discharge against the read constraint. A field reached
through a relation, as in `{ author: { is: { phone: … } } }`, is classified
against the related model, and no statement narrows the interrogated rows to
that model's read constraint. A filter nested inside `data` selects the
children of whatever parent row the statement matched, which no single
constraint describes. And the `where` an `upsert` probes with is classified
before the branch is chosen, against the read constraint, even though the
probe itself now selects with the update constraint — an ability whose update
reach exceeds its read reach can still name a conditionally-readable field
there.

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

### `upsert` chooses its branch from rows the caller may update

`upsert` probed for the existing row with no authorization constraint at all.
The branch it took was decided by whether a row existed **anywhere in the
table**, not by whether the caller could see or touch it, and the `where` of
an upsert is a unique selector — so the branch was an existence oracle over
unique keys, which is account enumeration when the key is an email.

The probe now runs under the caller's **update** constraint. That is the
constraint the branch commits to: an existing row leads to the update branch,
and the update branch selects with the update constraint anyway, so a row
outside it could never have been written. Probing with anything wider only
routed the caller to a branch that was going to refuse. A caller with no
`update` rule for the model at all reaches no row through that branch, so the
probe is skipped and the upsert creates.

The disclosure this closes is narrow and worth naming exactly. A caller who
may **create** could already learn that a unique key is taken by attempting
the create and reading the unique violation; golem does not hide that and
cannot. What leaked beyond it was the branch, visible to a caller who may
**not** create: an existing row produced a `NOT_FOUND` from the update
branch, a missing one produced a `FORBIDDEN` from the create branch, and two
different refusals for the same request separate the two cases. Both are now
the create branch's `FORBIDDEN`, whatever exists.

Two outcomes change for a caller who may create:

- A key held by a row outside the caller's update reach used to answer
  `NOT_FOUND`. It now takes the create branch and answers `CONFLICT` —
  exactly what the same `create` answers, and no more than it.
- An `upsert` whose `where` names a row outside that reach but whose `create`
  payload does not reproduce the selector now **creates** that payload rather
  than refusing. For that caller the row does not exist, so `upsert` creates,
  and the create is one they could have issued directly.

The rule the branch now follows is that an `upsert` tells its caller nothing a
`create` and an `update` issued directly with the same permissions would not.
What a unique constraint discloses to anyone who may create is unchanged, and
so is a row-condition refusal on the create payload: a caller allowed to
create at model level but not for these values sees `CONFLICT` on a taken key
and a row-check refusal on a free one, which is what the direct `create`
already showed them.

`upsert` has no field in the generated GraphQL schema. It is reachable
through the generated programmatic client and through the engine directly,
which is where this applies. The nested `upsert` inside a relation write is
Prisma's and is governed by the nested-write rules, not by this branch.

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
- A subscription stops when its client leaves. It previously did not. The
  resolver waited on the event bus inside an `await`, and a JavaScript async
  generator cannot honour `return()` while it is parked there — the request
  is queued until the generator next yields. A subscription that filters, or
  that an authorization rule silences, never yields again, so it stayed on
  the bus forever: one `findFirst` per event, for a client that had gone,
  holding its GraphQL context and the batch loaders keyed on it. Memory grew
  with subscriptions **opened**, not subscriptions **held**. Golem now races
  the bus against its own close signal, so teardown is immediate and does not
  depend on where the resolver happened to be waiting. It also no longer
  blocks on the bus's own `return()`, which for an `async *iterate()`
  implementation cannot resolve while that generator is itself parked.

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

# Known issues

Found while writing the Go specification in `docs/golem-go/`, against the
TypeScript at 0.6.0. None are fixed. Recorded so they are not rediscovered
from scratch.

---

## 1. Discharge is unsound, and the shortcut permits a filter oracle

**Severity: real. Reproduced against shipped code. Not fixed.**

### What it is

`dischargedByConstraint` is supposed to mean *every row the query can
reach is one where this field is readable*, and when it is true
`refuseUnreadableReferences` lets a filter on that field through
(`packages/core/src/field-references.ts:347`).

It is computed **syntactically** in the authorizer — roughly "this
field's rule chain contains no field-scoped and no inverted conditional
rule" — rather than by asking whether the row constraint actually implies
the field's condition.

Those differ, because a field-scoped grant for **another** field widens
the **row** constraint (a field grant grants the whole row) while being
dropped from **this** field's chain. The field's condition is then
computed against a narrower set than the query returns.

### Trigger

An ordinary shape: a row-level ownership grant plus a field-scoped grant
for some other field on the same model.

```ts
can('read', 'M', { userId: 'user-1' });
can('read', 'M', 'secretField', { flag: true });
```

### Reproduction

Run against the shipped `GolemAuthorizationAdapter`:

```
ROW CONSTRAINT    {"OR":[{"flag":true},{"userId":"user-1"}]}
FIELD CONDITION   {"userId":"user-1"}
CLASSIFICATION    {"access":"conditional","requires":["userId"],
                   "dischargedByConstraint":true}

ROW {"userId":"someone-else","flag":true}
  reachable by the query : true
  normalField readable   : false
  discharged says        : true
```

`OR[flag, userId]` does not imply `userId`. The row is returned by the
query and `normalField` is masked on it, yet filtering by `normalField`
is permitted — so its value is disclosed through row membership. That is
the value oracle the classification layer exists to close, reached
through the discharge shortcut rather than around it.

### Fix

Replace the syntactic proxy with the semantic question:

```
discharged  ⟺  Implies(rowConstraint, fieldCondition)
```

`constraintImplies` already exists in `packages/core/src/field-references.ts`
and is already used for the write-reach correction. Here it answers
correctly: `OR[flag, userId]` does not imply `userId`, so the filter is
refused.

Two things make this more than a one-line change.

- Discharge is computed in the authorizer, where the row constraint is
  not in hand. It has to move to core, where it is — the same relocation
  the write-reach fix made.
- `apps/demo/test/classify-discharge.e2e.test.ts:74` asserts the wrong
  answer as correct (`expect(result.normalField).toMatchObject({
  dischargedByConstraint: true })`). That assertion inverts. The suite is
  currently green *because* it encodes the defect.

---

## 2. Batch cache clearing misses the programmatic client

`clearBatchCaches` is called only from `written()` in
`packages/core/src/schema.ts`, which wraps the five generated GraphQL
write resolvers. A write through `forContext(ctx).post.create(...)` goes
straight to the engine and clears nothing, so a read after a write in the
same request still serves the stale batched value.

The clearing belongs in the engine, not the resolver. Half of the
read-after-write fix landed.

---

## 3. The shared-context guard does not cover every entry point

`assertContextWithinRequest` is applied at generated root query and
mutation resolvers and at batched computed fields. It is **not** applied
to subscriptions — `packages/nest/src/graphql-artifacts.ts` returns the
`subscribe` branch unwrapped — nor to the engine's own entry points, so
direct service calls are unguarded against the identity reuse the guard
exists for.

Related, now moot but worth knowing: the authorization memo is keyed by
the context object, so under a shared context the memoised `constrain`
and `classifyFields` answers were shared across callers too.

---

## 4. Latent and lower priority

**String ordering is UTF-16 in the evaluator, UTF-8 in SQL.** `"👍" <
""` is true in JavaScript and false under `COLLATE "C"`. A genuine
evaluator-versus-SQL divergence that the agreement oracle would catch if
the corpus reached it — it has an astral-plane row but never orders it
against U+E000–U+FFFF.

**`_relevance.fields` skips the unknown-field check.** Unlike every other
position, the names inside a full-text ordering are validated only as
strings, never against model metadata. A blanket `can('read', Model)`
then classifies any string as `always`. Prisma's validator catches the
consequence, so the fail-closed property here is borrowed rather than
golem's own.

**Cross-kind ordering operands validate but can never match.**
`{name: {lt: 5}}` passes validation, evaluates false for every row in
memory, and would be a Postgres type error if rendered. Unreachable today
only because the generated constructors do not permit it.

**Json operators are outside the two-valued probe suite.** The corpus
enumerates the scalar and scalar-list registries only. They appear
two-valued by inspection; nothing measures it.

**The batched relation path is an unbounded read.** It fetches every
child of every chunked parent and slices in memory, so `take: 5` over 900
parents with 10k children each pulls ~9M rows to return 4,500. Faithful
to Prisma, but "index your foreign key" only partly mitigates it.

**The 900-row chunk constant has no recorded rationale.** It sits
sensibly between SQLite's 32766 and Postgres's 65535 parameter ceilings,
but the reasoning was never written down and has twice been reconstructed
from the test fixtures rather than read.

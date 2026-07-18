# G8 Acceptance Report — eros against golem 0.2.0

Date: 2026-07-18
Consumer: **eros** (listening tracker; NestJS 11 + Prisma 7 + SQLite + better-sqlite3 adapter)
Versions tested: `@eleven-am/golem@0.2.0`, `-core@0.2.0`, `-authorizer@0.2.0`, `-generator@0.2.0`, `@eleven-am/golem-queue@0.1.7`
Verdict: **Accepted with two defects.** The core contract — engine-enforced, caller-scoped aggregation — works. Both defects are surface/ergonomic, neither is a policy leak.

---

## 1. What was verified

Config used (`Play` model only; every other model left without aggregations):

```ts
Play: {
  hidden: ['user', 'dedupeKey'],
  aggregations: {
    dimensions: ['trackId', 'skipped'],
    measures: ['msPlayed'],
    maxGroups: 100,
  },
}
```

Note the surprise, and it is a good one: **0.2.0 shipped a generated GraphQL surface** (`playsAggregate`, `playsGrouped`), which the brief said neither consumer required. It is the right call — eros deleted its planned `@CustomQuery` wrappers entirely. Zero application code now stands between a caller and a policy-scoped aggregate.

### Automated acceptance (7 e2e tests, all passing)

| Brief criterion | Result |
|---|---|
| eros #1 — cross-user isolation on grouped sums | **PASS**. Two-user fixture; user A's `playsGrouped(by: [trackId], sum: [msPlayed])` returns A's sums only. B has 500,000 ms on a track A also played; A's group for that track reads exactly 100,000 (A's two plays), zero contribution from B. |
| eros #2 — null over zero rows | **PASS**. `playsAggregate` with an empty range returns `count: 0`, `sum.msPlayed: null`, `max.msPlayed: null` — null, not zero. |
| eros #4 — order-by-measure + take | **PASS**. `orderBy: { sum: { msPlayed: desc } }, take: 1` returns the correct top group. |
| eros #5 — zero schema diff elsewhere | **PASS**. Aggregation fields exist for `Play` only; no `usersAggregate`/`tracksGrouped`/etc. All pre-existing SDL contract tests (no mutations, no `Session`/`Account`/`Job` types, no token-bearing fields, User field allowlist) unchanged. |
| Non-regression — `count` | **PASS**. Whole-model `playsAggregate(measures: { count: true })` returns per-caller counts (A: 3, B: 1). |
| Auth boundary | **PASS**. Unauthenticated aggregation returns `UNAUTHENTICATED`, not an unscoped result. |
| `maxGroups` guardrail | **PASS**. `take: 101` against `maxGroups: 100` → `BAD_USER_INPUT`, rejected not truncated. |

eros #3 (34k-group scale) is **deferred** — that dataset arrives with eros's history import (Phase 3). Current live corpus is 67 plays.

### Live verification (real Spotify data, authenticated HTTP)

```
playsAggregate → 67 plays, sum msPlayed 796,435, avg 79,644, max 177,591
playsGrouped by trackId, orderBy count desc → "With You Around" 6 plays, "Shallows" 4, "1 Up" 4 …
playsGrouped by trackId, orderBy sum msPlayed desc → "April" 210s, "With You Around" 207s, "KNOW ME" 178s
playsGrouped by skipped, orderBy count desc → false: 7 plays (avg 107s) | true: 3 plays (avg 15.6s) | null: 57 plays
```

That last row is the product working: eros's live watcher records `skipped` and real `msPlayed`; poll-sourced rows honestly carry `null` for both. The aggregation surface reflects that distinction without any application code.

---

## 2. Defect 1 — `groupBy` without an explicit `orderBy` always fails

**Severity: high (blocks the most natural query form).**

Any `playsGrouped` call that omits `orderBy` fails, regardless of dimension:

```graphql
{ playsGrouped(by: [trackId], measures: { count: true }, take: 3) { key { trackId } count } }
```

```
Invalid `delegate.groupBy()` invocation in
  @eleven-am/golem-core/dist/operations.js:703
Input error. Every field used for orderBy must be included in the by-arguments
of the query. Missing fields: id
extensions.code: INTERNAL_SERVER_ERROR
```

**Diagnosis**: with no `orderBy` supplied, a default ordering (`id`) is being passed through to `delegate.groupBy`. Prisma requires every `orderBy` field to appear in `by`, and `id` never will. Adding any explicit `orderBy` (e.g. `{ count: desc }`) makes the same query succeed.

**Expected**: omitting `orderBy` should produce an unordered `groupBy` — no ordering clause forwarded to Prisma.

**Secondary issue in the same finding**: the failure surfaces as `INTERNAL_SERVER_ERROR` with a raw Prisma invocation trace including internal file paths and source lines. Golem's stated error contract maps failures to stable codes with no Prisma internals; this path bypasses that mapping.

---

## 3. Defect 2 — enum columns cannot be aggregation dimensions

**Severity: medium (a natural dimension class is unavailable).**

Declaring an enum-typed column as a dimension crashes **schema construction at boot**, taking the whole app down:

```ts
aggregations: { dimensions: ['trackId', 'source', 'skipped'], … }  // source is enum PlaySource
```

```
Unsupported scalar type PlaySource on Play.source
  at scalarType (@eleven-am/golem-core/dist/schema.js:270)
  at fields  (@eleven-am/golem-core/dist/aggregations.js:87)
```

Enums are already first-class on the row surface (`Play.source` is queryable and filterable as `PlaySource`), so their absence from the aggregation type builder reads as an oversight rather than a decision. Grouping by an enum — source, status, type, category — is among the most common breakdowns an app wants.

**Workaround in eros today**: `source` dropped from dimensions; the poll/watch/import split can't be aggregated server-side.

**Expected**: enum dimensions supported as group keys (`PlayGroupKey.source: PlaySource`), or — if deliberately out of scope — a validation error at config time naming the field and the reason, consistent with 0.1.6's loud startup validation, rather than a scalar-mapper crash mid-schema-build.

---

## 4. Ergonomic note — `maxGroups` makes `take` mandatory

With `maxGroups` configured, omitting `take` is rejected:

```
playsGrouped requires take of at most 100   (BAD_USER_INPUT)
```

The *behavior* is right — refusing an unbounded group scan is exactly the fail-closed posture the brief asked for. The *message* is confusing when no `take` was supplied at all; it reads as though a too-large value was passed. Suggested: `"playsGrouped requires an explicit take of at most 100"`. Worth documenting in the README's aggregation section too, since it is a real API constraint, not just an error path.

---

## 5. Summary

- **Policy enforcement: correct.** Every isolation check passes. No path was found where one caller's rows influence another's aggregate. This is the part that mattered most and it holds.
- **Two defects**, both in the generated surface rather than the kernel: default-`orderBy` injection breaking unordered `groupBy` (high), and enum dimensions crashing schema build (medium).
- **eros is adopting 0.2.0 now**, with `source` omitted from dimensions and explicit `orderBy` on every grouped query as a temporary discipline. Both workarounds disappear when the defects are fixed; eros will add regression tests pinning the fixed behavior at that point rather than encoding the current behavior in tests today.
- Scale acceptance (34k groups) follows in eros Phase 3.

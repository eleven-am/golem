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

---

# Addendum — golem-core 0.2.1 re-acceptance (eros)

Date: 2026-07-18
Tested: `@eleven-am/golem-core@0.2.1` (peers left at 0.2.0; `^0.2.0` resolved to 0.2.1 as expected — no other package needed a bump)
Verdict: **Both defects fixed. Both workarounds dropped. No new defects.** eros is on 0.2.1.

## Fix verification (live, against real listening data)

| Fix | Result |
|---|---|
| Enum dimensions (`dimensionType`) | **FIXED.** `source` restored to `dimensions`; boot no longer crashes. `playsGrouped(by: [source])` returns `POLL: 57 plays (msPlayed null)` / `WATCH: 11 plays (1,019,476 ms)` — the poll/watch split is now aggregatable server-side, which was the capability the workaround cost us. |
| `groupBy` without explicit `orderBy` | **FIXED**, and better than requested. `byFieldsOrder(by)` supplies grouping-key ordering when `take` is present, so Prisma's take-requires-orderBy rule is satisfied legitimately rather than papered over. |
| Missing-`take` handling | **FIXED beyond the report.** `maxGroups` no longer forces `take` at all: the resolver fetches `limit + 1` and refuses only if the result genuinely exceeds the cap. Small groupings now work with neither `take` nor `orderBy` — verified live: `playsGrouped(by: [skipped], measures: { count, avg })` with no other args returns three groups. |
| Error mapping (P2009/P2019/P2020) | **FIXED.** Over-cap `take` now returns `BAD_USER_INPUT` with `playsGrouped requires an explicit take of at most 100`. |

## Correction to this report's earlier diagnosis (defect 1)

My 0.2.0 write-up attributed the `Missing fields: id` failure to golem injecting a default ordering. **That was wrong.** Reading 0.2.0's source: `toPrismaGroupOrderBy` returns `undefined` when no `orderBy` is given, and `engine.groupBy` only forwards `orderBy` when defined — golem injected nothing. The `id` ordering came from **Prisma**, which requires an explicit `orderBy` whenever `groupBy` uses `take`/`skip`. Your diagnosis — `take` is the trigger, and `maxGroups` is what made `take` unavoidable in eros's config — was correct.

One nuance worth keeping: on 0.2.0 the discipline *was* required **for models configured with `maxGroups`**, precisely because `take` was then mandatory and `take` triggered the Prisma rule. A model without a cap was unaffected, as you said. 0.2.1 makes the point moot by removing the forced `take`.

## Correction on "leaks raw Prisma internals"

Also partly mine to correct. On 0.2.1 the golem error object contains exactly `message` + `extensions.code` — no Prisma text, no paths. The `node_modules` paths still visible in eros's live responses come solely from Apollo's dev-mode `stacktrace` extension (`NODE_ENV` is not `production` locally); that is eros's own server hardening item, already tracked on our side, **not a golem issue**. The 0.2.0 complaint was valid only in that the *message itself* embedded the Prisma invocation text; that is gone.

## Regression tests added (eros)

`test/aggregation.e2e-spec.ts` — now 10 tests, pinning fixed behavior rather than the old shape:
- grouping by an enum dimension returns correct per-source counts;
- `groupBy` with `take` and **no** `orderBy` succeeds;
- `groupBy` with **neither** `take` nor `orderBy` succeeds when the result fits the cap;
- over-cap `take` → `BAD_USER_INPUT` carrying the "explicit take of at most 100" wording, with an assertion that the error object contains no `node_modules` path;
- plus the original seven (cross-user isolation on grouped sums and whole-model aggregates, null-over-zero-rows, order-by-measure top-N, surface scoping, unauthenticated rejection).

Full eros suite: **45 unit + 39 e2e green**, lint and build clean.

## Still open from eros

- **eros #3 (34k groups) remains unverified** — it needs Phase 3's history import, and that is where it will actually get tested. With the 0.2.1 semantics we now expect the refusal to fire at `maxGroups + 1` unless an explicit `take` is passed or the cap is raised; eros will pass an explicit `take` for top-N queries and reconsider the cap for full-catalog rollups.
- Everything else from the original brief that eros needs is delivered.

---

# Addendum 2 — golem 0.3.0 / golem-queue 0.2.0 adoption (eros)

Date: 2026-07-18
Tested: `@eleven-am/golem@0.3.0`, `-core@0.3.0`, `-authorizer@0.3.0`, `-generator@0.3.0`, `@eleven-am/golem-queue@0.2.0`
Verdict: **Adopted. One packaging defect found (typing only, no runtime impact).** All eros suites green: 68 unit + 41 e2e, lint and build clean, live boot verified.

## Defect — `PrismaJobStore` no longer accepts a generated Prisma client (type-level)

The documented wiring fails to compile against Prisma 7's generated client:

```ts
new PrismaJobStore(prisma)   // GolemPrismaService
```

```
error TS2345: Argument of type 'GolemPrismaService' is not assignable to parameter of type 'PrismaClientLike'.
  The types of 'job.groupBy' are incompatible between these types.
    Types of property 'by' are incompatible.
```

**Cause**: `PrismaJobDelegate.groupBy` (prisma-job-store.d.ts) declares

```ts
groupBy(args: { by: readonly string[]; where: Where; _count: { _all: true } }): Promise<Record<string, unknown>[]>;
```

Prisma's generated `job.groupBy` is a strict generic overload whose `by` is `JobScalarFieldEnum[]` with conditional constraints, so the real method is not assignable to the simpler structural signature. The other delegate methods (`create`, `findMany`, `findFirst`, `updateMany`, `deleteMany`) are fine — this is specific to the `groupBy` added for the database-side `countByStatus()`.

**Runtime is unaffected**: `job.groupBy({ by: ['status'], where, _count: { _all: true } })` executes correctly; the queue works (verified live — 110 SUCCEEDED jobs across five handler types under 0.2.0 validation).

**Why the release checks missed it**: this only surfaces when a *consumer* passes a Prisma client that has a `Job` model with generated types. A demo without `PrismaJobStore` wiring, or one whose client is typed loosely, compiles fine.

**Suggested fix**: relax the delegate signature so a generated client satisfies it — e.g. type `by` as `any[]`/`string[]` in a position that stays bivariant, or declare `groupBy` as `(args: never) => Promise<Record<string, unknown>[]>`-style structural escape, or accept `PrismaClientLike` as a generic parameter constrained only on the methods whose shapes are stable. Whatever shape is chosen, adding a consumer-side compile test that instantiates `PrismaJobStore` with a *generated* client would catch regressions here.

**eros workaround** (narrow, labeled, and removable): `new PrismaJobStore(prisma as unknown as PrismaClientLike)` with a TODO naming the exact condition for its removal.

## Migration items verified

| 0.3.0 change | eros result |
|---|---|
| Tightened `forContext()` argument allowlist | No compile errors. eros's context-client usage (`play.findMany`, `play.findUnique` in the policy e2e) is within the supported surface. |
| Aggregate output objects split per measure kind | Confirmed in generated SDL: `PlaySumValues` / `PlayAvgValues` / `PlayMinValues` / `PlayMaxValues` replace the shared `PlayMeasureValues`. eros's existing queries and assertions were source-compatible (same field names); `msPlayed` being `Int` means min/max stay `Int` and sum/avg are `Float`, so no BigInt/Decimal string-parsing changes applied here. |
| `forContext().upsert()` branch-probe semantics | No impact — every eros upsert (catalog, sync state, grant health, now-playing) is a deliberate system-stance plain-client call. |
| Queue startup + enqueue validation | Clean pass. `retention: { olderThanMs: 86_400_000 }`, five distinct handler types (`spotify-sync`, `spotify-watch`, `artist-enrich`, `timezone-rebucket`, plus scope-less enrichment), payloads of `{ userId: string }`. |

## Live verification after upgrade

```
playsAggregate → 94 plays, sum 3,420,655 ms, avg 117,953.6 ms, max 289,434 ms
playsGrouped by playedHour (local, Europe/Amsterdam) → 20:00 (23), 15:00 (14), 12:00 (13), 11:00 (13), 13:00 (11)
queue → 110 SUCCEEDED, 1 RUNNING
```

Aggregation answers are consistent with pre-upgrade values (corpus grew because the watcher kept recording during the upgrade). No regressions observed.

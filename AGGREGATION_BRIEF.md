# Aggregation Brief — Consolidated Requirements from eros and readable

> Status: historical requirements record. The local, policy-scoped aggregate/groupBy work was accepted in Golem 0.2.x. Current behavior and relation-dimension guidance live in `README.md`; this document is retained for design provenance and is not a current roadmap.

Date: 2026-07-18
Consumers: **readable-v3** (reading analytics, streaks, rollup replacement) and **eros** (listening analytics: top artists/tracks, time-of-day patterns, skip rates over ~500k plays).
Supersedes: the earlier eros "Policy-Aware Aggregation" requirements document in full. Where that document and this one disagree, this one wins.

---

## 1. Executive summary

Golem generates a complete, policy-enforced **row** surface but no **aggregation** surface. Both consumer apps need aggregation, and both currently face the same two options: paginate entire tables to aggregate client-side, or hand-write raw SQL where row scoping is a human-remembered `WHERE` clause instead of an engine-added one.

The consolidated ask is **G8: engine-enforced `aggregate` and `groupBy` on the context client** (`forContext(ctx).<model>.aggregate(...)` / `.groupBy(...)`), with the caller's constraint merged into the query by the same kernel that scopes `findMany` today. **No GraphQL surface is required by either consumer.** Both apps will expose stats through deliberate `@CustomQuery` operations that call the context client — the engine owns scoping, the operation owns transport shape.

The single most valuable item is the **R3 classification fix**: current 0.1.6 classification rejects legitimate aggregates on any scoped grant, because it classifies fields *before* scoping instead of *after*.

## 2. The defect in 0.1.6 (found by readable, verified in source)

`engine.aggregate` (`operations.ts:915`) runs `classifyFields('read', …)` and rejects any field not classified `always`-readable, then merges the constraint. For a scoped grant — either shape below — every field classifies as *conditional*, so **every aggregate on a scoped model is rejected**, even though every row surviving the merged `WHERE` is fully readable by construction.

Consequence in production today: readable falls back to `findMany + orderBy + take: 1` for max-progress, maintains hand-rolled `ReadingStat` rollup tables, and computes streaks in the browser. eros would be forced into hand-written raw SQL for every stat.

The correct rule (R3 below): **a field is aggregable iff it is readable on every row matched by the merged WHERE.** Classification runs after scoping, and stays fail-closed for conditions the merged WHERE does not discharge.

## 3. Fixtures — the two grant shapes that must both work

From readable's production schema (real shapes, not synthetic):

```prisma
model ReadingSession {
  id            String    @id @default(cuid())
  article       Article   @relation(fields: [articleId], references: [id], onDelete: Cascade)
  articleId     String
  startedAt     DateTime  @default(now())
  endedAt       DateTime?
  durationSec   Int       @default(0)
  progressStart Float     @default(0)
  progressEnd   Float     @default(0)
  sessionType   String    @default("reading")
}

model ReadingStat {
  id               String @id @default(cuid())
  userId           String
  date             String
  articlesRead     Int    @default(0)
  articlesListened Int    @default(0)
  readingSeconds   Int    @default(0)
  listeningSeconds Int    @default(0)
  wordsRead        Int    @default(0)
  @@unique([userId, date])
}
```

```ts
can(['read', 'create', 'update'], 'ReadingStat',    { userId: self.id });                    // owner-column scoped
can(['read', 'create', 'update'], 'ReadingSession', { article: { is: { userId: self.id } } }); // relation-scoped
```

`ReadingSession` has **no `userId` column** — the scope exists only through the relation. This is the hard case: the merged `WHERE` must traverse `article` exactly as G5's relation-hydrating verification does on the write side.

eros's grants are a strict subset — owner-column scoping only (`can('read', ['Play', 'SyncRun', 'ImportBatch'], { userId: user.id })`). Any implementation passing readable's fixtures passes eros automatically.

## 4. Requirements

### R1 — Constraint-merged `aggregate`

`_max`, `_min`, `_sum`, `_avg`, `_count` on the context client. The engine merges the ability constraint into the `WHERE` (relation traversal included), then aggregates over surviving rows. Canonical call (readable's real workaround site, `src/reading/reading.operations.ts:225`):

```ts
const { _max } = await client.readingSession.aggregate({
  where: { articleId, sessionType, endedAt: { not: null }, id: { not: currentSessionId } },
  _max: { progressEnd: true },
});
```

Must be equivalent to today's `findMany({ ...same where, orderBy: { progressEnd: 'desc' }, take: 1 })` — including returning `null` (not `0`) for `_max` over zero rows.

### R2 — Constraint-merged `groupBy`

Same constraint merging, with `by`, `where`, `having`, `orderBy`, `take`/`skip`, and the aggregate selectors. Canonical target — replacing readable's hand-maintained `ReadingStat` rollups and browser-side streak computation:

```ts
await client.readingSession.groupBy({
  by: ['sessionType'],
  where: { startedAt: { gte: windowStart } },
  _sum: { durationSec: true },
  _count: { _all: true },
});
```

eros canonical target — the trackId leg of the top-artists rollup:

```ts
await client.play.groupBy({
  by: ['trackId'],
  where: { playedAt: { gte: from, lt: to } },
  _sum: { msPlayed: true },
  _count: { _all: true },
  orderBy: { _sum: { msPlayed: 'desc' } },
  take: 20,
});
```

**Ordering by aggregate outputs is part of R2, not an extra.** `orderBy: { _sum: { … } }` (and the other measures) combined with `take`/`skip` must work, and the ordering measure classifies under R3 like any other readout. Without order-by-measure + take, every top-N query degrades to returning all groups (~34k for eros) and ranking client-side — the exact disease this brief exists to cure. Prisma's `groupBy` supports this natively; golem must not lose it in the wrapping.

Grouping keys are Prisma scalar-field enums — **expression grouping (date buckets) is explicitly out of scope**; both consumers accept the stored-bucket-column pattern (see §6).

### R3 — Field classification runs after scoping, and stays fail-closed

A field is aggregable **iff it is readable on every row matched by the merged WHERE**. For a whole-model scoped grant (both fixtures above), every scalar qualifies. Classification must still reject when readability is per-field conditional in a way the merged `WHERE` does not discharge (e.g. a field-level rule `can('read', 'M', 'secretField', { flag: true })` with a query not constrained to `flag: true`). Rejection is loud, naming the field and the undischarged condition — same spirit as 0.1.6's composite-model errors.

This rule also covers aggregate **outputs**: grouping keys and `having` fields are value readouts and classify under the same rule as measures.

### R4 — Interactive transactions

Both operations available on the `$transaction` context client (readable's `endReadingSession` computes progress inside its transaction).

### R5 — No GraphQL requirement

`operations: ['findOne', 'findMany']` stays the public surface for these models; aggregation is context-client-only for now. If a GraphQL aggregate surface ever ships, it must respect the per-model `operations` gate. Both consumers affirmatively **do not want** a generated GraphQL aggregation surface in v1 — public stats go through deliberate custom operations wrapping the context client.

## 5. Design principles (settled during review; treat as constraints)

1. **Scoping is engine-added for anything golem executes on behalf of a caller. It is never injected into developer-written SQL.** The plain client and `$queryRaw` remain the system stance — expert territory, no policy magic, exactly as documented today. The context client is the caller stance — policy always, automatically.
2. **Fail closed, always.** Any constraint shape or field condition the engine cannot provably discharge results in a **refused query with a named reason** — never a query that runs unscoped or a result that silently narrows. Error over leak, with no exceptions.
3. **One scoping implementation.** Everything in this brief is expressible through Prisma's own `where` slot (`aggregate` and `groupBy` both accept it), so the existing `mergeConstraint` path covers all of G8. **No constraint-to-SQL compiler is required for v1** — and none should be built until a v2 item (§6) actually demands it, because a second scoping implementation is a second place a leak can be born.

## 6. Explicitly deferred (v2 candidates, not v1 scope)

| Item | Why deferred | Sanctioned interim pattern |
|---|---|---|
| Expression/date-bucket grouping (`strftime`/`date_trunc`) | Requires golem to author SQL Prisma can't express → needs a constraint-to-SQL translation with leak-class failure modes | **Write-time bucket columns** (readable keeps one; eros will stamp `playedDate`/`playedHour` at ingestion) |
| Relation-hop grouping keys (`by: [track.artists.artistId]`) | Same reason | Context-client `groupBy` on the local FK + server-side rollup through the join table inside the custom operation |
| Distinct counts | Prisma has no distinct-count in `aggregate`/`groupBy` (`_count: { field }` counts non-nulls) | `groupBy` on the column, count groups server-side |
| Conditional measures (count-where alongside count) | Convenience, not capability | Two queries |
| Generated GraphQL aggregation surface | Neither consumer wants it; changes what a schema reveals about data distribution | Custom operations |
| Constraint→SQL compiler | Only justified if the first two rows proceed; if built: narrow scope (equality/`in` on own-model scalars), refuse anything unprovable | — |

## 7. Acceptance criteria

**readable-side** (verified on adoption):
1. `highestCompletedProgress` converted to R1 `aggregate` — behavior byte-identical to the `orderBy+take:1` version across: zero sessions, one session, excluded-current-session, and **cross-user isolation** (adversarial: user B's sessions on the same article never influence user A's `_max`).
2. A relation-scoped `groupBy` returns identical sums to the JS reduction it replaces.
3. Loud rejection (not silent narrowing) for the undischarged-field case in R3.
4. Full readable suite + the `graphql-public-contract` e2e stay green with zero schema diff.

**eros-side** (verified on adoption):
1. Top-tracks via R2 `groupBy(['trackId'])` under a two-user fixture: user A's grouped sums contain zero contribution from user B's plays of the same tracks (adversarial isolation at the aggregate level).
2. `_max`/`_sum` over zero rows returns `null`, matching R1 semantics, for a user with no plays in range.
3. `groupBy` at real scale: ~34,000 distinct groups (one user's historical distinct tracks) returns correctly on SQLite via the context client — no truncation, no cap on the programmatic stance.
4. Top-20 via `orderBy: { _sum: { msPlayed: 'desc' } }, take: 20` returns the same ranking as the full-fetch-and-sort it replaces.
5. eros's `graphql-public-contract` equivalents stay green with zero schema diff (`operations` gate untouched).

**Non-regression (both consumers):**
1. Existing constraint-merged `count` behavior from G2 — including `_count: { _all }` row counts on scoped grants — is byte-identical before and after the R3 reclassification. R3 touches the same classification path `count` already ships through; the one aggregate that works today must not be the casualty of fixing the rest.

## 8. Priority order (agreed by both consumers)

1. **R3** — the classify-after-scoping fix (unblocks `aggregate` for every scoped grant; a correctness fix, not a feature)
2. **R2** — `groupBy` with constraint merging (the core new capability)
3. **R1** — `aggregate` beyond `count` (falls out of R3 largely)
4. **R4** — transaction parity
5. Everything in §6: explicitly not v1.

## 9. Withdrawn items (for the record)

From the earlier eros document, superseded here:
- ~~Generated GraphQL aggregation queries (opt-in per model)~~ → withdrawn; R5 posture instead.
- ~~Relation-hop grouping as a v1 class~~ → deferred to §6; server-side rollup is acceptable.
- ~~Time-bucketed grouping as a v1 class~~ → deferred to §6; bucket columns are acceptable.
- ~~"Distinct counts are cheap to add"~~ → factually wrong (Prisma limitation); deferred to §6.
- ~~Constraint-to-SQL compiler as a v1 requirement~~ → withdrawn; explicitly discouraged for v1 by design principle 3.

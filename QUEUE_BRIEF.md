# Queue Brief — Cross-Handler Coordination in `@eleven-am/golem-queue`

Date: 2026-07-26
Version reviewed: `@eleven-am/golem-queue@0.3.1`
Consumer: **eros** (Spotify listening tracker — 494k rows, 7 job types, one shared third-party API)

---

## 1. Executive summary

The queue models each job type as an island. Concurrency, backoff, and leasing are all per-handler, and there is no vocabulary for expressing a relationship *between* two handlers. Three consequences follow, one of which caused a silent data defect in production and one of which is an unfixed exposure in every consumer that talks to a rate-limited API.

The asks, in priority order:

| | Ask | Status in consumer |
|---|---|---|
| **Q1** | Shared resource pools — a rate/concurrency budget spanning multiple handlers | Unfixed exposure today |
| **Q2** | Cross-type ordering constraints — `excludes` and/or `after` | Worked around app-side |
| **Q3** | Resource-level backpressure — a retryable error cools the pool, not just the job | Depends on Q1 |

Q1 is the most generic and the most valuable: any application whose handlers share a third-party API has this problem, and the queue currently offers no way to express it at all. Q2 is the more interesting primitive but overlaps with "write idempotent handlers," which is the better default regardless. Q3 only makes sense once Q1 exists.

**Section 5 lists what was checked and found already correct.** Several plausible-sounding gaps turned out to be handled well; they are documented so they are not rebuilt.

---

## 2. The structural observation

`job.dispatcher.js:39` declares `inFlight = new Map()`, keyed by job **type** (`:73`). Capacity is computed per type (`:125`):

```js
const capacity = handler.concurrency - (this.inFlight.get(type) ?? 0);
```

`QueueHandlerConfig` (`queue.decorators.d.ts`) offers exactly three knobs:

```ts
interface QueueHandlerConfig<TType extends JobType = JobType> {
  readonly type: TType;
  readonly concurrency?: number;
  readonly timeoutMs?: number;
}
```

There is no field by which one handler can refer to another, and no dispatcher state shared between types. Every ask below follows from that single fact.

---

## 3. Q1 — Shared resource pools

### The problem

eros runs four handlers that all call the Spotify Web API, which enforces one app-wide budget of roughly 180 requests/minute:

| Handler | `concurrency` | Calls Spotify |
|---|---|---|
| `spotify-watch` | 30 | yes — long-running poll loops, one per active user |
| `spotify-sync` | 3 | yes |
| `track-hydrate` | 1 | yes (50 IDs/call, 34k tracks) |
| `artist-enrich` | 1 | yes (50 IDs/call, 14k artists) |

Those numbers are each individually defensible and jointly meaningless: the declared concurrencies permit **up to 35 simultaneous Spotify-touching jobs**, and say nothing about the constraint that actually matters — *these four together must not exceed one shared budget.* The 30 is not carelessness; it is one watcher per user up to the provider's 25-user development-mode cap, and there is no way to say "30 watchers, but collectively polite."

Today the only lever is to hand-tune each handler's concurrency downward and hope the sum behaves. That is a guess that silently degrades as handlers are added, and it **cannot express a rate limit at all** — only a parallelism limit. A single handler at `concurrency: 1` issuing 50-ID batch calls in a tight loop can exhaust a per-minute budget on its own, which is exactly what `track-hydrate` does when working through a 34k-track backlog.

### The ask

Named resource pools declared in module config, referenced by handlers, acquired by the dispatcher before claiming:

```ts
GolemQueueModule.forRootAsync({
  resources: {
    spotify: { concurrency: 4, ratePerMinute: 180 },
  },
  // ...
})

@QueueHandler({ type: 'track-hydrate', concurrency: 1, resource: 'spotify', cost: 1 })
```

Both dimensions matter and they are not interchangeable: `concurrency` bounds simultaneous in-flight work, `ratePerMinute` bounds throughput over time. A consumer should be able to declare either or both. `cost` lets a handler that makes N calls per job declare its true weight.

Acquisition must happen **before** claiming, not before executing — otherwise jobs are claimed, leased, and then sit blocked, burning lease renewals and appearing RUNNING while doing nothing.

### Why the queue and not the application

An application-side throttle cannot see across handlers without becoming a shared singleton that every handler must remember to call — which is precisely the kind of implicit, unenforceable contract that produced the Q2 defect. The dispatcher already owns the claim decision; it is the only component positioned to make this correct by construction.

---

## 4. Q2 — Cross-type ordering constraints

### The defect this caused

In eros, `history-import` (bulk backfill from export files) and `track-hydrate` (catalog metadata from the API) are separate job types. Hydration also performed a denormalisation backfill: after hydrating a batch of tracks, it stamped `Play.primaryArtistId` for plays of those tracks.

That backfill was keyed to *the batch it had just hydrated*, and hydration only ever selects tracks not yet hydrated — so a track is never revisited. The correctness of this design rested on an assumption: **no import inserts plays for a track after that track has been hydrated.**

Nothing in the queue could express that assumption, and the reconciler enqueues both types freely, so they ran concurrently. Result:

- **104,536 plays** (21% of the library) permanently unattributed
- 1,359 tracks affected, every one hydrated before the final import batch finished
- `primaryArtistId: null` became the single largest bucket in the top-artists aggregation, outranking every real artist
- **Zero errors.** No job failed. No log line indicated a problem.

The defect was found only when a human eyeballed an aggregation result and asked why the top artist was `null`. An earlier run of the same code had produced 100% attribution — because that run happened to finish importing before hydration started. The bug was latent for weeks behind an ordering coincidence.

### The ask

A declarative constraint the dispatcher enforces at claim time. Two flavours, useful independently:

**Type-level exclusion** — do not claim while any job of the named types is PENDING or RUNNING:

```ts
@QueueHandler({ type: 'track-hydrate', excludes: ['history-import'] })
```

**Job-level dependency** — do not run until specific jobs reach a terminal state:

```ts
await queue.add('track-hydrate', payload, { after: [importJobId] });
```

The claim query already filters on type and status; exclusion is one additional `NOT EXISTS` predicate over the same index (`@@index([status, runAt])` plus a type filter). Job-level dependency needs a link table or a `dependsOn` column and is the heavier of the two — exclusion alone would have covered this case.

### Honest scoping

**This feature would not have made the original code correct, and it is not what the consumer ultimately shipped.** eros replaced the batch-coupled backfill with an idempotent one driven by the unattributed set (`primaryArtistId IS NULL AND EXISTS(<artist link>)`), which is strictly better: it also handles the case no exclusion rule covers — a future import inserting plays for tracks hydrated months earlier.

The value of Q2 is not that it fixes such bugs; it is that **it turns an invisible assumption into a declared one**. An assumption the queue can see is an assumption the queue can enforce or reject at startup. An assumption held only in a developer's head fails silently and stays failed. Consumers will keep encoding ordering assumptions whether or not the queue supports them — the question is only whether they are written down.

### A related finding worth passing on

While hardening the replacement, reverting the predicate to its hydration-coupled form produced a **non-terminating** repair loop, not a failing one: rows that can never be satisfied (tracks with genuinely no artist) were re-selected on every pass, each pass "updating" them to the same null. A chunked repair job whose pending set does not strictly shrink will spin forever inside its own timeout.

If Q2 or any future "repair job" pattern ships with guidance, that is the trap worth naming: **the pending predicate must exclude rows the work cannot change.** It is not obvious, it is invisible in unit tests with small fixtures, and its symptom is a hung job rather than an error.

---

## 5. Q3 — Resource-level backpressure

Today a 429 is handled per job:

```ts
throw new RetryableJobError(message, retryAfterMs, /* consumesAttempt */ false);
```

That correctly backs off the offending job — while every other handler sharing the same API keeps issuing requests into a limit the provider has just said is exhausted. A rate-limit response is information about the **resource**, not about the job that happened to receive it.

With Q1 in place, the natural shape is for a retryable error to optionally cool its pool:

```ts
throw new RetryableJobError(msg, retryAfterMs, false, { cooldownResource: true });
```

or, more simply, for the dispatcher to apply `retryAfterMs` to the whole pool whenever the failing handler declares a `resource`. Either way this is a small addition once pools exist, and near-impossible to retrofit without them.

---

## 6. Verified as already correct — please do not rebuild

These were checked against 0.3.1 source, not assumed. Several were on the initial suspect list and turned out to be handled well:

- **`dedupeKey` is cleared on every terminal transition** — `complete` (`prisma-job-store.js:122`), `fail` (`:145`), and `failExpiredLease` (`:174`). This is why a reconciler-ensures-work pattern self-heals after a crash: a FAILED job does not hold its dedupe key hostage and block re-enqueue. Correct, and non-obvious enough to be worth stating in the README.
- **`JobQueue.retryFailed(query)`** already exists for post-deploy recovery. (The consumer hand-deleted failed rows through SQL before noticing this; a README example would have prevented that.)
- **Introspection** — `find`, `findForScope`, `countByStatus` are sufficient; no new query surface needed.
- **Lease renewal** (`leaseDurationMs` / `leaseRenewIntervalMs`) and `abandonGraceMs` correctly recover jobs orphaned by process death. Verified live: leases advance on schedule and a killed process's RUNNING job is reclaimed.
- **`JobLifecycleObserver`** is a sufficient hook for metrics and alerting.

---

## 7. Acceptance criteria

**Q1**
1. Two handlers declaring the same `resource` never exceed the pool's `concurrency` in aggregate, verified under contention.
2. A single handler with `concurrency: 1` cannot exceed the pool's `ratePerMinute`.
3. `cost > 1` is respected — a handler declaring `cost: 5` consumes five units.
4. Resource acquisition happens before claim: a blocked job stays PENDING and unleased, and does not appear RUNNING.
5. Pool exhaustion never deadlocks — blocked handlers make progress once capacity frees.
6. Handlers with no `resource` are unaffected.

**Q2**
1. A handler declaring `excludes: ['x']` claims nothing while any `x` job is PENDING or RUNNING, and resumes immediately once none remain.
2. Exclusion does not deadlock when two handlers exclude each other — detected and rejected at startup, in the same spirit as the existing `concurrency` validation, which already throws on invalid values at registration.
3. Unknown type names in `excludes` fail at startup, not silently at runtime.
4. Exclusion adds no measurable claim latency when no excluded jobs exist.

**Q3**
1. A `RetryableJobError` carrying `retryAfterMs` from one handler defers every other handler sharing that resource for the same interval.
2. Cooldown expires without manual intervention and does not consume retry attempts for the deferred handlers.

---

## 8. Notes for prioritisation

If only one item ships, **Q1** is the one — it is the most generic, applies to every consumer touching a third-party API, and is currently inexpressible rather than merely awkward. Q2 is a genuinely interesting primitive but its main value is documentary; consumers should be writing idempotent handlers regardless, and the README could carry that guidance at zero engineering cost. Q3 is a small follow-on to Q1.

A README note covering the two findings in §4 (idempotent-over-ordered handlers; pending predicates must strictly shrink) would deliver a meaningful share of Q2's value with no API change at all.

# @eleven-am/golem-queue

Durable job queue for Golem apps. Lease-based claiming, retries with exponential backoff, per-handler concurrency, timeouts, and cooperative cancellation.

Built for the case where background work outlives a request: extraction pipelines, media processing, LLM calls — anything that must survive a restart and be retried honestly.

## Install

```bash
npm install @eleven-am/golem-queue
```

## 1. Add the schema

Prisma has no package-level models, so the `Job` model lives in your schema. Copy `prisma/golem-queue.prisma` into your `schema.prisma` and migrate:

```prisma
enum JobStatus { PENDING RUNNING SUCCEEDED FAILED }

model Job {
  id             String    @id @default(cuid())
  type           String
  payload        String
  scopeType      String?
  scopeId        String?
  status         JobStatus @default(PENDING)
  runAt          DateTime  @default(now())
  attempts       Int       @default(0)
  maxAttempts    Int       @default(3)
  lastError      String?
  dedupeKey      String?   @unique
  leaseOwner     String?
  leaseExpiresAt DateTime?
  createdAt      DateTime  @default(now())
  updatedAt      DateTime  @updatedAt

  @@index([status, runAt])
  @@index([status, leaseExpiresAt])
  @@index([scopeType, scopeId, status])
}
```

## 2. Declare your job types

Job types are yours, not Prisma's, so nothing can generate them. Declare them once — anywhere in your program — and both ends of the queue are checked against it:

```ts
declare global {
  interface GolemRegister {
    jobs: {
      'article.extract': { articleId: string };
      'article.audio': { articleId: string; voiceId: string };
    };
  }
}

export {};
```

This is optional. Without it `add` accepts any string and any payload, exactly as before.

## 3. Write a handler

The decorator registers the handler; the interface types it — the same split as `@Authorizer` and `WillAuthorize` in `@eleven-am/authorizer`.

```ts
import { Injectable } from '@nestjs/common';
import { QueueHandler, RetryableJobError, TerminalJobError, type JobEvent, type JobWork } from '@eleven-am/golem-queue';

@QueueHandler({ type: 'article.extract', concurrency: 2, timeoutMs: 60_000 })
@Injectable()
export class ExtractHandler implements JobWork<'article.extract'> {
  async handle({ payload, signal, attempt, maxAttempts }: JobEvent<'article.extract'>) {
    const res = await fetch(url, { signal });          // honour the signal
    if (res.status === 429 && attempt < maxAttempts) throw new RetryableJobError('rate limited', 60_000, false);
    if (res.status === 404) throw new TerminalJobError('gone');
  }
}
```

`payload` is typed from the registration. `attempt` and `maxAttempts` let a handler behave differently on its last try — notify, degrade, or write a tombstone — rather than failing silently into the dead-letter state.

A `JobEvent` carries `id`, `type`, `payload`, `attempt`, `maxAttempts`, `scope`, and `signal`.

- **`TerminalJobError`** — fail now, don't retry.
- **`RetryableJobError(message, retryAfterMs?, consumesAttempt?)`** — `retryAfterMs` is a *floor* on the backoff; `consumesAttempt: false` retries without burning an attempt (right for capacity/rate limits).
- Anything else retries with exponential backoff + jitter until `maxAttempts`.
- `signal` aborts on timeout, cancellation, and shutdown — pass it to your I/O.

### One job at a time per scope

Set `serializeByScope` when two jobs sharing a scope must never run together — one send per account, one export per workspace:

```ts
@QueueHandler({ type: 'message.send', concurrency: 8, serializeByScope: true })
```

Concurrency still applies across scopes: eight accounts send in parallel, one job per account at a time. Jobs with no scope are unaffected.

This is enforced in the store, not in the process, so it holds across N workers. A candidate is skipped when another job in its scope holds a **live** lease — `status = 'RUNNING' AND leaseExpiresAt > now`. A crashed worker's stranded row is not a blocker, or it would freeze its whole scope until lease recovery ran, which is precisely what recovery exists to fix.

`PrismaJobStore` needs `$executeRawUnsafe` on the client for this: the predicate and the claim have to be a single statement, and a same-table `NOT EXISTS` cannot be expressed through the delegate's where-shape. Check-then-claim across two statements races, on every engine — SQLite's single writer serializes statements, not a transaction with a read in the middle. If your `Job` model is `@@map`ped, pass the physical name: `new PrismaJobStore(prisma, { table: 'queue_jobs' })`.

`InMemoryJobStore` enforces the same rule, so tests exercise production semantics.

## 4. Register the module

```ts
import { GolemQueueModule, PrismaJobStore } from '@eleven-am/golem-queue';

GolemQueueModule.forRootAsync({
  imports: [PrismaModule],
  inject: [PrismaService, ConfigService],
  useFactory: (prisma: PrismaService, config: ConfigService) => ({
    store: new PrismaJobStore(prisma),
    pollIntervalMs: config.get('QUEUE_POLL_MS'),
    leaseDurationMs: config.get('QUEUE_LEASE_MS'),
  }),
  handlers: [ExtractHandler],
  observers: [PipelineStatusObserver],
})
```

The factory may return a bare store, or `{ store, ...options }` when the options themselves come from injected config. Anything it returns wins over statically supplied options.

`handlers` is optional, so a process that only enqueues — an API role in a split deployment — registers none and runs no dispatcher work.

## 5. Enqueue

```ts
await queue.add('article.extract', { articleId }, {
  scope: { type: 'Article', id: articleId },   // enables scope-wide cancellation
  dedupeKey: `article.extract:${articleId}`,   // returns false if already queued
});
```

Job types, dedupe keys, and supplied scope components must be non-empty; `maxAttempts` must be a positive integer and `runAt` a valid `Date`. Payloads must be JSON-safe objects. BigInt, circular references, non-finite numbers, functions, symbols, `undefined`, and unsupported object instances are rejected with `QueuePayloadError`; the error names the job type and failure category without printing the payload. Convert BigInt to a string before enqueueing.

Payloads can contain credentials, document content, or other sensitive application data. The persisted `Job.payload` and lifecycle observer transition `payload` contain the serialized value, so secure database access, logs, metrics, and observer destinations accordingly.

### Enqueueing inside your own transaction

Queueing work usually has to be atomic with a change to your own tables — mark a row "processing" *and* enqueue the job, or neither. Pass a `store` bound to your transaction:

```ts
await prisma.$transaction(async (tx) => {
  await tx.article.update({ where: { id }, data: { audioStatus: 'PENDING' } });
  await queue.add('article.audio', { articleId: id }, {
    scope: { type: 'Article', id },
    store: jobStore.withClient(tx),
  });
});
```

Without this the two writes can diverge — the job runs while your row still says idle, or your row says "processing" for work that was never queued.

Cancellation:

```ts
await queue.cancelForScope({ type: 'Article', id }, 'article deleted');  // aborts in-flight + drops queued
await queue.cancelPendingForScope({ type: 'Article', id }, 'superseded'); // leaves running work alone
await queue.cancelByDedupeKeys('article.audio', keys, 'voice changed');
```

`cancelForScope` aborts work running **on this process** synchronously and deletes the rows before returning. A job running on *another* worker is not stopped instantly: that worker notices the row is gone on its next reconcile pass and aborts then, so the bound is one `pollIntervalMs`, not zero.

## Long-running jobs: lease renewal

By default a lease is written once at claim time and lasts `timeoutMs + leaseGraceMs`. That makes `timeoutMs` control two unrelated things — how long a job may run, and how long a crashed worker's job stays stuck — with no value that is right for both. A short timeout expires the lease *while the job is still running* and another worker picks it up, executing it twice; a long one means slow recovery after a crash.

Set `leaseDurationMs` and the dispatcher heartbeats instead:

```ts
GolemQueueModule.forRootAsync({
  useFactory: (prisma: PrismaService) => new PrismaJobStore(prisma),
  leaseDurationMs: 60_000,          // how fast a crash is detected
  // leaseRenewIntervalMs defaults to leaseDurationMs / 3
  handlers: [ExtractHandler],
})
```

Now the lease is short regardless of job length, and `timeoutMs` is purely a cap on total duration. A job may run for an hour while a crashed worker is still recovered in a minute.

**If a renewal fails, the handler is aborted.** A worker that lost its lease — deposed, paused past expiry, partitioned — stops working immediately, because another worker may already own the job. Its `signal` fires exactly as it does for a timeout or a cancellation, so honouring the signal is what makes this safe. Without that abort, renewal would turn a bounded double-execution window into an unbounded one.

Renewal engages only when `leaseDurationMs` is set **and** the store implements `renewLease`. Both bundled stores do. A custom store without it keeps the write-once behaviour, unchanged.

Renewal is fenced like every other lease write: it updates only a `RUNNING` row still owned by this worker whose lease has **not** already expired. An expired lease cannot be renewed even by its original owner, because another worker may have claimed it in the meantime.

## Sharing a rate-limited dependency

Handler concurrency bounds one type. When several types draw on the same third-party API, what matters is the budget they share, and per-handler numbers cannot express it: four handlers at 30, 3, 1, and 1 permit 35 simultaneous callers against one budget, and each number looks defensible on its own.

Declare the pool in module config:

```ts
GolemQueueModule.forRootAsync({
  resources: {
    spotify: {
      concurrency: 4,
      types: ['spotify-sync', 'track-hydrate', 'artist-enrich'],
      costs: { 'spotify-sync': 2 },   // slots occupied while running, default 1
    },
  },
  handlers: [SyncHandler, HydrateHandler, EnrichHandler],
})
```

No handler changes. A job is claimed only when the summed cost of pool members already holding a live lease leaves room for it, evaluated inside the claiming transaction behind the pool's guard row — so two workers cannot both take the last slot.

**Membership is declared in config, not on the handler, because a worker counts only the types it is told about.** If `spotify-sync` runs in another process and is missing from `types`, every worker bounds the members it knows and the pool silently admits more than its limit. List every participating type, including those handled elsewhere; a type with no local handler is normal and accepted.

Occupancy is derived, never held. Nothing is acquired, so nothing leaks: a worker that dies frees its slots when its lease expires, through the same recovery that reclaims its job. A blocked job stays PENDING and unleased rather than sitting RUNNING and idle.

Each poll visits handlers in a rotating order, so a pool mate with high concurrency cannot take the whole pool every tick and starve a smaller one.

### What a pool cannot do

A pool bounds jobs, not the requests made inside them. A handler that runs for hours polling in a loop is one job to the queue: putting it in a pool of four would run four of them while each still calls the API as often as it likes. Rate-limit those calls in the client that makes them — the queue cannot see them.

## Ordering between job types

Handlers are independent by default: any two types can run at the same time. Two different constraints are available when that is not safe, and they are not interchangeable.

### `notWhileRunning` — never overlap

```ts
@QueueHandler({ type: 'track-hydrate', notWhileRunning: ['history-import'] })
@QueueHandler({ type: 'history-import', notWhileRunning: ['track-hydrate'] })
```

Neither type is claimed while the other holds a live lease. **Declare it on both sides** — one-sided is almost always a bug, because the undeclared side is free to start while the declared one is already running, which is the overlap you were trying to prevent.

It is safe in both directions: whichever claims first runs, the other waits, and progress is always possible. An expired lease does not block, for the same reason it does not block a scope: that job is not finished, it is about to be reclaimed, and treating it as a blocker would stop the recovery that resolves it.

### `waitsFor` — drain a backlog first

```ts
@QueueHandler({ type: 'nightly-rollup', waitsFor: ['event-ingest'] })
```

Blocks while the named types have *outstanding* work — a job that is due now, or one already running. It is a priority relation rather than an overlap rule: the rollup yields until ingestion has caught up.

Because it blocks on queued work and not just running work, it is **one-way by nature**. Two types that wait for each other could never start, so that is refused at startup. A job scheduled for later, or parked in retry backoff, does not block until it comes due.

### Which one

If your invariant is "these must not run at the same time", use `notWhileRunning` on both. If it is "do not start until that queue is empty", use `waitsFor`.

**Prefer an idempotent handler to either.** Both make an assumption enforceable, but a handler that can safely run at any time needs no assumption at all. Ordering constrains *when* work may run; idempotence removes the constraint, and it covers the case no ordering rule can — work arriving long after the job that would have blocked it.

If you do write a repair or backfill handler that processes a pending set in chunks, **the pending predicate must exclude rows the work cannot change.** A predicate that re-selects rows every pass without shrinking the set spins until the job times out. The symptom is a hung job rather than an error, and it does not reproduce against small fixtures.

### Starvation

Neither constraint can deadlock: `waitsFor` cycles are refused at startup, and `notWhileRunning` always lets one side proceed. Indefinite one-way starvation is still possible — a type waiting on work that never stops arriving never runs — so a handler blocked for many consecutive polls logs a warning naming what is blocking it, whether that is another type or a full resource pool.

The startup cycle check only sees handlers registered in **this** process. If two workers each register one side of a cycle, neither can detect it; `notWhileRunning` is the safe choice whenever the types are split across processes.

## Inspecting the queue

The queue is the source of truth for "is this still running?" — useful for driving UI state and for operator tooling.

```ts
await queue.findForScope({ type: 'Article', id });        // JobSummary[]
await queue.find({ types: ['article.audio'], statuses: ['FAILED'], limit: 50 });
await queue.countByStatus();                              // { PENDING, RUNNING, SUCCEEDED, FAILED }
```

A `JobSummary` carries `id, type, scope, status, attempts, maxAttempts, lastError, runAt, createdAt, updatedAt`.

## Dead letters and retention

```ts
await queue.retryFailed({ scopeType: 'Article', scopeId: id });  // requeue with a fresh attempt budget
await queue.prune({ olderThan: new Date(Date.now() - WEEK) });   // drop old SUCCEEDED/FAILED rows
```

`retryFailed` resets `attempts` to 0 and clears `lastError`, so a dead job gets a full budget rather than immediately re-failing.
Without `limit`/`skip`, it retries every failed job matching the filter; it does not inherit the 100-row listing default. Supplying `limit` and/or `skip` makes the administrative operation explicitly paginated.

Terminal jobs accumulate forever unless you prune them. Pruning can also run automatically — **opt-in**, because a library should not silently delete your rows:

```ts
retention: { olderThanMs: 7 * 24 * 60 * 60 * 1000, sweepIntervalMs: 60_000 }
```

## Delivery semantics

**Jobs run at least once.** A job can execute more than once when:

- a worker dies mid-job and its lease expires, so another worker recovers it;
- a handler ignores its `AbortSignal` past `abandonGraceMs` — the slot is freed and the job retried while the original work may still be running;
- the process is killed after the handler finished but before the completion write landed.

**Write your handlers to be idempotent.** `dedupeKey` only prevents duplicate *enqueues*; it does not make execution exactly-once.

## Observing lifecycle

Implement `JobLifecycleObserver` to react to work finishing — updating a status column, publishing a subscription event, emitting metrics:

```ts
onTransition({ jobId, type, scope, outcome, attempts, error, durationMs }) { ... }
```

Outcomes: `started`, `succeeded`, `retry-scheduled`, `failed-terminal`, `cancelled`.

## Options

| Option | Default | Meaning |
|---|---|---|
| `pollIntervalMs` | `500` | claim loop interval |
| `baseBackoffMs` | `2000` | retry backoff base (`base * 2^attempts` + jitter) |
| `maxBackoffMs` | `300_000` | ceiling on that growth — an explicit `retryAfterMs` still wins above it |
| `leaseGraceMs` | `30_000` | lease headroom beyond `timeoutMs` |
| `abandonGraceMs` | `5000` | how long to wait for an aborted handler to exit before freeing its slot |
| `shutdownGraceMs` | `15_000` | how long in-flight work may finish on shutdown before being aborted |
| `defaultMaxAttempts` | `3` | attempts when not set per job |
| `workerId` | `randomUUID()` | lease owner identity |
| `retention` | *off* | opt-in pruning of terminal jobs |

Options are validated when the module is configured. Durations that represent intervals must be positive, grace/backoff durations cannot be negative, `maxBackoffMs` must be at least `baseBackoffMs`, attempts must be a positive integer, worker IDs cannot be blank, and an enabled retention policy must have a useful positive age/interval and non-empty status set.

## How claiming works

Jobs are claimed with a compare-and-set `updateMany` guarded on `status` (and `leaseOwner` for completion), so **multiple workers are safe** — only one wins each job. A claim writes `leaseOwner` + `leaseExpiresAt` (`timeoutMs + leaseGraceMs`). If a worker dies, its lease expires and another worker recovers the job, incrementing `attempts`; once attempts are exhausted the job fails terminally rather than looping forever.

Cancellation across processes works through the database: the row is deleted, and each worker's reconcile pass aborts in-flight jobs it no longer owns. The in-process `JobCancellationRegistry` handles immediate aborts.

## Testing handlers

`InMemoryJobStore` implements the full `JobStore` port with no database:

```ts
const store = new InMemoryJobStore();
```

## Custom persistence

Implement the full `JobStore` port to back the queue with something other than Prisma.

The bundled `PrismaJobStore` claims work by polling for due candidates and winning each one with a compare-and-set update, through Prisma's own query API rather than hand-written SQL. That keeps it portable across SQLite, Postgres and MySQL, and it is the right default for most apps.

A claim that carries a guard — `serializeByScope`, `waitsFor`, `notWhileRunning`, or a resource pool — needs a serialization point, because the condition it tests is about *other* rows and engines do not serialize a read against a concurrent uncommitted write. Every competing claimer for a guard therefore writes one shared `JobGuard` row before reading anything, inside the transaction that claims. Writing first is what makes it work: competitors queue on a row lock rather than racing, and on SQLite it takes the writer lock up front instead of leaving a deferred reader that cannot upgrade. Guards are acquired in sorted order, so a claim needing several cannot deadlock against one acquiring the same guards in another order.

The trade-off is a round-trip per candidate, so claim throughput is bounded by the poll interval and the number of workers. If an app outgrows that, the escape hatch is a Postgres-native `JobStore` that claims a whole batch in one statement with `SELECT … FOR UPDATE SKIP LOCKED`. Nothing else has to change: the dispatcher only talks to the port.

## License

GPL-3.0

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

## 2. Write a handler

```ts
import { Injectable } from '@nestjs/common';
import { RetryableJobError, TerminalJobError, type JobHandler, type JobExecution } from '@eleven-am/golem-queue';

@Injectable()
export class ExtractHandler implements JobHandler {
  readonly type = 'article.extract';
  readonly concurrency = 2;
  readonly timeoutMs = 60_000;

  async handle(payload: Record<string, unknown>, { signal }: JobExecution) {
    const res = await fetch(url, { signal });          // honour the signal
    if (res.status === 429) throw new RetryableJobError('rate limited', 60_000, false);
    if (res.status === 404) throw new TerminalJobError('gone');
  }
}
```

- **`TerminalJobError`** — fail now, don't retry.
- **`RetryableJobError(message, retryAfterMs?, consumesAttempt?)`** — `retryAfterMs` is a *floor* on the backoff; `consumesAttempt: false` retries without burning an attempt (right for capacity/rate limits).
- Anything else retries with exponential backoff + jitter until `maxAttempts`.
- `signal` aborts on timeout, cancellation, and shutdown — pass it to your I/O.

## 3. Register the module

```ts
import { GolemQueueModule, PrismaJobStore } from '@eleven-am/golem-queue';

GolemQueueModule.forRootAsync({
  imports: [PrismaModule],
  inject: [PrismaService],
  useFactory: (prisma: PrismaService) => new PrismaJobStore(prisma),
  handlers: [ExtractHandler],
  observers: [PipelineStatusObserver],
})
```

## 4. Enqueue

```ts
await queue.add('article.extract', { articleId }, {
  scope: { type: 'Article', id: articleId },   // enables scope-wide cancellation
  dedupeKey: `article.extract:${articleId}`,   // returns false if already queued
});
```

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

## How claiming works

Jobs are claimed with a compare-and-set `updateMany` guarded on `status` (and `leaseOwner` for completion), so **multiple workers are safe** — only one wins each job. A claim writes `leaseOwner` + `leaseExpiresAt` (`timeoutMs + leaseGraceMs`). If a worker dies, its lease expires and another worker recovers the job, incrementing `attempts`; once attempts are exhausted the job fails terminally rather than looping forever.

Cancellation across processes works through the database: the row is deleted, and each worker's reconcile pass aborts in-flight jobs it no longer owns. The in-process `JobCancellationRegistry` handles immediate aborts.

## Testing handlers

`InMemoryJobStore` implements the full `JobStore` port with no database:

```ts
const store = new InMemoryJobStore();
```

## Custom persistence

Implement the `JobStore` port (12 operations) to back the queue with something other than Prisma.

The bundled `PrismaJobStore` claims work by polling for due candidates and winning each one with a compare-and-set update. That is deliberately portable — it behaves identically on SQLite, Postgres and MySQL — and it is the right default for most apps.

The trade-off is a round-trip per candidate, so claim throughput is bounded by the poll interval and the number of workers. If an app outgrows that, the escape hatch is a Postgres-native `JobStore` that claims a whole batch in one statement with `SELECT … FOR UPDATE SKIP LOCKED`. Nothing else has to change: the dispatcher only talks to the port.

## License

GPL-3.0

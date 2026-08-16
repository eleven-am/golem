# Durable job queue

Status: **implemented**. The package is a capability port of
`typescript/packages/queue`, corrected against two verified consumers: the
TypeScript implementation itself and Vela's production use of `backlite`.

The queue runs application-defined background jobs against the application's
own database: enqueue is durable and can join the caller's transaction, a
crashed worker's jobs are recovered by other workers, retries are bounded and
jittered, and a job that must not run twice does not run twice. It is the same
problem the outbox delivery machinery already solves for mutation facts,
applied to work the application defines rather than facts the framework
records.

## Packages and dependencies

- `go/queue` (public): job definitions, generic registration, outcome
  sentinels, operator surface, error codes. Standard library only; it imports
  nothing from `golem`, `runtime`, or generated code, matching the `render`
  precedent.
- `go/internal/queue/provider`: the provider-neutral store contract, mirroring
  `go/internal/event/provider` (validation helpers, sanitized row shape, no
  SQL escapes to callers).
- `go/internal/queue/worker`: the claim/dispatch/renew/outcome engine,
  mirroring `go/internal/event/outbox`.
- `go/internal/provider/sqlite/queue_store.go` and
  `go/internal/provider/postgresql/queue_store.go`: the two coordinators plus
  idempotent DDL bootstrap.
- `go/runtime`: `Config.Queue`, `RunQueueWorker`, `Enqueue`,
  `CallerTxEnqueue`, `SystemTxEnqueue`, `QueueOperator`.

`go.mod` gains no dependency. Payloads marshal with `encoding/json`.

## Durable state

One new table per application, `_golem_queue`, provider-owned storage in the
same style as `_golem_outbox_delivery` (microsecond-integer times on SQLite,
`timestamptz` on PostgreSQL):

- `id` (UUID text, primary key), `type` (text), `payload` (blob/bytea)
- `status` in `pending | leased | succeeded | failed | canceled`
- `attempt_count` (saturating), `max_attempts`
- `available_at`, `lease_token` (nullable), `lease_until` (nullable)
- `dedupe_key` (nullable), `exclusive_key` (nullable)
- `cancel_requested_at` (nullable), `last_code` (nullable)
- `enqueued_at`, `finished_at` (nullable), `updated_at`

Indexes: a claim index on `(status, available_at, type)`; a **partial unique**
index on `dedupe_key` where `status IN ('pending','leased')` (both engines
support partial unique indexes, and this replaces the TypeScript null-on-
terminal dance — the key survives on terminal rows for inspection, and
uniqueness only ever binds active work); a partial index on `exclusive_key`
where `status='leased'`.

### How the table exists without a physical format bump

This is the plan's most consequential constraint, verified in code. The
current physical format is version 3 and **frozen for publication**:
`physical.LatestFrozenPhysicalFormatVersion = 3`, and
`historical_v3.go` freezes the `SystemObjectKind` vocabulary at exactly
`{migration_ledger, migration_lock, outbox, outbox_delivery, upsert_guard}`
with a pinned shape hash and `TestCurrentPhysicalFormatIsFrozenForPublication`
guarding it. Introducing a `queue` system object kind therefore requires
physical format v4: new canonical encoding acceptance, a new historical freeze,
new fingerprint plumbing — precisely the class of release infrastructure this
repository just deleted 114,000 lines of. Rejected for v1.

The escape that stays entirely inside the frozen format is the existing
`Unmanaged` allowlist. `PhysicalSchema.Unmanaged` is a frozen v3 field whose
`Kind` is a free string validated only as an identifier
(`validateUnmanaged`); introspection deletes allowlisted objects from the
actual-catalog set before drift comparison
(`introspect.go: delete(actual, unmanaged.Kind+"\x00"+name)`); initial apply
tolerates them on a blank schema (`lifecycle.go`); SQLite migration only
special-cases unmanaged `trigger`/`view` dependents, not tables. Absence is
also tolerated — introspection deletes nothing when the object does not exist —
so the table may legitimately appear only after the queue first runs.

Therefore: when an application enables the queue, generation emits two
`UnmanagedObject` entries — `{Kind: "table", Name: "_golem_queue"}` and the
claim index — into both providers' lowered schemas, and the queue store
executes its own idempotent `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF
NOT EXISTS` before the first claim. Enabling the queue is thereby an ordinary
reviewed schema change (the canonical bytes and fingerprint change, the
migration plan contains zero DDL steps), and the migration CLI, introspection,
and initial apply all see a schema that already accounts for the table.

Two named uncertainties, to be verified as the first implementation task
before anything else is built:

1. `migration/diff.go` consults `Unmanaged` only in the initial-schema special
   case; it must be confirmed (or made true in a one-line change plus test)
   that an unmanaged-list delta between reviewed snapshots plans to zero DDL
   steps rather than a refusal.
2. The exact generation configuration surface where "queue enabled" is
   declared (alongside provider targets) is an implementation detail to be
   located; the requirement is one boolean reaching both lowerings.

If either verification fails in a way that cannot be repaired in a few lines,
the fallback is the v4 format bump — and that cost would be stated in a
revision of this plan, not absorbed silently.

## Public API

### `go/queue`

```go
type JobID string

type Registry struct{ /* unexported */ }

func NewRegistry() *Registry

type Backoff struct {
	Base time.Duration // default 5s
	Cap  time.Duration // default 5m
}

type Definition[T any] struct {
	Type          string
	Handle        func(context.Context, Job[T]) error
	MaxAttempts   int            // default 5
	Timeout       time.Duration  // handler context deadline; default 10m
	MaxConcurrent int            // per-type in-flight cap; 0 = global cap only
	Backoff       Backoff
	ExclusiveBy   func(T) string // optional; "" = not exclusive
}

type Job[T any] struct {
	ID          JobID
	Payload     T
	Attempt     int
	MaxAttempts int
	EnqueuedAt  time.Time
}

func Register[T any](registry *Registry, definition Definition[T]) (Type[T], error)

type Type[T any] struct{ /* unexported */ }

func (jobType Type[T]) New(payload T, options ...Option) (Pending, error)

type Option func(*enqueueOptions)

func After(delay time.Duration) Option
func Dedupe(key string) Option

type Pending struct{ /* unexported */ }

func Terminal(err error) error
func RetryIn(delay time.Duration, err error) error
func CompletedWith(code string, err error) error

type Limits struct {
	Concurrency     int           // default 4
	ClaimBatch      int           // default 16
	LeaseDuration   time.Duration // default 30s
	PollInterval    time.Duration // default 250ms
	ShutdownGrace   time.Duration // default 15s
	MaxPayloadBytes int           // default 1<<20
}

func DefaultLimits() Limits

type State string

const (
	StatePending   State = "pending"
	StateLeased    State = "leased"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

type Status struct {
	ID              JobID
	Type            string
	State           State
	Attempt         int
	MaxAttempts     int
	AvailableAt     time.Time
	LastCode        string
	CancelRequested bool
	EnqueuedAt      time.Time
	FinishedAt      *time.Time
}

type RetentionPolicy struct {
	OlderThan time.Time
	MaxRows   int
}

type Operator interface {
	Inspect(ctx context.Context, id JobID) (Status, error)
	Cancel(ctx context.Context, id JobID) (bool, error)
	Requeue(ctx context.Context, id JobID) (bool, error)
	RunRetention(ctx context.Context, policy RetentionPolicy) (int, error)
}

type ErrorCode string

const (
	CodeConfigInvalid  ErrorCode = "QUEUE_CONFIG_INVALID"
	CodePayloadInvalid ErrorCode = "QUEUE_PAYLOAD_INVALID"
	CodeJobNotFound    ErrorCode = "QUEUE_JOB_NOT_FOUND"
	CodeWorkerRunning  ErrorCode = "QUEUE_WORKER_RUNNING"
	CodeStoreFailure   ErrorCode = "QUEUE_STORE_FAILURE"
)

func CodeOf(err error) (ErrorCode, bool)
```

`CodeOf` recognizes failures through error wrapping and never classifies from
message text, matching `render.CodeOf`, `queryplan.CodeOf`, and
`events.CodeOf`. Every exported symbol carries godoc.

### `go/runtime` additions

```go
type QueueConfig struct {
	Registry *queue.Registry
	Limits   queue.Limits
}

// Config gains one field:
//   Queue *QueueConfig

func (app *App[P, A]) RunQueueWorker(ctx context.Context) error

func (app *App[P, A]) Enqueue(ctx context.Context, pending queue.Pending) (queue.JobID, error)

func CallerTxEnqueue[P, A any](ctx context.Context, transaction *CallerTx[P, A], pending queue.Pending) (queue.JobID, error)

func SystemTxEnqueue[P, A any](ctx context.Context, transaction *SystemTx[P, A], pending queue.Pending) (queue.JobID, error)

func (app *App[P, A]) QueueOperator() queue.Operator
```

`RunQueueWorker` follows the `RunEventPublisher` lifecycle exactly: `Open`
performs no background work, the worker owns its goroutines for the lifetime
of `ctx`, a second concurrent call fails with `CodeWorkerRunning`, and
shutdown honors `ShutdownGrace`.

### Smallest complete usage

```go
registry := queue.NewRegistry()

sendWelcome, err := queue.Register(registry, queue.Definition[WelcomeEmail]{
	Type: "email.welcome",
	Handle: func(ctx context.Context, job queue.Job[WelcomeEmail]) error {
		return mailer.Send(ctx, job.Payload.Address)
	},
})
if err != nil {
	return err
}

app, err := runtime.Open(ctx, runtime.Config[Principal, Actor]{
	// ... existing configuration ...
	Queue: &runtime.QueueConfig{Registry: registry, Limits: queue.DefaultLimits()},
})
if err != nil {
	return err
}
go app.RunQueueWorker(ctx)

pending, err := sendWelcome.New(WelcomeEmail{Address: address})
if err != nil {
	return err
}
jobID, err := app.Enqueue(ctx, pending)
```

### A long-running job with heartbeat and cancellation

Lease renewal is automatic: the worker renews every `LeaseDuration/3` through
the fenced `Renew`, and cancels the handler's context — with a cause the
handler can inspect — the moment renewal fails (the lease was lost) or a
durable cancellation request is observed. The handler's only obligation is the
ordinary Go one: honor its context.

```go
transcode, err := queue.Register(registry, queue.Definition[TranscodeRequest]{
	Type:        "video.transcode",
	MaxAttempts: 3,
	Timeout:     2 * time.Hour,
	ExclusiveBy: func(request TranscodeRequest) string { return request.VideoID },
	Handle: func(ctx context.Context, job queue.Job[TranscodeRequest]) error {
		for _, segment := range segmentsOf(job.Payload) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := renderSegment(ctx, segment); err != nil {
				if job.Attempt == job.MaxAttempts {
					return queue.Terminal(err)
				}
				return err
			}
		}
		return nil
	},
})
```

### Transactional enqueue

```go
err := runtime.CallerTransaction(ctx, caller, func(tx *runtime.CallerTx[Principal, Actor]) error {
	row, err := social.TxPosts(tx).Create(ctx, social.Posts.CreateInput{ /* ... */ })
	if err != nil {
		return err
	}
	pending, err := indexPost.New(IndexPost{PostID: rowID(row)})
	if err != nil {
		return err
	}
	_, err = runtime.CallerTxEnqueue(ctx, tx, pending)
	return err
})
```

The job row commits and rolls back with the domain write. This single property
deletes the entire class of workaround Vela built: the compensating saga and
three boot-time reconcilers exist only because backlite's queue lives in a
separate SQLite file where transactional enqueue is offered but unusable.
Runtime implements `CallerTxEnqueue`/`SystemTxEnqueue` against the execution
binding's queryer — the same seam every generated transactional operation uses
— so the insert cannot escape to the pool. The worker's in-process wake (below)
is nudged when the transaction commits, not when the row is written.

## Type safety

TypeScript bound job names to payload types through ambient module
augmentation. Go does it with generics at registration, the one place the pair
meet: `Register[T]` stores the definition alongside an unexported adapter
`func(context.Context, []byte, jobMeta) error` that unmarshals into `T` and
invokes the typed handler. The returned `Type[T]` is the only way to construct
a `Pending`, so a payload can never be enqueued under the wrong type name and
marshalling never appears in application code — the property worth taking from
backlite's generic queues. `Pending` is opaque and validated at construction
(`CodePayloadInvalid` for unmarshalable or oversized payloads), matching the
repository's pattern of refusing at the earliest boundary.

Registration is not decorator discovery: there is no DI layer, no
`enforcesClaimGuards`, no optional store methods. A `Registry` is a plain
value passed to `Open`, and Go's compiler replaces the ~280 lines of Nest
ceremony with nothing.

## The outcome vocabulary

Vela's central defect is vocabulary poverty: succeed/retry/dead-letter forced
`runStep` to swallow every error and lie. The queue's vocabulary is five
outcomes, each earned by a workload:

- **Succeeded** (`return nil`). Normal completion.
- **Succeeded with a recorded code** (`return queue.CompletedWith("sprites_partial", err)`).
  Vela's actual need: "this partially failed, record it, keep the pipeline
  moving." The job terminates successfully, `last_code` durably records what
  degraded, and the handler stops lying to the queue. This is one column
  write, not a new state.
- **Retry** (`return err`, or `return queue.RetryIn(delay, err)` to override
  the schedule). The default for a plain error, because transient failure is
  the common case and the safe default. Every retry consumes an attempt;
  exhaustion converts to failed with code `attempts_exhausted`. `RetryIn`
  exists for handlers that know the real horizon (a rate-limited API
  answering with `Retry-After`).
- **Failed** (`return queue.Terminal(err)`). Poison payloads and permanent
  refusals. Terminal, row retained for inspection, dedupe released (the
  partial index no longer binds). This state is the dead letter; there is no
  separate `blocked` — the outbox's blocked/retired distinction serves an
  operator triage flow that jobs express instead as failed plus operator
  `Requeue`.
- **Canceled** (externally requested). Distinct from failed — pinned by Vela's
  tests, and semantically necessary: a canceled job is a decision, a failed
  job is a defect. Cancellation is durable state, never an in-memory map.

The handler sees `Attempt` and `MaxAttempts` on the job, deleting Vela's
`SELECT MAX(attempts)` against a private table and its `finalAttempt` bool
threaded through three layers.

## Claiming, leases, fencing

Enqueue inserts a `pending` row with `available_at = now + delay`. When a
dedupe key is set, the insert is `ON CONFLICT` against the partial unique
index `DO NOTHING`, and the existing active job's ID is returned — enqueue of
in-flight work coalesces without error and without a table scan (Vela's dedupe
was a full-table JSON decode under a global mutex).

Claim, per provider, transcribing the delivery coordinator:

SQLite — `BEGIN IMMEDIATE`, then candidate discovery:

```sql
SELECT id FROM _golem_queue AS job
WHERE ((status='pending' AND available_at<=?) OR (status='leased' AND lease_until<=?))
  AND type IN (/* registered types with free capacity */)
  AND (exclusive_key IS NULL OR NOT EXISTS (
        SELECT 1 FROM _golem_queue AS holder
        WHERE holder.exclusive_key = job.exclusive_key
          AND holder.id <> job.id
          AND holder.status='leased' AND holder.lease_until > ?))
ORDER BY available_at, id LIMIT ?
```

then, per candidate, the fenced lease:

```sql
UPDATE _golem_queue
SET status='leased', attempt_count = /* saturating +1 */,
    lease_token=?, lease_until=?, updated_at=?
WHERE id=? AND ((status='pending' AND available_at<=?)
             OR (status='leased' AND lease_until<=?))
```

with `RowsAffected == 1` demanded, exactly as `event_delivery.go` does.

PostgreSQL — the same discovery with `FOR UPDATE SKIP LOCKED`, and for each
candidate carrying an `exclusive_key`, `pg_advisory_xact_lock` on a hash of
the key before evaluating the blocker predicate. The advisory lock closes the
READ COMMITTED window in which two claimers lease two *different* jobs sharing
one key (row locks protect the candidate row, not the key), and it is the
established Golem idiom: the upsert guard system object is documented as
"SQLite renders a guard-token relation; PostgreSQL implements the same
semantic object with transaction-scoped advisory locks," and
`postgresql.advisory_xact_lock` is already a verified provider capability.

The TypeScript `JobGuard` row and its write-only `seq` column do not port at
all. That mechanism manufactures a write-write conflict so claimers queue on a
row lock — a workaround for engines that give the claimer no serialization
primitive. Golem's SQLite claims already serialize on the database write lock
(`BEGIN IMMEDIATE`), and PostgreSQL already locks candidate rows
(`FOR UPDATE SKIP LOCKED`) with advisory locks for key-level exclusion. Both
engines provide natively what the guard row simulated.

Every subsequent transition is fenced on `(id, lease_token, status='leased')`:

- `Renew(id, token, duration)` — extends `lease_until`, and returns the
  durable `cancel_requested_at` flag alongside success, so renewal doubles as
  the cancellation poll with zero extra queries.
- `Succeed(id, token, code)` / `Fail(id, token, code)` /
  `RetryAt(id, token, availableAt, code)` / `MarkCanceled(id, token, code)` /
  `Release(id, token)` — each a single fenced UPDATE returning
  `changed bool`; a stale token changes nothing and reports it.

Renewal follows outbox semantics, not TypeScript's: an expired-but-unreclaimed
lease may still be renewed by its owner. The TypeScript rule (renewal requires
an unexpired lease) kills a live job because one heartbeat was late; the fence
already guarantees that once another worker reclaims the row, the old token is
dead. The dangerous case is takeover, and the token handles it.

Attempts increment at claim (saturating), for the poison-pill reason argued in
the outbox decision. Recovery of a crashed worker's jobs is the ordinary claim
predicate — no separate reaper, no recovery path to test separately.

## Concurrency and exclusivity

Two knobs, both enforced at claim time so that no job ever bounces through the
database (the Vela defect where a capacity bounce deleted the row as
*successful*, reinserted it, reset `attempts` to zero, and sent it to the back
of the queue):

- **Global concurrency** (`Limits.Concurrency`): the worker runs at most this
  many handlers; it claims at most the number of free slots.
- **Per-type limits** (`Definition.MaxConcurrent`): the worker tracks
  in-flight counts per type and simply omits saturated types from the claim's
  `type IN (...)` list. A gated job stays `pending`, untouched — attempts
  unconsumed, position preserved, invisible to the database entirely until
  capacity frees.

The `type IN` filter also means a worker only ever claims types its registry
knows; a deployment where different processes register different types
partitions naturally, and an unknown type sits pending rather than failing.

**Exclusivity** (`Definition.ExclusiveBy`): at most one live holder per key,
enforced inside the claim by the three-way liveness rule the TypeScript
implementation proved: a blocker must be `leased` with `lease_until > now`
(a crashed holder's stranded row never freezes the key, and recovery needs no
special case because expired leases fail the predicate), and a candidate is
never its own blocker (`holder.id <> job.id`). Waiting jobs stay `pending`;
there is no re-enqueue, no delay hop, no attempt consumption.

## Dispatch

Polling, with an in-process wake. Argued:

- The in-repo precedent is the outbox publisher: claim, and on an empty claim
  sleep `RetryBase` (250ms default) — accepted practice for the framework's
  own durable delivery.
- A PostgreSQL LISTEN/NOTIFY path costs a dedicated connection, reconnect and
  missed-notification handling, and a second dispatch code path to gate — and
  buys latency below 250ms for cross-process enqueues of *background* jobs.
- SQLite has no cross-process notification primitive at all, so the poll floor
  must exist regardless; notification would be a PostgreSQL-only fast path,
  violating provider parity for marginal gain.

What is taken from backlite is the part that costs nothing: a coalesced
in-process wake (a `chan struct{}` of capacity one), nudged by `Enqueue` and
by transaction commit when a `TxEnqueue` occurred inside it. In the common
single-process deployment — every SQLite deployment — enqueue-to-execution
latency is effectively zero and an idle worker issues no queries between
wakes beyond the `PollInterval` heartbeat that serves cross-process claims
and scheduled `available_at` arrivals.

## Cancellation, timeouts, heartbeat, shutdown

All four are `context.Context` mechanics over durable state, and the plan is
honest about the limit: Go cannot kill a goroutine. Cancellation and timeout
*request* termination by canceling the handler's context; a handler that
ignores its context runs until it returns, and the lease keeps renewing while
it does. The framework's guarantees are about state, not preemption: outcomes
recorded after a lost lease are fenced into no-ops, so even a zombie handler
cannot corrupt a reclaimed job.

- **Cancellation.** `Operator.Cancel` on a `pending` job is one CAS to
  `canceled`. On a `leased` job it durably sets `cancel_requested_at`; the
  owner observes it on the next renewal tick and cancels the handler context
  with a cancellation cause, and the eventual return records `canceled`, never
  `failed`. Because the flag is a column, it survives restarts — if the owner
  crashes, whichever worker reclaims the row sees the flag at claim time and
  finalizes `canceled` without executing. Vela's in-memory cancellation map,
  lost on restart, is the pinned counterexample.
- **Timeouts.** `Definition.Timeout` is a per-type context deadline —
  `video_sprites` at 452s and `video_embedding` at 6ms stop sharing one flat
  2-hour timeout. Timeout expiry is an ordinary retryable failure. The lease
  is deliberately independent of the timeout: crash detection latency stays
  30s even for two-hour jobs, because the heartbeat carries the lease, not
  the job duration.
- **Heartbeat.** Automatic, every `LeaseDuration/3`, via fenced `Renew`. A
  failed renewal means the lease was lost; the worker cancels the handler
  context immediately and discards the outcome (the fence would refuse it
  anyway). Handlers never manage heartbeats — the TypeScript design made
  renewal-failure-aborts-handler the application's job; here it is the
  worker's.
- **Shutdown.** Context cancellation stops claiming; running handlers get
  `ShutdownGrace`; claimed-but-unstarted jobs are released immediately
  (fenced `Release`: `pending`, available now), so a deploy does not strand
  work behind lease expiry.

## Failure semantics

- **Fails at Open / worker start:** nil registry, duplicate type names, empty
  type name, nil handler, non-positive `MaxAttempts`, invalid limits — all
  `CodeConfigInvalid` before any background work, matching `render.New`.
- **`CodePayloadInvalid`** at `Type.New`: oversized or unmarshalable payload,
  refused before it can be durable.
- **Store errors during a claim/transition** are returned to the worker loop,
  which observes-and-retries with the same backoff discipline as the outbox
  publisher rather than dying; `RunQueueWorker` only returns on context
  cancellation or unrecoverable configuration failure.
- **Observations: none in v1**, on the RENDER.md precedent and argument:
  `observe` is a closed validated vocabulary, and extending it touches the
  kind/operation sets, the telemetry manifest, and the adapters. Queue depth
  and outcomes are one `SELECT status, COUNT(*)` away for any operator, and
  the `Operator` interface exposes per-job state. If evidence later demands
  it, the addition follows the manifest procedure as its own change.


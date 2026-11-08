# The durable queue

Background work that survives a restart. Jobs live in your application's own
database, so enqueueing can share a transaction with the write that caused it —
no broker, and no window where the row committed but the job did not.

The program on this page is executed by `TestQueueApplicationRuns`.

## Registering a job type

A job type binds a payload struct to a handler. Registration happens before the
application opens, so a misconfigured type fails at startup rather than when
the first job runs.

```go
registry := queue.NewRegistry()

welcome, err := queue.Register(registry, queue.Definition[Welcome]{
	Type:        "welcome",
	MaxAttempts: 3,
	Timeout:     5 * time.Second,
	Handle: func(_ context.Context, job queue.Job[Welcome]) error {
		fmt.Printf("welcoming %s\n", job.Payload.Handle)
		return nil
	},
})
```

| Field | Meaning |
|---|---|
| `Type` | stable name, stored on the row; lower-case `[a-z0-9._-]`, at most 128 bytes |
| `Handle` | the work; returning an error schedules a retry |
| `MaxAttempts` | attempts before the job is dead-lettered; zero means 5 |
| `Timeout` | per-attempt limit; the handler's context is cancelled and the attempt is retried; zero means 10 minutes |
| `Backoff` | `Base` and `Cap` for the retry delay, exponential with full jitter; zero means 5 seconds and 5 minutes |
| `MaxConcurrent` | ceiling on leases of this type held at once by one worker process; zero means unbounded. Fleet-wide budgets are `QueueConfig.Resources` |
| `ExclusiveBy` | derives a key; jobs sharing one never run concurrently, across every worker |

Pass the registry when opening:

```go
application, err := notes.Open(ctx, notes.Config[Principal]{
	Database: database,
	Queue:    &golemruntime.QueueConfig{Registry: registry},
	...
})
```

Without `Queue`, `Enqueue` and `RunQueueWorker` refuse rather than silently
doing nothing.

## Enqueue and run

```go
pending, err := welcome.New(Welcome{Handle: "ada"})
jobID, err := application.Enqueue(ctx, pending)
```

`New` marshals and validates the payload against the registered type;
`Enqueue` writes the row. A worker claims and executes:

```go
go application.RunQueueWorker(ctx)
```

`RunQueueWorker` blocks until its context is cancelled. Run one per
application: a second concurrent call on the same application returns
`CodeWorkerRunning`. Several processes may each run one against the same
database, and claims are exclusive.

### Leases

A claim leases the job for `LeaseDuration` (30 seconds by default) and the
worker renews the lease every third of that while the handler runs. A renewal
that fails while the lease is still valid is tried again at the next renewal.
A renewal the store refuses, or one that fails once the lease has expired,
cancels the handler's context and discards its outcome; once the lease expires
another worker may claim the job, and that claim counts a new attempt. Renewals also carry `Cancel` requests to a running handler.

### Enqueue inside your own transaction

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	if _, err := tx.Notes.Create(ctx, notes.Notes.Create(notes.Notes.Title.Create("hello"))); err != nil {
		return err
	}
	_, err := tx.Enqueue(ctx, pending)
	return err
})
```

The job row commits with the note or neither does. This is the reason the queue
lives in your database rather than a broker.

## Retries

A handler that returns an error is retried until `MaxAttempts`, with the
backoff you configured. The program below registers a job that fails once:

```
welcome=succeeded attempt=1
flaky=succeeded attempt=2
```

`Attempt` counts executions, so a job that succeeded second time reports 2.
After the final attempt the job becomes `failed` with
`LastCode = attempts_exhausted`.

`queue.Terminal(err)` fails the job at once with `LastCode = terminal`, leaving
its remaining attempts unspent. `queue.CompletedWith(code, err)` records
success together with a code describing what degraded. A handler that panics
is recovered and retried like an error, with `LastCode = handler_panic`.

Return `queue.RetryIn(delay, err)` to override the backoff, or
`queue.RetryInWithoutAttempt(delay, err)` to defer **without** consuming an
attempt — for a dependency that is unavailable rather than a failure. Both
delays are clamped to 24 hours. That second form never exhausts attempts, so a
handler that always returns it produces a job that never dead-letters and never
ages out. Bound it on something other than the attempt count.

## Options

```go
welcome.New(payload, queue.After(30*time.Second))
welcome.New(payload, queue.Dedupe("welcome:ada"))
```

`After` delays first execution by up to 24 hours. `Dedupe` coalesces:
enqueueing an identical key while a job carrying it is pending or leased
returns that job's ID rather than creating a second; once the job is terminal
the key is free again. Keys are at most 256 bytes. `New` returns the error for
an option it refuses.

## Inspecting and operating

```go
operator := application.QueueOperator()
status, err := operator.Inspect(ctx, jobID)
```

`Status` carries `ID`, `Type`, `State`, `Attempt`, `MaxAttempts`,
`AvailableAt`, `LastCode`, `CancelRequested`, `EnqueuedAt` and `FinishedAt`,
and never the payload. The operator also offers `List`, `ListFailed`,
`CountByState`, `Cancel`, `CancelMany`, `Requeue`, `RequeueFailed` and
`RunRetention`. Pages and bulk actions are bounded at 256 jobs.

To cancel from inside your own transaction, call `tx.CancelJob(ctx, jobID)` on a
`CallerTx` or `SystemTx`. It returns the same `(bool, error)` as `Cancel` and
commits or rolls back with your other writes. Cancel only a job ID you read, in
the same transaction, from a row your policy allowed; never an ID taken from
arguments. Do no slow work after it in the same transaction: on PostgreSQL the
job row stays locked until commit.

`Cancel` is immediate for a pending job and cooperative for a leased one: the
request is recorded, the running handler's context is cancelled at its next
lease renewal, and completion may win first.

A handler returning is not the same as the job being recorded succeeded. If you
poll for a terminal state immediately after your handler runs, you will observe
`leased` before you observe `succeeded`.

## Worker limits

`QueueConfig.Limits` bounds the worker. Zero-valued fields take these
defaults:

| Field | Default |
|---|---|
| `Concurrency` | 4 handlers at once |
| `ClaimBatch` | 16 jobs per claim |
| `LeaseDuration` | 30s |
| `PollInterval` | 250ms |
| `ShutdownGrace` | 15s |
| `AbandonGrace` | 5s |
| `MaxPayloadBytes` | 1 MiB, which is also the ceiling |
| `RetentionAge` | 30 days |
| `RetentionEvery` | 1 minute |
| `RetentionRows` | 256 per pass |

`Open` refuses limits outside their ranges with `CodeConfigInvalid`:
`LeaseDuration` 1s..10m, `PollInterval` at most 24h, `AbandonGrace` at most
2m, `RetentionAge` 1h..10y, `RetentionEvery` 1m..24h or `RetentionDisabled`,
and `RetentionRows` at most 4096.

### Changing limits without a restart

`app.ReconfigureQueue(ctx, queue.Plan{...})` changes the live worker's
`Concurrency`, `ClaimBatch`, per-type `MaxConcurrent` and resource capacities.
`app.QueuePlan()` reports what is active.

```go
status, err := app.ReconfigureQueue(ctx, queue.Plan{
	MaxConcurrent: map[string]int{"analyze": 8},
	Capacities:    map[string]int{"analyze_pool": 8},
})
```

- **A plan states only the fields it changes.** A zero `Concurrency` or
  `ClaimBatch`, and any type or resource the plan leaves out, keeps the value
  in the active plan, so an earlier change survives a later one. A
  `MaxConcurrent` of 0 for a type means uncapped.
- **A refused plan changes nothing.** A negative value, an unregistered type,
  an unknown resource, a `ClaimBatch` above the maximum or a capacity below a
  cost returns `CodeConfigInvalid` and the old plan stays active.
- **Raising applies on the next claim, and lowering never interrupts.** There
  is no drain: running handlers finish, and nothing new is claimed until the
  live count is under the new value. When `ReconfigureQueue` returns, no claim
  computed from the old plan is still to start.
- **It works before `RunQueueWorker` starts and while it runs.**
- **`PlanStatus.Generation`** starts at 1 and rises by one for every accepted
  plan; `AppliedAt` is wall time. The plan lives in the process. It is not
  stored, so after a restart the application applies its own saved plan again
  after `Open`.
- **Resource capacity is fleet-wide.** Each claim uses the smaller of its own
  capacity and the capacity recorded on every live lease. After you raise a
  resource from 2 to 8, jobs already leased under 2 hold the budget at 2 until
  they finish; the new 8 takes effect as they end. Replicas that disagree on
  a capacity never exceed the smallest value held by a live lease.
  `Concurrency`, `ClaimBatch` and `MaxConcurrent` are per process.
- **Not changeable live:** resource costs and membership, `LeaseDuration`,
  `PollInterval`, `ShutdownGrace`, `AbandonGrace`, `MaxPayloadBytes` and the
  retention settings. Change those with a restart, and drain workers first
  for costs and membership.
- **Observation.** Each plan outcome emits one `PhaseApply` queue observation
  with the type name `plan`.

## Retention

Workers delete terminal job rows older than `RetentionAge` every
`RetentionEvery`, at most `RetentionRows` per pass. **This is on by default** —
30 days, checked every minute. Live rows are never deleted.

To keep history indefinitely:

```go
limits := queue.DefaultLimits()
limits.RetentionEvery = queue.RetentionDisabled
```

and pass `Limits: limits` in `QueueConfig`. History then survives until an
operator calls `RunRetention`.

## The queue's own tables

Golem creates `golem_queue` and its indexes when the application opens with
`Queue` set, and checks then, and again each time `RunQueueWorker` starts,
that the primary key is exactly `id` and that **every index on `golem_queue`
is one golem itself defined, with golem's exact definition**. A same-named
index with a different shape, or any other index on the table, stops startup
with an error naming the object. So does a golem index that is missing because
an object elsewhere in the database already carries its name. Drop it and let
golem recreate its own; accepting a wrong-shaped `golem_queue_dedupe` would
quietly turn off deduplication.

On SQLite the table is `main.golem_queue`. On PostgreSQL it lives in the
system namespace, `_golem` unless your schema names another.

If you added your own index to `golem_queue`, startup now refuses it. Golem
owns this table, so move that index to a table you own.

### Operator-history indexes

Three indexes serve the operator surface rather than the claim loop:

| Index | Columns | Serves |
| --- | --- | --- |
| `golem_queue_enqueued` | `(enqueued_at, id)` | `List` with no state filter |
| `golem_queue_history` | `(status, enqueued_at, id)` | `List` filtered by state |
| `golem_queue_terminal` | `(status, finished_at, id)` | `ListFailed` and retention |

On SQLite the store names them with `INDEXED BY`; on PostgreSQL the planner
chooses.

**Golem creates them only when your generated schema admits them.** That
happens when you regenerate and author the migration that records them, so a
library upgrade on its own changes nothing and stays reversible. Until you
regenerate, the queue behaves exactly as it did before, with no speedup. The
same regeneration also delivers the semantic strike column, so one action
delivers both.

## Timeouts and abandonment

When a handler exceeds its `Timeout`, its context is cancelled. Go cannot kill
a goroutine, so if the handler ignores cancellation and keeps running past
`AbandonGrace` (5 seconds by default), the worker records the retry anyway,
counting the attempt; on the last attempt the job becomes `failed`. The job
becomes claimable while the abandoned goroutine is still executing — it can
run twice, concurrently.

On shutdown a running handler gets `ShutdownGrace` to finish before its
context is cancelled. An error it returns after that is not recorded, and the
job is reclaimed once its lease expires; a success it returns within
`AbandonGrace` of the cancellation is still recorded.

Handlers must honour `ctx.Done()`. This is the one place the queue cannot
protect you.

## Not included

- **Recurring or cron scheduling.** A loop calling `Enqueue` with `Dedupe` is a
  few lines; building it in drags timezones, drift and leader election into a
  queue that has none of them.
- **Priority.** Delayed execution plus one queue per job class covers most
  orderings. Revisit on a named starvation case.

## Recipe: durable actions

An action that takes longer than a request (an export, a render, an import) has
the same shape every time. The caller asks, gets a handle back at once, and
watches the handle change. The work happens in a job, and the job may run more
than once, fail, or be cancelled. This recipe wires that shape from parts
golem already has, and the program at the end of it runs as written.

### The status row is the handle

Model the action's state as an ordinary model, keyed by the action's domain
identity (here, the export's name) rather than by the job ID. It carries
`golem.Subscriptions`, so a client watches it with `useGolemQuery` and sees
`queued` become `succeeded` without polling, and a policy, so only its owner
reads or writes it. Expose only the read operations over GraphQL: the status
changes through your mutations and your handler, never through a generic
`updateExport`.

### Accepting the action

`requestExport` is a [custom mutation](GUIDE.md#custom-operations). In one
`caller.Transaction` it enqueues the job and upserts the status row as
`queued`. The job carries `queue.Dedupe` set to the same domain key, so asking
twice while a job is still active returns the first job's ID instead of
starting a second. The status row and the job commit together or not at all,
and the mutation returns the row as the handle. The owner is not an argument:
the mutation reads it from `golem.ActorFrom[Actor](ctx)`, so a client cannot
name someone else. When you call the mutation from Go, go through the
generated `Mutate`, as the program below does; calling the resolver function
directly skips the actor. The policy is the ownership check: an upsert over someone else's row is refused as an update the caller
may not make.

### The acknowledgement window

The handler's transaction and the queue's acknowledgement are separate commits,
so delivery is at-least-once and there are two windows where "handler
committed, queue not yet acknowledged". A request can arrive after `RunExport`
committed `succeeded` but before the worker recorded the job as done; `Dedupe`
still sees the active job and returns its ID. `requestExport` therefore compares
the returned job ID with the row's stored one: the same ID is a coalesced
request and the row is returned untouched, a different ID is a new job (the
previous one is terminal) and the row is reset to `queued`. That reset is the
recipe's reprocess rule: asking again after a job has finished runs the export
again. Likewise, lease recovery can fail a job whose handler already committed
`succeeded`, so the failure callback updates only the row whose job ID equals
`failure.ID` and whose status is neither `succeeded` nor `canceled`.

### Every new row gets a job

When the rule is "every new post gets a job" there is no mutation to put the
call in. Enqueue from an `AfterCreate` hook:

```go
func (Post) AfterCreate(ctx context.Context, result PostCreateResult) error {
	pending, err := thumbnailJob.New(Thumbnail{PostID: result.Row().ID})
	if err != nil {
		return err
	}
	_, err = golem.HookEnqueue(ctx, result.Executor(), pending)
	return err
}
```

`HookEnqueue` writes the job on the hook's own transaction, so the job exists
if and only if the post commits. That is the reason not to use `AfterCommit`
here: an after-commit hook runs once the transaction is already durable, and a
crash or an error between the commit and the enqueue loses the job with
nothing left to retry it from. See the
[Hooks](GUIDE.md#hooks) section for the full contract.

### The handler runs at least once

The queue delivers a job at least once. A worker that dies after the handler
finished but before the job was recorded succeeded leaves a job another worker
will claim and run again, and a lease that expires mid-run does the same. The
handler must therefore be safe to run twice:

1. Read the status row first. If it is already `succeeded` or `canceled`,
   return: the work is done or no longer wanted.
2. Do the work.
3. In one `SystemTx`, write the result, set the status to `succeeded`, and
   enqueue the next stage if there is one.

Step 3 is the whole trick. The result, the status and the next job commit
together, so a second run either sees `succeeded` at step 1 and stops, or
finds nothing committed and redoes the work. It never finds a result without
its status, or a status without its next stage. `RunExport` below keeps its
work small: it reads the row and writes the result in the same transaction. A
handler with slow work reads the row first, does the work outside any
transaction, and writes in a second `SystemTx`.

When a stage has several parents, the last parent to finish enqueues the
child. Each parent marks itself done and counts the parents still open under a
lock key on the action, and only the one that sees none left enqueues the child:

```go
err := system.Transaction(ctx, func(tx *SystemTx[Principal]) error {
	if err := tx.LockKeys(ctx, golem.MustLockKey("export", exportID)); err != nil {
		return err
	}
	if _, err := tx.Parts.Update(ctx, Parts.ByID.Value(partID), Parts.Update(Parts.Done.Set(true))); err != nil {
		return err
	}
	open, err := tx.Parts.Count(ctx, Parts.Where(Parts.ExportID.Eq(exportID).And(Parts.Done.Eq(false))))
	if err != nil || open > 0 {
		return err
	}
	_, err = tx.Enqueue(ctx, assemble)
	return err
})
```

The child's `queue.Dedupe` on the action key is the second guard: if two
parents both see none left, the second enqueue returns the first job.

### Failure

A handler that returns an error is retried until its attempts run out. The
last failure moves the job to `failed`, and a handler that crashes on its last
attempt never gets to write anything. `OnJobFailed` closes that gap. golem runs
the callback in the transaction that marks the job failed, so the status row
becomes `failed` with a safe code (`attempts_exhausted`, `terminal`) if and
only if the job does. Pass the code, never the error text: the status row is
read by the owner's client.

### Cancel

`cancelExport` sets `cancel_requested` on the row and calls `tx.CancelJob` on
the job in the same transaction, so the job's cancellation commits or rolls back
with the `canceled` status. Cancel only a job ID you read, in that transaction,
from a row the caller's policy allowed; never cancel an ID taken from the
arguments. Cancellation is cooperative, not a
rollback. A job that has not started is cancelled at once. A job that is
running is told at its next lease renewal, and may finish first. Work already
done stays done. That is why the handler checks the row before it writes: a
handler that finishes after the cancel must not overwrite `canceled` with `succeeded`. A
handler with several steps repeats the check between steps. In the program
below the mutation marks a queued row `canceled` in its own transaction, and
`RunExport` declines any row that is no longer `queued`.

### Retry or reprocess

A custom mutation resets one stage's row to `queued` and enqueues it again,
exactly as `requestExport` does: the same transaction, the same `Dedupe` key.
Reprocessing a succeeded export is the same call. The handler's first check
looks at the row, finds `queued`, and does the work again.

### The program

Four files and three commands. The schema holds the status model and its
policy:

```go
// durable/schema.go
package durable

import "github.com/eleven-am/golem/go/golem"

type Principal struct{ UserID golem.UUID }

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

func (Status) GolemEnum() golem.EnumSpec[Status] {
	return golem.DefineEnum(
		golem.EnumValue(StatusQueued),
		golem.EnumValue(StatusSucceeded),
		golem.EnumValue(StatusFailed),
		golem.EnumValue(StatusCanceled),
	)
}

type Export struct {
	_ struct{} `golem:"model;id=example.durable.Export;table=exports;graphql=Export"`

	ID              string     `db:"id" golem:"id=example.durable.Export.ID;pk;type=varchar(64)"`
	OwnerID         golem.UUID `db:"owner_id"`
	JobID           string     `db:"job_id" golem:"type=varchar(64)"`
	Status          Status     `db:"status" golem:"default=queued"`
	CancelRequested bool       `db:"cancel_requested" golem:"default=false"`
	ErrorCode       string     `db:"error_code" golem:"type=varchar(64)"`
	Result          string     `db:"result" golem:"type=varchar(200)"`
}

func (Export) GolemModel() golem.ModelSpec[Export] {
	return golem.DefineModel(
		golem.GraphQL[Export](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLFindMany)),
		golem.Subscriptions[Export](),
	)
}

func (Export) DefinePolicy(rules *golem.Rules[Export], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := Exports.OwnerID.Eq(actor.UserID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "durable")
	golem.Actor[Actor](schema)
	golem.Model[Export](schema)
	golem.Providers(schema, golem.SQLite)
}
```

The actions, the handler and the failure callback live in one file. `ExportJob`
is a package variable because a custom operation has no handle to the queue's
job type; `main` sets it once, before the application opens. The generated `Caller`, `System`,
`Exports` and `Open` come from the next command:

```go
// durable/exports.go
package durable

import (
	"context"
	"errors"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
)

type ExportPayload struct {
	ID   string `json:"id"`
	Fail bool   `json:"fail"`
}

var ExportJob queue.Type[ExportPayload]

type RequestExportArgs struct {
	ID   string `golem:"graphql=id"`
	Fail bool   `golem:"graphql=fail"`
}

type CancelExportArgs struct {
	ID string `golem:"graphql=id"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "requestExport", RequestExport)
	golem.Mutation(graphql, "cancelExport", CancelExport)
}

func RequestExport(ctx context.Context, caller *Caller[Principal], args RequestExportArgs) (golem.Row[Export], error) {
	pending, err := ExportJob.New(ExportPayload{ID: args.ID, Fail: args.Fail}, queue.Dedupe("export:"+args.ID))
	if err != nil {
		return golem.Row[Export]{}, err
	}
	var handle golem.Row[Export]
	err = caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		jobID, err := tx.Enqueue(ctx, pending)
		if err != nil {
			return err
		}
		existing, err := tx.Exports.FindMany(ctx, Exports.Where(Exports.ID.Eq(args.ID)))
		if err != nil {
			return err
		}
		if len(existing) == 1 {
			if stored, _ := golem.Value(existing[0], Exports.JobID).Get(); stored == string(jobID) {
				handle = existing[0]
				return nil
			}
		}
		handle, err = tx.Exports.Upsert(ctx, Exports.ByID.Value(args.ID),
			Exports.Create(Exports.ID.Create(args.ID), Exports.OwnerID.Create(golem.ActorFrom[Actor](ctx).UserID), Exports.JobID.Create(string(jobID)), Exports.ErrorCode.Create(""), Exports.Result.Create("")),
			Exports.Update(Exports.JobID.Set(string(jobID)), Exports.Status.Set(StatusQueued), Exports.CancelRequested.Set(false), Exports.ErrorCode.Set("")),
		)
		return err
	}, golem.RetryConflicts(3))
	return handle, err
}

func RunExport(ctx context.Context, system System[Principal], payload ExportPayload) error {
	return system.Transaction(ctx, func(tx *SystemTx[Principal]) error {
		current, err := tx.Exports.FindUnique(ctx, Exports.ByID.Value(payload.ID))
		if err != nil {
			return err
		}
		if status, _ := golem.Value(current, Exports.Status).Get(); status != StatusQueued {
			return nil
		}
		if payload.Fail {
			return errors.New("renderer unavailable")
		}
		_, err = tx.Exports.Update(ctx, Exports.ByID.Value(payload.ID),
			Exports.Update(Exports.Status.Set(StatusSucceeded), Exports.Result.Set("export-"+payload.ID+".zip")))
		return err
	})
}

func MarkExportFailed(ctx context.Context, tx *SystemTx[Principal], failure queue.Failure[ExportPayload]) error {
	_, err := tx.Exports.UpdateMany(ctx,
		Exports.ID.Eq(failure.Payload.ID).
			And(Exports.JobID.Eq(string(failure.ID))).
			And(Exports.Status.Ne(StatusSucceeded)).
			And(Exports.Status.Ne(StatusCanceled)),
		Exports.UpdateMany(Exports.Status.Set(StatusFailed), Exports.ErrorCode.Set(string(failure.Code))),
	)
	return err
}

func CancelExport(ctx context.Context, caller *Caller[Principal], args CancelExportArgs) (golem.Row[Export], error) {
	var handle golem.Row[Export]
	err := caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		var err error
		handle, err = tx.Exports.FindUnique(ctx, Exports.ByID.Value(args.ID))
		if err != nil {
			return err
		}
		jobID, _ := golem.Value(handle, Exports.JobID).Get()
		if _, err = tx.CancelJob(ctx, queue.JobID(jobID)); err != nil {
			return err
		}
		_, err = tx.Exports.UpdateMany(ctx,
			Exports.ID.Eq(args.ID).And(Exports.Status.Eq(StatusQueued)),
			Exports.UpdateMany(Exports.CancelRequested.Set(true), Exports.Status.Set(StatusCanceled)),
		)
		return err
	}, golem.RetryConflicts(3))
	return handle, err
}
```

`CancelExport` calls `tx.CancelJob` before it touches the status row because
the failure callback locks the job row first and its status row second; taking
them in the other order deadlocks on PostgreSQL. Both mutations also pass
`golem.RetryConflicts(3)`, since a conflict is a normal outcome there.

The program accepts three exports, cancels one before the worker starts, makes
one fail, and prints each final status:

```go
// cmd/durable/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/notes/durable"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:durable.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		log.Fatal(err)
	}

	var application *durable.App[durable.Principal]
	registry := queue.NewRegistry()
	durable.ExportJob, err = queue.Register(registry, queue.Definition[durable.ExportPayload]{
		Type:        "export",
		MaxAttempts: 2,
		Timeout:     5 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
		Handle: func(ctx context.Context, job queue.Job[durable.ExportPayload]) error {
			return durable.RunExport(ctx, application.System(), job.Payload)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	application, err = durable.Open(ctx, durable.Config[durable.Principal]{
		Database:            database,
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		Queue:               &golemruntime.QueueConfig{Registry: registry},
		QueueFailed:         []golemruntime.JobFailed[durable.Principal, durable.Actor]{durable.OnJobFailed(durable.ExportJob, durable.MarkExportFailed)},
		ResolvePrincipal: func(_ context.Context, principal durable.Principal) (durable.Actor, error) {
			return durable.Actor{UserID: principal.UserID, Authenticated: true}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	owner := golem.NewUUID([16]byte{1})
	caller, err := application.ForPrincipal(ctx, durable.Principal{UserID: owner})
	if err != nil {
		log.Fatal(err)
	}
	for _, request := range []durable.RequestExportArgs{
		{ID: "report"},
		{ID: "failing", Fail: true},
		{ID: "canceled"},
	} {
		if _, err := durable.Mutate(ctx, caller, durable.RequestExport, request); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := durable.Mutate(ctx, caller, durable.CancelExport, durable.CancelExportArgs{ID: "canceled"}); err != nil {
		log.Fatal(err)
	}

	worker, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := application.RunQueueWorker(worker); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker stopped: %v", err)
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := caller.Exports.FindMany(ctx, durable.Exports.Where(durable.Exports.Status.Ne(durable.StatusQueued)))
		if err != nil {
			log.Fatal(err)
		}
		if len(rows) == 3 {
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("stalled with %d of 3 exports finished", len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range []string{"report", "failing", "canceled"} {
		row, err := caller.Exports.FindUnique(ctx, durable.Exports.ByID.Value(id))
		if err != nil {
			log.Fatal(err)
		}
		status, _ := golem.Value(row, durable.Exports.Status).Get()
		code, _ := golem.Value(row, durable.Exports.ErrorCode).Get()
		fmt.Printf("export=%s status=%s code=%s\n", id, status, code)
	}
}
```

```sh
golem migration new --schema ./durable --name init
golem generate --schema ./durable --app-out ./durable
golem migration apply --provider sqlite --dsn "file:durable.db"
```

It prints:

```
export=report status=succeeded code=
export=failing status=failed code=attempts_exhausted
export=canceled status=canceled code=
```

The program does not run the event publisher, so nothing reaches a client
watching `Export`. A server runs `application.RunEventPublisher(ctx)` beside
the queue worker; see [Event transports](GUIDE.md#event-transports).

## The whole program

A minimal schema, so the application has a database to keep jobs in:

```go
// notes/schema.go
package notes

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`

	ID    golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	Title string     `db:"title" golem:"type=varchar(200)"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	rules.CanRead(golem.All[Note]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Note]())
	}
}
```

And the program itself:

```go
// cmd/notes/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/notes/notes"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type Principal struct{ Authenticated bool }

type Welcome struct {
	Handle string `json:"handle"`
}

type Flaky struct {
	SucceedOnAttempt int `json:"succeedOnAttempt"`
}

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:notes.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	registry := queue.NewRegistry()
	welcome, err := queue.Register(registry, queue.Definition[Welcome]{
		Type:        "welcome",
		MaxAttempts: 3,
		Timeout:     5 * time.Second,
		Handle: func(_ context.Context, job queue.Job[Welcome]) error {
			fmt.Printf("welcoming %s\n", job.Payload.Handle)
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	runs := 0
	flaky, err := queue.Register(registry, queue.Definition[Flaky]{
		Type:        "flaky",
		MaxAttempts: 3,
		Timeout:     5 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
		Handle: func(_ context.Context, job queue.Job[Flaky]) error {
			runs++
			if runs < job.Payload.SucceedOnAttempt {
				return errors.New("downstream not ready")
			}
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	application, err := notes.Open(ctx, notes.Config[Principal]{
		Database: database,
		Queue:    &golemruntime.QueueConfig{Registry: registry},
		ResolvePrincipal: func(_ context.Context, principal Principal) (notes.Actor, error) {
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	welcomePending, err := welcome.New(Welcome{Handle: "ada"})
	if err != nil {
		log.Fatal(err)
	}
	welcomeJob, err := application.Enqueue(ctx, welcomePending)
	if err != nil {
		log.Fatal(err)
	}
	flakyPending, err := flaky.New(Flaky{SucceedOnAttempt: 2})
	if err != nil {
		log.Fatal(err)
	}
	flakyJob, err := application.Enqueue(ctx, flakyPending)
	if err != nil {
		log.Fatal(err)
	}

	worker, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := application.RunQueueWorker(worker); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker stopped: %v", err)
		}
	}()

	operator := application.QueueOperator()
	deadline := time.Now().Add(20 * time.Second)
	for {
		first, err := operator.Inspect(ctx, welcomeJob)
		if err != nil {
			log.Fatal(err)
		}
		second, err := operator.Inspect(ctx, flakyJob)
		if err != nil {
			log.Fatal(err)
		}
		if first.State == queue.StateSucceeded && second.State == queue.StateSucceeded {
			fmt.Printf("welcome=%s attempt=%d\n", first.State, first.Attempt)
			fmt.Printf("flaky=%s attempt=%d\n", second.State, second.Attempt)
			return
		}
		if time.Now().After(deadline) {
			log.Fatalf("stalled welcome=%s flaky=%s", first.State, second.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

```

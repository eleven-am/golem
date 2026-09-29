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
that fails cancels the handler's context and discards its outcome; once the
lease expires another worker may claim the job, and that claim counts a new
attempt. Renewals also carry `Cancel` requests to a running handler.

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
`LeaseDuration` 1s..10m, `AbandonGrace` at most 2m, `RetentionAge` 1h..10y,
`RetentionEvery` 1m..24h or `RetentionDisabled`.

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
context is cancelled. An outcome it returns after that is not recorded; the
job is reclaimed once its lease expires.

Handlers must honour `ctx.Done()`. This is the one place the queue cannot
protect you.

## Not included

- **Recurring or cron scheduling.** A loop calling `Enqueue` with `Dedupe` is a
  few lines; building it in drags timezones, drift and leader election into a
  queue that has none of them.
- **Priority.** Delayed execution plus one queue per job class covers most
  orderings. Revisit on a named starvation case.

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

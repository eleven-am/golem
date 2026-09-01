package gate

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/golempolicykit/policy"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type queuePayload struct {
	Name string `json:"name"`
}

func TestExternalGeneratedApplicationQueueIsUsable(t *testing.T) {
	for _, value := range targets(t) {
		value := value
		t.Run(value.name, func(t *testing.T) {
			database := value.open(t)
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()

			handled := make(chan string, 3)
			registry := queue.NewRegistry()
			jobType, err := queue.Register(registry, queue.Definition[queuePayload]{
				Type: "gate.generated",
				Handle: func(_ context.Context, job queue.Job[queuePayload]) error {
					handled <- job.Payload.Name
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
			if err != nil {
				t.Fatal(err)
			}
			application, err := policy.Open(context.Background(), policy.Config[policy.Actor]{
				Database:       database,
				EventTransport: transport,
				Queue: &golemruntime.QueueConfig{
					Registry: registry,
					Limits:   queue.Limits{Concurrency: 2, ClaimBatch: 2, LeaseDuration: time.Second, PollInterval: 10 * time.Millisecond, ShutdownGrace: time.Second},
				},
				ResolvePrincipal:    func(_ context.Context, value policy.Actor) (policy.Actor, error) { return value, nil },
				SnapshotPrincipal:   func(value policy.Actor) (policy.Actor, error) { return value, nil },
				SnapshotActor:       func(value policy.Actor) (policy.Actor, error) { return value, nil },
				AuditPrincipal:      func(policy.Actor) string { return "queue-gate" },
				ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
				ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
				AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
			})
			if err != nil {
				t.Fatal(err)
			}
			operator := application.QueueOperator()
			if operator == nil {
				t.Fatal("generated application exposes no queue operator")
			}

			direct := queuePending(t, jobType, "direct")
			directID, err := application.Enqueue(context.Background(), direct)
			if err != nil {
				t.Fatal(err)
			}
			if status, inspectErr := operator.Inspect(context.Background(), directID); inspectErr != nil || status.State != queue.StatePending {
				t.Fatalf("direct enqueue status=%#v error=%v", status, inspectErr)
			}

			caller, err := application.ForPrincipal(context.Background(), actor(t))
			if err != nil {
				t.Fatal(err)
			}
			rollback := errors.New("rollback")
			var rolledBackID queue.JobID
			err = caller.Transaction(context.Background(), func(transaction *policy.CallerTx[policy.Actor]) error {
				rolledBackID, err = transaction.Enqueue(context.Background(), queuePending(t, jobType, "rolled-back"))
				if err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatalf("rollback transaction: %v", err)
			}
			if _, inspectErr := operator.Inspect(context.Background(), rolledBackID); queueCode(inspectErr) != queue.CodeJobNotFound {
				t.Fatalf("rolled-back job survived: %v", inspectErr)
			}

			var callerID queue.JobID
			if err := caller.Transaction(context.Background(), func(transaction *policy.CallerTx[policy.Actor]) error {
				callerID, err = transaction.Enqueue(context.Background(), queuePending(t, jobType, "caller"))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var systemID queue.JobID
			if err := application.System().Transaction(context.Background(), func(transaction *policy.SystemTx[policy.Actor]) error {
				systemID, err = transaction.Enqueue(context.Background(), queuePending(t, jobType, "system"))
				return err
			}); err != nil {
				t.Fatal(err)
			}

			workerContext, stop := context.WithCancel(context.Background())
			finished := make(chan error, 1)
			go func() { finished <- application.RunQueueWorker(workerContext) }()
			seen := make(map[string]bool, 3)
			for len(seen) != 3 {
				select {
				case name := <-handled:
					seen[name] = true
				case <-time.After(20 * time.Second):
					t.Fatalf("handled=%v", seen)
				}
			}
			stop()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if !seen["direct"] || !seen["caller"] || !seen["system"] || seen["rolled-back"] {
				t.Fatalf("handled=%v", seen)
			}
			for _, identity := range []queue.JobID{directID, callerID, systemID} {
				status, inspectErr := operator.Inspect(context.Background(), identity)
				if inspectErr != nil || status.State != queue.StateSucceeded {
					t.Fatalf("completed job %s status=%#v error=%v", identity, status, inspectErr)
				}
			}
		})
	}
}

func queuePending(t *testing.T, jobType queue.Type[queuePayload], name string) queue.Pending {
	t.Helper()
	pending, err := jobType.New(queuePayload{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func queueCode(err error) queue.ErrorCode {
	code, _ := queue.CodeOf(err)
	return code
}

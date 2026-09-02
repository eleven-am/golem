package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

func TestOperatorRequeueValidatesIdentity(t *testing.T) {
	fixture := newHarness(t)
	control, err := NewOperator(fixture.store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []queue.JobID{"", queue.JobID(strings.Repeat("x", queueprovider.MaximumIdentityBytes+1))} {
		changed, err := control.Requeue(ctx, id)
		code, classified := queue.CodeOf(err)
		if changed || !classified || code != queue.CodeConfigInvalid {
			t.Fatalf("requeue of a %d byte identity returned changed=%t code=%q error=%v", len(id), changed, code, err)
		}
	}
}

func TestOperatorRequeueRestoresTerminalWork(t *testing.T) {
	fixture := newHarness(t)
	control, err := NewOperator(fixture.store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	registry := queue.NewRegistry()
	jobType := register(t, registry, queue.Definition[gatePayload]{
		Type:   "gate.requeue",
		Handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
	})
	identity := fixture.enqueue(t, newPending(t, jobType))
	leased, err := fixture.store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.requeue"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(leased) != 1 {
		t.Fatalf("claim %#v error=%v", leased, err)
	}
	if changed, err := fixture.store.Fail(ctx, identity, leased[0].LeaseToken, codeTerminal); err != nil || !changed {
		t.Fatalf("fail changed=%t error=%v", changed, err)
	}
	changed, err := control.Requeue(ctx, queue.JobID(identity))
	if err != nil || !changed {
		t.Fatalf("requeue of a failed job returned changed=%t error=%v", changed, err)
	}
	status, err := control.Inspect(ctx, queue.JobID(identity))
	if err != nil || status.State != queue.StatePending || status.Attempt != 0 || status.FinishedAt != nil || status.LastCode != "" {
		t.Fatalf("requeued job %#v error=%v", status, err)
	}
}

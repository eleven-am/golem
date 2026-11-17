package sqlite

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

func TestRestoreResumeClaimsPreservePriorityAndFinalization(t *testing.T) {
	ctx := context.Background()
	engine := New()
	database, _, err := engine.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := engine.QueueStore(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	old := map[string]queueprovider.Record{}
	for _, kind := range []string{"ordinary", "exhausted", "terminal", "cancel"} {
		attempts := 4
		if kind == "exhausted" {
			attempts = 1
		}
		typeName := "restore." + kind
		if _, err := store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: kind, Type: typeName, Priority: 1, Payload: []byte(kind), MaxAttempts: attempts}); err != nil {
			t.Fatal(err)
		}
		jobs, err := store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{typeName}, Limit: 1, LeaseDuration: 5 * time.Minute,
			Resource: &queueprovider.ClaimResource{Name: typeName, Concurrency: 1, Costs: map[string]int64{typeName: 1}}})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("original %s claim: jobs=%v error=%v", kind, jobs, err)
		}
		old[kind] = jobs[0]
		if kind == "terminal" {
			if changed, err := store.DeferFailure(ctx, kind, jobs[0].LeaseToken, queueprovider.CodeTerminal); err != nil || !changed {
				t.Fatalf("terminal marker: changed=%v error=%v", changed, err)
			}
		}
		if kind == "cancel" {
			if _, err := store.Cancel(ctx, nil, kind); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: "delayed", Type: "restore.delayed", Priority: 1, Payload: []byte("delay"), MaxAttempts: 4, Delay: time.Hour}); err != nil {
		t.Fatal(err)
	}
	delayed, err := store.Inspect(ctx, "delayed")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileRestoreWork(ctx, database, physical.SystemSchema{Namespace: physical.Namespace{Name: "main"}}, RestoreResumeAtLeastOnce); err != nil {
		t.Fatal(err)
	}
	for id, job := range old {
		if renewed, err := store.Renew(ctx, id, job.LeaseToken, time.Minute); err != nil || renewed.Renewed {
			t.Fatalf("old %s owner renewed after restore: %+v error=%v", id, renewed, err)
		}
		if changed, err := store.Succeed(ctx, id, job.LeaseToken, "old-owner"); err != nil || changed {
			t.Fatalf("old %s owner completed after restore: changed=%v error=%v", id, changed, err)
		}
	}
	if after, err := store.Inspect(ctx, "delayed"); err != nil || !reflect.DeepEqual(delayed, after) {
		t.Fatalf("restore changed pending scheduling or payload: error=%v", err)
	}
	jobs, err := store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{"restore.ordinary", "restore.exhausted", "restore.terminal", "restore.cancel", "restore.delayed"}, Limit: 4, LeaseDuration: time.Minute})
	if err != nil || len(jobs) != 3 {
		t.Fatalf("resume must run one ordinary job and finalize two failures, not run cancellation: jobs=%v error=%v", jobs, err)
	}
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.ID] = true
		before := old[job.ID]
		if job.Priority != 1 || !bytes.Equal(job.Payload, before.Payload) || job.LeaseToken == before.LeaseToken {
			t.Fatalf("restored job lost priority/payload or reused its token: %+v", job)
		}
		if job.ID == "ordinary" {
			if job.Finalize || job.AttemptCount != 2 {
				t.Fatalf("ordinary retry did not retain its consumed attempt: %+v", job)
			}
		} else if !job.Finalize || job.AttemptCount != before.AttemptCount {
			t.Fatalf("restored failure would rerun the business handler: %+v", job)
		}
	}
	if !seen["ordinary"] || !seen["exhausted"] || !seen["terminal"] {
		t.Fatalf("restored work disappeared: %v", seen)
	}
	if canceled, err := store.Inspect(ctx, "cancel"); err != nil || canceled.State != queueprovider.StateCanceled || canceled.AttemptCount != 1 {
		t.Fatalf("restored cancellation ran another attempt: %+v error=%v", canceled, err)
	}
}

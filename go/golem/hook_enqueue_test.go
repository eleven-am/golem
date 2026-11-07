package golem

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/queue"
)

func TestHookEnqueueRequestAndResultCarryThePendingJob(t *testing.T) {
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[string]{
		Type:   "golem.hook_enqueue",
		Handle: func(context.Context, queue.Job[string]) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := jobType.New("payload")
	if err != nil {
		t.Fatal(err)
	}
	request := RuntimeHookExecutorEnqueue(pending)
	got, ok := request.Pending()
	if !ok || request.Operation() != RuntimeHookExecutorEnqueueOperation || got.TypeName() != "golem.hook_enqueue" || string(got.Payload()) != `"payload"` {
		t.Fatalf("request pending=%#v ok=%t operation=%d", got, ok, request.Operation())
	}
	if RuntimeHookExecutorCount(1).JobID() != "" {
		t.Fatal("a count result carries a job")
	}
	if _, present := RuntimeHookExecutorDeleteMany(ModelID{}, FrozenPredicate{}).Pending(); present {
		t.Fatal("a non-enqueue request carries a pending job")
	}
	var captured RuntimeHookExecutorRequest
	executor := RuntimeHookExecutor(func(_ context.Context, request RuntimeHookExecutorRequest) (RuntimeHookExecutorResult, error) {
		captured = request
		return RuntimeHookExecutorJob("job-1"), nil
	})
	id, err := HookEnqueue(context.Background(), executor, pending)
	if err != nil || id != "job-1" || captured.Operation() != RuntimeHookExecutorEnqueueOperation {
		t.Fatalf("HookEnqueue id=%q err=%v operation=%d", id, err, captured.Operation())
	}
	if _, err := HookEnqueue(context.Background(), HookExecutor{}, pending); err == nil {
		t.Fatal("HookEnqueue ran without an executor")
	}
}

package subscription

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

const p7HubPanicChild = "GOLEM_P7_HUB_PANIC_CHILD"

// A panic in this path would normally terminate the entire go test process.
// Run every adversarial callback in a child copy of the test binary so the
// parent can distinguish containment from a process crash.
func TestP7HubPanicBoundaryContainsEvaluateEvaluateStateCloneAndPolicyFactory(t *testing.T) {
	if scenario := os.Getenv(p7HubPanicChild); scenario != "" {
		runP7HubPanicChild(t, scenario)
		return
	}
	for _, scenario := range []string{"evaluate", "evaluate-state", "clone", "policy-factory"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestP7HubPanicBoundaryContainsEvaluateEvaluateStateCloneAndPolicyFactory$")
			command.Env = append(os.Environ(), p7HubPanicChild+"="+scenario)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s panic escaped the hub worker: %v\n%s", scenario, err, output)
			}
		})
	}
}

func runP7HubPanicChild(t *testing.T, scenario string) {
	t.Helper()
	source := newFakeSource()
	config := Config[golem.EventID]{
		Generation: golem.SchemaDigest{1},
		Model:      golem.ModelID{2},
		Limits:     events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source:     sourceFactory(source),
		Evaluate: func(context.Context, events.Notice, SubscriberKey) (Evaluation[golem.EventID], error) {
			panic("private evaluator panic")
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	}
	stateful := scenario == "evaluate-state" || scenario == "policy-factory"
	if stateful {
		config.Evaluate = nil
		config.EvaluateState = func(context.Context, events.Notice, SubscriberKey, any) (Evaluation[golem.EventID], error) {
			if scenario == "policy-factory" {
				invokePanickingGeneratedPolicyFactory(t)
			}
			panic("private stateful evaluator panic")
		}
	}
	if scenario == "clone" {
		config.Evaluate = identityEvaluator
		config.Clone = func(golem.EventID) (golem.EventID, error) {
			panic("private cloner panic")
		}
	}
	hub, err := NewModelHub(config)
	if err != nil {
		t.Fatal(err)
	}
	var drops atomic.Int64
	var stream *Stream[golem.EventID]
	if stateful {
		stream, err = hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "member", false), struct{}{}, func(any) {
			drops.Add(1)
			panic("private cleanup panic")
		})
	} else {
		stream, err = hub.Subscribe(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "member", true))
	}
	if err != nil {
		t.Fatal(err)
	}
	source.send(testNotice(t, 1))
	receiveContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = stream.Recv(receiveContext); code(t, err) != events.CodeSubscriptionSourceClosed {
		t.Fatalf("panic returned %v", err)
	}
	if stateful {
		deadline := time.Now().Add(time.Second)
		for drops.Load() != 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if drops.Load() != 1 {
			t.Fatalf("retained state release count = %d", drops.Load())
		}
	}
	shutdown(t, hub)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.members) != 0 || len(hub.runs) != 0 {
		t.Fatalf("panic retained members=%d runs=%d", len(hub.members), len(hub.runs))
	}
}

type p7PanickingPolicyModel struct{}

func invokePanickingGeneratedPolicyFactory(t testing.TB) {
	t.Helper()
	generation := golem.SchemaDigest{1}
	model := golem.ModelID{2}
	binding := golem.GeneratedPolicyBinding[struct{}, p7PanickingPolicyModel](model, func(struct{}) (golem.FrozenPolicy, error) {
		panic("private generated policy factory panic")
	})
	bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, []golem.PolicyBinding[struct{}]{binding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = golem.BuildGeneratedPolicySet(bindings, struct{}{})
}

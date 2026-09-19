package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queue"
)

func TestFinalizationStoreFailureIsObserved(t *testing.T) {
	storeDown := errors.New("queue storage unavailable")
	rows := []struct {
		name     string
		typeName string
		attempts int
		handle   func(context.Context, queue.Job[gatePayload]) error
		enqueue  bool
		stub     func(store queueprovider.Store, typeName string) stubStore
	}{
		{
			name: "succeed", typeName: "gate.commit.succeed", enqueue: true,
			handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
			stub: func(store queueprovider.Store, _ string) stubStore {
				return stubStore{Store: store, succeed: func(context.Context, string, string, string) (bool, error) { return false, storeDown }}
			},
		},
		{
			name: "terminal fail", typeName: "gate.commit.terminal", enqueue: true,
			handle: func(context.Context, queue.Job[gatePayload]) error { return queue.Terminal(errors.New("poison")) },
			stub: func(store queueprovider.Store, _ string) stubStore {
				return stubStore{Store: store, fail: func(context.Context, string, string, string) (bool, error) { return false, storeDown }}
			},
		},
		{
			name: "exhausted fail", typeName: "gate.commit.exhausted", attempts: 1, enqueue: true,
			handle: func(context.Context, queue.Job[gatePayload]) error { return errors.New("transient") },
			stub: func(store queueprovider.Store, _ string) stubStore {
				return stubStore{Store: store, fail: func(context.Context, string, string, string) (bool, error) { return false, storeDown }}
			},
		},
		{
			name: "retry", typeName: "gate.commit.retry", attempts: 3, enqueue: true,
			handle: func(context.Context, queue.Job[gatePayload]) error { return errors.New("transient") },
			stub: func(store queueprovider.Store, _ string) stubStore {
				return stubStore{Store: store, retryAt: func(context.Context, string, string, time.Duration, string, bool) (bool, error) {
					return false, storeDown
				}}
			},
		},
		{
			name: "cancel", typeName: "gate.commit.cancel", enqueue: true,
			handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
			stub: func(store queueprovider.Store, _ string) stubStore {
				return stubStore{
					Store: store,
					claim: func(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
						records, err := store.Claim(ctx, options)
						for index := range records {
							records[index].CancelRequested = true
						}
						return records, err
					},
					canceled: func(context.Context, string, string, string) (bool, error) { return false, storeDown },
				}
			},
		},
		{
			name: "release", typeName: "gate.commit.unregistered",
			handle: func(context.Context, queue.Job[gatePayload]) error { return nil },
			stub: func(store queueprovider.Store, typeName string) stubStore {
				var handed atomic.Bool
				return stubStore{
					Store: store,
					claim: func(ctx context.Context, options queueprovider.ClaimOptions) ([]queueprovider.Record, error) {
						if handed.CompareAndSwap(false, true) {
							return []queueprovider.Record{{ID: "orphan", LeaseToken: "orphan-token", Type: typeName, AttemptCount: 1, MaxAttempts: 1}}, nil
						}
						return store.Claim(ctx, options)
					},
					release: func(context.Context, string, string) (bool, error) { return false, storeDown },
				}
			},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			fixture := newHarness(t)
			registry := queue.NewRegistry()
			registered := row.typeName
			if !row.enqueue {
				registered = "gate.commit.registered"
			}
			jobType := register(t, registry, queue.Definition[gatePayload]{
				Type: registered, MaxAttempts: row.attempts, Handle: row.handle,
				Backoff: queue.Backoff{Base: time.Hour, Cap: time.Hour},
			})
			if row.enqueue {
				fixture.enqueue(t, newPending(t, jobType))
			}
			var mutex sync.Mutex
			var seen []observe.Observation
			committed := make(chan observe.Observation, 1)
			_, stop := fixture.startObserved(t, row.stub(fixture.store, row.typeName), registry, gateLimits(), golem.SQLite, queueObserverFunc(func(_ context.Context, value observe.Observation) {
				mutex.Lock()
				seen = append(seen, value)
				mutex.Unlock()
				if value.Phase() == observe.PhaseCommit {
					select {
					case committed <- value:
					default:
					}
				}
			}))
			var failure observe.Observation
			select {
			case failure = <-committed:
			case <-time.After(20 * time.Second):
				stop()
				t.Fatal("a failed finalization produced no observation")
			}
			stop()
			if failure.Kind() != observe.KindQueue || failure.Operation() != observe.OperationQueueExecute || failure.QueueType() != row.typeName ||
				failure.Provider() != golem.SQLite || failure.Outcome() != observe.OutcomeFailure || failure.Reason() != observe.ReasonProvider || failure.Attempt() != 1 {
				t.Fatalf("finalization failure observation = kind %s operation %s type %s provider %s outcome %s reason %s attempt %d",
					failure.Kind(), failure.Operation(), failure.QueueType(), failure.Provider(), failure.Outcome(), failure.Reason(), failure.Attempt())
			}
			mutex.Lock()
			defer mutex.Unlock()
			for _, value := range seen {
				switch value.Phase() {
				case observe.PhaseFinish, observe.PhaseRetry, observe.PhaseCancel:
					t.Fatalf("an unrecorded transition was observed as %s/%s", value.Phase(), value.Outcome())
				}
			}
		})
	}
}

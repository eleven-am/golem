package observe

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	internalvalue "github.com/eleven-am/golem/go/internal/observation"
)

type blockingTarget struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (target *blockingTarget) ObserveGolem(context.Context, Observation) {
	target.calls.Add(1)
	select {
	case target.entered <- struct{}{}:
	default:
	}
	<-target.release
}

func validDispatcherValue() internalvalue.Value {
	return internalvalue.Value{
		KindValue:      string(KindRead),
		PhaseValue:     string(PhaseFinish),
		OutcomeValue:   string(OutcomeSuccess),
		OperationValue: string(OperationReadFindMany),
	}
}

func TestP8ObservationCardinalityAndBoundedDispatcher(t *testing.T) {
	before := runtime.NumGoroutine()
	target := &blockingTarget{entered: make(chan struct{}, 1), release: make(chan struct{})}
	dispatcher, err := NewDispatcher(target, DispatcherConfig{QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	internalvalue.Emit(dispatcher, validDispatcherValue())
	select {
	case <-target.entered:
	case <-time.After(time.Second):
		t.Fatal("dispatcher worker did not start")
	}
	for range 3 {
		started := time.Now()
		internalvalue.Emit(dispatcher, validDispatcherValue())
		if time.Since(started) > 50*time.Millisecond {
			t.Fatal("bounded offer blocked on target")
		}
	}
	if dispatcher.Dropped() != 1 {
		t.Fatalf("full queue dropped=%d want 1", dispatcher.Dropped())
	}
	if delta := runtime.NumGoroutine() - before; delta > 2 {
		t.Fatalf("dispatcher created more than one worker: goroutine delta=%d", delta)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked shutdown error=%v", err)
	}
	if dispatcher.Dropped() != 3 {
		t.Fatalf("shutdown must drop two pending records: dropped=%d", dispatcher.Dropped())
	}
	internalvalue.Emit(dispatcher, validDispatcherValue())
	if dispatcher.Dropped() != 4 {
		t.Fatalf("post-shutdown offer dropped=%d want 4", dispatcher.Dropped())
	}
	close(target.release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if target.calls.Load() != 1 {
		t.Fatalf("target calls=%d want 1", target.calls.Load())
	}
}

func TestDispatcherRejectsInvalidConfigAndRecoversTargetPanic(t *testing.T) {
	if _, err := NewDispatcher(nil, DispatcherConfig{}); err == nil {
		t.Fatal("nil target accepted")
	}
	target := observerFunc(func(context.Context, Observation) { panic("target") })
	dispatcher, err := NewDispatcher(target, DispatcherConfig{QueueCapacity: MaximumQueueCapacity + 1})
	if err == nil || dispatcher != nil {
		t.Fatal("oversized queue accepted")
	}
	dispatcher, err = NewDispatcher(target, DispatcherConfig{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	internalvalue.Emit(dispatcher, validDispatcherValue())
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); err != nil {
		t.Fatalf("panic stranded worker: %v", err)
	}
}

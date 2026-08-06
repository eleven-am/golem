package extension

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestComputedLoaderDispatchesExplicitlyCoalescesAndBoundsBatches(t *testing.T) {
	execution := NewExecution()
	var calls atomic.Int32
	var batches [][]RequestKey[int]
	loader, err := NewLoader[int, string](execution, LoaderConfig{FieldID: "greeting", MaxBatchSize: 2, MaxPending: 4}, func(_ context.Context, keys []RequestKey[int]) (map[RequestKey[int]]BatchValue[string], error) {
		calls.Add(1)
		batches = append(batches, append([]RequestKey[int](nil), keys...))
		result := map[RequestKey[int]]BatchValue[string]{}
		for _, key := range keys {
			result[key] = BatchValue[string]{Value: key.Arguments}
		}
		return result, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	argsA, _ := CanonicalArguments(CanonicalArgument{Name: "prefix", Value: []byte(`"hello"`)}, CanonicalArgument{Name: "count", Value: []byte(`1`)})
	argsAAgain, _ := CanonicalArguments(CanonicalArgument{Name: "count", Value: []byte(`1`)}, CanonicalArgument{Name: "prefix", Value: []byte(`"hello"`)})
	if argsA != argsAAgain {
		t.Fatalf("canonical arguments differ: %s != %s", argsA, argsAAgain)
	}
	first, _ := loader.Queue(RequestKey[int]{Arguments: argsA, CacheKey: 7})
	duplicate, _ := loader.Queue(RequestKey[int]{Arguments: argsAAgain, CacheKey: 7})
	second, _ := loader.Queue(RequestKey[int]{Arguments: `{"prefix":"bye"}`, CacheKey: 7})
	third, _ := loader.Queue(RequestKey[int]{Arguments: `{}`, CacheKey: 8})
	if calls.Load() != 0 {
		t.Fatal("Queue invoked the loader before executor dispatch")
	}
	if err := loader.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loader.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", loader.Pending())
	}
	for _, future := range []*Future[string]{first, duplicate, second} {
		if _, err := future.Await(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := loader.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, err := third.Await(context.Background()); err != nil || value != `{}` {
		t.Fatalf("third = %q, %v", value, err)
	}
	if calls.Load() != 2 || !reflect.DeepEqual([]int{len(batches[0]), len(batches[1])}, []int{2, 1}) {
		t.Fatalf("calls/batches = %d %#v", calls.Load(), batches)
	}
}

func TestComputedLoadersNeverCrossExecutionsAndWritesInvalidate(t *testing.T) {
	var calls atomic.Int32
	batch := func(_ context.Context, keys []RequestKey[int]) (map[RequestKey[int]]BatchValue[int], error) {
		calls.Add(1)
		result := map[RequestKey[int]]BatchValue[int]{}
		for _, key := range keys {
			result[key] = BatchValue[int]{Value: key.CacheKey * 10}
		}
		return result, nil
	}
	leftExecution, rightExecution := NewExecution(), NewExecution()
	left, _ := NewLoader[int, int](leftExecution, LoaderConfig{FieldID: ir.ExtensionID("score")}, batch)
	right, _ := NewLoader[int, int](rightExecution, LoaderConfig{FieldID: ir.ExtensionID("score")}, batch)
	key := RequestKey[int]{Arguments: `{}`, CacheKey: 4}
	leftFuture, _ := left.Queue(key)
	rightFuture, _ := right.Queue(key)
	if err := left.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := right.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("separate executions shared a batch: calls = %d", calls.Load())
	}
	for _, future := range []*Future[int]{leftFuture, rightFuture} {
		if value, err := future.Await(context.Background()); err != nil || value != 40 {
			t.Fatalf("value = %d, %v", value, err)
		}
	}

	cached, _ := left.Queue(key)
	if value, err := cached.Await(context.Background()); err != nil || value != 40 {
		t.Fatalf("cached = %d, %v", value, err)
	}
	leftExecution.InvalidateAfterWrite()
	fresh, _ := left.Queue(key)
	if err := left.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, err := fresh.Await(context.Background()); err != nil || value != 40 || calls.Load() != 3 {
		t.Fatalf("fresh = %d, %v; calls = %d", value, err, calls.Load())
	}
}

func TestComputedLoaderFailuresCancellationAndInflightInvalidationUnwindWaiters(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	execution := NewExecution()
	loader, err := NewLoader[int, int](execution, LoaderConfig{FieldID: "slow"}, func(ctx context.Context, keys []RequestKey[int]) (map[RequestKey[int]]BatchValue[int], error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		result := map[RequestKey[int]]BatchValue[int]{}
		for _, key := range keys {
			result[key] = BatchValue[int]{Value: key.CacheKey}
		}
		return result, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	future, _ := loader.Queue(RequestKey[int]{Arguments: `{}`, CacheKey: 1})
	dispatched := make(chan error, 1)
	go func() { dispatched <- loader.Dispatch(context.Background()) }()
	<-started
	execution.InvalidateAfterWrite()
	if _, err := future.Await(context.Background()); !errors.Is(err, ErrWriteInvalidated) {
		t.Fatalf("waiter error = %v", err)
	}
	close(release)
	if err := <-dispatched; !errors.Is(err, ErrWriteInvalidated) {
		t.Fatalf("dispatch error = %v", err)
	}

	cancelFuture, _ := loader.Queue(RequestKey[int]{Arguments: `{}`, CacheKey: 2})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cancelFuture.Await(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	execution.Close()
	if _, err := cancelFuture.Await(context.Background()); !errors.Is(err, ErrExecutionClosed) {
		t.Fatalf("close error = %v", err)
	}
	if _, err := loader.Queue(RequestKey[int]{CacheKey: 3}); !errors.Is(err, ErrExecutionClosed) {
		t.Fatalf("queue after close = %v", err)
	}
}

func TestComputedLoaderRejectsIncompleteBatchMapsAndPendingOverflow(t *testing.T) {
	if _, err := CanonicalArguments(CanonicalArgument{Name: "x", Value: []byte(`1`)}, CanonicalArgument{Name: "x", Value: []byte(`2`)}); err == nil {
		t.Fatal("duplicate canonical argument name was accepted")
	}
	if _, err := CanonicalArguments(CanonicalArgument{Name: "x", Value: []byte(`NaN`)}); err == nil {
		t.Fatal("invalid exact scalar JSON was accepted")
	}
	execution := NewExecution()
	loader, err := NewLoader[int, int](execution, LoaderConfig{FieldID: "broken", MaxBatchSize: 2, MaxPending: 2}, func(context.Context, []RequestKey[int]) (map[RequestKey[int]]BatchValue[int], error) {
		return map[RequestKey[int]]BatchValue[int]{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := loader.Queue(RequestKey[int]{CacheKey: 1})
	_, _ = loader.Queue(RequestKey[int]{CacheKey: 2})
	if _, err := loader.Queue(RequestKey[int]{CacheKey: 3}); !errors.Is(err, ErrPendingLimit) {
		t.Fatalf("pending limit error = %v", err)
	}
	if err := loader.Dispatch(context.Background()); err == nil {
		t.Fatal("incomplete batch map was accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.Await(ctx); err == nil {
		t.Fatal("batch contract failure did not reach waiter")
	}
}

package subscription

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func recheckLimits() events.Limits {
	return events.Limits{SubscriberQueue: 1, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond}
}

func TestRecheckFailureTerminatesOnlyThatStream(t *testing.T) {
	for name, testCase := range map[string]struct {
		failure error
		want    events.ErrorCode
	}{
		"plain error":     {errors.New("share revoked"), events.CodeSubscriptionForbidden},
		"whitelisted":     {events.Failure(events.CodeSubscriptionRevalidation), events.CodeSubscriptionRevalidation},
		"not whitelisted": {events.Failure(events.CodeSubscriptionOverflow), events.CodeSubscriptionForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
			revoked := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "revoked", false))
			kept := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "kept", false))
			var denied atomic.Bool
			if !revoked.Recheck(5*time.Millisecond, func(context.Context) error {
				if denied.Load() {
					return testCase.failure
				}
				return nil
			}) {
				t.Fatal("recheck was not installed on a live stream")
			}
			denied.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := revoked.Recv(ctx); code(t, err) != testCase.want {
				t.Fatalf("revoked stream code=%v want %s", err, testCase.want)
			}
			quiet, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer stop()
			if _, err := kept.Recv(quiet); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("an unrelated stream ended with the revoked one: %v", err)
			}
			shutdown(t, hub)
		})
	}
}

func TestRecheckPanicEndsTheStreamSanitized(t *testing.T) {
	hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "member", false))
	stream.Recheck(5*time.Millisecond, func(context.Context) error { panic("policy exploded") })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); code(t, err) != events.CodeSubscriptionSourceClosed {
		t.Fatalf("panicking recheck code=%v", err)
	}
	shutdown(t, hub)
}

func TestRecheckGoroutinesEndWithTheirStream(t *testing.T) {
	hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
	anchor := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "anchor", false))
	baseline := runtime.NumGoroutine()
	started := make(chan struct{}, 8)
	cancelled := make(chan struct{}, 8)
	streams := make([]*Stream[golem.EventID], 0, 8)
	for index := range 8 {
		stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", string(rune('a'+index)), false))
		stream.Recheck(time.Millisecond, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil
		})
		streams = append(streams, stream)
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("a recheck never ran")
		}
	}
	if running := runtime.NumGoroutine(); running < baseline+8 {
		t.Fatalf("goroutines=%d, want at least baseline %d + 8 rechecks", running, baseline)
	}
	for _, stream := range streams {
		_ = stream.Close()
	}
	for range 8 {
		select {
		case <-cancelled:
		case <-time.After(2 * time.Second):
			t.Fatal("a running recheck did not observe its stream closing")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running := runtime.NumGoroutine(); running > baseline {
		t.Fatalf("goroutines=%d after every rechecked stream closed; baseline=%d", running, baseline)
	}
	_ = anchor.Close()
	shutdown(t, hub)
}

func TestRecheckRefusesARemovedStreamAndShutdownWaits(t *testing.T) {
	hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
	live := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "live", false))
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "member", false))
	_ = stream.Close()
	if stream.Recheck(time.Millisecond, func(context.Context) error { return nil }) {
		t.Fatal("recheck was installed on a closed stream")
	}
	finished := make(chan struct{})
	live.Recheck(time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	time.Sleep(10 * time.Millisecond)
	shutdown(t, hub)
	select {
	case <-finished:
	default:
		t.Fatal("Shutdown returned while a recheck was still running")
	}
	if _, err := live.Recv(context.Background()); code(t, err) != events.CodeSubscriptionCancelled {
		t.Fatalf("shutdown code=%v", err)
	}
}

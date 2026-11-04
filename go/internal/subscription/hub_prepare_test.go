package subscription

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

type preparedShare struct{ notice golem.EventID }

func TestPrepareNoticeRunsOncePerNoticeWithEveryRetainedStateAndSharesItsResult(t *testing.T) {
	source := newFakeSource()
	var prepares atomic.Int64
	prepared := make(chan []any, 4)
	seen := make(chan any, 8)
	hub, err := NewModelHub(Config[golem.EventID]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2},
		Limits: events.Limits{SubscriberQueue: 2, EvaluationConcurrency: 2, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source: sourceFactory(source),
		PrepareNotice: func(_ context.Context, notice events.Notice, retained []any) any {
			prepares.Add(1)
			prepared <- retained
			return &preparedShare{notice: notice.EventID()}
		},
		EvaluateState: func(_ context.Context, notice events.Notice, _ SubscriberKey, _ any, shared any) (Evaluation[golem.EventID], error) {
			seen <- shared
			return Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	firstState, secondState := &struct{ n int }{1}, &struct{ n int }{2}
	first, err := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "one", false), firstState, func(any, error) {})
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "two", false), secondState, func(any, error) {})
	if err != nil {
		t.Fatal(err)
	}
	source.send(testNotice(t, 1))
	_ = recv(t, first)
	_ = recv(t, second)
	if prepares.Load() != 1 {
		t.Fatalf("PrepareNotice ran %d times for one notice and two members", prepares.Load())
	}
	retained := <-prepared
	if len(retained) != 2 {
		t.Fatalf("PrepareNotice saw %d retained states, want 2", len(retained))
	}
	found := map[any]bool{}
	for _, state := range retained {
		found[state] = true
	}
	if !found[firstState] || !found[secondState] {
		t.Fatal("PrepareNotice did not see both retained states")
	}
	one, two := <-seen, <-seen
	share, ok := one.(*preparedShare)
	if !ok || share.notice != (golem.EventID{1}) || one != two {
		t.Fatalf("members received %v and %v, want one shared value for the notice", one, two)
	}
	shutdown(t, hub)
}

func TestPrepareNoticePanicLeavesSharedNilAndDoesNotEndTheHub(t *testing.T) {
	source := newFakeSource()
	seen := make(chan any, 2)
	hub, err := NewModelHub(Config[golem.EventID]{
		Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2},
		Limits: events.Limits{SubscriberQueue: 2, EvaluationConcurrency: 1, RetryBase: time.Millisecond, RetryCap: time.Millisecond},
		Source: sourceFactory(source),
		PrepareNotice: func(context.Context, events.Notice, []any) any {
			panic("private prepare panic")
		},
		EvaluateState: func(_ context.Context, notice events.Notice, _ SubscriberKey, _ any, shared any) (Evaluation[golem.EventID], error) {
			seen <- shared
			return Deliver(notice.EventID()), nil
		},
		Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "one", false), &struct{}{}, func(any, error) {})
	if err != nil {
		t.Fatal(err)
	}
	source.send(testNotice(t, 1))
	source.send(testNotice(t, 2))
	if got := recv(t, stream); got != (golem.EventID{1}) {
		t.Fatalf("first event = %v", got)
	}
	if got := recv(t, stream); got != (golem.EventID{2}) {
		t.Fatalf("second event = %v", got)
	}
	if first, second := <-seen, <-seen; first != nil || second != nil {
		t.Fatalf("a panicking PrepareNotice shared %v and %v", first, second)
	}
	shutdown(t, hub)
}

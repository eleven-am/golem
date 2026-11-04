package subscription

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestEndAllEndsEveryMemberWithTheGivenFailureAndIsIdempotent(t *testing.T) {
	hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
	first := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "first", false))
	second := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "second", false))
	failure := events.FailureWithReason(events.CodeSubscriptionResync, events.ReasonDependencyChanged, "")
	hub.EndAll(failure)
	for name, stream := range map[string]*Stream[golem.EventID]{"first": first, "second": second} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := stream.Recv(ctx)
		cancel()
		if !errors.Is(err, failure) {
			t.Fatalf("%s ended with %v, want the given failure", name, err)
		}
		if reason, ok := events.ReasonOf(err); !ok || reason != events.ReasonDependencyChanged {
			t.Fatalf("%s lost its reason: %v", name, err)
		}
	}
	hub.EndAll(failure)
	shutdown(t, hub)
}

func TestEndOnANilOrDetachedStreamIsANoOp(t *testing.T) {
	var absent *Stream[golem.EventID]
	absent.End(events.Failure(events.CodeSubscriptionResync))
	(&Stream[golem.EventID]{}).End(events.Failure(events.CodeSubscriptionResync))
	absent.Terminate(events.CodeSubscriptionResync)
}

func TestEndKeepsTheFirstFailure(t *testing.T) {
	hub := newTestHub(t, sourceFactory(newFakeSource()), recheckLimits(), identityEvaluator, nil)
	stream := subscribe(t, hub, testKey(t, "p", "v", "f", "s", "d", "e", "member", false))
	first := events.FailureWithReason(events.CodeSubscriptionResync, events.ReasonDependencyChanged, "")
	stream.End(first)
	stream.End(events.Failure(events.CodeSubscriptionRevalidation))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := stream.Recv(ctx); !errors.Is(err, first) {
		t.Fatalf("stream ended with %v, want the first failure", err)
	}
	shutdown(t, hub)
}

func TestDropReceivesTheFailureThatRemovedTheMember(t *testing.T) {
	for name, test := range map[string]struct {
		remove func(*ModelHub[golem.EventID], *Stream[golem.EventID])
		want   events.ErrorCode
	}{
		"end": {func(_ *ModelHub[golem.EventID], stream *Stream[golem.EventID]) {
			stream.End(events.Failure(events.CodeSubscriptionRevalidation))
		}, events.CodeSubscriptionRevalidation},
		"close": {func(_ *ModelHub[golem.EventID], stream *Stream[golem.EventID]) { _ = stream.Close() }, events.CodeSubscriptionCancelled},
		"end all": {func(hub *ModelHub[golem.EventID], _ *Stream[golem.EventID]) {
			hub.EndAll(events.Failure(events.CodeSubscriptionResync))
		}, events.CodeSubscriptionResync},
		"terminate": {func(_ *ModelHub[golem.EventID], stream *Stream[golem.EventID]) {
			stream.Terminate(events.CodeSubscriptionForbidden)
		}, events.CodeSubscriptionForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			hub, err := NewModelHub(Config[golem.EventID]{
				Generation: golem.SchemaDigest{1}, Model: golem.ModelID{2}, Limits: recheckLimits(), Source: sourceFactory(newFakeSource()),
				EvaluateState: func(context.Context, events.Notice, SubscriberKey, any, any) (Evaluation[golem.EventID], error) {
					return Suppress[golem.EventID](events.SuppressionFiltered), nil
				},
				Clone: func(value golem.EventID) (golem.EventID, error) { return value, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			dropped := make(chan error, 1)
			stream, err := hub.SubscribeWithState(context.Background(), testKey(t, "p", "v", "f", "s", "d", "e", "member", false), struct{}{}, func(_ any, failure error) { dropped <- failure })
			if err != nil {
				t.Fatal(err)
			}
			test.remove(hub, stream)
			select {
			case failure := <-dropped:
				if code, ok := events.CodeOf(failure); !ok || code != test.want {
					t.Fatalf("drop received %v, want %s", failure, test.want)
				}
			case <-time.After(time.Second):
				t.Fatal("drop did not run")
			}
			shutdown(t, hub)
		})
	}
}

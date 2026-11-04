package graphql

import (
	"context"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/events"
)

func TestPresentErrorCarriesTheEventReasonAndItsDetail(t *testing.T) {
	for name, test := range map[string]struct {
		err         error
		wantMessage string
		wantReason  string
	}{
		"resync with a reason": {events.FailureWithReason(events.CodeSubscriptionResync, events.ReasonDependencyChanged, ""), "GOLEM_SUBSCRIPTION_RESYNC", "DEPENDENCY_CHANGED"},
		"refusal naming model": {fmt.Errorf("open: %w", events.FailureWithReason(events.CodeSubscriptionInvalid, events.ReasonDependencyUnobservable, "Tag")), "GOLEM_SUBSCRIPTION_INVALID: Tag", "DEPENDENCY_UNOBSERVABLE"},
		"resync without":       {events.Failure(events.CodeSubscriptionResync), "GOLEM_SUBSCRIPTION_RESYNC", ""},
		"detail without":       {events.Failf(events.CodeSubscriptionInvalid, "internal %s", "secret"), "GOLEM_SUBSCRIPTION_INVALID", ""},
	} {
		t.Run(name, func(t *testing.T) {
			presented := PresentError(context.Background(), test.err, nil, nil)
			if presented.Message != test.wantMessage {
				t.Fatalf("message = %q, want %q", presented.Message, test.wantMessage)
			}
			reason, has := presented.Extensions["reason"]
			if (test.wantReason == "") == has || has && reason != test.wantReason {
				t.Fatalf("reason = %v (present %t), want %q", reason, has, test.wantReason)
			}
			if code := presented.Extensions["code"]; code == "" || code == nil {
				t.Fatalf("code extension lost: %v", presented.Extensions)
			}
		})
	}
}

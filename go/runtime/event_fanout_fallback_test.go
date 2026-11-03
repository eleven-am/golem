package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func expectStreamSilent(t *testing.T, stream golem.EventStream[p7EventOracleValue]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err == nil {
		t.Fatalf("stream delivered %x", value.validated.Metadata().EventID())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream ended with %v, want it to stay open and silent", err)
	}
}

func expectStreamEnds(t *testing.T, stream golem.EventStream[p7EventOracleValue], want events.ErrorCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err == nil {
		t.Fatalf("stream delivered %x instead of ending with %s", value.validated.Metadata().EventID(), want)
	}
	if code, ok := events.CodeOf(err); !ok || code != want {
		t.Fatalf("stream ended with %v, want %s", err, want)
	}
}

func TestEventHookRunsOncePerCandidateNotOncePerSubscriber(t *testing.T) {
	var hookCalls atomic.Int64
	policies := eventFanoutPolicies()
	policies.hook = func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
		hookCalls.Add(1)
		return golem.All[deleteEvalComment]()
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		fixture.seedOwners(t, 16)
		streams := make([]golem.EventStream[p7EventOracleValue], 16)
		for index := range streams {
			streams[index] = fixture.subscribeActor(t, fixture.schema.Comment, fmt.Sprintf("u%d", index))
		}
		hookCalls.Store(0)
		fixture.publishCommentEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		delivered, suppressed := fixture.awaitSettled(t, golem.EventUpdated, 16)
		if delivered != 1 || suppressed != 15 {
			t.Fatalf("delivered %d suppressed %d, want 1 and 15", delivered, suppressed)
		}
		if event := receiveP7Event(t, streams[0]); event.validated.Metadata().EventID() != p7OracleID(1) {
			t.Fatalf("owner received %x", event.validated.Metadata().EventID())
		}
		if got := hookCalls.Load(); got != 1 {
			t.Fatalf("read-before hook ran %d times for one event and 16 subscribers, want once for the one candidate", got)
		}
	})
}

func TestEventNarrowingHookDeliversExactlyWhatTheExactReadDelivers(t *testing.T) {
	policies := deleteEvalPolicies{
		hook: func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return fields.commentBody.Eq("hi")
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.publishCommentEvent(t, golem.EventUpdated, 1, deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalPublished), "hello")
		fixture.publishCommentEvent(t, golem.EventUpdated, 2, deleteEvalUUID(deleteEvalOnDraft), deleteEvalUUID(deleteEvalDraft), "hi")
		event := receiveP7Event(t, stream)
		if event.validated.Metadata().EventID() != p7OracleID(2) {
			t.Fatalf("received %x, want the comment the narrowing hook admits", event.validated.Metadata().EventID())
		}
		expectStreamSilent(t, stream)
		if fixture.suppressions(events.SuppressionFiltered) != 1 {
			t.Fatalf("filtered suppressions = %d, want the hook-excluded comment suppressed once", fixture.suppressions(events.SuppressionFiltered))
		}
	})
}

func TestEventToManyPolicyIsUndecidedAndTheExactReadDecides(t *testing.T) {
	policies := deleteEvalPolicies{
		post: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postComments.Some(fields.commentBody.Eq("hello")), true
		},
	}
	runTracedEventProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture, trace *p5SQLTrace) {
		const subscribers = 8
		streams := make([]golem.EventStream[p7EventOracleValue], subscribers)
		for index := range streams {
			streams[index] = fixture.subscribeActor(t, fixture.schema.Post, fmt.Sprintf("u%d", index))
		}
		trace.reset()
		fixture.publishPostEvent(t, golem.EventUpdated, 1, deleteEvalUUID(deleteEvalPublished), deleteEvalUUID(deleteEvalAlice), "published")
		delivered, suppressed := fixture.awaitSettled(t, golem.EventUpdated, subscribers)
		if delivered != subscribers || suppressed != 0 {
			t.Fatalf("delivered %d suppressed %d, want every subscriber served by the exact read", delivered, suppressed)
		}
		for _, stream := range streams {
			if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1) {
				t.Fatalf("received %x", event.validated.Metadata().EventID())
			}
		}
		trace.reset()
		fixture.publishPostEvent(t, golem.EventUpdated, 2, deleteEvalUUID(deleteEvalDraft), deleteEvalUUID(deleteEvalBob), "draft")
		delivered, suppressed = fixture.awaitSettled(t, golem.EventUpdated, 2*subscribers)
		if delivered != subscribers || suppressed != subscribers {
			t.Fatalf("delivered %d suppressed %d after the draft", delivered, suppressed)
		}
		if reads := len(eventFanoutReads(trace)); reads < subscribers {
			t.Fatalf("only %d reads for %d subscribers whose policy the judge cannot decide", reads, subscribers)
		}
		for _, stream := range streams {
			expectStreamSilent(t, stream)
		}
	})
}

func TestEventExactReadFailsTheStreamWhenThePrincipalCannotBeResolved(t *testing.T) {
	var failing atomic.Bool
	policies := eventFanoutPolicies()
	policies.resolveFails = &failing
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		fixture.seedOwners(t, 3)
		streams := make([]golem.EventStream[p7EventOracleValue], 3)
		for index := range streams {
			streams[index] = fixture.subscribeActor(t, fixture.schema.Post, fmt.Sprintf("u%d", index))
		}
		failing.Store(true)
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		expectStreamEnds(t, streams[0], events.CodeSubscriptionRevalidation)
		expectStreamSilent(t, streams[1])
		expectStreamSilent(t, streams[2])
		failing.Store(false)
		fixture.publishPostEvent(t, golem.EventUpdated, 2, eventFanoutUUID(1), eventFanoutUUID(1), "edited")
		if event := receiveP7Event(t, streams[1]); event.validated.Metadata().EventID() != p7OracleID(2) {
			t.Fatalf("received %x", event.validated.Metadata().EventID())
		}
	})
}

func TestEventDecisionUsesTheConstraintFrozenWhenTheStreamOpened(t *testing.T) {
	var broadened, revoked atomic.Bool
	policies := deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			owner := eventFanoutUUID(eventFanoutActorIndex(actor))
			switch actor.Name {
			case "u0":
				if broadened.Load() {
					return golem.All[deleteEvalPost](), true
				}
				return fields.postAuthorID.Eq(deleteEvalUUID(250)), true
			case "u1":
				if revoked.Load() {
					return fields.postAuthorID.Eq(deleteEvalUUID(250)), true
				}
				return golem.All[deleteEvalPost](), true
			default:
				return fields.postAuthorID.Eq(owner), true
			}
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		broadened.Store(false)
		revoked.Store(false)
		fixture.seedOwners(t, 2)
		broadening := fixture.subscribeActor(t, fixture.schema.Post, "u0")
		revoking := fixture.subscribeActor(t, fixture.schema.Post, "u1")
		broadened.Store(true)
		revoked.Store(true)
		fixture.app.InvalidatePolicies()
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		fixture.publishPostEvent(t, golem.EventUpdated, 2, eventFanoutUUID(1), eventFanoutUUID(1), "edited")
		fixture.awaitSettled(t, golem.EventUpdated, 4)
		expectStreamSilent(t, broadening)
		expectStreamSilent(t, revoking)
	})
}

func TestEventMaskedFieldPayloadStillComesFromTheExactRead(t *testing.T) {
	policies := deleteEvalPolicies{
		postTitleRule: func(fields deleteEvalFields, actor deleteEvalActor) golem.Predicate[deleteEvalPost] {
			return fields.postAuthorID.Eq(eventFanoutUUID(eventFanoutActorIndex(actor)))
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		fixture.seedOwners(t, 2)
		fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` SET "title" = ? WHERE "id" = ?`, "edited", mutationResultUUIDText(eventFanoutFirstID))
		owner := fixture.subscribeActor(t, fixture.schema.Post, "u0", golem.EventSelect[deleteEvalPost](fixture.fields.postTitle))
		other := fixture.subscribeActor(t, fixture.schema.Post, "u1", golem.EventSelect[deleteEvalPost](fixture.fields.postTitle))
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		for name, want := range map[string]struct {
			stream  golem.EventStream[p7EventOracleValue]
			visible bool
		}{"owner": {owner, true}, "other": {other, false}} {
			event := receiveP7Event(t, want.stream)
			entity, present := event.validated.Entity()
			if !present {
				t.Fatalf("%s: the selected entity is absent", name)
			}
			title, titlePresent := golem.RuntimeTransportField(entity, fixture.schema.PostTitle).Get()
			if titlePresent != want.visible || (want.visible && title != "edited") {
				t.Fatalf("%s: title %q present=%t, want visible=%t", name, title, titlePresent, want.visible)
			}
		}
	})
}

func TestEventSnapshotReadFailureEndsEveryJudgedMemberWithRevalidation(t *testing.T) {
	runTracedEventProfiles(t, eventFanoutPolicies(), func(t *testing.T, fixture deleteEvalFixture, trace *p5SQLTrace) {
		fixture.seedOwners(t, 3)
		streams := make([]golem.EventStream[p7EventOracleValue], 3)
		for index := range streams {
			streams[index] = fixture.subscribeActor(t, fixture.schema.Post, fmt.Sprintf("u%d", index))
		}
		var consumed atomic.Bool
		trace.setFault(func(statement string) error {
			if strings.Contains(statement, `"posts"`) && consumed.CompareAndSwap(false, true) {
				return errors.New("injected read fault")
			}
			return nil
		})
		defer trace.setFault(nil)
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		for _, stream := range streams {
			expectStreamEnds(t, stream, events.CodeSubscriptionRevalidation)
		}
	})
}

func TestEventUnplannableSnapshotLeavesEveryMemberToTheExactRead(t *testing.T) {
	policies := deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			if actor.Name == "u0" {
				return fields.postAuthorID.Eq(eventFanoutUUID(0)), true
			}
			return fields.postTitle.Eq("edited"), true
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		fixture.seedOwners(t, 2)
		byAuthor := fixture.subscribeActor(t, fixture.schema.Post, "u0")
		byTitle := fixture.subscribeActor(t, fixture.schema.Post, "u1")
		fixture.app.readLimits.plan.MaxSelected = 2
		fixture.exec(t, `UPDATE `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` SET "title" = ? WHERE "id" = ?`, "edited", mutationResultUUIDText(eventFanoutFirstID))
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		for _, stream := range []golem.EventStream[p7EventOracleValue]{byAuthor, byTitle} {
			if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(1) {
				t.Fatalf("received %x", event.validated.Metadata().EventID())
			}
		}
	})
}

func TestEventForARowThatNoLongerExistsSuppressesEveryMemberWithoutAnotherRead(t *testing.T) {
	runTracedEventProfiles(t, eventFanoutPolicies(), func(t *testing.T, fixture deleteEvalFixture, trace *p5SQLTrace) {
		fixture.seedOwners(t, 3)
		streams := make([]golem.EventStream[p7EventOracleValue], 3)
		for index := range streams {
			streams[index] = fixture.subscribeActor(t, fixture.schema.Post, fmt.Sprintf("u%d", index))
		}
		fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` WHERE "post_id" = ?`, mutationResultUUIDText(eventFanoutFirstID))
		fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` WHERE "id" = ?`, mutationResultUUIDText(eventFanoutFirstID))
		trace.reset()
		fixture.publishPostEvent(t, golem.EventUpdated, 1, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
		delivered, suppressed := fixture.awaitSettled(t, golem.EventUpdated, 3)
		if delivered != 0 || suppressed != 3 {
			t.Fatalf("delivered %d suppressed %d, want all three suppressed", delivered, suppressed)
		}
		if reads := eventFanoutReads(trace); len(reads) != 1 {
			t.Fatalf("%d reads for a vanished row, want only the shared read: %q", len(reads), reads)
		}
		for _, stream := range streams {
			expectStreamSilent(t, stream)
		}
	})
}

func TestEventPolicyUnavailableMidStreamEndsStreamWithRevalidation(t *testing.T) {
	broken := &atomic.Bool{}
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		postUnavailable: broken,
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		broken.Store(false)
		stream := fixture.subscribeComments(t, "alice")
		broken.Store(true)
		fixture.app.InvalidatePolicies()
		fixture.publishCommentEvent(t, golem.EventUpdated, 1, deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalPublished), "edited")
		expectStreamEnds(t, stream, events.CodeSubscriptionRevalidation)
	})
}

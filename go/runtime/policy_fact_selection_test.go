package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestPolicyFactRuntimeNestedSelectionRecovery(t *testing.T) {
	for _, mode := range []string{"child row", "child field mask", "child count"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
				unusedFact: true, postLocalFact: true, postFactMask: mode == "child field mask",
			})
			posts := golem.GeneratedToMany[policyFactRuntimeUser, policyFactRuntimePost](fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post)
			selection := golem.Select[policyFactRuntimeUser](posts.Args(golem.Select[policyFactRuntimePost](fixture.title)))
			if mode == "child count" {
				selection = golem.Select[policyFactRuntimeUser](posts.Count())
			}
			request, err := golem.FreezeFindMany(fixture.users, selection)
			if err != nil {
				t.Fatal(err)
			}
			caller := fixture.caller(t)
			state := &callerEventSubscription[policyFactRuntimePrincipal]{context: fixture.ctx, principal: fixture.principal, model: fixture.schema.User, readRequest: request}
			planned, err := fixture.app.eventDependencyPlan(fixture.ctx, caller, state)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, path := range planned.facts.Paths() {
				found = found || path.TargetModel() == policyir.ModelID(fixture.schema.Post)
			}
			if !found {
				t.Fatal("selected child policy fact has no observation path")
			}
			assertSelection := func(stored, ready bool) {
				t.Helper()
				rows, err := CallerFindMany(fixture.ctx, fixture.caller(t), fixture.users, selection)
				if err != nil || len(rows) != 1 {
					t.Fatalf("selected source rows=%d err=%v", len(rows), err)
				}
				want := 0
				if ready || mode == "child field mask" && stored {
					want = 1
				}
				if mode == "child count" {
					value, present := golem.RelationCount(rows[0], posts).Get()
					if !present || value != int64(want) {
						t.Fatalf("selected child count=%d present=%v want=%d", value, present, want)
					}
					return
				}
				children, present := golem.Many(rows[0], posts).Get()
				if !present || len(children) != want {
					t.Fatalf("selected children=%d present=%v want=%d", len(children), present, want)
				}
				if len(children) != 0 {
					value := golem.Value(children[0], fixture.title)
					if ready {
						if title, present := value.Get(); !present || title != "ready" {
							t.Fatalf("selected ready title=%q present=%v", title, present)
						}
					} else if !value.IsNull() {
						t.Fatal("denied selected child title was not masked")
					}
				}
			}
			assertSelection(false, false)
			subscribe := func() golem.EventStream[golem.EventMetadata] {
				t.Helper()
				stream, err := CallerFrozenReadEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, golem.EventMetadata](fixture.ctx, fixture.caller(t), request, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = stream.Close() })
				return stream
			}
			stream := subscribe()
			fixture.startPublisher(t)
			// A child fact's actor scope must survive its parent selection prefix.
			fixture.mutate(t, golem.EventCreated, 12, 2, "ready")
			fixture.awaitPublicationAndEvaluation(t, golem.EventCreated, 1)
			receive, cancel := context.WithTimeout(fixture.ctx, 20*time.Millisecond)
			_, err = stream.Recv(receive)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("selected child disclosed another actor's activity: %v", err)
			}
			assertSelection(false, false)
			for _, change := range []struct {
				action golem.EventAction
				title  string
			}{{golem.EventCreated, "ready"}, {golem.EventUpdated, "draft"}, {golem.EventDeleted, ""}} {
				fixture.mutate(t, change.action, 11, 1, change.title)
				fixture.awaitPublicationAndEvaluation(t, change.action, 1)
				fixture.expectTermination(t, stream, events.CodeSubscriptionResync, events.ReasonDependencyChanged)
				assertSelection(change.action != golem.EventDeleted, change.title == "ready")
				stream = subscribe()
			}
		})
	}
}

func TestPolicyFactRuntimeUnusedSelectedChildScope(t *testing.T) {
	for _, count := range []bool{false, true} {
		name := "child rows"
		if count {
			name = "child count"
		}
		t.Run(name, func(t *testing.T) {
			auth := &policyFactRuntimeAuth{}
			fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
				auth: auth, rootNoFacts: true, postLocalFact: true, postUnusedFact: true, revalidateEvery: time.Second,
			})
			posts := golem.GeneratedToMany[policyFactRuntimeUser, policyFactRuntimePost](fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post)
			selection := golem.Select[policyFactRuntimeUser](posts.Args(golem.Select[policyFactRuntimePost](fixture.title)))
			if count {
				selection = golem.Select[policyFactRuntimeUser](posts.Count())
			}
			request, err := golem.FreezeFindMany(fixture.users, selection)
			if err != nil {
				t.Fatal(err)
			}
			state := &callerEventSubscription[policyFactRuntimePrincipal]{context: fixture.ctx, principal: fixture.principal, model: fixture.schema.User, readRequest: request}
			planned, err := fixture.app.eventDependencyPlan(fixture.ctx, fixture.caller(t), state)
			if err != nil {
				t.Fatal(err)
			}
			if len(planned.facts.Paths()) == 0 {
				t.Fatal("unused selected child definitions lost their historical scope path")
			}
			for _, path := range planned.facts.Paths() {
				if path.TargetModel() != policyir.ModelID(fixture.schema.Post) {
					t.Fatal("scope observation traversed an unused fact body")
				}
			}
			subscribe := func() golem.EventStream[golem.EventMetadata] {
				t.Helper()
				stream, err := CallerFrozenReadEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, golem.EventMetadata](fixture.ctx, fixture.caller(t), request, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = stream.Close() })
				return stream
			}
			stream := subscribe()
			// The root has no facts and its policy is unchanged. No data event
			// is available to reveal this child-only authority change.
			auth.grantID.Store(2)
			fixture.expectTermination(t, stream, events.CodeSubscriptionResync, "")
			auth.grantID.Store(0)
			stream = subscribe()
			fixture.startPublisher(t)
			fixture.mutate(t, golem.EventCreated, 11, 1, "draft")
			fixture.awaitPublicationAndEvaluation(t, golem.EventCreated, 1)
			fixture.expectRecovery(t, stream, golem.EventCreated)
			stream = subscribe()
			target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey,
				golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 11}))
			_, err = SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, target,
				golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
					golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2})))
			if err != nil {
				t.Fatal(err)
			}
			fixture.awaitPublicationAndEvaluation(t, golem.EventUpdated, 1)
			fixture.expectRecovery(t, stream, golem.EventUpdated)
			stream = subscribe()
			_, err = SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, target,
				golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
					golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})))
			if err != nil {
				t.Fatal(err)
			}
			fixture.awaitPublicationAndEvaluation(t, golem.EventUpdated, 1)
			fixture.expectRecovery(t, stream, golem.EventUpdated)
			stream = subscribe()
			fixture.mutate(t, golem.EventDeleted, 11, 1, "")
			fixture.awaitPublicationAndEvaluation(t, golem.EventDeleted, 1)
			fixture.expectRecovery(t, stream, golem.EventDeleted)
		})
	}
}

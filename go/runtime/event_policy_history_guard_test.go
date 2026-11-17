package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/observe"
)

func TestEventHistoryCurrentGuardChainsShareSnapshotWithoutBorrowingAnswers(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryScopePosts(t, fixture)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	for _, seed := range []struct {
		id    byte
		title string
	}{{171, "blocked"}, {172, "allowed"}, {173, "allowed"}, {174, "blocked"}} {
		fixture.mutate(t, golem.EventCreated, seed.id, 1, seed.title)
	}
	equal := func(model golem.ModelID, fields ...mutationdecode.IdentityComponent) policyir.Condition {
		t.Helper()
		condition, err := equalityCondition(fixture.app.registry, fixture.app.provider, policyir.ModelID(model), fields)
		if err != nil {
			t.Fatal(err)
		}
		return condition
	}
	component := func(field golem.FieldID, value policyir.Value) mutationdecode.IdentityComponent {
		t.Helper()
		result, err := mutationdecode.IdentityValue(policyir.FieldID(field), value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	allowed, err := policyir.StringValue("allowed")
	if err != nil {
		t.Fatal(err)
	}
	prefix := equal(fixture.schema.User, component(fixture.schema.UserID, policyir.UUIDValue([16]byte{15: 1})))
	guard := func(id byte) policyir.Condition {
		return equal(fixture.schema.Post,
			component(fixture.schema.PostID, policyir.UUIDValue([16]byte{15: id})),
			component(fixture.schema.PostTitle, allowed))
	}
	// The matcher certifies each recorded witness separately. Current truth
	// from one witness may never fill a false guard in the other witness.
	alternatives := []eventHistoryRouteAlternative{
		{prefix: prefix, guards: []policyir.Condition{guard(171), guard(172)}},
		{prefix: prefix, guards: []policyir.Condition{guard(173), guard(174)}},
	}
	check := func(want bool) {
		t.Helper()
		ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
		defer observeexec.Finish(span, observe.OutcomeSuccess, observe.ReasonNone)
		_, scope, err := fixture.app.prepareEventHistoryScope(ctx, caller, evidence, evidence.value.Graph().RootKeys()[0])
		if err != nil {
			t.Fatal(err)
		}
		questions := append([]eventHistoryRouteAlternative(nil), alternatives...)
		for index := range questions {
			questions[index].scopes = []*deleteProbe{scope}
		}
		matches, err := fixture.app.matchEventHistoryRoutes(ctx, caller, fixture.schema.User, questions)
		if err != nil || len(matches) != len(questions) || span.StatementCount() != 1 {
			t.Fatalf("current witness results=%+v statements=%d err=%v", matches, span.StatementCount(), err)
		}
		matched := false
		for _, match := range matches {
			if len(match.scopes) != 1 || !match.scopes[0] {
				t.Fatalf("current witness lost its scope result: %+v", match)
			}
			matched = matched || match.prefix && match.guards
		}
		if matched != want {
			t.Fatalf("current witness conjunction: matched=%v want=%v results=%+v", matched, want, matches)
		}
	}
	check(false)
	setTitle := func(id byte, title string) {
		t.Helper()
		if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, id), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))); err != nil {
			t.Fatal(err)
		}
	}
	setTitle(171, "allowed")
	check(true)
	setTitle(171, "blocked")
	setTitle(174, "allowed")
	check(true)
	falsehood, err := policyir.NewConstant(policyir.ModelID(fixture.schema.User), false)
	if err != nil {
		t.Fatal(err)
	}
	for index := range alternatives {
		alternatives[index].prefix = falsehood
	}
	check(false)
	// A true guard behind a false prefix cannot authorize the other chain's
	// true prefix with a false guard, either.
	alternatives = []eventHistoryRouteAlternative{
		{prefix: falsehood, guards: []policyir.Condition{guard(172)}},
		{prefix: prefix, guards: []policyir.Condition{guard(171)}},
	}
	check(false)
	setTitle(171, "allowed")
	check(true)
	for _, limit := range []string{"aliases", "key bytes"} {
		t.Run(limit, func(t *testing.T) {
			original := fixture.app.readLimits.plan
			defer func() { fixture.app.readLimits.plan = original }()
			if limit == "aliases" {
				fixture.app.readLimits.plan.MaxStatementAliases = 1
			} else {
				fixture.app.readLimits.plan.MaxStatementBytes = 1
			}
			ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
			defer observeexec.Finish(span, observe.OutcomeRefused, observe.ReasonLimit)
			_, scope, err := fixture.app.prepareEventHistoryScope(ctx, caller, evidence, evidence.value.Graph().RootKeys()[0])
			if err != nil {
				t.Fatal(err)
			}
			questions := append([]eventHistoryRouteAlternative(nil), alternatives...)
			for index := range questions {
				questions[index].scopes = []*deleteProbe{scope}
			}
			matches, err := fixture.app.matchEventHistoryRoutes(ctx, caller, fixture.schema.User, questions)
			var refusal *events.Error
			if matches != nil || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation || refusal.Reason() != "" || refusal.Detail() != "" || span.StatementCount() != 0 {
				t.Fatalf("over-budget chains returned a partial result or ran SQL: results=%+v statements=%d err=%v", matches, span.StatementCount(), err)
			}
		})
	}
}

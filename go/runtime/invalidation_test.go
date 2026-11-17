package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestInvalidationUnknownAuthorityDoesNotBecomeActivity(t *testing.T) {
	model := policyir.ModelID{1}
	yes, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	no, err := policyir.NewConstant(model, false)
	if err != nil {
		t.Fatal(err)
	}
	// A wrong-model predicate is unknown to the ordinary evaluator, never true.
	unknown, err := policyir.NewConstant(policyir.ModelID{2}, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &eventSnapshot{found: true, evaluator: &deleteEvaluator{model: model, cells: map[policyir.FieldID]mutationdecode.Cell{}, shared: &deleteEvaluation{}}}
	cases := []struct {
		name    string
		row     policyir.Condition
		filter  *policyir.Condition
		changed bool
		code    events.ErrorCode
	}{
		{"outside", no, &yes, false, ""},
		{"unknown domain", unknown, &yes, false, ""},
		{"authorized", yes, &yes, true, ""},
		{"filtered", yes, &no, false, ""},
		{"unknown relevance within domain", yes, &unknown, false, events.CodeSubscriptionRevalidation},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			changed, err := readableInvalidationVerdict(&eventJudgeSpec{row: test.row, filter: test.filter}, nil, snapshot, true)
			code, _ := events.CodeOf(err)
			if changed != test.changed || code != test.code {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
		})
	}
	if changed, err := readableInvalidationVerdict(&eventJudgeSpec{row: yes}, snapshot, snapshot, false); changed || err != nil {
		t.Fatalf("unwatched noise invalidated: %v %v", changed, err)
	}
	if changed, err := readableInvalidationVerdict(&eventJudgeSpec{row: yes}, snapshot, &eventSnapshot{}, false); !changed || err != nil {
		t.Fatalf("membership loss was suppressed with unchanged watched value: %v %v", changed, err)
	}
	// Keeping a zero record in this unit fixture is deliberate: constants never
	// read it. Real codecs/private snapshots are exercised by the generated app.
}

func TestInvalidationPageAudienceTracksRevisionAndPinsAdmittedActor(t *testing.T) {
	type actor struct{ Account, Revision int }
	calls := 0
	pages := &pageRuntime[actor]{contract: "contract", incarnation: "data-1", scope: func(ctx context.Context, admitted actor) (string, error) {
		calls++
		if golem.ActorFrom[actor](ctx) != admitted {
			return "", fmt.Errorf("ambient actor replaced admission")
		}
		return fmt.Sprintf("%d/%d", admitted.Account, admitted.Revision), nil
	}}
	caller := &Caller[string, actor]{actor: actor{1, 1}, app: &App[string, actor]{pages: pages}}
	operation := compilerir.CustomOperationContractIR{ExtensionID: "feed", Name: "feed", Pagination: &compilerir.CustomPaginationIR{Revision: "rank-1"}}
	ctx := golem.RuntimeContextWithActor(context.Background(), actor{9, 9})
	arguments := []byte("canonical ordinary arguments")
	before, err := invalidationAudience(ctx, caller, operation, arguments)
	if err != nil || before != "1/1" {
		t.Fatalf("admitted audience unavailable: %q %v", before, err)
	}
	oldScope, pageAudience, err := callerPageScope(ctx, caller, operation, arguments)
	if err != nil || pageAudience != before {
		t.Fatalf("page and invalidation audience differ: %q %v", pageAudience, err)
	}
	// Only the durable audience revision changes; dependency policies can stay
	// identical. Fresh admission must still invalidate the page's old chain.
	fresh := *caller
	fresh.actor.Revision = 2
	after, err := invalidationAudience(ctx, &fresh, operation, arguments)
	if err != nil || after == before {
		t.Fatalf("revision-only audience change was lost: %q %v", after, err)
	}
	newScope, pageAudience, err := callerPageScope(ctx, &fresh, operation, arguments)
	if err != nil || pageAudience != after || oldScope == newScope {
		t.Fatalf("continuation and invalidation disagree about revision: %v", err)
	}
	operation.Pagination = nil
	priorCalls := calls
	if audience, err := invalidationAudience(ctx, caller, operation, arguments); err != nil || audience != "" || calls != priorCalls {
		t.Fatalf("ordinary query invoked page audience: %q %v calls=%d", audience, err, calls)
	}
	operation.Pagination = &compilerir.CustomPaginationIR{Revision: "rank-1"}
	for name, scope := range map[string]func(context.Context, actor) (string, error){
		"empty":     func(context.Context, actor) (string, error) { return "", nil },
		"error":     func(context.Context, actor) (string, error) { return "", errors.New("unavailable") },
		"oversized": func(context.Context, actor) (string, error) { return strings.Repeat("x", 128*1024+1), nil },
	} {
		t.Run(name, func(t *testing.T) {
			pages.scope = scope
			if _, err := invalidationAudience(ctx, caller, operation, arguments); err == nil {
				t.Fatal("invalid page audience accepted by invalidation")
			}
			if _, _, err := callerPageScope(ctx, caller, operation, arguments); err == nil {
				t.Fatal("invalid audience accepted by continuation")
			}
		})
	}
}

func invalidationPanicBinder(context.Context, struct{}, struct{}) (golem.Predicate[struct{}], error) {
	panic("binder fixture")
}

func TestInvalidationBinderPanicIsSanitized(t *testing.T) {
	binding, err := golem.GeneratedInvalidationBinder[struct{}]("op", "dep", invalidationPanicBinder, func(context.Context, struct{}, map[string]any) (golem.RuntimeInvalidationDependency, error) {
		panic("private binder detail")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = callInvalidationBinder(context.Background(), binding, struct{}{}, nil)
	if code, _ := events.CodeOf(err); code != events.CodeSubscriptionRevalidation {
		t.Fatalf("unsanitized binder failure: %v", err)
	}
}

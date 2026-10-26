package plan

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

func TestRootBatchUnderFieldScopedReadPlansAndNarrows(t *testing.T) {
	for _, operation := range []mutationir.Operation{mutationir.DeleteMany, mutationir.UpdateMany} {
		handles := newWriteReachHandles(t)
		rules := golem.NewRules[reachPost]()
		rules.CanReadFields(handles.author.Eq(uuid(9)), handles.id, handles.title)
		rules.CanUpdate(golem.All[reachPost]())
		rules.CanDelete(golem.All[reachPost]())
		policies := handles.policies(t, rules, nil)
		predicate := boundBatchPredicate(t, handles.fixture)
		request := baseRequest(t, handles.fixture, policies, mutationir.Caller, operation)
		request.Predicate = &predicate
		if operation == mutationir.UpdateMany {
			request.Update = &handles.inputs.updateMany
		}
		planned, err := BuildRoot(request)
		if err != nil {
			t.Fatalf("root batch operation=%d under a field-scoped read rule refused: %v", operation, err)
		}
		policy, _ := policies.Policy(policyir.ModelID(handles.fixture.Post))
		readable, err := resolve.RowConstraint(policy, policyir.ActionRead, policyir.ModelID(handles.fixture.Post))
		if err != nil {
			t.Fatal(err)
		}
		if !implies(t, rootSelection(t, planned), readable) {
			t.Fatalf("root batch operation=%d did not narrow to readable rows", operation)
		}
	}
}

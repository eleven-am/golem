package plan

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

func TestExistingConstraintConjoinsUnreadablePositionAsFalse(t *testing.T) {
	handles := newWriteReachHandles(t)
	model := policyir.ModelID(handles.fixture.Post)
	policy := policyWithRules(t, model, rule(t, model, policyir.ActionDelete, nil, 0))
	policies := policySet(handles.fixture, policy)
	row, err := resolve.RowConstraint(policy, policyir.ActionDelete, model)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := ExistingConstraint(policies, model, row, []policyir.FieldID{policyir.FieldID(handles.fixture.PostID)}, 0, handles.inputs.target.SelectorPredicate())
	if err != nil {
		t.Fatal(err)
	}
	if truth, constant := complete.Constant(); !constant || truth {
		t.Fatal("never-readable position did not make the constraint false")
	}
}

func TestExistingConstraintWithoutPositionFieldsNarrowsToReadableRow(t *testing.T) {
	handles := newWriteReachHandles(t)
	rules := golem.NewRules[reachPost]()
	rules.CanRead(handles.author.Eq(uuid(9)))
	rules.CanDelete(golem.All[reachPost]())
	policies := handles.policies(t, rules, nil)
	model := policyir.ModelID(handles.fixture.Post)
	policy, _ := policies.Policy(model)
	row, err := resolve.RowConstraint(policy, policyir.ActionDelete, model)
	if err != nil {
		t.Fatal(err)
	}
	readRow, err := resolve.RowConstraint(policy, policyir.ActionRead, model)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := ExistingConstraint(policies, model, row, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !implies(t, complete, readRow) {
		t.Fatal("constraint without position fields did not narrow to the readable row")
	}
}

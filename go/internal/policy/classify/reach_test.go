package classify

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

func TestReachLensDefinesTheRequiredSideOnly(t *testing.T) {
	model := modelID(1)
	title, owner, marker := fieldID(1), fieldID(2), fieldID(3)
	mine := scalar(t, model, owner, 9)
	extra := scalar(t, model, marker, 4)
	registry := identities(model, title, owner, marker)
	policy := newPolicy(t, model, ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine})
	widen := func(condition ir.Condition) (ir.Condition, error) {
		return conjoin(t, model, condition, extra), nil
	}
	expanded := Reach{Selecting: widen, Read: widen}

	if _, err := NewRequestWithReach(registry, policy, model, []ir.FieldID{title}, UseFilter, ir.ActionRead, mine, expanded); err == nil {
		t.Fatal("an actual reach that omits the lens output was accepted")
	}

	actual := conjoin(t, model, mine, extra)
	request, err := NewRequestWithReach(registry, policy, model, []ir.FieldID{title}, UseFilter, ir.ActionRead, actual, expanded)
	if err != nil {
		t.Fatal(err)
	}
	if !sameBytes(t, request.ReadConstraint(), actual) || !sameBytes(t, request.SelectingConstraint(), actual) {
		t.Fatal("request constraints are not the actual reach and the lens-expanded read row")
	}
	plan, err := Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	classification := plan.Classifications()[0]
	raw, conditional := classification.FieldCondition()
	reach, reachConditional := classification.ReachCondition()
	if classification.Access() != AccessConditional || !conditional || !reachConditional || !classification.DischargedByConstraint() {
		t.Fatalf("classification access=%d discharged=%v", classification.Access(), classification.DischargedByConstraint())
	}
	if !sameBytes(t, raw, mine) || !sameBytes(t, reach, actual) {
		t.Fatal("classification did not keep both the raw and the reach condition")
	}

	split := Reach{Selecting: RawReach().Selecting, Read: widen}
	request, err = NewRequestWithReach(registry, policy, model, []ir.FieldID{title}, UseFilter, ir.ActionRead, mine, split)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = Fields(request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Classifications()[0].DischargedByConstraint() {
		t.Fatal("discharge was judged against the raw field condition instead of the read lens")
	}
}

func TestReachLensFailuresFailClosed(t *testing.T) {
	model := modelID(1)
	title, owner := fieldID(1), fieldID(2)
	mine := scalar(t, model, owner, 9)
	registry := identities(model, title, owner)
	policy := newPolicy(t, model, ruleSpec{action: ir.ActionRead, effect: ir.EffectGrant, condition: &mine})
	sentinel := errors.New("expansion refused")
	refuse := func(ir.Condition) (ir.Condition, error) { return ir.Condition{}, sentinel }
	foreign := func(ir.Condition) (ir.Condition, error) { return scalar(t, modelID(2), owner, 9), nil }

	cases := map[string]Reach{
		"MissingSelecting": {Read: RawReach().Read},
		"MissingRead":      {Selecting: RawReach().Selecting},
		"RefusingRead":     {Selecting: RawReach().Selecting, Read: refuse},
		"ForeignModel":     {Selecting: foreign, Read: RawReach().Read},
	}
	for name, reach := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewRequestWithReach(registry, policy, model, []ir.FieldID{title}, UseFilter, ir.ActionRead, mine, reach)
			if err == nil {
				t.Fatal("a broken reach lens was accepted")
			}
			if name == "RefusingRead" && !errors.Is(err, sentinel) {
				t.Fatalf("lens failure was not preserved: %v", err)
			}
		})
	}
	if _, err := Fields(Request{}); err == nil {
		t.Fatal("a zero request without lenses was classified")
	}
}

func conjoin(t *testing.T, model ir.ModelID, conditions ...ir.Condition) ir.Condition {
	t.Helper()
	logical, err := ir.NewLogical(model, ir.LogicalAnd, conditions)
	if err != nil {
		t.Fatal(err)
	}
	result, err := normalize.Condition(logical)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sameBytes(t *testing.T, left, right ir.Condition) bool {
	t.Helper()
	leftBytes, err := ir.CanonicalCondition(left)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := ir.CanonicalCondition(right)
	if err != nil {
		t.Fatal(err)
	}
	return string(leftBytes) == string(rightBytes)
}

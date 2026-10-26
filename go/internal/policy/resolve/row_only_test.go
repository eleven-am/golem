package resolve

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

func TestRowOnlyReadGrantWidensRowsButNoFieldLens(t *testing.T) {
	conditionA := scalarBoolCondition(t, fieldA, true)
	conditionB := scalarBoolCondition(t, fieldB, true)
	base, err := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &conditionA, 0)
	if err != nil {
		t.Fatal(err)
	}
	rowOnly, err := ir.NewRowRule(ir.ActionRead, ir.EffectGrant, testModel, &conditionB, 1)
	if err != nil {
		t.Fatal(err)
	}
	if fields, modelWide := rowOnly.Fields(); modelWide || len(fields) != 0 || !rowOnly.RowOnly() {
		t.Fatalf("row-only rule fields=%x modelWide=%t rowOnly=%t", fields, modelWide, rowOnly.RowOnly())
	}
	policy, err := ir.NewPolicy(testModel, []ir.Rule{base, rowOnly})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !normalized.Rules()[1].RowOnly() {
		t.Fatal("normalization dropped the row-only marker")
	}
	wideRule, err := ir.NewModelRule(ir.ActionRead, ir.EffectGrant, testModel, &conditionB, 1)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := ir.NewPolicy(testModel, []ir.Rule{base, wideRule})
	if err != nil {
		t.Fatal(err)
	}
	encodedRowOnly, err := ir.CanonicalPolicy(normalized)
	if err != nil {
		t.Fatal(err)
	}
	encodedWide, err := ir.CanonicalPolicy(wide)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encodedRowOnly, encodedWide) {
		t.Fatal("a row-only rule encodes like a model-wide rule")
	}
	for _, row := range []testRow{{fieldA: false, fieldB: false}, {fieldA: false, fieldB: true}, {fieldA: true, fieldB: false}, {fieldA: true, fieldB: true}} {
		rows, err := RowConstraint(normalized, ir.ActionRead, testModel)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := evaluate(t, rows, row), row[fieldA] || row[fieldB]; got != want {
			t.Fatalf("row %v: row reach=%t want %t", row, got, want)
		}
		for _, field := range []ir.FieldID{fieldA, fieldB, fieldC} {
			lens, err := FieldCondition(normalized, ir.ActionRead, testModel, field)
			if err != nil {
				t.Fatal(err)
			}
			if got := evaluate(t, lens, row); got != row[fieldA] {
				t.Fatalf("row %v field %x: lens=%t want the base lens %t", row, field, got, row[fieldA])
			}
		}
	}
	for _, refused := range []struct {
		action ir.Action
		effect ir.Effect
	}{{ir.ActionCreate, ir.EffectGrant}, {ir.ActionRead, ir.EffectDeny}, {ir.ActionDelete, ir.EffectGrant}} {
		if _, err := ir.NewRowRule(refused.action, refused.effect, testModel, &conditionB, 0); err == nil {
			t.Fatalf("a row-only rule was built for action %d effect %d", refused.action, refused.effect)
		}
	}
}

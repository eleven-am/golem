package decode

import (
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestPersistedRowDiffNoOpAndIdentityTransition(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	before := postRow(t, fixture, [16]byte{1}, "same", 9_007_199_254_740_993)
	after := postRow(t, fixture, [16]byte{2}, "same", 9_007_199_254_740_993)

	changed, err := ChangedFields(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != policyir.FieldID(fixture.PostID) {
		t.Fatalf("changed=%x; want only identity", changed)
	}
	authored, err := AuthoredFields(fixture.Registry, before, after, []policyir.FieldID{policyir.FieldID(fixture.PostTitle), policyir.FieldID(fixture.PostID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(authored) != 1 || authored[0] != policyir.FieldID(fixture.PostID) {
		t.Fatalf("authored=%x; no-op title must not require field authorization", authored)
	}

	transition, err := PrimaryIdentityTransition(fixture.Registry, &before, &after)
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity, oldOK := transition.Before()
	newIdentity, newOK := transition.After()
	if !oldOK || !newOK || oldIdentity.KeyID() != fixture.PostKey || newIdentity.KeyID() != fixture.PostKey {
		t.Fatal("identity transition did not retain both primary-key images")
	}
	oldValue, _ := oldIdentity.Components()[0].PolicyValue()
	newValue, _ := newIdentity.Components()[0].PolicyValue()
	if EqualValue(oldValue, newValue) {
		t.Fatal("identity-changing update collapsed before and after IDs")
	}
}

func TestPersistedRowIsCompleteSortedAndDetached(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	row := postRow(t, fixture, [16]byte{1}, "title", 42)
	cells := row.Cells()
	for index := 1; index < len(cells); index++ {
		left, right := cells[index-1].FieldID(), cells[index].FieldID()
		if string(left[:]) >= string(right[:]) {
			t.Fatal("row cells are not sorted by stable field identity")
		}
	}
	partial, err := NewRow(fixture.Registry, policyir.ModelID(fixture.Post), cells[:1])
	if err != nil || len(partial.Cells()) != 1 {
		t.Fatalf("valid partial persisted image was rejected: %v", err)
	}
	if _, err := NewCompleteRow(fixture.Registry, policyir.ModelID(fixture.Post), cells[:len(cells)-1]); err == nil {
		t.Fatal("incomplete persisted image passed the explicit complete-row boundary")
	}
	complete, err := NewCompleteRow(fixture.Registry, policyir.ModelID(fixture.Post), cells)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := complete.IsComplete(fixture.Registry); err != nil || !ok {
		t.Fatalf("complete row proof failed: ok=%v err=%v", ok, err)
	}
	if err := partial.RequireFields([]policyir.FieldID{policyir.FieldID(fixture.PostTitle)}); err == nil {
		t.Fatal("partial image claimed a field it did not project")
	}

	input := []byte{1, 2, 3}
	value := policyir.BytesValue(input)
	input[0] = 9
	want := policyir.BytesValue([]byte{1, 2, 3})
	if !EqualValue(value, want) {
		t.Fatal("exact bytes value retained mutable caller storage")
	}
	returned, _ := value.Bytes()
	returned[1] = 9
	if !EqualValue(value, want) {
		t.Fatal("bytes accessor leaked mutable internal storage")
	}
}

func TestTemporalPrecisionNormalizationAndStructuralEquality(t *testing.T) {
	precision := uint16(3)
	timeValue, _ := policyir.NewTimeValue(1_234_567)
	normalized, err := normalizeAndValidate(timeValue, compilerir.LogicalTypeIR{Kind: compilerir.TypeTime, Precision: &precision})
	if err != nil {
		t.Fatal(err)
	}
	microseconds, _ := normalized.Time()
	if microseconds != 1_234_000 {
		t.Fatalf("normalized time=%d", microseconds)
	}
	instant, _ := policyir.NewDateTimeValue(123, 123_456_000)
	normalized, err = normalizeAndValidate(instant, compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &precision})
	if err != nil {
		t.Fatal(err)
	}
	seconds, nanos, _ := normalized.DateTime()
	if seconds != 123 || nanos != 123_000_000 {
		t.Fatalf("normalized datetime=(%d,%d)", seconds, nanos)
	}

	number, _ := policyir.NewJSONNumber(false, []byte("9007199254740993"), 0)
	exact, _ := policyir.JSONNumberValueOf(number)
	memberB, _ := policyir.NewJSONMember("b", exact)
	memberA, _ := policyir.NewJSONMember("a", policyir.JSONBoolValue(true))
	leftJSON, _ := policyir.JSONObjectValue([]policyir.JSONMember{memberB, memberA})
	rightJSON, _ := policyir.JSONObjectValue([]policyir.JSONMember{memberA, memberB})
	left, _ := policyir.NewJSONValue(leftJSON)
	right, _ := policyir.NewJSONValue(rightJSON)
	if !EqualValue(left, right) {
		t.Fatal("canonical JSON structure did not compare equal")
	}
	first, _ := policyir.StringValue("a")
	second, _ := policyir.StringValue("b")
	list1, _ := policyir.NewListValue([]policyir.Value{first, second})
	list2, _ := policyir.NewListValue([]policyir.Value{second, first})
	if EqualValue(list1, list2) {
		t.Fatal("ordered scalar lists compared equal after reordering")
	}
}

func TestCompositeIdentityPreservesDeclaredOrder(t *testing.T) {
	one, _ := policyir.SignedValue(policyir.ValueInt64, 1)
	two, _ := policyir.StringValue("two")
	first, _ := IdentityValue(policyir.FieldID{2}, one)
	second, _ := IdentityValue(policyir.FieldID{1}, two)
	identity, err := NewIdentity([16]byte{7}, []IdentityComponent{first, second})
	if err != nil {
		t.Fatal(err)
	}
	components := identity.Components()
	if len(components) != 2 || components[0].FieldID() != (policyir.FieldID{2}) || components[1].FieldID() != (policyir.FieldID{1}) {
		t.Fatal("composite identity order was sorted instead of preserving key order")
	}
}

func postRow(t *testing.T, fixture schematest.Fixture, id [16]byte, title string, big int64) Row {
	t.Helper()
	author := policyir.UUIDValue([16]byte{9})
	identifier := policyir.UUIDValue(id)
	text, _ := policyir.StringValue(title)
	integer, _ := policyir.SignedValue(policyir.ValueInt64, big)
	decimal, _ := policyir.NewDecimalValue(123_456_789_012_345_678, 13)
	row, err := NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []Cell{
		Value(policyir.FieldID(fixture.PostDecimal), decimal),
		Value(policyir.FieldID(fixture.PostTitle), text),
		Value(policyir.FieldID(fixture.PostID), identifier),
		Value(policyir.FieldID(fixture.PostBigInt), integer),
		Value(policyir.FieldID(fixture.AuthorID), author),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

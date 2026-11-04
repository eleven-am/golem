package evaluate

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestRecordConstructorsDistinguishEveryLoadedState(t *testing.T) {
	model, target, scalarField, relationField := modelID(1), modelID(2), fieldID(1), fieldID(2)
	value := stringValue(t, "loaded")
	related := record(t, target)
	row := record(t, model,
		NullField(scalarField),
		toMany(t, relationField, target, related),
	)
	if row.model != model || row.fields[scalarField].kind != fieldNull || row.fields[relationField].kind != fieldRelation || len(row.fields[relationField].rows) != 1 {
		t.Fatalf("record states = %#v", row)
	}
	if _, loaded := row.fields[fieldID(99)]; loaded {
		t.Fatal("omitted field became loaded")
	}
	if _, err := NewRecord(model, valueField(t, scalarField, value), NullField(scalarField)); err == nil {
		t.Fatal("duplicate field accepted")
	}
	if _, err := ToOneField(relationField, target, related, related); err == nil {
		t.Fatal("multi-row to-one accepted")
	}
}

func TestRecordAndFieldConstructionCopyCallerSlices(t *testing.T) {
	model, target, relationField := modelID(1), modelID(2), fieldID(1)
	rows := []Record{record(t, target)}
	field, err := ToManyField(relationField, target, rows...)
	if err != nil {
		t.Fatal(err)
	}
	rows[0] = Record{}
	row := record(t, model, field)
	if row.fields[relationField].rows[0].ModelID() != target {
		t.Fatal("relation constructor retained caller row storage")
	}

	bytes := []byte{1, 2, 3}
	bytesValue := ir.BytesValue(bytes)
	bytes[0] = 9
	loaded := valueField(t, fieldID(2), bytesValue)
	got, _ := loaded.value.Bytes()
	if got[0] != 1 {
		t.Fatal("value field changed after caller byte mutation")
	}
}

func TestWithRelationsTakesOnlyTheKeptRelationsAndNoScalars(t *testing.T) {
	model, target := modelID(1), modelID(2)
	scalar, kept, dropped := fieldID(1), fieldID(2), fieldID(3)
	base := record(t, model, valueField(t, scalar, stringValue(t, "before")))
	source := record(t, model,
		valueField(t, scalar, stringValue(t, "after")),
		toOne(t, kept, target, record(t, target)),
		toOne(t, dropped, target, record(t, target)),
	)
	merged := base.WithRelations(source, map[ir.FieldID]struct{}{kept: {}})
	if got, _ := merged.fields[scalar].value.Text(); got != "before" {
		t.Fatalf("scalar = %q, want the base record's value", got)
	}
	if merged.fields[kept].kind != fieldRelation || len(merged.fields[kept].rows) != 1 {
		t.Fatal("the kept relation was not copied")
	}
	if _, present := merged.fields[dropped]; present {
		t.Fatal("a relation outside the keep set was copied")
	}
	if _, present := base.fields[kept]; present {
		t.Fatal("WithRelations changed the receiver")
	}
	scalarOnly := base.WithRelations(source, map[ir.FieldID]struct{}{scalar: {}})
	if got, _ := scalarOnly.fields[scalar].value.Text(); got != "before" {
		t.Fatalf("a scalar named in the keep set replaced the base value: %q", got)
	}
}

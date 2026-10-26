package bind

import (
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestUniqueSelectorAndCursorRefuseSubMicrosecondDateTimeNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	descriptor := golem.GeneratedModelDescriptor[post](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	at := time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC)
	selector := func(value time.Time) golem.UniqueSelectorValue[post] {
		return golem.GeneratedUniqueSelectorValue[post](fixture.Post, fixture.PostKey,
			golem.GeneratedSelectorComponent(fixture.PostID, golem.NewUUID([16]byte{4})),
			golem.GeneratedSelectorComponent(fixture.PostAt, value))
	}
	unique, err := golem.FreezeFindUnique(descriptor, selector(at))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := golem.FreezeFindMany(descriptor, golem.Cursor(selector(at)))
	if err != nil {
		t.Fatal(err)
	}
	for name, frozen := range map[string]golem.FrozenReadRequest{"find-unique": unique, "cursor": cursor} {
		t.Run(name, func(t *testing.T) {
			_, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
			var failure *policybind.Error
			if !errors.As(err, &failure) || failure.Reason != golem.ReasonValuePrecision || failure.FieldID != policyir.FieldID(fixture.PostAt) {
				t.Fatalf("sub-microsecond selector err=%v", err)
			}
		})
	}
	onGrid, err := golem.FreezeFindUnique(descriptor, selector(at.Truncate(time.Microsecond)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Request(onGrid, fixture.Registry, policyir.PortableProviders()); err != nil {
		t.Fatalf("on-grid selector refused: %v", err)
	}
	atField := golem.GeneratedOrderedField[post, time.Time](fixture.PostAt)
	filter, err := golem.FreezeFindMany(descriptor, golem.Where(atField.GTE(at)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Request(filter, fixture.Registry, policyir.PortableProviders()); err != nil {
		t.Fatalf("sub-microsecond filter refused: %v", err)
	}
}

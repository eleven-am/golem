package custom

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

type gateJSONDocument struct{ encoded []byte }

func (document gateJSONDocument) Bytes() []byte { return document.encoded }

func TestCustomOperationScalarValidationCoversEveryScalarTheLogicalTypeSystemProduces(t *testing.T) {
	uuid, err := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	date, err := golem.ParseDate("2024-01-02")
	if err != nil {
		t.Fatal(err)
	}
	clock, err := golem.ParseTime("03:04:05")
	if err != nil {
		t.Fatal(err)
	}
	exact := map[string]any{
		"Boolean":  true,
		"Int":      int32(7),
		"Float":    1.5,
		"String":   "text",
		"BigInt":   int64(9007199254740993),
		"Decimal":  decimal,
		"UUID":     uuid,
		"Date":     date,
		"Time":     clock,
		"DateTime": time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		"Bytes":    []byte("hi"),
		"JSON":     gateJSONDocument{encoded: []byte(`{"key":"value"}`)},
	}
	names := compilerir.ScalarGraphQLNames()
	if len(names) == 0 {
		t.Fatal("the logical type system reported no GraphQL scalars")
	}
	for _, name := range names {
		if !compilerir.IsScalarGraphQLName(name) {
			t.Fatalf("scalar %q is produced but not recognised", name)
		}
		value, present := exact[name]
		if !present {
			t.Errorf("scalar %q has no exact Go value in this gate; validateScalar must gain a case for it", name)
			continue
		}
		if err := validateScalar(name, value); err != nil {
			t.Errorf("validateScalar rejects scalar %q that the logical type system produces: %v", name, err)
		}
	}
	for name := range exact {
		if !compilerir.IsScalarGraphQLName(name) {
			t.Errorf("gate lists scalar %q that the logical type system no longer produces", name)
		}
	}
}

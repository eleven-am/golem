package selectset

import (
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestComputedArgumentCoercionCoversEveryScalarTheLogicalTypeSystemProduces(t *testing.T) {
	wire := map[string]any{
		"Boolean":  true,
		"Int":      int32(7),
		"Float":    1.5,
		"String":   "text",
		"BigInt":   "9007199254740993",
		"Decimal":  "1.25",
		"UUID":     "00000000-0000-0000-0000-000000000001",
		"Date":     "2024-01-02",
		"Time":     "03:04:05",
		"DateTime": "2024-01-02T03:04:05Z",
		"Bytes":    "aGk=",
		"JSON":     map[string]any{"key": "value"},
	}
	names := compilerir.ScalarGraphQLNames()
	if len(names) == 0 {
		t.Fatal("the logical type system reported no GraphQL scalars")
	}
	for _, name := range names {
		raw, present := wire[name]
		if !present {
			t.Errorf("scalar %q has no computed-argument wire value in this gate; coerceComputedValue must gain a case for it", name)
			continue
		}
		typ := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: name}
		if _, _, err := coerceComputedValue(typ, raw, nil, 16); err != nil {
			t.Errorf("coerceComputedValue rejects scalar %q that the logical type system produces: %v", name, err)
		}
	}
	for name := range wire {
		if !compilerir.IsScalarGraphQLName(name) {
			t.Errorf("gate lists scalar %q that the logical type system no longer produces", name)
		}
	}
}

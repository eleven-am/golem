package bind

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ExactStoredScalarValue routes CDC adapter values through the same logical
// scalar binder used by mutations. It is intentionally not a provider decoder:
// adapters must supply Golem's exact public scalar types, never driver values.
func ExactStoredScalarValue(raw any, field schema.Field, registry *schema.Registry) (policyir.Value, error) {
	if registry == nil || field.ID() == (golem.FieldID{}) || field.Kind() == compilerir.FieldRelation {
		return policyir.Value{}, fmt.Errorf("P7_CDC_IMAGE: scalar registry field is absent")
	}
	typ, err := bindType(field.LogicalType(), field.Nullable())
	if err != nil {
		return policyir.Value{}, fmt.Errorf("P7_CDC_IMAGE: scalar type: %w", err)
	}
	value, err := bindValue(raw, field.LogicalType(), typ, registry)
	if err != nil {
		return policyir.Value{}, fmt.Errorf("P7_CDC_IMAGE: scalar value: %w", err)
	}
	return value, nil
}

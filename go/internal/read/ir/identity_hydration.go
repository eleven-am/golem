package ir

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// IdentityHydration is a bounded set of complete equality tuples, not an
// arbitrary privileged predicate. Bind and plan additionally check its ordered
// fields against the active model's primary key.
type IdentityHydration struct {
	condition policyir.Condition
	fields    []policyir.FieldID
	count     int
}

func NewIdentityHydration(condition policyir.Condition, fields []policyir.FieldID) (IdentityHydration, error) {
	invalid := func() (IdentityHydration, error) {
		return IdentityHydration{}, fmt.Errorf("P3_READ_IR_IDENTITY: complete scalar equality tuples are required")
	}
	if condition.ModelID() == (policyir.ModelID{}) || len(fields) == 0 || len(fields) > golem.RuntimeIdentityHydrationLimit {
		return invalid()
	}
	seen := make(map[policyir.FieldID]bool, len(fields))
	for _, field := range fields {
		if field == (policyir.FieldID{}) || seen[field] {
			return invalid()
		}
		seen[field] = true
	}
	count := 1
	operator, logical := condition.LogicalOperator()
	if logical && operator == policyir.LogicalOr {
		count = condition.ChildCount()
	}
	if count == 0 || count > golem.RuntimeIdentityHydrationLimit/len(fields) {
		return invalid()
	}
	for index := 0; index < count; index++ {
		tuple := condition
		if logical && operator == policyir.LogicalOr {
			tuple = condition.Child(index)
		}
		if len(fields) > 1 {
			operator, ok := tuple.LogicalOperator()
			if !ok || operator != policyir.LogicalAnd || tuple.ChildCount() != len(fields) {
				return invalid()
			}
		}
		for component, field := range fields {
			leaf := tuple
			if len(fields) > 1 {
				leaf = tuple.Child(component)
			}
			id, _ := leaf.Field()
			op, _ := leaf.Operator()
			mode, _ := leaf.Mode()
			operand, _ := leaf.Operand()
			if leaf.ModelID() != condition.ModelID() || leaf.Kind() != policyir.ConditionScalar || id != field || op != policyir.OperatorEqual || mode != policyir.ComparisonSensitive || operand.Kind() != policyir.OperandOne {
				return invalid()
			}
		}
	}
	return IdentityHydration{condition: condition, fields: append([]policyir.FieldID(nil), fields...), count: count}, nil
}

func (value IdentityHydration) Condition() policyir.Condition { return value.condition }
func (value IdentityHydration) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), value.fields...)
}
func (value IdentityHydration) Count() int               { return value.count }
func (value IdentityHydration) clone() IdentityHydration { value.fields = value.Fields(); return value }

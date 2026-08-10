package ir

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type SelectorValue struct {
	field policyir.FieldID
	value policyir.Value
}

func NewSelectorValue(field policyir.FieldID, value policyir.Value) (SelectorValue, error) {
	if field == (policyir.FieldID{}) {
		return SelectorValue{}, fmt.Errorf("P4_MUTATION_IR_SELECTOR: field identity is zero")
	}
	if err := value.Validate(); err != nil {
		return SelectorValue{}, fmt.Errorf("P4_MUTATION_IR_SELECTOR: invalid value: %w", err)
	}
	return SelectorValue{field: field, value: value}, nil
}

func (value SelectorValue) FieldID() policyir.FieldID { return value.field }
func (value SelectorValue) Value() policyir.Value     { return value.value }

type Target struct {
	model  policyir.ModelID
	key    golem.KeyID
	values []SelectorValue
	guard  *policyir.Condition
}

func NewTarget(model policyir.ModelID, key golem.KeyID, values []SelectorValue, guard *policyir.Condition) (Target, error) {
	result := Target{model: model, key: key, values: append([]SelectorValue(nil), values...)}
	if guard != nil {
		copy := *guard
		result.guard = &copy
	}
	if err := result.validate(); err != nil {
		return Target{}, err
	}
	return result, nil
}

func (target Target) ModelID() policyir.ModelID { return target.model }
func (target Target) KeyID() golem.KeyID        { return target.key }
func (target Target) Values() []SelectorValue   { return append([]SelectorValue(nil), target.values...) }
func (target Target) Guard() (policyir.Condition, bool) {
	if target.guard == nil {
		return policyir.Condition{}, false
	}
	return *target.guard, true
}
func (target Target) Validate() error { return target.validate() }

func (target Target) validate() error {
	if err := validateModel(target.model, "TARGET"); err != nil {
		return err
	}
	if target.key == (golem.KeyID{}) || len(target.values) == 0 {
		return fmt.Errorf("P4_MUTATION_IR_TARGET: key and selector values are required")
	}
	seen := make(map[policyir.FieldID]struct{}, len(target.values))
	for index, value := range target.values {
		if value.field == (policyir.FieldID{}) {
			return fmt.Errorf("P4_MUTATION_IR_TARGET: selector value %d has a zero field", index)
		}
		if err := value.value.Validate(); err != nil {
			return fmt.Errorf("P4_MUTATION_IR_TARGET: selector value %d: %w", index, err)
		}
		if _, exists := seen[value.field]; exists {
			return fmt.Errorf("P4_MUTATION_IR_TARGET: selector field is duplicate")
		}
		seen[value.field] = struct{}{}
	}
	if target.guard != nil {
		if err := target.guard.Validate(); err != nil {
			return fmt.Errorf("P4_MUTATION_IR_TARGET: invalid guard: %w", err)
		}
		if target.guard.ModelID() != target.model {
			return fmt.Errorf("P4_MUTATION_IR_TARGET: guard model does not match target")
		}
	}
	return nil
}

func (target Target) clone() Target {
	copy := target
	copy.values = append([]SelectorValue(nil), target.values...)
	if target.guard != nil {
		guard := *target.guard
		copy.guard = &guard
	}
	return copy
}

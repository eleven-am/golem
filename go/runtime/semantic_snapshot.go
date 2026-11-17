package runtime

import (
	"fmt"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
)

// Semantic state and drain jobs are flushed at commit. Until then, derive
// candidacy from the transaction's existing mark buffer as well, so a read
// cannot serve an old ready vector for a row this transaction just changed.
// No state is flushed early and outbox/queue commit behavior is unchanged.
func semanticPendingExclusion[P, A any](app *App[P, A], prepared PreparedRead, model policyir.ModelID) (policyir.Condition, bool, error) {
	if prepared.executor == nil {
		return policyir.Condition{}, false, fmt.Errorf("P9_SEMANTIC_RUNTIME: semantic snapshot executor is unavailable")
	}
	prepared.executor.stateMu.Lock()
	state := prepared.executor.state
	prepared.executor.stateMu.Unlock()
	marks := state.semanticMarks()
	relevant := make([]semanticMark, 0)
	for _, mark := range marks {
		if mark.model == golem.ModelID(model) {
			relevant = append(relevant, mark)
		}
	}
	if len(relevant) == 0 {
		return policyir.Condition{}, false, nil
	}
	fact, ok := app.registry.Model(golem.ModelID(model))
	if !ok {
		return policyir.Condition{}, false, fmt.Errorf("P9_SEMANTIC_SCHEMA: marked model is absent")
	}
	var primary golem.KeyID
	for _, identity := range fact.Identities() {
		if identity.Kind() == ir.KeyPrimary {
			primary = identity.KeyID()
			break
		}
	}
	fields := fact.PrimaryKey()
	conditions := make([]policyir.Condition, len(relevant))
	values := make([]policyir.Value, 0, len(relevant))
	for i, mark := range relevant {
		if len(mark.identity) != len(fields) {
			return policyir.Condition{}, false, fmt.Errorf("P9_SEMANTIC_MARK: pending identity width is invalid")
		}
		components := make([]golem.RuntimeSelectorValue, len(fields))
		for j, field := range fields {
			metadata, ok := app.registry.Field(golem.ModelID(model), field)
			if !ok {
				return policyir.Condition{}, false, fmt.Errorf("P9_SEMANTIC_SCHEMA: pending identity field is absent")
			}
			value, err := semanticSelectorIdentityValue(metadata.LogicalType().Kind, mark.identity[j])
			if err != nil {
				return policyir.Condition{}, false, err
			}
			components[j] = golem.RuntimeSelectorValue{Field: field, Value: value}
		}
		target, err := golem.RuntimeMutationTargetFromIdentity(golem.ModelID(model), primary, components)
		if err != nil {
			return policyir.Condition{}, false, err
		}
		conditions[i], err = policybind.ExactPredicate(target.SelectorPredicate(), app.registry, app.providers)
		if err != nil {
			return policyir.Condition{}, false, err
		}
		if len(fields) == 1 {
			operand, _ := conditions[i].Operand()
			value, present := operand.One()
			if !present {
				return policyir.Condition{}, false, fmt.Errorf("P9_SEMANTIC_MARK: pending identity has no scalar operand")
			}
			values = append(values, value)
		}
	}
	if len(fields) == 1 {
		// Compact large pending-write sets through the same typed set codec used
		// by actor grants, preserving the existing statement parameter ceiling.
		typ, _ := conditions[0].FieldType()
		operand, err := policyir.ManyOperand(values)
		if err != nil {
			return policyir.Condition{}, false, err
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorNotIn, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: app.providers})
		if err != nil {
			return policyir.Condition{}, false, err
		}
		condition, err := policyir.NewScalar(model, policyir.FieldID(fields[0]), typ, policyir.OperatorNotIn, policyir.ComparisonSensitive, operand, requirements)
		return condition, err == nil, err
	}
	any := conditions[0]
	if len(conditions) > 1 {
		var err error
		any, err = policyir.NewLogical(model, policyir.LogicalOr, conditions)
		if err != nil {
			return policyir.Condition{}, false, err
		}
	}
	condition, err := policyir.NewLogical(model, policyir.LogicalNot, []policyir.Condition{any})
	return condition, err == nil, err
}

// Marks use the semantic key codec's canonical native values: integer widths
// collapse to int64 and UUIDs become [16]byte. Authored selector operands keep
// their logical Go types, which the existing binder then validates exactly.
func semanticSelectorIdentityValue(kind ir.LogicalTypeKind, native any) (any, error) {
	switch kind {
	case ir.TypeInt16:
		value, ok := native.(int64)
		if !ok || int64(int16(value)) != value {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: pending int16 identity is invalid")
		}
		return int16(value), nil
	case ir.TypeInt32:
		value, ok := native.(int64)
		if !ok || int64(int32(value)) != value {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: pending int32 identity is invalid")
		}
		return int32(value), nil
	case ir.TypeUUID:
		value, ok := native.([16]byte)
		if !ok {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: pending UUID identity is invalid")
		}
		return golem.UUID(value), nil
	default:
		return native, nil
	}
}

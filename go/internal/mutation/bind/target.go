package bind

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// BoundTarget retains both the closed mutation target facts and the exact
// already-bound selector predicate needed by the authorization planner. The
// planner must not reconstruct equality from untyped selector payloads.
type BoundTarget struct {
	target            mutationir.Target
	selectorPredicate policyir.Condition
}

func (target BoundTarget) Target() mutationir.Target             { return target.target }
func (target BoundTarget) SelectorPredicate() policyir.Condition { return target.selectorPredicate }

// Target binds one frozen unique target for the model owned by the operation.
// Selector values are emitted in the active identity's declared order even
// when predicate canonicalization has reordered equality leaves.
func Target(frozen golem.FrozenMutationTarget, expectedModel golem.ModelID, registry *schema.Registry) (BoundTarget, error) {
	if registry == nil {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "active schema registry is required", nil)
	}
	model, ok := registry.Model(expectedModel)
	if expectedModel == (golem.ModelID{}) || !ok {
		return BoundTarget{}, fail(CodeModel, expectedModel, golem.FieldID{}, "target model is absent from the active schema", nil)
	}
	public := frozen.Selector()
	if public.ModelID() != expectedModel || public.KeyID() == (golem.KeyID{}) {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector model or key does not belong to the operation", nil)
	}
	identity, ok := model.Identity(public.KeyID())
	if !ok || !sameFields(identity.Fields(), public.Fields()) {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector identity or component order does not match the active schema", nil)
	}
	for _, fieldID := range identity.Fields() {
		field, present := registry.Field(expectedModel, fieldID)
		if !present || field.Kind() == compilerir.FieldRelation {
			return BoundTarget{}, fail(CodeField, expectedModel, fieldID, "selector component is absent, foreign, or not scalar", nil)
		}
		if !readable(field) {
			return BoundTarget{}, fail(CodeExposure, expectedModel, fieldID, "hidden or write-only field cannot select a public mutation target", nil)
		}
	}

	providers, err := providerSet(registry)
	if err != nil {
		return BoundTarget{}, fail(CodeInternal, expectedModel, golem.FieldID{}, "active schema provider set is invalid", err)
	}
	selectorPredicate, err := policybind.Predicate(frozen.SelectorPredicate(), registry, providers)
	if err != nil {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector predicate did not bind", err)
	}
	if selectorPredicate.ModelID() != policyir.ModelID(expectedModel) {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector predicate belongs to another model", nil)
	}
	byField := make(map[policyir.FieldID]policyir.Value, len(identity.Fields()))
	if err := collectSelectorValues(selectorPredicate, byField); err != nil {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector predicate is not exact identity equality", err)
	}
	if len(byField) != len(identity.Fields()) {
		return BoundTarget{}, fail(CodeTarget, expectedModel, golem.FieldID{}, "selector predicate component inventory is incomplete", nil)
	}
	values := make([]mutationir.SelectorValue, len(identity.Fields()))
	for index, fieldID := range identity.Fields() {
		value, present := byField[policyir.FieldID(fieldID)]
		if !present {
			return BoundTarget{}, fail(CodeTarget, expectedModel, fieldID, "selector predicate omitted an identity component", nil)
		}
		bound, valueErr := mutationir.NewSelectorValue(policyir.FieldID(fieldID), value)
		if valueErr != nil {
			return BoundTarget{}, fail(CodeInternal, expectedModel, fieldID, "target IR rejected a validated selector value", valueErr)
		}
		values[index] = bound
	}

	var guard *policyir.Condition
	if publicGuard, present := frozen.Guard(); present {
		bound, bindErr := policybind.Predicate(publicGuard, registry, providers)
		if bindErr != nil {
			return BoundTarget{}, fail(CodeGuard, expectedModel, golem.FieldID{}, "target guard did not bind", bindErr)
		}
		if bound.ModelID() != policyir.ModelID(expectedModel) {
			return BoundTarget{}, fail(CodeGuard, expectedModel, golem.FieldID{}, "target guard belongs to another model", nil)
		}
		if exposureErr := validateReadableCondition(bound, registry); exposureErr != nil {
			return BoundTarget{}, exposureErr
		}
		guard = &bound
	}

	bound, err := mutationir.NewTarget(policyir.ModelID(expectedModel), public.KeyID(), values, guard)
	if err != nil {
		return BoundTarget{}, fail(CodeInternal, expectedModel, golem.FieldID{}, "target IR rejected validated target facts", err)
	}
	return BoundTarget{target: bound, selectorPredicate: selectorPredicate}, nil
}

// BatchPredicate binds one top-level update-many/delete-many filter. It owns
// schema identity, exact value, and public exposure validation only;
// classification remains with the planner because that requires the resolved
// caller policy and concrete selecting action.
func BatchPredicate(frozen golem.FrozenPredicate, expectedModel golem.ModelID, registry *schema.Registry) (policyir.Condition, error) {
	if registry == nil {
		return policyir.Condition{}, fail(CodeInput, expectedModel, golem.FieldID{}, "active schema registry is required", nil)
	}
	if expectedModel == (golem.ModelID{}) || !registry.HasModel(expectedModel) {
		return policyir.Condition{}, fail(CodeModel, expectedModel, golem.FieldID{}, "batch predicate model is absent from the active schema", nil)
	}
	providers, err := providerSet(registry)
	if err != nil {
		return policyir.Condition{}, fail(CodeInternal, expectedModel, golem.FieldID{}, "active schema provider set is invalid", err)
	}
	bound, err := policybind.Predicate(frozen, registry, providers)
	if err != nil {
		return policyir.Condition{}, fail(CodeGuard, expectedModel, golem.FieldID{}, "batch predicate did not bind", err)
	}
	if bound.ModelID() != policyir.ModelID(expectedModel) {
		return policyir.Condition{}, fail(CodeGuard, expectedModel, golem.FieldID{}, "batch predicate belongs to another model", nil)
	}
	if err := validateReadableCondition(bound, registry); err != nil {
		return policyir.Condition{}, err
	}
	return bound, nil
}

func collectSelectorValues(condition policyir.Condition, values map[policyir.FieldID]policyir.Value) error {
	switch condition.Kind() {
	case policyir.ConditionScalar:
		field, fieldOK := condition.Field()
		operator, operatorOK := condition.Operator()
		operand, operandOK := condition.Operand()
		value, valueOK := operand.One()
		if !fieldOK || !operatorOK || !operandOK || !valueOK || operator != policyir.OperatorEqual {
			return fmt.Errorf("selector leaf is not scalar equality with one value")
		}
		if _, duplicate := values[field]; duplicate {
			return fmt.Errorf("selector field is duplicate")
		}
		values[field] = value
		return nil
	case policyir.ConditionLogical:
		operator, children, ok := condition.Logical()
		if !ok || operator != policyir.LogicalAnd || len(children) < 2 {
			return fmt.Errorf("selector logical node is not a normalized conjunction")
		}
		for _, child := range children {
			if err := collectSelectorValues(child, values); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("selector predicate contains a non-scalar node")
	}
}

func validateReadableCondition(condition policyir.Condition, registry *schema.Registry) error {
	modelID := golem.ModelID(condition.ModelID())
	switch condition.Kind() {
	case policyir.ConditionConstant:
		return nil
	case policyir.ConditionLogical:
		_, children, ok := condition.Logical()
		if !ok {
			return fail(CodeGuard, modelID, golem.FieldID{}, "guard logical node is malformed", nil)
		}
		for _, child := range children {
			if err := validateReadableCondition(child, registry); err != nil {
				return err
			}
		}
		return nil
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		fieldID, ok := condition.Field()
		if !ok {
			return fail(CodeGuard, modelID, golem.FieldID{}, "guard field node is malformed", nil)
		}
		field, present := registry.Field(modelID, golem.FieldID(fieldID))
		if !present || !readable(field) {
			return fail(CodeExposure, modelID, golem.FieldID(fieldID), "hidden or write-only field cannot be used by a mutation guard", nil)
		}
		return nil
	case policyir.ConditionRelation:
		fieldID, relationID, targetID, _, child, ok := condition.Relation()
		if !ok {
			return fail(CodeGuard, modelID, golem.FieldID{}, "guard relation node is malformed", nil)
		}
		field, present := registry.Field(modelID, golem.FieldID(fieldID))
		if !present || !readable(field) {
			return fail(CodeExposure, modelID, golem.FieldID(fieldID), "hidden or write-only relation cannot be used by a mutation guard", nil)
		}
		endpoint, present := registry.RelationEndpoint(modelID, golem.FieldID(fieldID), golem.RelationID(relationID))
		if !present || endpoint.TargetModelID() != golem.ModelID(targetID) {
			return fail(CodeGuard, modelID, golem.FieldID(fieldID), "guard relation does not match the active schema", nil)
		}
		if child != nil {
			return validateReadableCondition(*child, registry)
		}
		return nil
	default:
		return fail(CodeGuard, modelID, golem.FieldID{}, "guard contains an unknown condition kind", nil)
	}
}

func readable(field schema.Field) bool {
	for _, mode := range field.Modes() {
		if mode == compilerir.ModeHidden || mode == compilerir.ModeWriteOnly {
			return false
		}
	}
	return true
}

func sameFields(left, right []golem.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func providerSet(registry *schema.Registry) (policyir.ProviderSet, error) {
	providers := registry.Providers()
	values := make([]policyir.Provider, len(providers))
	for index, provider := range providers {
		switch provider {
		case golem.SQLite:
			values[index] = policyir.ProviderSQLite
		case golem.PostgreSQL:
			values[index] = policyir.ProviderPostgreSQL
		default:
			return 0, fmt.Errorf("unknown registry provider %q", provider)
		}
	}
	return policyir.NewProviderSet(values...)
}

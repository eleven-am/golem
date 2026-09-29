package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func ReferenceCondition(registry *schema.Registry, policies PolicySet, model policyir.ModelID, operations []mutationir.ScalarOperation, readDepth int) (*policyir.Condition, error) {
	authored := make(map[golem.FieldID]struct{}, len(operations))
	for _, operation := range operations {
		if !operation.RuntimeOwned() && !operation.HookAuthored() {
			authored[golem.FieldID(operation.FieldID())] = struct{}{}
		}
	}
	if len(authored) == 0 {
		return nil, nil
	}
	if registry == nil || policies == nil {
		return nil, fmt.Errorf("reference condition requires the active registry and caller policies")
	}
	logical, ok := registry.Model(golem.ModelID(model))
	if !ok {
		return nil, fmt.Errorf("reference condition model is absent")
	}
	providers, err := providerSet(registry)
	if err != nil {
		return nil, err
	}
	var references []policyir.Condition
	for _, fieldID := range logical.Fields() {
		field, present := registry.Field(golem.ModelID(model), fieldID)
		if !present || field.Kind() != compilerir.FieldRelation {
			continue
		}
		relation, hasRelation := field.RelationID()
		if !hasRelation {
			continue
		}
		endpoint, hasEndpoint := registry.RelationEndpoint(golem.ModelID(model), fieldID, relation)
		if !hasEndpoint || endpoint.Role() != compilerir.RelationSource || endpoint.Cardinality() != compilerir.RelationOne {
			continue
		}
		pairs := endpoint.Correlation()
		written := false
		for _, pair := range pairs {
			if _, present := authored[pair.ParentFieldID()]; present {
				written = true
				break
			}
		}
		if !written {
			continue
		}
		target := policyir.ModelID(endpoint.TargetModelID())
		reach, reachErr := readplan.ReadReach(policies, target, readDepth)
		if reachErr != nil {
			return nil, reachErr
		}
		if truth, constant := reach.Constant(); constant && truth {
			continue
		}
		requirements, shapeErr := operator.ValidateShape(policyir.OperatorRelationIs, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: providers})
		if shapeErr != nil {
			return nil, shapeErr
		}
		readable, relationErr := policyir.NewRelation(model, policyir.FieldID(fieldID), policyir.RelationID(relation), target, policyir.RelationToOne, policyir.OperatorRelationIs, &reach, requirements)
		if relationErr != nil {
			return nil, relationErr
		}
		alternatives := make([]policyir.Condition, 0, len(pairs)+1)
		for _, pair := range pairs {
			correlation, present := registry.Field(golem.ModelID(model), pair.ParentFieldID())
			if !present {
				return nil, fmt.Errorf("reference correlation field is absent")
			}
			if !correlation.Nullable() {
				continue
			}
			typ, typeErr := mutationbind.FieldType(correlation)
			if typeErr != nil {
				return nil, typeErr
			}
			nullRequirements, nullShapeErr := operator.ValidateShape(policyir.OperatorIsNull, operator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Providers: providers})
			if nullShapeErr != nil {
				return nil, nullShapeErr
			}
			absent, nullErr := policyir.NewScalar(model, policyir.FieldID(pair.ParentFieldID()), typ, policyir.OperatorIsNull, policyir.ComparisonSensitive, policyir.NoOperand(), nullRequirements)
			if nullErr != nil {
				return nil, nullErr
			}
			alternatives = append(alternatives, absent)
		}
		if len(alternatives) == 0 {
			references = append(references, readable)
			continue
		}
		either, eitherErr := policyir.NewLogical(model, policyir.LogicalOr, append(alternatives, readable))
		if eitherErr != nil {
			return nil, eitherErr
		}
		references = append(references, either)
	}
	switch len(references) {
	case 0:
		return nil, nil
	case 1:
		normalized, normalizeErr := normalize.Condition(references[0])
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		return &normalized, nil
	default:
		all, allErr := policyir.NewLogical(model, policyir.LogicalAnd, references)
		if allErr != nil {
			return nil, allErr
		}
		normalized, normalizeErr := normalize.Condition(all)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		return &normalized, nil
	}
}

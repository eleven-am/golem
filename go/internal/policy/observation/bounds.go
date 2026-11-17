// Package observation plans conservative, source-scoped invalidation for
// physical policy facts. It never grants access to a dependency row.
package observation

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

type projection uint8

const (
	opaqueFacts projection = iota
	opaqueRelations
)

type bounds struct{ lower, upper ir.Condition }

// project preserves known local predicates while bounding unknown facts or
// relationships. Negation exchanges lower and upper bounds; replacing every
// unknown by true would incorrectly eliminate negative-fact observation.
func project(input ir.Condition, mode projection) (bounds, error) {
	if err := input.Validate(); err != nil {
		return bounds{}, err
	}
	switch input.Kind() {
	case ir.ConditionConstant, ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
		return bounds{lower: input, upper: input}, nil
	case ir.ConditionFact:
		_, body, _ := input.Fact()
		if _, constant := body.Constant(); constant {
			return bounds{lower: body, upper: body}, nil
		}
		if mode == opaqueFacts {
			return unknown(input.ModelID())
		}
		return project(body, mode)
	case ir.ConditionLogical:
		operation, children, _ := input.Logical()
		lower, upper := make([]ir.Condition, len(children)), make([]ir.Condition, len(children))
		for index, child := range children {
			value, err := project(child, mode)
			if err != nil {
				return bounds{}, err
			}
			lower[index], upper[index] = value.lower, value.upper
		}
		if operation == ir.LogicalNot {
			lower, upper = upper, lower
		}
		return combineBounds(input.ModelID(), operation, lower, upper)
	case ir.ConditionRelation:
		if mode == opaqueRelations {
			return unknown(input.ModelID())
		}
		field, relation, target, cardinality, child, _ := input.Relation()
		if child == nil {
			return bounds{lower: input, upper: input}, nil
		}
		value, err := project(*child, mode)
		if err != nil {
			return bounds{}, err
		}
		operation, _ := input.Operator()
		if operation == ir.OperatorRelationNone || operation == ir.OperatorRelationIsNot {
			value.lower, value.upper = value.upper, value.lower
		}
		requirements, _ := input.RelationOwnRequirements()
		lower, err := ir.NewRelation(input.ModelID(), field, relation, target, cardinality, operation, &value.lower, requirements)
		if err != nil {
			return bounds{}, err
		}
		upper, err := ir.NewRelation(input.ModelID(), field, relation, target, cardinality, operation, &value.upper, requirements)
		if err != nil {
			return bounds{}, err
		}
		return normalizedBounds(lower, upper)
	default:
		return bounds{}, fmt.Errorf("policy observation: unresolved or unknown condition kind %d", input.Kind())
	}
}

func unknown(model ir.ModelID) (bounds, error) {
	lower, err := ir.NewConstant(model, false)
	if err != nil {
		return bounds{}, err
	}
	upper, err := ir.NewConstant(model, true)
	return bounds{lower: lower, upper: upper}, err
}

func combineBounds(model ir.ModelID, operation ir.LogicalOperator, lower, upper []ir.Condition) (bounds, error) {
	left, err := logical(model, operation, lower)
	if err != nil {
		return bounds{}, err
	}
	right, err := logical(model, operation, upper)
	return bounds{lower: left, upper: right}, err
}

func normalizedBounds(lower, upper ir.Condition) (bounds, error) {
	lower, err := normalize.Condition(lower)
	if err != nil {
		return bounds{}, err
	}
	upper, err = normalize.Condition(upper)
	return bounds{lower: lower, upper: upper}, err
}

func logical(model ir.ModelID, operation ir.LogicalOperator, children []ir.Condition) (ir.Condition, error) {
	if operation != ir.LogicalNot {
		if len(children) == 0 {
			return ir.NewConstant(model, operation == ir.LogicalAnd)
		}
		if len(children) == 1 {
			return normalize.Condition(children[0])
		}
	}
	condition, err := ir.NewLogical(model, operation, children)
	if err != nil {
		return ir.Condition{}, err
	}
	return normalize.Condition(condition)
}

func negate(condition ir.Condition) (ir.Condition, error) {
	return logical(condition.ModelID(), ir.LogicalNot, []ir.Condition{condition})
}

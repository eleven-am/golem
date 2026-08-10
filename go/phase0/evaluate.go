package phase0

import (
	"fmt"
	"reflect"
)

// Record is a deliberately small provider-neutral row representation for the
// Phase 0 evaluator. It is not a proposed production persistence model.
type Record struct {
	Fields    map[string]any      `json:"fields"`
	Relations map[string][]Record `json:"relations,omitempty"`
}

// Evaluate is the first interpreter for the policy AST. A later SQL compiler
// must agree with it for every supported SQLite and PostgreSQL operator.
func Evaluate[M any](predicate Predicate[M], record Record) (bool, error) {
	return evaluateNode(normalizeNode(predicate.node), record)
}

func evaluateNode(current node, record Record) (bool, error) {
	switch current.Operator {
	case OpAll:
		return true, nil
	case OpNone:
		return false, nil
	case OpEqual, OpNotEqual, OpIn:
		if current.Field == nil {
			return false, fmt.Errorf("%s predicate has no field", current.Operator)
		}
		value, present := record.Fields[current.Field.Name]
		if !present {
			return false, nil
		}
		switch current.Operator {
		case OpEqual:
			return reflect.DeepEqual(value, current.Value), nil
		case OpNotEqual:
			return !reflect.DeepEqual(value, current.Value), nil
		case OpIn:
			operands, ok := current.Value.([]any)
			if !ok {
				return false, fmt.Errorf("in predicate has invalid operands %T", current.Value)
			}
			for _, operand := range operands {
				if reflect.DeepEqual(value, operand) {
					return true, nil
				}
			}
			return false, nil
		}
	case OpAnd:
		for _, child := range current.Children {
			matches, err := evaluateNode(child, record)
			if err != nil || !matches {
				return false, err
			}
		}
		return true, nil
	case OpOr:
		for _, child := range current.Children {
			matches, err := evaluateNode(child, record)
			if err != nil {
				return false, err
			}
			if matches {
				return true, nil
			}
		}
		return false, nil
	case OpNot:
		if len(current.Children) != 1 {
			return false, fmt.Errorf("not predicate requires exactly one child")
		}
		matches, err := evaluateNode(current.Children[0], record)
		return !matches, err
	case OpRelationIs, OpRelationIsNot, OpRelationSome, OpRelationEvery, OpRelationNone:
		return evaluateRelation(current, record)
	}
	return false, fmt.Errorf("unsupported predicate operator %q", current.Operator)
}

func evaluateRelation(current node, record Record) (bool, error) {
	if current.Relation == nil || len(current.Children) != 1 {
		return false, fmt.Errorf("%s predicate requires one relation and one child", current.Operator)
	}
	related := record.Relations[current.Relation.Name]
	child := current.Children[0]

	switch current.Operator {
	case OpRelationIs:
		if len(related) > 1 {
			return false, fmt.Errorf("to-one relation %s contains %d rows", current.Relation.Name, len(related))
		}
		if len(related) == 0 {
			return false, nil
		}
		return evaluateNode(child, related[0])
	case OpRelationIsNot:
		if len(related) > 1 {
			return false, fmt.Errorf("to-one relation %s contains %d rows", current.Relation.Name, len(related))
		}
		if len(related) == 0 {
			return true, nil
		}
		matches, err := evaluateNode(child, related[0])
		return !matches, err
	case OpRelationSome:
		for _, row := range related {
			matches, err := evaluateNode(child, row)
			if err != nil {
				return false, err
			}
			if matches {
				return true, nil
			}
		}
		return false, nil
	case OpRelationEvery:
		for _, row := range related {
			matches, err := evaluateNode(child, row)
			if err != nil || !matches {
				return false, err
			}
		}
		return true, nil
	case OpRelationNone:
		for _, row := range related {
			matches, err := evaluateNode(child, row)
			if err != nil {
				return false, err
			}
			if matches {
				return false, nil
			}
		}
		return true, nil
	}
	return false, fmt.Errorf("unsupported relation operator %q", current.Operator)
}

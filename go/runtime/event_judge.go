package runtime

import (
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type deleteTruth uint8

const (
	deleteUnknown deleteTruth = iota
	deleteFalse
	deleteTrue
)

func deleteTruthOf(value bool) deleteTruth {
	if value {
		return deleteTrue
	}
	return deleteFalse
}

func (truth deleteTruth) not() deleteTruth {
	switch truth {
	case deleteTrue:
		return deleteFalse
	case deleteFalse:
		return deleteTrue
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) truth(condition policyir.Condition) deleteTruth {
	return evaluator.truthAt(condition, true)
}

func (evaluator *deleteEvaluator) truthAt(condition policyir.Condition, positive bool) deleteTruth {
	if condition.ModelID() != evaluator.model {
		return deleteUnknown
	}
	switch condition.Kind() {
	case policyir.ConditionConstant:
		value, ok := condition.Constant()
		if !ok {
			return deleteUnknown
		}
		return deleteTruthOf(value)
	case policyir.ConditionLogical:
		return evaluator.logical(condition, positive)
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		return evaluator.leaf(condition)
	case policyir.ConditionRelation:
		return evaluator.relation(condition, positive)
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) logical(condition policyir.Condition, positive bool) deleteTruth {
	op, children, ok := condition.Logical()
	if !ok || len(children) == 0 {
		return deleteUnknown
	}
	switch op {
	case policyir.LogicalNot:
		if len(children) != 1 {
			return deleteUnknown
		}
		return evaluator.truthAt(children[0], !positive).not()
	case policyir.LogicalAnd:
		result := deleteTrue
		for _, child := range children {
			switch evaluator.truthAt(child, positive) {
			case deleteFalse:
				return deleteFalse
			case deleteUnknown:
				result = deleteUnknown
			}
		}
		return result
	case policyir.LogicalOr:
		result := deleteFalse
		for _, child := range children {
			switch evaluator.truthAt(child, positive) {
			case deleteTrue:
				return deleteTrue
			case deleteUnknown:
				result = deleteUnknown
			}
		}
		return result
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) leaf(condition policyir.Condition) deleteTruth {
	field, ok := condition.Field()
	if !ok {
		return deleteUnknown
	}
	if _, present := evaluator.cells[field]; !present {
		return deleteUnknown
	}
	if evaluator.shared.fromRecord && !judgeKindsProven(condition) {
		return deleteUnknown
	}
	value, err := evaluate.Condition(condition, evaluator.record, evaluator.shared.providers)
	if err != nil {
		return deleteUnknown
	}
	return deleteTruthOf(value)
}

func (node *deleteEvaluator) fromSnapshot(operator policyir.OperatorID, child *policyir.Condition, positive bool) deleteTruth {
	switch operator {
	case policyir.OperatorRelationIsNotNull:
		return deleteTrue
	case policyir.OperatorRelationIsNull:
		return deleteFalse
	case policyir.OperatorRelationIs:
		if child == nil {
			return deleteTrue
		}
		return node.truthAt(*child, positive)
	case policyir.OperatorRelationIsNot:
		if child == nil {
			return deleteFalse
		}
		return node.truthAt(*child, !positive).not()
	default:
		return deleteUnknown
	}
}

func (evaluator *deleteEvaluator) relationFromRecord(condition policyir.Condition) deleteTruth {
	_, _, _, cardinality, _, ok := condition.Relation()
	if !ok || cardinality != policyir.RelationToOne || !judgeKindsProven(condition) {
		return deleteUnknown
	}
	value, err := evaluate.Condition(condition, evaluator.record, evaluator.shared.providers)
	if err != nil {
		return deleteUnknown
	}
	return deleteTruthOf(value)
}

var judgeProvenKinds = map[policyir.ValueKind]bool{
	policyir.ValueBool:     true,
	policyir.ValueInt32:    true,
	policyir.ValueInt64:    true,
	policyir.ValueDecimal:  true,
	policyir.ValueDateTime: true,
	policyir.ValueEnum:     true,
	policyir.ValueUUID:     true,
	policyir.ValueString:   true,
}

func judgeKindsProven(condition policyir.Condition) bool {
	switch condition.Kind() {
	case policyir.ConditionConstant:
		return true
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if !judgeKindsProven(child) {
				return false
			}
		}
		return true
	case policyir.ConditionScalar:
		typ, ok := condition.FieldType()
		return ok && judgeProvenKinds[typ.Kind()]
	case policyir.ConditionRelation:
		_, _, _, _, child, _ := condition.Relation()
		return child == nil || judgeKindsProven(*child)
	default:
		return false
	}
}

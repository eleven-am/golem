package evaluate

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

// Condition evaluates one validated policy condition against one fully loaded
// immutable record. It deliberately does not call operator.RequireAgreement:
// this evaluator is one side of the P2-H agreement proof. Runtime activation
// applies that separate gate before an operation is planned.
func Condition(condition ir.Condition, record Record, providers ir.ProviderSet) (bool, error) {
	if err := operator.ValidateCondition(condition, providers); err != nil {
		return false, &Error{Code: CodeOperator, ModelID: condition.ModelID(), Detail: "condition validation failed", Cause: err}
	}
	if record.model != condition.ModelID() {
		return false, &Error{Code: CodeModel, ModelID: record.model, Detail: "record and condition root models differ"}
	}
	plan, err := dependency.Collect(condition)
	if err != nil {
		return false, &Error{Code: CodeInternal, ModelID: record.model, Detail: "dependency collection failed", Cause: err}
	}
	if err := record.checkDependencies(plan.Dependencies()); err != nil {
		return false, err
	}
	if err := validateLoadedShape(condition, record); err != nil {
		return false, err
	}
	return evaluate(condition, record)
}

// validateLoadedShape walks every static branch before boolean evaluation.
// This prevents an early true/false branch from hiding a malformed loaded
// dependency. Missing-state validation is performed by the dependency plan;
// this pass proves the loaded value/relation family and endpoint context.
func validateLoadedShape(condition ir.Condition, record Record) error {
	switch condition.Kind() {
	case ir.ConditionConstant:
		return nil
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := validateLoadedShape(child, record); err != nil {
				return err
			}
		}
		return nil
	case ir.ConditionScalar:
		fieldID, _ := condition.Field()
		field := record.fields[fieldID]
		typ, _ := condition.FieldType()
		operatorID, _ := condition.Operator()
		if field.kind == fieldNull {
			return nil
		}
		if field.kind != fieldValue || field.value.Kind() != typ.Kind() {
			return typeFailure(record.model, fieldID, operatorID, "loaded scalar value does not match the condition field type")
		}
		return nil
	case ir.ConditionList:
		fieldID, _ := condition.Field()
		field := record.fields[fieldID]
		operatorID, _ := condition.Operator()
		if field.kind == fieldNull {
			return nil
		}
		if _, ok := loadedList(field); !ok {
			return typeFailure(record.model, fieldID, operatorID, "loaded scalar-list field has an incompatible value")
		}
		return nil
	case ir.ConditionJSON:
		fieldID, _ := condition.Field()
		field := record.fields[fieldID]
		operatorID, _ := condition.Operator()
		if field.kind == fieldNull {
			return nil
		}
		if field.kind != fieldValue || field.value.Kind() != ir.ValueJSON {
			return typeFailure(record.model, fieldID, operatorID, "loaded JSON field has an incompatible value")
		}
		return nil
	case ir.ConditionRelation:
		fieldID, _, target, cardinality, child, _ := condition.Relation()
		field := record.fields[fieldID]
		operatorID, _ := condition.Operator()
		if field.kind != fieldRelation || field.target != target || field.cardinality != cardinality {
			return typeFailure(record.model, fieldID, operatorID, "loaded relation does not match the condition endpoint")
		}
		if child != nil {
			for index := range field.rows {
				if err := validateLoadedShape(*child, field.rows[index]); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return &Error{Code: CodeInternal, ModelID: record.model, Detail: fmt.Sprintf("unknown condition kind %d", condition.Kind())}
	}
}

func evaluate(condition ir.Condition, record Record) (bool, error) {
	switch condition.Kind() {
	case ir.ConditionConstant:
		truth, _ := condition.Constant()
		return truth, nil
	case ir.ConditionLogical:
		return evaluateLogical(condition, record)
	case ir.ConditionScalar:
		return evaluateScalar(condition, record)
	case ir.ConditionList:
		return evaluateList(condition, record)
	case ir.ConditionJSON:
		return evaluateJSON(condition, record)
	case ir.ConditionRelation:
		return evaluateRelation(condition, record)
	default:
		return false, &Error{Code: CodeInternal, ModelID: record.model, Detail: fmt.Sprintf("unknown condition kind %d", condition.Kind())}
	}
}

func evaluateLogical(condition ir.Condition, record Record) (bool, error) {
	logical, children, _ := condition.Logical()
	switch logical {
	case ir.LogicalAnd:
		for _, child := range children {
			matched, err := evaluate(child, record)
			if err != nil || !matched {
				return matched, err
			}
		}
		return true, nil
	case ir.LogicalOr:
		for _, child := range children {
			matched, err := evaluate(child, record)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	case ir.LogicalNot:
		matched, err := evaluate(children[0], record)
		return !matched, err
	default:
		return false, &Error{Code: CodeInternal, ModelID: record.model, Detail: fmt.Sprintf("unknown logical operator %d", logical)}
	}
}

func evaluateScalar(condition ir.Condition, record Record) (bool, error) {
	fieldID, _ := condition.Field()
	field := record.fields[fieldID]
	operatorID, _ := condition.Operator()
	typ, _ := condition.FieldType()

	if operatorID == ir.OperatorIsNull {
		return field.kind == fieldNull, nil
	}
	if operatorID == ir.OperatorIsNotNull {
		return field.kind != fieldNull, nil
	}
	if field.kind == fieldNull {
		switch operatorID {
		case ir.OperatorNotEqual, ir.OperatorNotIn:
			return true, nil
		default:
			return false, nil
		}
	}
	if field.kind != fieldValue || field.value.Kind() != typ.Kind() {
		return false, typeFailure(record.model, fieldID, operatorID, "loaded scalar value does not match the condition field type")
	}
	operand, _ := condition.Operand()
	mode, _ := condition.Mode()
	return scalarOperator(operatorID, mode, field.value, operand), nil
}

func evaluateList(condition ir.Condition, record Record) (bool, error) {
	fieldID, _ := condition.Field()
	field := record.fields[fieldID]
	operatorID, _ := condition.Operator()
	if operatorID == ir.OperatorListIsNull {
		return field.kind == fieldNull, nil
	}
	if operatorID == ir.OperatorListIsNotNull {
		return field.kind != fieldNull, nil
	}
	if field.kind == fieldNull {
		return false, nil
	}
	elements, ok := loadedList(field)
	if !ok {
		return false, typeFailure(record.model, fieldID, operatorID, "loaded scalar-list field has an incompatible value")
	}
	operand, _ := condition.Operand()
	switch operatorID {
	case ir.OperatorListEqual:
		wanted, _ := operand.One()
		values, _ := wanted.List()
		if len(elements) != len(values) {
			return false, nil
		}
		for index := range elements {
			if !elements[index].valid || !valuesEqual(elements[index].value, values[index]) {
				return false, nil
			}
		}
		return true, nil
	case ir.OperatorListHas:
		wanted, _ := operand.One()
		return listHas(elements, wanted), nil
	case ir.OperatorListHasEvery:
		wanted, _ := operand.Many()
		for _, value := range wanted {
			if !listHas(elements, value) {
				return false, nil
			}
		}
		return true, nil
	case ir.OperatorListHasSome:
		wanted, _ := operand.Many()
		for _, value := range wanted {
			if listHas(elements, value) {
				return true, nil
			}
		}
		return false, nil
	case ir.OperatorListIsEmpty:
		wantEmpty, _ := operand.Flag()
		return (len(elements) == 0) == wantEmpty, nil
	default:
		return false, operatorFailure(record.model, fieldID, operatorID, "unsupported list operator")
	}
}

func loadedList(field Field) ([]listElement, bool) {
	if field.kind == fieldList {
		return append([]listElement(nil), field.list...), true
	}
	if field.kind != fieldValue || field.value.Kind() != ir.ValueScalarList {
		return nil, false
	}
	values, _ := field.value.List()
	result := make([]listElement, len(values))
	for index := range values {
		result[index] = listElement{valid: true, value: values[index]}
	}
	return result, true
}

func listHas(elements []listElement, wanted ir.Value) bool {
	for _, element := range elements {
		if element.valid && valuesEqual(element.value, wanted) {
			return true
		}
	}
	return false
}

func evaluateRelation(condition ir.Condition, record Record) (bool, error) {
	fieldID, _, target, cardinality, child, _ := condition.Relation()
	operatorID, _ := condition.Operator()
	field := record.fields[fieldID]
	if field.kind != fieldRelation || field.target != target || field.cardinality != cardinality {
		return false, typeFailure(record.model, fieldID, operatorID, "loaded relation does not match the condition endpoint")
	}
	rows := field.rows
	switch operatorID {
	case ir.OperatorRelationIsNull:
		return len(rows) == 0, nil
	case ir.OperatorRelationIsNotNull:
		return len(rows) == 1, nil
	case ir.OperatorRelationIs, ir.OperatorRelationIsNot:
		matched := false
		if len(rows) == 1 {
			var err error
			matched, err = evaluate(*child, rows[0])
			if err != nil {
				return false, err
			}
		}
		if operatorID == ir.OperatorRelationIsNot {
			matched = !matched
		}
		return matched, nil
	case ir.OperatorRelationSome:
		for index := range rows {
			matched, err := evaluate(*child, rows[index])
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	case ir.OperatorRelationEvery:
		for index := range rows {
			matched, err := evaluate(*child, rows[index])
			if err != nil || !matched {
				return matched, err
			}
		}
		return true, nil
	case ir.OperatorRelationNone:
		for index := range rows {
			matched, err := evaluate(*child, rows[index])
			if err != nil {
				return false, err
			}
			if matched {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, operatorFailure(record.model, fieldID, operatorID, "unsupported relation operator")
	}
}

func typeFailure(model ir.ModelID, field ir.FieldID, operatorID ir.OperatorID, detail string) error {
	return &Error{Code: CodeType, ModelID: model, FieldID: field, OperatorID: operatorID, Detail: detail}
}

func operatorFailure(model ir.ModelID, field ir.FieldID, operatorID ir.OperatorID, detail string) error {
	return &Error{Code: CodeOperator, ModelID: model, FieldID: field, OperatorID: operatorID, Detail: detail}
}

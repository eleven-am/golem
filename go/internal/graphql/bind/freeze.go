package bind

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// FreezePredicate converts an already-bound P2 condition into the frozen
// public predicate boundary shared by P3/P4 runtime adapters. Scalar parsing,
// enum GraphQL-name resolution, provider/operator validation, and resource
// limits have already run in Binder; this function preserves their exact value
// and stable-identity result without reinterpreting public names.
func (b *Binder) FreezePredicate(condition policyir.Condition) (golem.FrozenPredicate, error) {
	if b == nil {
		return golem.FrozenPredicate{}, fmt.Errorf("P5_BIND_FREEZE: binder is absent")
	}
	node, err := b.frozenCondition(condition)
	if err != nil {
		return golem.FrozenPredicate{}, err
	}
	return golem.RuntimeFreezePredicate(golem.ModelID(condition.ModelID()), node)
}

func (b *Binder) frozenCondition(condition policyir.Condition) (golem.RuntimePredicateNode, error) {
	switch condition.Kind() {
	case policyir.ConditionConstant:
		truth, ok := condition.Constant()
		if !ok {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed constant")
		}
		return golem.RuntimePredicateNode{Kind: golem.FrozenConditionConstant, Truth: truth, Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandFlag, Flag: truth}}, nil
	case policyir.ConditionLogical:
		logical, children, ok := condition.Logical()
		if !ok {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed logical node")
		}
		operator := map[policyir.LogicalOperator]golem.FrozenOperator{policyir.LogicalAnd: golem.FrozenOperatorAnd, policyir.LogicalOr: golem.FrozenOperatorOr, policyir.LogicalNot: golem.FrozenOperatorNot}[logical]
		if operator == 0 {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: unknown logical operator %d", logical)
		}
		node := golem.RuntimePredicateNode{Kind: golem.FrozenConditionLogical, Operator: operator, Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}, Children: make([]golem.RuntimePredicateNode, len(children))}
		for index, child := range children {
			var err error
			node.Children[index], err = b.frozenCondition(child)
			if err != nil {
				return golem.RuntimePredicateNode{}, err
			}
		}
		return node, nil
	case policyir.ConditionRelation:
		field, relation, _, _, child, ok := condition.Relation()
		operatorID, operatorOK := condition.Operator()
		if !ok || !operatorOK {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed relation node")
		}
		operator, err := frozenOperator(operatorID)
		if err != nil {
			return golem.RuntimePredicateNode{}, err
		}
		node := golem.RuntimePredicateNode{Kind: golem.FrozenConditionRelation, Operator: operator, Field: golem.FieldID(field), Relation: golem.RelationID(relation), Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}}
		if child != nil {
			converted, childErr := b.frozenCondition(*child)
			if childErr != nil {
				return golem.RuntimePredicateNode{}, childErr
			}
			node.Children = []golem.RuntimePredicateNode{converted}
		}
		return node, nil
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		field, fieldOK := condition.Field()
		operatorID, operatorOK := condition.Operator()
		operand, operandOK := condition.Operand()
		if !fieldOK || !operatorOK || !operandOK {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed field node")
		}
		operator, err := frozenOperator(operatorID)
		if err != nil {
			return golem.RuntimePredicateNode{}, err
		}
		converted, err := b.frozenOperand(operand)
		if err != nil {
			return golem.RuntimePredicateNode{}, err
		}
		kind := map[policyir.ConditionKind]golem.FrozenConditionKind{policyir.ConditionScalar: golem.FrozenConditionScalar, policyir.ConditionList: golem.FrozenConditionList, policyir.ConditionJSON: golem.FrozenConditionJSON}[condition.Kind()]
		var mode golem.FrozenComparisonMode
		if condition.Kind() == policyir.ConditionScalar || condition.Kind() == policyir.ConditionJSON {
			mode = golem.FrozenComparisonSensitive
			if value, ok := condition.Mode(); ok && value == policyir.ComparisonASCIIInsensitive {
				mode = golem.FrozenComparisonASCIIInsensitive
			}
		}
		node := golem.RuntimePredicateNode{Kind: kind, Operator: operator, Mode: mode, Field: golem.FieldID(field), Operand: converted}
		if condition.Kind() == policyir.ConditionJSON {
			path, ok := condition.Path()
			if ok {
				segments := path.Segments()
				public := make([]golem.JSONPathSegment, len(segments))
				for index, segment := range segments {
					if key, isKey := segment.Key(); isKey {
						public[index] = golem.JSONKey(key)
					} else if arrayIndex, isIndex := segment.Index(); isIndex && arrayIndex <= uint64(^uint32(0)) {
						public[index] = golem.JSONIndex(uint32(arrayIndex))
					} else {
						return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: JSON path segment is invalid")
					}
				}
				if len(public) != 0 {
					node.Path = golem.NewJSONPath(public[0], public[1:]...)
				} else if operatorID != policyir.OperatorJSONIsNull && operatorID != policyir.OperatorJSONIsNotNull {
					// P2 represents both the JSON document root and column-level
					// presence with an empty path. P4 distinguishes them: root
					// comparisons carry an explicitly-present empty path, while
					// IsNull/IsNotNull carry no path.
					node.Path = golem.RuntimeJSONRootPath()
				}
			}
		}
		return node, nil
	default:
		return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: unknown condition kind %d", condition.Kind())
	}
}

func (b *Binder) frozenOperand(operand policyir.Operand) (golem.RuntimePredicateOperand, error) {
	switch operand.Kind() {
	case policyir.OperandNone:
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}, nil
	case policyir.OperandOne:
		value, _ := operand.One()
		if value.Kind() == policyir.ValueScalarList {
			items, _ := value.List()
			values := make([]golem.RuntimePredicateValue, len(items))
			for index, item := range items {
				var err error
				values[index], err = b.frozenValue(item)
				if err != nil {
					return golem.RuntimePredicateOperand{}, err
				}
			}
			return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandMany, Many: values}, nil
		}
		valueOut, err := b.frozenValue(value)
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandOne, One: valueOut}, err
	case policyir.OperandMany:
		items, _ := operand.Many()
		values := make([]golem.RuntimePredicateValue, len(items))
		for index, item := range items {
			var err error
			values[index], err = b.frozenValue(item)
			if err != nil {
				return golem.RuntimePredicateOperand{}, err
			}
		}
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandMany, Many: values}, nil
	case policyir.OperandFlag:
		flag, _ := operand.Flag()
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandFlag, Flag: flag}, nil
	case policyir.OperandJSONNull:
		value, _ := operand.JSONNull()
		kind := map[policyir.JSONNullKind]golem.FrozenJSONNullKind{policyir.JSONDbNull: golem.FrozenJSONDbNull, policyir.JSONDocumentNull: golem.FrozenJSONDocumentNull, policyir.JSONAnyNull: golem.FrozenJSONAnyNull}[value]
		if kind == 0 {
			return golem.RuntimePredicateOperand{}, fmt.Errorf("P5_BIND_FREEZE: unknown JSON null kind")
		}
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandJSONNull, JSONNull: kind}, nil
	default:
		return golem.RuntimePredicateOperand{}, fmt.Errorf("P5_BIND_FREEZE: unknown operand kind %d", operand.Kind())
	}
}

func (b *Binder) frozenValue(value policyir.Value) (golem.RuntimePredicateValue, error) {
	switch value.Kind() {
	case policyir.ValueBool:
		v, _ := value.Bool()
		return runtimeValue(golem.FrozenValueBool, v), nil
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		v, _ := value.Signed()
		if value.Kind() == policyir.ValueInt16 {
			return runtimeValue(golem.FrozenValueInt16, int16(v)), nil
		}
		if value.Kind() == policyir.ValueInt32 {
			return runtimeValue(golem.FrozenValueInt32, int32(v)), nil
		}
		return runtimeValue(golem.FrozenValueInt64, v), nil
	case policyir.ValueFloat32:
		bits, _ := value.Float32Bits()
		return runtimeValue(golem.FrozenValueFloat32, math.Float32frombits(bits)), nil
	case policyir.ValueFloat64:
		bits, _ := value.Float64Bits()
		return runtimeValue(golem.FrozenValueFloat64, math.Float64frombits(bits)), nil
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		decimal, err := golem.NewDecimal(coefficient, scale)
		return runtimeValue(golem.FrozenValueDecimal, decimal), err
	case policyir.ValueString:
		text, _ := value.Text()
		return runtimeValue(golem.FrozenValueString, text), nil
	case policyir.ValueBytes:
		bytes, _ := value.Bytes()
		return runtimeValue(golem.FrozenValueBytes, bytes), nil
	case policyir.ValueUUID:
		uuid, _ := value.UUID()
		return runtimeValue(golem.FrozenValueUUID, golem.NewUUID(uuid)), nil
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		date, err := golem.NewDate(int(year), time.Month(month), int(day))
		return runtimeValue(golem.FrozenValueDate, date), err
	case policyir.ValueTime:
		microseconds, _ := value.Time()
		hour := microseconds / 3_600_000_000
		remainder := microseconds % 3_600_000_000
		minute := remainder / 60_000_000
		remainder %= 60_000_000
		second := remainder / 1_000_000
		clock, err := golem.NewTime(int(hour), int(minute), int(second), int(remainder%1_000_000))
		return runtimeValue(golem.FrozenValueTime, clock), err
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return runtimeValue(golem.FrozenValueDateTime, time.Unix(seconds, int64(nanos)).UTC()), nil
	case policyir.ValueEnum:
		enum, member, _ := value.Enum()
		label, ok := b.enumLabels[enumMember{enum: enum, value: member}]
		if !ok {
			return golem.RuntimePredicateValue{}, fmt.Errorf("P5_BIND_FREEZE: enum member has no wire label")
		}
		return runtimeValue(golem.FrozenValueString, label), nil
	case policyir.ValueJSON:
		json, _ := value.JSON()
		public, err := publicJSON(json)
		return runtimeValue(golem.FrozenValueJSON, public), err
	default:
		return golem.RuntimePredicateValue{}, fmt.Errorf("P5_BIND_FREEZE: unsupported value kind %d", value.Kind())
	}
}

func runtimeValue(kind golem.FrozenValueKind, value any) golem.RuntimePredicateValue {
	return golem.RuntimePredicateValue{Kind: kind, Value: value}
}

func frozenOperator(value policyir.OperatorID) (golem.FrozenOperator, error) {
	operators := map[policyir.OperatorID]golem.FrozenOperator{
		policyir.OperatorEqual: golem.FrozenOperatorEq, policyir.OperatorNotEqual: golem.FrozenOperatorNe, policyir.OperatorIn: golem.FrozenOperatorIn, policyir.OperatorNotIn: golem.FrozenOperatorNotIn,
		policyir.OperatorLessThan: golem.FrozenOperatorLT, policyir.OperatorLessThanOrEqual: golem.FrozenOperatorLTE, policyir.OperatorGreaterThan: golem.FrozenOperatorGT, policyir.OperatorGreaterThanOrEqual: golem.FrozenOperatorGTE,
		policyir.OperatorContains: golem.FrozenOperatorContains, policyir.OperatorStartsWith: golem.FrozenOperatorStartsWith, policyir.OperatorEndsWith: golem.FrozenOperatorEndsWith,
		policyir.OperatorIsNull: golem.FrozenOperatorIsNull, policyir.OperatorIsNotNull: golem.FrozenOperatorIsNotNull,
		policyir.OperatorListEqual: golem.FrozenOperatorListEq, policyir.OperatorListHas: golem.FrozenOperatorListHas, policyir.OperatorListHasEvery: golem.FrozenOperatorListHasEvery,
		policyir.OperatorListHasSome: golem.FrozenOperatorListHasSome, policyir.OperatorListIsEmpty: golem.FrozenOperatorListIsEmpty,
		policyir.OperatorListIsNull: golem.FrozenOperatorIsNull, policyir.OperatorListIsNotNull: golem.FrozenOperatorIsNotNull,
		policyir.OperatorJSONIsNull: golem.FrozenOperatorIsNull, policyir.OperatorJSONIsNotNull: golem.FrozenOperatorIsNotNull,
		policyir.OperatorJSONEqual: golem.FrozenOperatorJSONEq, policyir.OperatorJSONNotEqual: golem.FrozenOperatorJSONNe, policyir.OperatorJSONLessThan: golem.FrozenOperatorJSONLT,
		policyir.OperatorJSONLessThanOrEqual: golem.FrozenOperatorJSONLTE, policyir.OperatorJSONGreaterThan: golem.FrozenOperatorJSONGT, policyir.OperatorJSONGreaterThanOrEqual: golem.FrozenOperatorJSONGTE,
		policyir.OperatorJSONStringContains: golem.FrozenOperatorJSONStringContains, policyir.OperatorJSONStringStartsWith: golem.FrozenOperatorJSONStringStartsWith,
		policyir.OperatorJSONStringEndsWith: golem.FrozenOperatorJSONStringEndsWith, policyir.OperatorJSONArrayContains: golem.FrozenOperatorJSONArrayContains,
		policyir.OperatorJSONArrayStartsWith: golem.FrozenOperatorJSONArrayStartsWith, policyir.OperatorJSONArrayEndsWith: golem.FrozenOperatorJSONArrayEndsWith,
		policyir.OperatorRelationIs: golem.FrozenOperatorRelationIs, policyir.OperatorRelationIsNot: golem.FrozenOperatorRelationIsNot,
		policyir.OperatorRelationIsNull: golem.FrozenOperatorRelationIsNull, policyir.OperatorRelationIsNotNull: golem.FrozenOperatorRelationIsNotNull,
		policyir.OperatorRelationSome: golem.FrozenOperatorRelationSome, policyir.OperatorRelationEvery: golem.FrozenOperatorRelationEvery, policyir.OperatorRelationNone: golem.FrozenOperatorRelationNone,
	}
	operator, ok := operators[value]
	if !ok {
		return 0, fmt.Errorf("P5_BIND_FREEZE: unsupported operator %d", value)
	}
	return operator, nil
}

func publicJSON(value policyir.JSONValue) (golem.JSONValue, error) {
	switch value.Kind() {
	case policyir.JSONNull:
		return golem.JSONNull, nil
	case policyir.JSONBool:
		boolean, _ := value.Bool()
		return golem.JSONBool(boolean), nil
	case policyir.JSONNumber:
		number, _ := value.Number()
		text := string(number.Coefficient())
		if number.Negative() {
			text = "-" + text
		}
		if number.Exponent() != 0 {
			text += "e" + strconv.FormatInt(int64(number.Exponent()), 10)
		}
		return golem.ParseJSONNumber(text)
	case policyir.JSONString:
		text, _ := value.Text()
		return golem.JSONString(text), nil
	case policyir.JSONArray:
		items, _ := value.Array()
		public := make([]golem.JSONValue, len(items))
		for index, item := range items {
			var err error
			public[index], err = publicJSON(item)
			if err != nil {
				return nil, err
			}
		}
		return golem.JSONArray(public...), nil
	case policyir.JSONObject:
		members, _ := value.Object()
		public := make(map[string]golem.JSONValue, len(members))
		for _, member := range members {
			converted, err := publicJSON(member.Value())
			if err != nil {
				return nil, err
			}
			public[member.Key()] = converted
		}
		return golem.JSONObject(public), nil
	default:
		return nil, fmt.Errorf("P5_BIND_FREEZE: unknown JSON kind %d", value.Kind())
	}
}

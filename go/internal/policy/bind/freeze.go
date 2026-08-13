package bind

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// EnumLabels resolves a bound enum member back to the authored label that
// Predicate accepts on the way in. It is the exact inverse of the label lookup
// in schema.Registry.EnumValue, and a caller that resolves labels from another
// vocabulary produces predicates this package cannot bind again.
type EnumLabels func(enum ir.EnumID, member ir.EnumValueID) (string, bool)

// RegistryEnumLabels resolves enum labels from a validated schema registry. It
// is the label source for any caller that already holds the registry a bound
// condition was validated against.
func RegistryEnumLabels(registry *schema.Registry) EnumLabels {
	return func(enum ir.EnumID, member ir.EnumValueID) (string, bool) {
		if registry == nil {
			return "", false
		}
		return registry.EnumLabel(compilerir.EnumID(hex.EncodeToString(enum[:])), compilerir.EnumValueID(hex.EncodeToString(member[:])))
	}
}

// FreezeCondition converts an already-bound condition into the frozen public
// predicate boundary shared by runtime adapters. Scalar parsing,
// provider/operator validation, and resource limits have already run when the
// condition was bound; this function preserves their exact value and
// stable-identity result without reinterpreting public names. Enum members are
// labelled by labels rather than by this package, because the vocabulary
// belongs to the caller's schema rather than to the condition.
func FreezeCondition(model golem.ModelID, condition ir.Condition, labels EnumLabels) (golem.FrozenPredicate, error) {
	if labels == nil {
		return golem.FrozenPredicate{}, fmt.Errorf("P5_BIND_FREEZE: enum label source is absent")
	}
	node, err := freezeCondition(condition, labels)
	if err != nil {
		return golem.FrozenPredicate{}, err
	}
	return golem.RuntimeFreezePredicate(model, node)
}

func freezeCondition(condition ir.Condition, labels EnumLabels) (golem.RuntimePredicateNode, error) {
	switch condition.Kind() {
	case ir.ConditionConstant:
		truth, ok := condition.Constant()
		if !ok {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed constant")
		}
		return golem.RuntimePredicateNode{Kind: golem.FrozenConditionConstant, Truth: truth, Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandFlag, Flag: truth}}, nil
	case ir.ConditionLogical:
		logical, children, ok := condition.Logical()
		if !ok {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: malformed logical node")
		}
		operator := map[ir.LogicalOperator]golem.FrozenOperator{ir.LogicalAnd: golem.FrozenOperatorAnd, ir.LogicalOr: golem.FrozenOperatorOr, ir.LogicalNot: golem.FrozenOperatorNot}[logical]
		if operator == 0 {
			return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: unknown logical operator %d", logical)
		}
		node := golem.RuntimePredicateNode{Kind: golem.FrozenConditionLogical, Operator: operator, Operand: golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}, Children: make([]golem.RuntimePredicateNode, len(children))}
		for index, child := range children {
			var err error
			node.Children[index], err = freezeCondition(child, labels)
			if err != nil {
				return golem.RuntimePredicateNode{}, err
			}
		}
		return node, nil
	case ir.ConditionRelation:
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
			converted, childErr := freezeCondition(*child, labels)
			if childErr != nil {
				return golem.RuntimePredicateNode{}, childErr
			}
			node.Children = []golem.RuntimePredicateNode{converted}
		}
		return node, nil
	case ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
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
		converted, err := freezeOperand(operand, labels)
		if err != nil {
			return golem.RuntimePredicateNode{}, err
		}
		kind := map[ir.ConditionKind]golem.FrozenConditionKind{ir.ConditionScalar: golem.FrozenConditionScalar, ir.ConditionList: golem.FrozenConditionList, ir.ConditionJSON: golem.FrozenConditionJSON}[condition.Kind()]
		var mode golem.FrozenComparisonMode
		if condition.Kind() == ir.ConditionScalar || condition.Kind() == ir.ConditionJSON {
			mode = golem.FrozenComparisonSensitive
			if value, ok := condition.Mode(); ok && value == ir.ComparisonASCIIInsensitive {
				mode = golem.FrozenComparisonASCIIInsensitive
			}
		}
		node := golem.RuntimePredicateNode{Kind: kind, Operator: operator, Mode: mode, Field: golem.FieldID(field), Operand: converted}
		if condition.Kind() == ir.ConditionJSON {
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
				} else if operatorID != ir.OperatorJSONIsNull && operatorID != ir.OperatorJSONIsNotNull {
					node.Path = golem.RuntimeJSONRootPath()
				}
			}
		}
		return node, nil
	default:
		return golem.RuntimePredicateNode{}, fmt.Errorf("P5_BIND_FREEZE: unknown condition kind %d", condition.Kind())
	}
}

func freezeOperand(operand ir.Operand, labels EnumLabels) (golem.RuntimePredicateOperand, error) {
	switch operand.Kind() {
	case ir.OperandNone:
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandNone}, nil
	case ir.OperandOne:
		value, _ := operand.One()
		if value.Kind() == ir.ValueScalarList {
			items, _ := value.List()
			values := make([]golem.RuntimePredicateValue, len(items))
			for index, item := range items {
				var err error
				values[index], err = freezeValue(item, labels)
				if err != nil {
					return golem.RuntimePredicateOperand{}, err
				}
			}
			return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandMany, Many: values}, nil
		}
		valueOut, err := freezeValue(value, labels)
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandOne, One: valueOut}, err
	case ir.OperandMany:
		items, _ := operand.Many()
		values := make([]golem.RuntimePredicateValue, len(items))
		for index, item := range items {
			var err error
			values[index], err = freezeValue(item, labels)
			if err != nil {
				return golem.RuntimePredicateOperand{}, err
			}
		}
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandMany, Many: values}, nil
	case ir.OperandFlag:
		flag, _ := operand.Flag()
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandFlag, Flag: flag}, nil
	case ir.OperandJSONNull:
		value, _ := operand.JSONNull()
		kind := map[ir.JSONNullKind]golem.FrozenJSONNullKind{ir.JSONDbNull: golem.FrozenJSONDbNull, ir.JSONDocumentNull: golem.FrozenJSONDocumentNull, ir.JSONAnyNull: golem.FrozenJSONAnyNull}[value]
		if kind == 0 {
			return golem.RuntimePredicateOperand{}, fmt.Errorf("P5_BIND_FREEZE: unknown JSON null kind")
		}
		return golem.RuntimePredicateOperand{Kind: golem.FrozenOperandJSONNull, JSONNull: kind}, nil
	default:
		return golem.RuntimePredicateOperand{}, fmt.Errorf("P5_BIND_FREEZE: unknown operand kind %d", operand.Kind())
	}
}

func freezeValue(value ir.Value, labels EnumLabels) (golem.RuntimePredicateValue, error) {
	switch value.Kind() {
	case ir.ValueBool:
		v, _ := value.Bool()
		return runtimeValue(golem.FrozenValueBool, v), nil
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		v, _ := value.Signed()
		if value.Kind() == ir.ValueInt16 {
			return runtimeValue(golem.FrozenValueInt16, int16(v)), nil
		}
		if value.Kind() == ir.ValueInt32 {
			return runtimeValue(golem.FrozenValueInt32, int32(v)), nil
		}
		return runtimeValue(golem.FrozenValueInt64, v), nil
	case ir.ValueFloat32:
		bits, _ := value.Float32Bits()
		return runtimeValue(golem.FrozenValueFloat32, math.Float32frombits(bits)), nil
	case ir.ValueFloat64:
		bits, _ := value.Float64Bits()
		return runtimeValue(golem.FrozenValueFloat64, math.Float64frombits(bits)), nil
	case ir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		decimal, err := golem.NewDecimal(coefficient, scale)
		return runtimeValue(golem.FrozenValueDecimal, decimal), err
	case ir.ValueString:
		text, _ := value.Text()
		return runtimeValue(golem.FrozenValueString, text), nil
	case ir.ValueBytes:
		bytes, _ := value.Bytes()
		return runtimeValue(golem.FrozenValueBytes, bytes), nil
	case ir.ValueUUID:
		uuid, _ := value.UUID()
		return runtimeValue(golem.FrozenValueUUID, golem.NewUUID(uuid)), nil
	case ir.ValueDate:
		year, month, day, _ := value.Date()
		date, err := golem.NewDate(int(year), time.Month(month), int(day))
		return runtimeValue(golem.FrozenValueDate, date), err
	case ir.ValueTime:
		microseconds, _ := value.Time()
		hour := microseconds / 3_600_000_000
		remainder := microseconds % 3_600_000_000
		minute := remainder / 60_000_000
		remainder %= 60_000_000
		second := remainder / 1_000_000
		clock, err := golem.NewTime(int(hour), int(minute), int(second), int(remainder%1_000_000))
		return runtimeValue(golem.FrozenValueTime, clock), err
	case ir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return runtimeValue(golem.FrozenValueDateTime, time.Unix(seconds, int64(nanos)).UTC()), nil
	case ir.ValueEnum:
		enum, member, _ := value.Enum()
		label, ok := labels(enum, member)
		if !ok {
			return golem.RuntimePredicateValue{}, fmt.Errorf("P5_BIND_FREEZE: enum member has no wire label")
		}
		return runtimeValue(golem.FrozenValueString, label), nil
	case ir.ValueJSON:
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

func frozenOperator(value ir.OperatorID) (golem.FrozenOperator, error) {
	operators := map[ir.OperatorID]golem.FrozenOperator{
		ir.OperatorEqual: golem.FrozenOperatorEq, ir.OperatorNotEqual: golem.FrozenOperatorNe, ir.OperatorIn: golem.FrozenOperatorIn, ir.OperatorNotIn: golem.FrozenOperatorNotIn,
		ir.OperatorLessThan: golem.FrozenOperatorLT, ir.OperatorLessThanOrEqual: golem.FrozenOperatorLTE, ir.OperatorGreaterThan: golem.FrozenOperatorGT, ir.OperatorGreaterThanOrEqual: golem.FrozenOperatorGTE,
		ir.OperatorContains: golem.FrozenOperatorContains, ir.OperatorStartsWith: golem.FrozenOperatorStartsWith, ir.OperatorEndsWith: golem.FrozenOperatorEndsWith,
		ir.OperatorIsNull: golem.FrozenOperatorIsNull, ir.OperatorIsNotNull: golem.FrozenOperatorIsNotNull,
		ir.OperatorListEqual: golem.FrozenOperatorListEq, ir.OperatorListHas: golem.FrozenOperatorListHas, ir.OperatorListHasEvery: golem.FrozenOperatorListHasEvery,
		ir.OperatorListHasSome: golem.FrozenOperatorListHasSome, ir.OperatorListIsEmpty: golem.FrozenOperatorListIsEmpty,
		ir.OperatorListIsNull: golem.FrozenOperatorIsNull, ir.OperatorListIsNotNull: golem.FrozenOperatorIsNotNull,
		ir.OperatorJSONIsNull: golem.FrozenOperatorIsNull, ir.OperatorJSONIsNotNull: golem.FrozenOperatorIsNotNull,
		ir.OperatorJSONEqual: golem.FrozenOperatorJSONEq, ir.OperatorJSONNotEqual: golem.FrozenOperatorJSONNe, ir.OperatorJSONLessThan: golem.FrozenOperatorJSONLT,
		ir.OperatorJSONLessThanOrEqual: golem.FrozenOperatorJSONLTE, ir.OperatorJSONGreaterThan: golem.FrozenOperatorJSONGT, ir.OperatorJSONGreaterThanOrEqual: golem.FrozenOperatorJSONGTE,
		ir.OperatorJSONStringContains: golem.FrozenOperatorJSONStringContains, ir.OperatorJSONStringStartsWith: golem.FrozenOperatorJSONStringStartsWith,
		ir.OperatorJSONStringEndsWith: golem.FrozenOperatorJSONStringEndsWith, ir.OperatorJSONArrayContains: golem.FrozenOperatorJSONArrayContains,
		ir.OperatorJSONArrayStartsWith: golem.FrozenOperatorJSONArrayStartsWith, ir.OperatorJSONArrayEndsWith: golem.FrozenOperatorJSONArrayEndsWith,
		ir.OperatorRelationIs: golem.FrozenOperatorRelationIs, ir.OperatorRelationIsNot: golem.FrozenOperatorRelationIsNot,
		ir.OperatorRelationIsNull: golem.FrozenOperatorRelationIsNull, ir.OperatorRelationIsNotNull: golem.FrozenOperatorRelationIsNotNull,
		ir.OperatorRelationSome: golem.FrozenOperatorRelationSome, ir.OperatorRelationEvery: golem.FrozenOperatorRelationEvery, ir.OperatorRelationNone: golem.FrozenOperatorRelationNone,
	}
	operator, ok := operators[value]
	if !ok {
		return 0, fmt.Errorf("P5_BIND_FREEZE: unsupported operator %d", value)
	}
	return operator, nil
}

func publicJSON(value ir.JSONValue) (golem.JSONValue, error) {
	switch value.Kind() {
	case ir.JSONNull:
		return golem.JSONNull, nil
	case ir.JSONBool:
		boolean, _ := value.Bool()
		return golem.JSONBool(boolean), nil
	case ir.JSONNumber:
		number, _ := value.Number()
		text := string(number.Coefficient())
		if number.Negative() {
			text = "-" + text
		}
		if number.Exponent() != 0 {
			text += "e" + strconv.FormatInt(int64(number.Exponent()), 10)
		}
		return golem.ParseJSONNumber(text)
	case ir.JSONString:
		text, _ := value.Text()
		return golem.JSONString(text), nil
	case ir.JSONArray:
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
	case ir.JSONObject:
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

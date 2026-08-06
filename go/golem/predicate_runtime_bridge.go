package golem

import (
	"fmt"
	"math"
	"time"
)

// RuntimePredicateValue is one exact value in the model-erased
// compiler/runtime predicate bridge. Value must carry the ordinary exact Go
// representation identified by Kind.
type RuntimePredicateValue struct {
	Kind  FrozenValueKind
	Value any
}

type RuntimePredicateOperand struct {
	Kind     FrozenOperandKind
	One      RuntimePredicateValue
	Many     []RuntimePredicateValue
	Flag     bool
	JSONNull FrozenJSONNullKind
}

// RuntimePredicateNode is a stable-identity predicate tree used only by
// generated compilers which cannot name an application's generic model type.
// RuntimeFreezePredicate validates it through the same public freeze machinery
// as generated Predicate[M] values.
type RuntimePredicateNode struct {
	Kind     FrozenConditionKind
	Operator FrozenOperator
	Mode     FrozenComparisonMode
	Truth    bool
	Field    FieldID
	Relation RelationID
	Operand  RuntimePredicateOperand
	Path     JSONPath
	Children []RuntimePredicateNode
}

// RuntimeJSONRootPath is the explicit JSON-document root used by generated
// predicate compilers. JSONPath's ordinary zero value means no authored path.
func RuntimeJSONRootPath() JSONPath { return jsonRootPath() }

func RuntimeFreezePredicate(model ModelID, root RuntimePredicateNode) (FrozenPredicate, error) {
	if model == (ModelID{}) {
		return FrozenPredicate{}, fmt.Errorf("runtime predicate: model identity is absent")
	}
	node, err := runtimePredicateNode(root, 0)
	if err != nil {
		return FrozenPredicate{}, err
	}
	return Predicate[struct{}]{node: node}.freezeForModel(model)
}

func runtimePredicateNode(value RuntimePredicateNode, depth int) (*predicateNode, error) {
	if depth > 256 {
		return nil, fmt.Errorf("runtime predicate: depth exceeds 256")
	}
	operand, err := runtimePredicateOperand(value.Operand)
	if err != nil {
		return nil, err
	}
	result := &predicateNode{
		kind: value.Kind, operator: value.Operator, mode: value.Mode, truth: value.Truth,
		field: value.Field, relation: value.Relation, operand: operand, path: cloneJSONPath(value.Path),
		children: make([]*predicateNode, len(value.Children)),
	}
	for index, child := range value.Children {
		result.children[index], err = runtimePredicateNode(child, depth+1)
		if err != nil {
			return nil, fmt.Errorf("runtime predicate child %d: %w", index, err)
		}
	}
	return result, nil
}

func runtimePredicateOperand(value RuntimePredicateOperand) (frozenOperand, error) {
	result := frozenOperand{kind: value.Kind, flag: value.Flag, jsonNull: value.JSONNull}
	var err error
	switch value.Kind {
	case FrozenOperandNone:
	case FrozenOperandOne:
		result.one, err = runtimePredicateValue(value.One)
	case FrozenOperandMany:
		if value.Many == nil {
			return frozenOperand{}, fmt.Errorf("runtime predicate: many operand must distinguish empty from absent")
		}
		result.many = make([]frozenValue, len(value.Many))
		for index, item := range value.Many {
			result.many[index], err = runtimePredicateValue(item)
			if err != nil {
				return frozenOperand{}, fmt.Errorf("runtime predicate operand %d: %w", index, err)
			}
		}
	case FrozenOperandFlag:
	case FrozenOperandJSONNull:
		if value.JSONNull < FrozenJSONDbNull || value.JSONNull > FrozenJSONAnyNull {
			return frozenOperand{}, fmt.Errorf("runtime predicate: JSON null kind is invalid")
		}
	default:
		return frozenOperand{}, fmt.Errorf("runtime predicate: operand kind %d is invalid", value.Kind)
	}
	if err != nil {
		return frozenOperand{}, err
	}
	return result, nil
}

func runtimePredicateValue(value RuntimePredicateValue) (frozenValue, error) {
	result := frozenValue{kind: value.Kind}
	switch value.Kind {
	case FrozenValueBool:
		var ok bool
		result.boolean, ok = value.Value.(bool)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "bool")
		}
	case FrozenValueInt16:
		typed, ok := value.Value.(int16)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "int16")
		}
		result.signed = int64(typed)
	case FrozenValueInt32:
		typed, ok := value.Value.(int32)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "int32")
		}
		result.signed = int64(typed)
	case FrozenValueInt64:
		var ok bool
		result.signed, ok = value.Value.(int64)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "int64")
		}
	case FrozenValueFloat32:
		typed, ok := value.Value.(float32)
		if !ok || math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return frozenValue{}, runtimePredicateType(value, "finite float32")
		}
		result.floatBits = uint64(math.Float32bits(typed))
	case FrozenValueFloat64:
		typed, ok := value.Value.(float64)
		if !ok || math.IsNaN(typed) || math.IsInf(typed, 0) {
			return frozenValue{}, runtimePredicateType(value, "finite float64")
		}
		result.floatBits = math.Float64bits(typed)
	case FrozenValueDecimal:
		var ok bool
		result.decimal, ok = value.Value.(Decimal)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "golem.Decimal")
		}
	case FrozenValueString:
		var ok bool
		result.text, ok = value.Value.(string)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "string")
		}
	case FrozenValueBytes:
		typed, ok := value.Value.([]byte)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "[]byte")
		}
		result.bytes = append([]byte(nil), typed...)
	case FrozenValueUUID:
		var ok bool
		result.uuid, ok = value.Value.(UUID)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "golem.UUID")
		}
	case FrozenValueDate:
		var ok bool
		result.date, ok = value.Value.(Date)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "golem.Date")
		}
	case FrozenValueTime:
		var ok bool
		result.clock, ok = value.Value.(Time)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "golem.Time")
		}
	case FrozenValueDateTime:
		instant, ok := value.Value.(time.Time)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "time.Time")
		}
		instant = instant.UTC()
		result.seconds, result.nanosecond = instant.Unix(), uint32(instant.Nanosecond())
	case FrozenValueJSON:
		typed, ok := value.Value.(JSONValue)
		if !ok {
			return frozenValue{}, runtimePredicateType(value, "golem.JSONValue")
		}
		var copied bool
		result.json, copied = copyJSONValue(typed)
		if !copied {
			return frozenValue{}, fmt.Errorf("runtime predicate: JSON value is invalid")
		}
	default:
		return frozenValue{}, fmt.Errorf("runtime predicate: value kind %d is invalid", value.Kind)
	}
	return result, nil
}

func runtimePredicateType(value RuntimePredicateValue, expected string) error {
	return fmt.Errorf("runtime predicate: value kind %d requires %s, got %T", value.Kind, expected, value.Value)
}

// RuntimeMutationTargetFromPredicate reconstructs a frozen unique target from
// a P5/P4-bound equality predicate without passing through public names again.
// The P4 binder remains responsible for validating the key and exact component
// inventory against the active schema.
func RuntimeMutationTargetFromPredicate(model ModelID, key KeyID, fields []FieldID, predicate FrozenPredicate) (FrozenMutationTarget, error) {
	if model == (ModelID{}) || key == (KeyID{}) || len(fields) == 0 || predicate.View().RootModelID() != model {
		return FrozenMutationTarget{}, fmt.Errorf("runtime mutation target: model, key, fields, and predicate are required")
	}
	seen := make(map[FieldID]struct{}, len(fields))
	for _, field := range fields {
		if field == (FieldID{}) {
			return FrozenMutationTarget{}, fmt.Errorf("runtime mutation target: selector field is absent")
		}
		if _, duplicate := seen[field]; duplicate {
			return FrozenMutationTarget{}, fmt.Errorf("runtime mutation target: selector field is duplicate")
		}
		seen[field] = struct{}{}
	}
	return FrozenMutationTarget{
		selector:          FrozenUniqueSelector{model: model, key: key, fields: append([]FieldID(nil), fields...)},
		selectorPredicate: cloneFrozenPredicate(predicate),
	}, nil
}

package publicvalue

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type EnumWire func(ir.EnumID, ir.EnumValueID) (string, bool)

func Value(value ir.Value, wire EnumWire) (any, error) {
	switch value.Kind() {
	case ir.ValueBool:
		result, _ := value.Bool()
		return result, nil
	case ir.ValueInt16:
		result, _ := value.Signed()
		return int16(result), nil
	case ir.ValueInt32:
		result, _ := value.Signed()
		return int32(result), nil
	case ir.ValueInt64:
		result, _ := value.Signed()
		return result, nil
	case ir.ValueFloat32:
		result, _ := value.Float32Bits()
		return math.Float32frombits(result), nil
	case ir.ValueFloat64:
		result, _ := value.Float64Bits()
		return math.Float64frombits(result), nil
	case ir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		return golem.NewDecimal(coefficient, scale)
	case ir.ValueString:
		result, _ := value.Text()
		return result, nil
	case ir.ValueBytes:
		result, _ := value.Bytes()
		return result, nil
	case ir.ValueUUID:
		result, _ := value.UUID()
		return golem.NewUUID(result), nil
	case ir.ValueDate:
		year, month, day, _ := value.Date()
		return golem.NewDate(int(year), time.Month(month), int(day))
	case ir.ValueTime:
		microseconds, _ := value.Time()
		hour := int(microseconds / 3_600_000_000)
		microseconds %= 3_600_000_000
		minute := int(microseconds / 60_000_000)
		microseconds %= 60_000_000
		second := int(microseconds / 1_000_000)
		return golem.NewTime(hour, minute, second, int(microseconds%1_000_000))
	case ir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return time.Unix(seconds, int64(nanos)).UTC(), nil
	case ir.ValueEnum:
		enum, member, _ := value.Enum()
		label, ok := wire(enum, member)
		if !ok {
			return nil, fmt.Errorf("publicvalue: enum member has no active wire value")
		}
		return label, nil
	case ir.ValueJSON:
		document, _ := value.JSON()
		public, err := JSON(document)
		if err != nil {
			return nil, err
		}
		canonical, err := golem.CanonicalJSON(public)
		if err != nil {
			return nil, err
		}
		return golem.NewJSONDocument[any](canonical)
	default:
		return nil, fmt.Errorf("publicvalue: value kind %d has no scalar public form", value.Kind())
	}
}

func Typed(value ir.Value, typ ir.TypeRef, wire EnumWire) (any, error) {
	if value.Kind() != ir.ValueScalarList {
		return Value(value, wire)
	}
	element, ok := typ.Element()
	if !ok {
		return nil, fmt.Errorf("publicvalue: list value has no element type")
	}
	items, _ := value.List()
	slice := reflect.MakeSlice(reflect.SliceOf(listElementTypes[element.Kind()]), len(items), len(items))
	for index, item := range items {
		public, err := Value(item, wire)
		if err != nil {
			return nil, fmt.Errorf("publicvalue: list element %d: %w", index, err)
		}
		slice.Index(index).Set(reflect.ValueOf(public))
	}
	return slice.Interface(), nil
}

var listElementTypes = map[ir.ValueKind]reflect.Type{
	ir.ValueBool:     reflect.TypeFor[bool](),
	ir.ValueInt16:    reflect.TypeFor[int16](),
	ir.ValueInt32:    reflect.TypeFor[int32](),
	ir.ValueInt64:    reflect.TypeFor[int64](),
	ir.ValueFloat32:  reflect.TypeFor[float32](),
	ir.ValueFloat64:  reflect.TypeFor[float64](),
	ir.ValueDecimal:  reflect.TypeFor[golem.Decimal](),
	ir.ValueString:   reflect.TypeFor[string](),
	ir.ValueUUID:     reflect.TypeFor[golem.UUID](),
	ir.ValueDate:     reflect.TypeFor[golem.Date](),
	ir.ValueTime:     reflect.TypeFor[golem.Time](),
	ir.ValueDateTime: reflect.TypeFor[time.Time](),
	ir.ValueEnum:     reflect.TypeFor[string](),
}

func JSON(value ir.JSONValue) (golem.JSONValue, error) {
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
			public[index], err = JSON(item)
			if err != nil {
				return nil, err
			}
		}
		return golem.JSONArray(public...), nil
	case ir.JSONObject:
		members, _ := value.Object()
		public := make(map[string]golem.JSONValue, len(members))
		for _, member := range members {
			converted, err := JSON(member.Value())
			if err != nil {
				return nil, err
			}
			public[member.Key()] = converted
		}
		return golem.JSONObject(public), nil
	default:
		return nil, fmt.Errorf("publicvalue: unknown JSON kind %d", value.Kind())
	}
}

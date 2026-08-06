package bind

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const listCapability = compilerir.CapabilityID("scalar-list:json-array:v1")

func bindType(logical compilerir.LogicalTypeIR, nullable bool) (policyir.TypeRef, error) {
	kind, ok := map[compilerir.LogicalTypeKind]policyir.ValueKind{
		compilerir.TypeBool: policyir.ValueBool, compilerir.TypeInt16: policyir.ValueInt16,
		compilerir.TypeInt32: policyir.ValueInt32, compilerir.TypeInt64: policyir.ValueInt64,
		compilerir.TypeFloat32: policyir.ValueFloat32, compilerir.TypeFloat64: policyir.ValueFloat64,
		compilerir.TypeDecimal: policyir.ValueDecimal, compilerir.TypeString: policyir.ValueString,
		compilerir.TypeBytes: policyir.ValueBytes, compilerir.TypeUUID: policyir.ValueUUID,
		compilerir.TypeDate: policyir.ValueDate, compilerir.TypeTime: policyir.ValueTime,
		compilerir.TypeDateTime: policyir.ValueDateTime, compilerir.TypeEnum: policyir.ValueEnum,
		compilerir.TypeJSON: policyir.ValueJSON, compilerir.TypeScalarList: policyir.ValueScalarList,
	}[logical.Kind]
	if !ok {
		return policyir.TypeRef{}, fmt.Errorf("P5_BIND_TYPE: unsupported logical type %q", logical.Kind)
	}
	var precision, scale uint16
	if logical.Precision != nil {
		precision = *logical.Precision
	}
	if logical.Scale != nil {
		scale = *logical.Scale
	}
	var enum policyir.EnumID
	if logical.EnumID != nil {
		id, err := fixedID(string(*logical.EnumID))
		if err != nil {
			return policyir.TypeRef{}, err
		}
		enum = policyir.EnumID(id)
	}
	var element *policyir.TypeRef
	if logical.Element != nil {
		value, err := bindType(*logical.Element, false)
		if err != nil {
			return policyir.TypeRef{}, err
		}
		element = &value
	}
	var capability policyir.Capability
	if logical.Capability != nil {
		if *logical.Capability != listCapability {
			return policyir.TypeRef{}, fmt.Errorf("P5_BIND_TYPE: unsupported capability %q", *logical.Capability)
		}
		capability = policyir.CapabilityScalarListJSON
	}
	return policyir.NewTypeRef(kind, nullable, precision, scale, enum, element, capability)
}

func (s *inputState) value(logical compilerir.LogicalTypeIR, raw any) (policyir.Value, error) {
	if raw == nil {
		return policyir.Value{}, fmt.Errorf("null is not a scalar operand")
	}
	switch logical.Kind {
	case compilerir.TypeBool:
		value, ok := raw.(bool)
		if !ok {
			return policyir.Value{}, fmt.Errorf("expected Boolean, got %T", raw)
		}
		return policyir.BoolValue(value), nil
	case compilerir.TypeInt16, compilerir.TypeInt32:
		value, err := integer64(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		kind := policyir.ValueInt16
		if logical.Kind == compilerir.TypeInt32 {
			kind = policyir.ValueInt32
		}
		return policyir.SignedValue(kind, value)
	case compilerir.TypeInt64:
		value, err := graphqlscalar.BigInt(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.SignedValue(policyir.ValueInt64, value)
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		bits := 64
		if logical.Kind == compilerir.TypeFloat32 {
			bits = 32
		}
		value, err := graphqlscalar.Float(raw, bits)
		if err != nil {
			return policyir.Value{}, err
		}
		if bits == 32 {
			return policyir.Float32Value(float32(value))
		}
		return policyir.Float64Value(value)
	case compilerir.TypeDecimal:
		value, err := graphqlscalar.Decimal(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDecimalValue(value.Coefficient(), value.Scale())
	case compilerir.TypeString:
		value, ok := raw.(string)
		if !ok || !utf8.ValidString(value) {
			return policyir.Value{}, fmt.Errorf("expected UTF-8 String, got %T", raw)
		}
		if logical.MaxLength != nil && uint32(utf8.RuneCountInString(value)) > *logical.MaxLength {
			return policyir.Value{}, fmt.Errorf("String exceeds maximum length %d", *logical.MaxLength)
		}
		return policyir.StringValue(value)
	case compilerir.TypeBytes:
		value, err := graphqlscalar.Bytes(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		if logical.MaxLength != nil && uint32(len(value)) > *logical.MaxLength {
			return policyir.Value{}, fmt.Errorf("Bytes exceeds maximum length %d", *logical.MaxLength)
		}
		return policyir.BytesValue(value), nil
	case compilerir.TypeUUID:
		value, err := graphqlscalar.UUID(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.UUIDValue(value.Bytes()), nil
	case compilerir.TypeDate:
		value, err := graphqlscalar.Date(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDateValue(int16(value.Year()), uint8(value.Month()), uint8(value.Day()))
	case compilerir.TypeTime:
		value, err := graphqlscalar.Time(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewTimeValue(value.Microseconds())
	case compilerir.TypeDateTime:
		value, ok := raw.(time.Time)
		var err error
		if !ok {
			value, err = graphqlscalar.DateTime(raw)
			if err != nil {
				return policyir.Value{}, err
			}
		}
		value = value.UTC()
		if value.Year() < 1 || value.Year() > 9999 {
			return policyir.Value{}, fmt.Errorf("DateTime is outside portable year range")
		}
		return policyir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
	case compilerir.TypeEnum:
		name, ok := raw.(string)
		if !ok || logical.EnumID == nil {
			return policyir.Value{}, fmt.Errorf("expected generated enum name")
		}
		enum, ok := s.binder.enums[*logical.EnumID]
		if !ok {
			return policyir.Value{}, fmt.Errorf("enum %s is absent", *logical.EnumID)
		}
		var valueID compilerir.EnumValueID
		for _, value := range enum.Values {
			if value.GraphQLName == name {
				valueID = value.ValueID
				break
			}
		}
		if valueID == "" {
			return policyir.Value{}, fmt.Errorf("unknown enum value %q", name)
		}
		enumID, _ := fixedID(string(*logical.EnumID))
		memberID, _ := fixedID(string(valueID))
		return policyir.NewEnumValue(policyir.EnumID(enumID), policyir.EnumValueID(memberID))
	case compilerir.TypeJSON:
		value, err := jsonPolicyValue(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewJSONValue(value)
	case compilerir.TypeScalarList:
		if logical.Element == nil {
			return policyir.Value{}, fmt.Errorf("scalar list has no element type")
		}
		items, err := list(raw, "scalar list")
		if err != nil || len(items) > s.binder.limits.MaxListItems {
			return policyir.Value{}, fmt.Errorf("scalar list is invalid or exceeds item limit")
		}
		values := make([]policyir.Value, len(items))
		for index, item := range items {
			value, valueErr := s.value(*logical.Element, item)
			if valueErr != nil {
				return policyir.Value{}, fmt.Errorf("element %d: %w", index, valueErr)
			}
			values[index] = value
		}
		return policyir.NewListValue(values)
	default:
		return policyir.Value{}, fmt.Errorf("unsupported logical type %q", logical.Kind)
	}
}

func integer64(raw any) (int64, error) {
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case int64:
		return value, nil
	case json.Number:
		return strconv.ParseInt(string(value), 10, 64)
	default:
		return 0, fmt.Errorf("expected exact integer, got %T", raw)
	}
}

func jsonPolicyValue(raw any) (policyir.JSONValue, error) {
	switch value := raw.(type) {
	case nil:
		return policyir.JSONNullValue(), nil
	case bool:
		return policyir.JSONBoolValue(value), nil
	case string:
		return policyir.JSONStringValue(value)
	case json.Number:
		return jsonNumber(string(value))
	case int:
		return jsonNumber(strconv.Itoa(value))
	case int64:
		return jsonNumber(strconv.FormatInt(value, 10))
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return policyir.JSONValue{}, fmt.Errorf("JSON number must be finite")
		}
		return jsonNumber(strconv.FormatFloat(value, 'g', -1, 64))
	case []any:
		items := make([]policyir.JSONValue, len(value))
		for index, item := range value {
			converted, err := jsonPolicyValue(item)
			if err != nil {
				return policyir.JSONValue{}, fmt.Errorf("array element %d: %w", index, err)
			}
			items[index] = converted
		}
		return policyir.JSONArrayValue(items)
	case map[string]any:
		members := make([]policyir.JSONMember, 0, len(value))
		for _, key := range sortedKeys(value) {
			converted, err := jsonPolicyValue(value[key])
			if err != nil {
				return policyir.JSONValue{}, fmt.Errorf("object key %q: %w", key, err)
			}
			member, err := policyir.NewJSONMember(key, converted)
			if err != nil {
				return policyir.JSONValue{}, err
			}
			members = append(members, member)
		}
		return policyir.JSONObjectValue(members)
	default:
		return policyir.JSONValue{}, fmt.Errorf("unsupported JSON value %T", raw)
	}
}

func jsonNumber(text string) (policyir.JSONValue, error) {
	parsed, err := golem.ParseJSONNumber(text)
	if err != nil {
		return policyir.JSONValue{}, err
	}
	negative, coefficient, exponent, ok := parsed.Parts()
	if !ok {
		return policyir.JSONValue{}, fmt.Errorf("invalid JSON number")
	}
	number, err := policyir.NewJSONNumber(negative, []byte(coefficient), exponent)
	if err != nil {
		return policyir.JSONValue{}, err
	}
	return policyir.JSONNumberValueOf(number)
}

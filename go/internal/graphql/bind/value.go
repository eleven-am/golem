package bind

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
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
		return policyir.TypeRef{}, compileerr.Invariantf("unsupported logical type %q", logical.Kind)
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
			return policyir.TypeRef{}, compileerr.Invariantf("unsupported capability %q", *logical.Capability)
		}
		capability = policyir.CapabilityScalarListJSON
	}
	ref, err := policyir.NewTypeRef(kind, nullable, precision, scale, enum, element, capability)
	if err != nil {
		return policyir.TypeRef{}, compileerr.Invariant(err)
	}
	return ref, nil
}

func invalid(detail compileerr.Detail, scalar string, options ...compileerr.Option) error {
	return compileerr.New(compileerr.KindInvalidValue, detail, append([]compileerr.Option{compileerr.Type(scalar)}, options...)...)
}

func (s *inputState) value(logical compilerir.LogicalTypeIR, raw any) (policyir.Value, error) {
	if raw == nil {
		return policyir.Value{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
	}
	switch logical.Kind {
	case compilerir.TypeBool:
		value, ok := raw.(bool)
		if !ok {
			return policyir.Value{}, invalid(compileerr.ValueWrongKind, "Boolean")
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
		signed, signedErr := policyir.SignedValue(kind, value)
		if signedErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "Int")
		}
		return signed, nil
	case compilerir.TypeInt64:
		value, err := graphqlscalar.BigInt(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		signed, signedErr := policyir.SignedValue(policyir.ValueInt64, value)
		if signedErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "BigInt")
		}
		return signed, nil
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		bits := 64
		if logical.Kind == compilerir.TypeFloat32 {
			bits = 32
		}
		value, err := graphqlscalar.Float(raw, bits)
		if err != nil {
			return policyir.Value{}, err
		}
		var floating policyir.Value
		var floatErr error
		if bits == 32 {
			floating, floatErr = policyir.Float32Value(float32(value))
		} else {
			floating, floatErr = policyir.Float64Value(value)
		}
		if floatErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueNotFinite, "Float")
		}
		return floating, nil
	case compilerir.TypeDecimal:
		value, err := graphqlscalar.Decimal(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		decimal, decimalErr := policyir.NewDecimalValue(value.Coefficient(), value.Scale())
		if decimalErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "Decimal")
		}
		return decimal, nil
	case compilerir.TypeString:
		value, ok := raw.(string)
		if !ok || !utf8.ValidString(value) {
			return policyir.Value{}, invalid(compileerr.ValueWrongKind, "String")
		}
		if logical.MaxLength != nil && uint32(utf8.RuneCountInString(value)) > *logical.MaxLength {
			return policyir.Value{}, invalid(compileerr.ValueTooLong, "String", compileerr.Max(int(*logical.MaxLength)))
		}
		if strings.IndexByte(value, 0) >= 0 {
			return policyir.Value{}, invalid(compileerr.ValueContainsNUL, "String")
		}
		text, textErr := policyir.PortableStringValue(value)
		if textErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueMalformed, "String")
		}
		return text, nil
	case compilerir.TypeBytes:
		value, err := graphqlscalar.Bytes(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		if logical.MaxLength != nil && uint32(len(value)) > *logical.MaxLength {
			return policyir.Value{}, invalid(compileerr.ValueTooLong, "Bytes", compileerr.Max(int(*logical.MaxLength)))
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
		date, dateErr := policyir.NewDateValue(int16(value.Year()), uint8(value.Month()), uint8(value.Day()))
		if dateErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "Date")
		}
		return date, nil
	case compilerir.TypeTime:
		value, err := graphqlscalar.Time(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		clock, clockErr := policyir.NewTimeValue(value.Microseconds())
		if clockErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "Time")
		}
		return clock, nil
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
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "DateTime")
		}
		instant, instantErr := policyir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
		if instantErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "DateTime")
		}
		return instant, nil
	case compilerir.TypeEnum:
		if logical.EnumID == nil {
			return policyir.Value{}, compileerr.Invariantf("enum-typed field has no enum identity")
		}
		enum, ok := s.binder.enums[*logical.EnumID]
		if !ok {
			return policyir.Value{}, compileerr.Invariantf("enum %s is absent", *logical.EnumID)
		}
		name, ok := raw.(string)
		if !ok {
			return policyir.Value{}, invalid(compileerr.ValueWrongKind, enum.GraphQLName)
		}
		var valueID compilerir.EnumValueID
		for _, value := range enum.Values {
			if value.GraphQLName == name {
				valueID = value.ValueID
				break
			}
		}
		if valueID == "" {
			return policyir.Value{}, invalid(compileerr.ValueUnknownEnum, enum.GraphQLName)
		}
		enumID, enumErr := fixedID(string(*logical.EnumID))
		if enumErr != nil {
			return policyir.Value{}, enumErr
		}
		memberID, memberErr := fixedID(string(valueID))
		if memberErr != nil {
			return policyir.Value{}, memberErr
		}
		member, memberValueErr := policyir.NewEnumValue(policyir.EnumID(enumID), policyir.EnumValueID(memberID))
		if memberValueErr != nil {
			return policyir.Value{}, compileerr.Invariant(memberValueErr)
		}
		return member, nil
	case compilerir.TypeJSON:
		value, err := jsonPolicyValue(raw)
		if err != nil {
			return policyir.Value{}, err
		}
		document, documentErr := policyir.NewJSONValue(value)
		if documentErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "JSON")
		}
		return document, nil
	case compilerir.TypeScalarList:
		if logical.Element == nil {
			return policyir.Value{}, compileerr.Invariantf("scalar list has no element type")
		}
		items, err := list(raw, "")
		if err != nil {
			return policyir.Value{}, err
		}
		if len(items) > s.binder.limits.MaxListItems {
			return policyir.Value{}, compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit("MaxListItems", s.binder.limits.MaxListItems))
		}
		values := make([]policyir.Value, len(items))
		for index, item := range items {
			value, valueErr := s.value(*logical.Element, item)
			if valueErr != nil {
				return policyir.Value{}, compileerr.At(valueErr, compileerr.Index("", index))
			}
			values[index] = value
		}
		listValue, listErr := policyir.NewListValue(values)
		if listErr != nil {
			return policyir.Value{}, invalid(compileerr.ValueOutOfRange, "list")
		}
		return listValue, nil
	default:
		return policyir.Value{}, compileerr.Invariantf("unsupported logical type %q", logical.Kind)
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
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, invalid(compileerr.ValueOutOfRange, "Int")
		}
		return parsed, nil
	default:
		return 0, invalid(compileerr.ValueWrongKind, "Int")
	}
}

func jsonPolicyValue(raw any) (policyir.JSONValue, error) {
	switch value := raw.(type) {
	case nil:
		return policyir.JSONNullValue(), nil
	case bool:
		return policyir.JSONBoolValue(value), nil
	case string:
		if strings.IndexByte(value, 0) >= 0 {
			return policyir.JSONValue{}, invalid(compileerr.ValueContainsNUL, "JSON")
		}
		text, err := policyir.JSONStringValue(value)
		if err != nil {
			return policyir.JSONValue{}, invalid(compileerr.ValueMalformed, "JSON")
		}
		return text, nil
	case json.Number:
		return jsonNumber(string(value))
	case int:
		return jsonNumber(strconv.Itoa(value))
	case int64:
		return jsonNumber(strconv.FormatInt(value, 10))
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return policyir.JSONValue{}, invalid(compileerr.ValueNotFinite, "JSON number")
		}
		return jsonNumber(strconv.FormatFloat(value, 'g', -1, 64))
	case []any:
		items := make([]policyir.JSONValue, len(value))
		for index, item := range value {
			converted, err := jsonPolicyValue(item)
			if err != nil {
				return policyir.JSONValue{}, compileerr.At(err, compileerr.Index("", index))
			}
			items[index] = converted
		}
		array, arrayErr := policyir.JSONArrayValue(items)
		if arrayErr != nil {
			return policyir.JSONValue{}, invalid(compileerr.ValueOutOfRange, "JSON")
		}
		return array, nil
	case map[string]any:
		members := make([]policyir.JSONMember, 0, len(value))
		for _, key := range sortedKeys(value) {
			converted, err := jsonPolicyValue(value[key])
			if err != nil {
				return policyir.JSONValue{}, err
			}
			if strings.IndexByte(key, 0) >= 0 {
				return policyir.JSONValue{}, invalid(compileerr.ValueContainsNUL, "JSON")
			}
			member, err := policyir.NewJSONMember(key, converted)
			if err != nil {
				return policyir.JSONValue{}, invalid(compileerr.ValueMalformed, "JSON")
			}
			members = append(members, member)
		}
		object, objectErr := policyir.JSONObjectValue(members)
		if objectErr != nil {
			return policyir.JSONValue{}, invalid(compileerr.ValueOutOfRange, "JSON")
		}
		return object, nil
	default:
		return policyir.JSONValue{}, invalid(compileerr.ValueWrongKind, "JSON")
	}
}

func jsonNumber(text string) (policyir.JSONValue, error) {
	parsed, err := golem.ParseJSONNumber(text)
	if err != nil {
		return policyir.JSONValue{}, invalid(compileerr.ValueMalformed, "JSON number")
	}
	negative, coefficient, exponent, ok := parsed.Parts()
	if !ok {
		return policyir.JSONValue{}, invalid(compileerr.ValueMalformed, "JSON number")
	}
	number, err := policyir.NewJSONNumber(negative, []byte(coefficient), exponent)
	if err != nil {
		return policyir.JSONValue{}, invalid(compileerr.ValueOutOfRange, "JSON number")
	}
	value, err := policyir.JSONNumberValueOf(number)
	if err != nil {
		return policyir.JSONValue{}, invalid(compileerr.ValueOutOfRange, "JSON number")
	}
	return value, nil
}

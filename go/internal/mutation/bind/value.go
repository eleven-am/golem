package bind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

const golemPackagePath = "github.com/eleven-am/golem/go/golem"

func bindValue(raw any, logical compilerir.LogicalTypeIR, typ policyir.TypeRef, registry *schema.Registry) (policyir.Value, error) {
	if raw == nil {
		return policyir.Value{}, fmt.Errorf("nil is not a scalar value; use the explicit null operation")
	}
	// Nested membership hooks receive runtime-derived FK assignments which are
	// already exact, validated policy values. They still pass through the
	// declared TypeRef and ScalarOperation validation below; this narrow path
	// avoids lossy conversion through an arbitrary public Go representation.
	if exact, ok := raw.(policyir.Value); ok {
		if err := exact.Validate(); err != nil {
			return policyir.Value{}, err
		}
		if exact.Kind() != typ.Kind() {
			return policyir.Value{}, fmt.Errorf("runtime value kind %d does not match field kind %d", exact.Kind(), typ.Kind())
		}
		return exact, nil
	}
	value := reflect.ValueOf(raw)
	if !value.IsValid() || value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		return policyir.Value{}, fmt.Errorf("value has an unsupported indirection type %T", raw)
	}

	var result policyir.Value
	var err error
	switch logical.Kind {
	case compilerir.TypeBool:
		if value.Kind() != reflect.Bool {
			return result, wrongGoType(raw, "bool")
		}
		result = policyir.BoolValue(value.Bool())
	case compilerir.TypeInt16:
		result, err = signedReflect(value, policyir.ValueInt16, reflect.Int16)
	case compilerir.TypeInt32:
		result, err = signedReflect(value, policyir.ValueInt32, reflect.Int32)
	case compilerir.TypeInt64:
		result, err = signedReflect(value, policyir.ValueInt64, reflect.Int64)
	case compilerir.TypeFloat32:
		if value.Kind() != reflect.Float32 {
			return result, wrongGoType(raw, "float32")
		}
		result, err = policyir.Float32Value(float32(value.Float()))
	case compilerir.TypeFloat64:
		if value.Kind() != reflect.Float64 {
			return result, wrongGoType(raw, "float64")
		}
		result, err = policyir.Float64Value(value.Float())
	case compilerir.TypeDecimal:
		decimal, ok := raw.(golem.Decimal)
		if !ok {
			return result, wrongGoType(raw, "golem.Decimal")
		}
		result, err = policyir.NewDecimalValue(decimal.Coefficient(), decimal.Scale())
	case compilerir.TypeString:
		if value.Kind() != reflect.String {
			return result, wrongGoType(raw, "string")
		}
		text := value.String()
		if logical.MaxLength != nil && uint32(utf8.RuneCountInString(text)) > *logical.MaxLength {
			return result, fmt.Errorf("string exceeds maximum rune length %d", *logical.MaxLength)
		}
		result, err = policyir.StringValue(text)
	case compilerir.TypeBytes:
		if value.Kind() != reflect.Slice || value.Type().Elem().Kind() != reflect.Uint8 {
			return result, wrongGoType(raw, "[]byte")
		}
		bytesValue := append([]byte(nil), value.Bytes()...)
		if logical.MaxLength != nil && uint32(len(bytesValue)) > *logical.MaxLength {
			return result, fmt.Errorf("bytes exceed maximum byte length %d", *logical.MaxLength)
		}
		result = policyir.BytesValue(bytesValue)
	case compilerir.TypeUUID:
		uuid, ok := raw.(golem.UUID)
		if !ok {
			return result, wrongGoType(raw, "golem.UUID")
		}
		result = policyir.UUIDValue(uuid.Bytes())
	case compilerir.TypeDate:
		date, ok := raw.(golem.Date)
		if !ok {
			return result, wrongGoType(raw, "golem.Date")
		}
		result, err = policyir.NewDateValue(int16(date.Year()), uint8(date.Month()), uint8(date.Day()))
	case compilerir.TypeTime:
		clock, ok := raw.(golem.Time)
		if !ok {
			return result, wrongGoType(raw, "golem.Time")
		}
		result, err = policyir.NewTimeValue(clock.Microseconds())
	case compilerir.TypeDateTime:
		instant, ok := raw.(time.Time)
		if !ok {
			return result, wrongGoType(raw, "time.Time")
		}
		instant = instant.UTC()
		if instant.Year() < 1 || instant.Year() > 9999 {
			return result, fmt.Errorf("datetime is outside the portable year range 0001-9999")
		}
		result, err = policyir.NewDateTimeValue(instant.Unix(), uint32(instant.Nanosecond()))
	case compilerir.TypeEnum:
		if value.Kind() != reflect.String || logical.EnumID == nil {
			return result, wrongGoType(raw, "generated enum string")
		}
		member, ok := registry.EnumValue(*logical.EnumID, value.String())
		if !ok {
			return result, fmt.Errorf("enum label is absent from the active schema")
		}
		enum, idErr := fixedID(string(*logical.EnumID))
		if idErr != nil {
			return result, idErr
		}
		memberID, idErr := fixedID(string(member))
		if idErr != nil {
			return result, idErr
		}
		result, err = policyir.NewEnumValue(policyir.EnumID(enum), policyir.EnumValueID(memberID))
	case compilerir.TypeJSON:
		if !isGolemGeneric(value.Type(), "JSON[") {
			return result, wrongGoType(raw, "golem.JSON[T]")
		}
		document, ok := raw.(interface{ Bytes() []byte })
		if !ok {
			return result, wrongGoType(raw, "golem.JSON[T]")
		}
		public, parseErr := golem.ParseJSON(document.Bytes())
		if parseErr != nil {
			return result, fmt.Errorf("invalid canonical JSON document: %w", parseErr)
		}
		canonical, canonicalErr := golem.CanonicalJSON(public)
		if canonicalErr != nil || !bytes.Equal(canonical, document.Bytes()) {
			return result, fmt.Errorf("JSON document is not canonical")
		}
		bound, bindErr := bindJSON(public)
		if bindErr != nil {
			return result, bindErr
		}
		result, err = policyir.NewJSONValue(bound)
	case compilerir.TypeScalarList:
		if !isGolemGeneric(value.Type(), "List[") || value.Kind() != reflect.Slice || logical.Element == nil {
			return result, wrongGoType(raw, "golem.List[T]")
		}
		elementType, typeErr := bindType(*logical.Element, false)
		if typeErr != nil {
			return result, typeErr
		}
		items := make([]policyir.Value, value.Len())
		for index := 0; index < value.Len(); index++ {
			item, itemErr := bindValue(value.Index(index).Interface(), *logical.Element, elementType, registry)
			if itemErr != nil {
				return result, fmt.Errorf("scalar-list element %d: %w", index, itemErr)
			}
			items[index] = item
		}
		result, err = policyir.NewListValue(items)
	default:
		return result, fmt.Errorf("unsupported logical type %q", logical.Kind)
	}
	if err != nil {
		return policyir.Value{}, err
	}
	if result.Kind() != typ.Kind() {
		return policyir.Value{}, fmt.Errorf("bound value kind %d does not match field kind %d", result.Kind(), typ.Kind())
	}
	return result, nil
}

func signedReflect(value reflect.Value, kind policyir.ValueKind, want reflect.Kind) (policyir.Value, error) {
	if value.Kind() != want {
		return policyir.Value{}, fmt.Errorf("value type %s does not match signed width", value.Type())
	}
	return policyir.SignedValue(kind, value.Int())
}

func wrongGoType(value any, want string) error {
	return fmt.Errorf("value type %T does not match %s", value, want)
}

func isGolemGeneric(typ reflect.Type, prefix string) bool {
	return typ.PkgPath() == golemPackagePath && strings.HasPrefix(typ.Name(), prefix)
}

func bindJSON(value golem.JSONValue) (policyir.JSONValue, error) {
	canonical, err := golem.CanonicalJSON(value)
	if err != nil {
		return policyir.JSONValue{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return policyir.JSONValue{}, err
	}
	return bindDecodedJSON(decoded)
}

func bindDecodedJSON(value any) (policyir.JSONValue, error) {
	switch value := value.(type) {
	case nil:
		return policyir.JSONNullValue(), nil
	case bool:
		return policyir.JSONBoolValue(value), nil
	case string:
		return policyir.JSONStringValue(value)
	case json.Number:
		number, err := golem.ParseJSONNumber(string(value))
		if err != nil {
			return policyir.JSONValue{}, err
		}
		negative, coefficient, exponent, ok := number.Parts()
		if !ok {
			return policyir.JSONValue{}, fmt.Errorf("invalid canonical JSON number")
		}
		exact, err := policyir.NewJSONNumber(negative, []byte(coefficient), exponent)
		if err != nil {
			return policyir.JSONValue{}, err
		}
		return policyir.JSONNumberValueOf(exact)
	case []any:
		items := make([]policyir.JSONValue, len(value))
		for index, item := range value {
			converted, err := bindDecodedJSON(item)
			if err != nil {
				return policyir.JSONValue{}, fmt.Errorf("JSON array %d: %w", index, err)
			}
			items[index] = converted
		}
		return policyir.JSONArrayValue(items)
	case map[string]any:
		members := make([]policyir.JSONMember, 0, len(value))
		for key, item := range value {
			converted, err := bindDecodedJSON(item)
			if err != nil {
				return policyir.JSONValue{}, fmt.Errorf("JSON object key %q: %w", key, err)
			}
			member, err := policyir.NewJSONMember(key, converted)
			if err != nil {
				return policyir.JSONValue{}, err
			}
			members = append(members, member)
		}
		return policyir.JSONObjectValue(members)
	default:
		return policyir.JSONValue{}, fmt.Errorf("unsupported decoded JSON type %T", value)
	}
}

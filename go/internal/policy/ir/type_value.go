package ir

import (
	"fmt"
	"math"
	"sort"
	"unicode/utf8"
)

type TypeRef struct {
	kind       ValueKind
	nullable   bool
	precision  uint16
	scale      uint16
	enum       EnumID
	element    *TypeRef
	capability Capability
}

func NewTypeRef(kind ValueKind, nullable bool, precision, scale uint16, enum EnumID, element *TypeRef, capability Capability) (TypeRef, error) {
	value := TypeRef{kind: kind, nullable: nullable, precision: precision, scale: scale, enum: enum, capability: capability}
	if element != nil {
		copy := element.clone()
		value.element = &copy
	}
	if err := value.validate(); err != nil {
		return TypeRef{}, err
	}
	return value, nil
}

func (value TypeRef) Kind() ValueKind        { return value.kind }
func (value TypeRef) Nullable() bool         { return value.nullable }
func (value TypeRef) Precision() uint16      { return value.precision }
func (value TypeRef) Scale() uint16          { return value.scale }
func (value TypeRef) EnumID() (EnumID, bool) { return value.enum, value.kind == ValueEnum }
func (value TypeRef) Capability() Capability { return value.capability }
func (value TypeRef) Element() (TypeRef, bool) {
	if value.element == nil {
		return TypeRef{}, false
	}
	return value.element.clone(), true
}
func (value TypeRef) Validate() error { return value.validate() }
func (value TypeRef) clone() TypeRef {
	copy := value
	if value.element != nil {
		element := value.element.clone()
		copy.element = &element
	}
	return copy
}

func (value TypeRef) validate() error {
	if !validValueKind(value.kind) {
		return fmt.Errorf("policy IR: invalid logical value kind %d", value.kind)
	}
	if value.kind != ValueDecimal && value.scale != 0 {
		return fmt.Errorf("policy IR: scale is valid only for decimal")
	}
	if value.kind == ValueDecimal {
		if value.precision == 0 || value.precision > 18 || value.scale > value.precision {
			return fmt.Errorf("policy IR: invalid portable decimal(%d,%d)", value.precision, value.scale)
		}
	} else if value.kind == ValueTime || value.kind == ValueDateTime {
		if value.precision > 6 {
			return fmt.Errorf("policy IR: temporal precision %d exceeds 6", value.precision)
		}
	} else if value.precision != 0 {
		return fmt.Errorf("policy IR: precision is invalid for value kind %d", value.kind)
	}
	if value.kind == ValueEnum {
		if value.enum == (EnumID{}) {
			return fmt.Errorf("policy IR: enum type requires a non-zero enum ID")
		}
	} else if value.enum != (EnumID{}) {
		return fmt.Errorf("policy IR: enum ID is valid only for enum type")
	}
	if value.kind == ValueScalarList {
		if value.element == nil {
			return fmt.Errorf("policy IR: scalar list requires an element type")
		}
		if value.element.nullable || value.element.kind == ValueBytes || value.element.kind == ValueJSON || value.element.kind == ValueScalarList {
			return fmt.Errorf("policy IR: invalid scalar-list element type")
		}
		if value.element.capability != 0 {
			return fmt.Errorf("policy IR: scalar-list element must not carry storage capability")
		}
		if err := value.element.validate(); err != nil {
			return fmt.Errorf("policy IR: invalid scalar-list element: %w", err)
		}
	} else if value.element != nil {
		return fmt.Errorf("policy IR: element type is valid only for scalar list")
	}
	if value.capability != 0 && !validCapability(value.capability) {
		return fmt.Errorf("policy IR: unknown storage capability %d", value.capability)
	}
	return nil
}

type DecimalValue struct {
	coefficient int64
	scale       uint8
}
type DateValue struct {
	year       int16
	month, day uint8
}
type TimeValue struct{ microseconds int64 }
type DateTimeValue struct {
	unixSeconds int64
	nanosecond  uint32
}
type EnumValue struct {
	enum  EnumID
	value EnumValueID
}

type Value struct {
	kind        ValueKind
	boolean     bool
	signed      int64
	float32Bits uint32
	float64Bits uint64
	decimal     DecimalValue
	text        string
	bytes       []byte
	uuid        [16]byte
	date        DateValue
	time        TimeValue
	instant     DateTimeValue
	enum        EnumValue
	json        JSONValue
	list        []Value
}

func BoolValue(value bool) Value { return Value{kind: ValueBool, boolean: value} }
func SignedValue(kind ValueKind, value int64) (Value, error) {
	switch kind {
	case ValueInt16:
		if value < math.MinInt16 || value > math.MaxInt16 {
			return Value{}, fmt.Errorf("policy IR: int16 operand out of range")
		}
	case ValueInt32:
		if value < math.MinInt32 || value > math.MaxInt32 {
			return Value{}, fmt.Errorf("policy IR: int32 operand out of range")
		}
	case ValueInt64:
	default:
		return Value{}, fmt.Errorf("policy IR: signed operand requires an integer kind")
	}
	return Value{kind: kind, signed: value}, nil
}
func Float32Value(value float32) (Value, error) {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
		return Value{}, fmt.Errorf("policy IR: float32 operand must be finite")
	}
	if value == 0 {
		value = 0
	}
	return Value{kind: ValueFloat32, float32Bits: math.Float32bits(value)}, nil
}
func Float64Value(value float64) (Value, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Value{}, fmt.Errorf("policy IR: float64 operand must be finite")
	}
	if value == 0 {
		value = 0
	}
	return Value{kind: ValueFloat64, float64Bits: math.Float64bits(value)}, nil
}
func NewDecimalValue(coefficient int64, scale uint8) (Value, error) {
	if scale > 18 {
		return Value{}, fmt.Errorf("policy IR: decimal scale exceeds 18")
	}
	for coefficient != 0 && scale > 0 && coefficient%10 == 0 {
		coefficient /= 10
		scale--
	}
	if coefficient == 0 {
		scale = 0
	}
	if decimalMagnitude(coefficient) > 999_999_999_999_999_999 {
		return Value{}, fmt.Errorf("policy IR: decimal coefficient exceeds portable 18-digit precision")
	}
	return Value{kind: ValueDecimal, decimal: DecimalValue{coefficient: coefficient, scale: scale}}, nil
}
func StringValue(value string) (Value, error) {
	if !utf8.ValidString(value) {
		return Value{}, fmt.Errorf("policy IR: string operand is not valid UTF-8")
	}
	return Value{kind: ValueString, text: value}, nil
}
func BytesValue(value []byte) Value {
	return Value{kind: ValueBytes, bytes: append([]byte(nil), value...)}
}
func UUIDValue(value [16]byte) Value { return Value{kind: ValueUUID, uuid: value} }
func NewDateValue(year int16, month, day uint8) (Value, error) {
	if !validDate(year, month, day) {
		return Value{}, fmt.Errorf("policy IR: invalid Gregorian date")
	}
	return Value{kind: ValueDate, date: DateValue{year: year, month: month, day: day}}, nil
}
func NewTimeValue(microseconds int64) (Value, error) {
	if microseconds < 0 || microseconds >= 86_400_000_000 {
		return Value{}, fmt.Errorf("policy IR: time is outside one day")
	}
	return Value{kind: ValueTime, time: TimeValue{microseconds: microseconds}}, nil
}
func NewDateTimeValue(unixSeconds int64, nanosecond uint32) (Value, error) {
	if nanosecond >= 1_000_000_000 || nanosecond%1_000 != 0 {
		return Value{}, fmt.Errorf("policy IR: datetime must have at most microsecond precision")
	}
	return Value{kind: ValueDateTime, instant: DateTimeValue{unixSeconds: unixSeconds, nanosecond: nanosecond}}, nil
}
func NewEnumValue(enum EnumID, member EnumValueID) (Value, error) {
	if enum == (EnumID{}) || member == (EnumValueID{}) {
		return Value{}, fmt.Errorf("policy IR: enum operand requires non-zero enum and member IDs")
	}
	return Value{kind: ValueEnum, enum: EnumValue{enum: enum, value: member}}, nil
}
func NewJSONValue(value JSONValue) (Value, error) {
	if err := value.validate(); err != nil {
		return Value{}, err
	}
	return Value{kind: ValueJSON, json: value.clone()}, nil
}
func NewListValue(values []Value) (Value, error) {
	copy := make([]Value, len(values))
	var elementKind ValueKind
	for index, value := range values {
		if err := value.validate(); err != nil {
			return Value{}, fmt.Errorf("policy IR: invalid scalar-list element %d", index)
		}
		if value.kind == ValueBytes || value.kind == ValueJSON || value.kind == ValueScalarList {
			return Value{}, fmt.Errorf("policy IR: invalid scalar-list element kind %d", value.kind)
		}
		if index == 0 {
			elementKind = value.kind
		} else if value.kind != elementKind {
			return Value{}, fmt.Errorf("policy IR: heterogeneous scalar list")
		}
		copy[index] = value.clone()
	}
	return Value{kind: ValueScalarList, list: copy}, nil
}

func (value Value) Kind() ValueKind    { return value.kind }
func (value Value) Bool() (bool, bool) { return value.boolean, value.kind == ValueBool }
func (value Value) Signed() (int64, bool) {
	return value.signed, value.kind == ValueInt16 || value.kind == ValueInt32 || value.kind == ValueInt64
}
func (value Value) Float32Bits() (uint32, bool) { return value.float32Bits, value.kind == ValueFloat32 }
func (value Value) Float64Bits() (uint64, bool) { return value.float64Bits, value.kind == ValueFloat64 }
func (value Value) Decimal() (coefficient int64, scale uint8, ok bool) {
	return value.decimal.coefficient, value.decimal.scale, value.kind == ValueDecimal
}
func (value Value) Text() (string, bool) { return value.text, value.kind == ValueString }
func (value Value) Bytes() ([]byte, bool) {
	return append([]byte(nil), value.bytes...), value.kind == ValueBytes
}
func (value Value) UUID() ([16]byte, bool) { return value.uuid, value.kind == ValueUUID }
func (value Value) Date() (year int16, month, day uint8, ok bool) {
	return value.date.year, value.date.month, value.date.day, value.kind == ValueDate
}
func (value Value) Time() (int64, bool) { return value.time.microseconds, value.kind == ValueTime }
func (value Value) DateTime() (seconds int64, nanos uint32, ok bool) {
	return value.instant.unixSeconds, value.instant.nanosecond, value.kind == ValueDateTime
}
func (value Value) Enum() (EnumID, EnumValueID, bool) {
	return value.enum.enum, value.enum.value, value.kind == ValueEnum
}
func (value Value) JSON() (JSONValue, bool) { return value.json.clone(), value.kind == ValueJSON }
func (value Value) List() ([]Value, bool) {
	if value.kind != ValueScalarList {
		return nil, false
	}
	return cloneValues(value.list), true
}
func (value Value) Validate() error { return value.validate() }
func (value Value) clone() Value {
	copy := value
	copy.bytes = append([]byte(nil), value.bytes...)
	copy.json = value.json.clone()
	copy.list = cloneValues(value.list)
	return copy
}
func cloneValues(input []Value) []Value {
	if input == nil {
		return nil
	}
	output := make([]Value, len(input))
	for index := range input {
		output[index] = input[index].clone()
	}
	return output
}
func (value Value) validate() error {
	if !validValueKind(value.kind) {
		return fmt.Errorf("policy IR: invalid value kind %d", value.kind)
	}
	switch value.kind {
	case ValueBool:
	case ValueInt16:
		if value.signed < math.MinInt16 || value.signed > math.MaxInt16 {
			return fmt.Errorf("policy IR: int16 operand out of range")
		}
	case ValueInt32:
		if value.signed < math.MinInt32 || value.signed > math.MaxInt32 {
			return fmt.Errorf("policy IR: int32 operand out of range")
		}
	case ValueInt64:
	case ValueDecimal:
		if value.decimal.scale > 18 || decimalMagnitude(value.decimal.coefficient) > 999_999_999_999_999_999 {
			return fmt.Errorf("policy IR: invalid portable decimal")
		}
		if value.decimal.coefficient != 0 && value.decimal.scale > 0 && value.decimal.coefficient%10 == 0 {
			return fmt.Errorf("policy IR: decimal is not normalized")
		}
		if value.decimal.coefficient == 0 && value.decimal.scale != 0 {
			return fmt.Errorf("policy IR: decimal zero is not canonical")
		}
	case ValueFloat32:
		v := math.Float32frombits(value.float32Bits)
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v == 0 && value.float32Bits != 0 {
			return fmt.Errorf("policy IR: non-canonical float32")
		}
	case ValueFloat64:
		v := math.Float64frombits(value.float64Bits)
		if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 && value.float64Bits != 0 {
			return fmt.Errorf("policy IR: non-canonical float64")
		}
	case ValueString:
		if !utf8.ValidString(value.text) {
			return fmt.Errorf("policy IR: invalid UTF-8 string")
		}
	case ValueDate:
		if !validDate(value.date.year, value.date.month, value.date.day) {
			return fmt.Errorf("policy IR: invalid date")
		}
	case ValueTime:
		if value.time.microseconds < 0 || value.time.microseconds >= 86_400_000_000 {
			return fmt.Errorf("policy IR: invalid time")
		}
	case ValueDateTime:
		if value.instant.nanosecond >= 1_000_000_000 || value.instant.nanosecond%1_000 != 0 {
			return fmt.Errorf("policy IR: invalid datetime")
		}
	case ValueEnum:
		if value.enum.enum == (EnumID{}) || value.enum.value == (EnumValueID{}) {
			return fmt.Errorf("policy IR: invalid enum value")
		}
	case ValueJSON:
		if err := value.json.validate(); err != nil {
			return err
		}
	case ValueScalarList:
		var elementKind ValueKind
		for index, element := range value.list {
			if element.kind == ValueBytes || element.kind == ValueJSON || element.kind == ValueScalarList || !validValueKind(element.kind) {
				return fmt.Errorf("policy IR: invalid list element kind")
			}
			if index == 0 {
				elementKind = element.kind
			} else if element.kind != elementKind {
				return fmt.Errorf("policy IR: heterogeneous list")
			}
			if err := element.validate(); err != nil {
				return err
			}
		}
	}
	if value.kind != ValueBool && value.boolean || value.kind != ValueInt16 && value.kind != ValueInt32 && value.kind != ValueInt64 && value.signed != 0 || value.kind != ValueFloat32 && value.float32Bits != 0 || value.kind != ValueFloat64 && value.float64Bits != 0 || value.kind != ValueDecimal && value.decimal != (DecimalValue{}) || value.kind != ValueString && value.text != "" || value.kind != ValueBytes && value.bytes != nil || value.kind != ValueUUID && value.uuid != ([16]byte{}) || value.kind != ValueDate && value.date != (DateValue{}) || value.kind != ValueTime && value.time != (TimeValue{}) || value.kind != ValueDateTime && value.instant != (DateTimeValue{}) || value.kind != ValueEnum && value.enum != (EnumValue{}) || value.kind != ValueJSON && !value.json.zero() || value.kind != ValueScalarList && value.list != nil {
		return fmt.Errorf("policy IR: value kind %d populates an inactive union member", value.kind)
	}
	return nil
}

func decimalMagnitude(value int64) uint64 {
	if value >= 0 {
		return uint64(value)
	}
	return uint64(-(value + 1)) + 1
}

func validDate(year int16, month, day uint8) bool {
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 {
		return false
	}
	days := [...]uint8{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	max := days[month]
	if month == 2 && (year%400 == 0 || year%4 == 0 && year%100 != 0) {
		max = 29
	}
	return day <= max
}

type JSONNumberValue struct {
	negative    bool
	coefficient []byte
	exponent    int32
}
type JSONMember struct {
	key   string
	value JSONValue
}
type JSONValue struct {
	kind    JSONKind
	boolean bool
	number  JSONNumberValue
	text    string
	array   []JSONValue
	object  []JSONMember
}

func NewJSONNumber(negative bool, coefficient []byte, exponent int32) (JSONNumberValue, error) {
	value := JSONNumberValue{negative: negative, coefficient: append([]byte(nil), coefficient...), exponent: exponent}
	if err := value.validate(); err != nil {
		return JSONNumberValue{}, err
	}
	return value, nil
}
func JSONNullValue() JSONValue           { return JSONValue{kind: JSONNull} }
func JSONBoolValue(value bool) JSONValue { return JSONValue{kind: JSONBool, boolean: value} }
func JSONNumberValueOf(value JSONNumberValue) (JSONValue, error) {
	if err := value.validate(); err != nil {
		return JSONValue{}, err
	}
	return JSONValue{kind: JSONNumber, number: value.clone()}, nil
}
func JSONStringValue(value string) (JSONValue, error) {
	if !utf8.ValidString(value) {
		return JSONValue{}, fmt.Errorf("policy IR: invalid UTF-8 JSON string")
	}
	return JSONValue{kind: JSONString, text: value}, nil
}
func JSONArrayValue(values []JSONValue) (JSONValue, error) {
	output := JSONValue{kind: JSONArray, array: cloneJSONValues(values)}
	if err := output.validate(); err != nil {
		return JSONValue{}, err
	}
	return output, nil
}
func JSONObjectValue(members []JSONMember) (JSONValue, error) {
	copy := cloneJSONMembers(members)
	sort.Slice(copy, func(i, j int) bool { return copy[i].key < copy[j].key })
	output := JSONValue{kind: JSONObject, object: copy}
	if err := output.validate(); err != nil {
		return JSONValue{}, err
	}
	return output, nil
}
func NewJSONMember(key string, value JSONValue) (JSONMember, error) {
	if !utf8.ValidString(key) {
		return JSONMember{}, fmt.Errorf("policy IR: invalid UTF-8 JSON key")
	}
	if err := value.validate(); err != nil {
		return JSONMember{}, err
	}
	return JSONMember{key: key, value: value.clone()}, nil
}
func (member JSONMember) Key() string             { return member.key }
func (member JSONMember) Value() JSONValue        { return member.value.clone() }
func (value JSONNumberValue) Negative() bool      { return value.negative }
func (value JSONNumberValue) Coefficient() []byte { return append([]byte(nil), value.coefficient...) }
func (value JSONNumberValue) Exponent() int32     { return value.exponent }
func (value JSONNumberValue) clone() JSONNumberValue {
	copy := value
	copy.coefficient = append([]byte(nil), value.coefficient...)
	return copy
}
func (value JSONNumberValue) validate() error {
	if len(value.coefficient) == 0 {
		return fmt.Errorf("policy IR: JSON number has no coefficient")
	}
	for _, digit := range value.coefficient {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("policy IR: JSON number coefficient is not decimal")
		}
	}
	if string(value.coefficient) == "0" {
		if value.negative || value.exponent != 0 {
			return fmt.Errorf("policy IR: JSON zero must be canonical")
		}
		return nil
	}
	if value.coefficient[0] == '0' || value.coefficient[len(value.coefficient)-1] == '0' {
		return fmt.Errorf("policy IR: JSON number coefficient is not normalized")
	}
	return nil
}
func (value JSONValue) Kind() JSONKind     { return value.kind }
func (value JSONValue) Bool() (bool, bool) { return value.boolean, value.kind == JSONBool }
func (value JSONValue) Number() (JSONNumberValue, bool) {
	return value.number.clone(), value.kind == JSONNumber
}
func (value JSONValue) Text() (string, bool) { return value.text, value.kind == JSONString }
func (value JSONValue) Array() ([]JSONValue, bool) {
	return cloneJSONValues(value.array), value.kind == JSONArray
}
func (value JSONValue) Object() ([]JSONMember, bool) {
	return cloneJSONMembers(value.object), value.kind == JSONObject
}
func (value JSONValue) clone() JSONValue {
	copy := value
	copy.number = value.number.clone()
	copy.array = cloneJSONValues(value.array)
	copy.object = cloneJSONMembers(value.object)
	return copy
}
func cloneJSONValues(input []JSONValue) []JSONValue {
	if input == nil {
		return nil
	}
	output := make([]JSONValue, len(input))
	for i := range input {
		output[i] = input[i].clone()
	}
	return output
}
func cloneJSONMembers(input []JSONMember) []JSONMember {
	if input == nil {
		return nil
	}
	output := make([]JSONMember, len(input))
	for i := range input {
		output[i] = JSONMember{key: input[i].key, value: input[i].value.clone()}
	}
	return output
}
func (value JSONValue) validate() error {
	switch value.kind {
	case JSONNull, JSONBool:
	case JSONNumber:
		if err := value.number.validate(); err != nil {
			return err
		}
	case JSONString:
		if !utf8.ValidString(value.text) {
			return fmt.Errorf("policy IR: invalid UTF-8 JSON string")
		}
	case JSONArray:
		for i := range value.array {
			if err := value.array[i].validate(); err != nil {
				return fmt.Errorf("policy IR: invalid JSON array element %d: %w", i, err)
			}
		}
	case JSONObject:
		previous := ""
		for i, member := range value.object {
			if !utf8.ValidString(member.key) {
				return fmt.Errorf("policy IR: invalid JSON object key")
			}
			if i > 0 && member.key <= previous {
				return fmt.Errorf("policy IR: JSON object keys must be unique and sorted")
			}
			previous = member.key
			if err := member.value.validate(); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("policy IR: invalid JSON kind %d", value.kind)
	}
	if value.kind != JSONBool && value.boolean || value.kind != JSONNumber && (value.number.negative || value.number.coefficient != nil || value.number.exponent != 0) || value.kind != JSONString && value.text != "" || value.kind != JSONArray && value.array != nil || value.kind != JSONObject && value.object != nil {
		return fmt.Errorf("policy IR: JSON kind %d populates an inactive union member", value.kind)
	}
	return nil
}

func (value JSONValue) zero() bool {
	return value.kind == 0 && !value.boolean && !value.number.negative && value.number.coefficient == nil && value.number.exponent == 0 && value.text == "" && value.array == nil && value.object == nil
}

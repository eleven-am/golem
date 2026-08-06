package golem

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDecimalCoefficient uint64 = 999_999_999_999_999_999

// NewUUID constructs a UUID from its exact 16-byte representation.
func NewUUID(value [16]byte) UUID { return UUID(value) }

// ParseUUID accepts the canonical hyphenated UUID form. Hexadecimal digits may
// be upper or lower case; String always emits lower case.
func ParseUUID(input string) (UUID, error) {
	var value UUID
	if len(input) != 36 || input[8] != '-' || input[13] != '-' || input[18] != '-' || input[23] != '-' {
		return value, errors.New("UUID must use the 8-4-4-4-12 hyphenated form")
	}
	compact := input[:8] + input[9:13] + input[14:18] + input[19:23] + input[24:]
	if _, err := hex.Decode(value[:], []byte(compact)); err != nil {
		return UUID{}, fmt.Errorf("invalid UUID: %w", err)
	}
	return value, nil
}

// Bytes returns the exact UUID bytes by value.
func (value UUID) Bytes() [16]byte { return [16]byte(value) }

func (value UUID) String() string {
	var output [36]byte
	hex.Encode(output[0:8], value[0:4])
	output[8] = '-'
	hex.Encode(output[9:13], value[4:6])
	output[13] = '-'
	hex.Encode(output[14:18], value[6:8])
	output[18] = '-'
	hex.Encode(output[19:23], value[8:10])
	output[23] = '-'
	hex.Encode(output[24:36], value[10:16])
	return string(output[:])
}

// NewDecimal constructs a portable exact decimal. coefficient is scaled by
// 10^-scale. The result is normalized and limited to the portable P1 ceiling
// of 18 coefficient digits and 18 fractional digits.
func NewDecimal(coefficient int64, scale uint8) (Decimal, error) {
	if scale > 18 {
		return Decimal{}, fmt.Errorf("decimal scale %d exceeds portable maximum 18", scale)
	}
	for coefficient != 0 && scale > 0 && coefficient%10 == 0 {
		coefficient /= 10
		scale--
	}
	if magnitude(coefficient) > maxDecimalCoefficient {
		return Decimal{}, errors.New("decimal coefficient exceeds portable 18-digit precision")
	}
	return Decimal{coefficient: coefficient, scale: scale}, nil
}

// ParseDecimal parses a base-10 decimal without exponent notation.
func ParseDecimal(input string) (Decimal, error) {
	if input == "" {
		return Decimal{}, errors.New("decimal is empty")
	}
	negative := false
	if input[0] == '-' || input[0] == '+' {
		negative = input[0] == '-'
		input = input[1:]
	}
	if input == "" {
		return Decimal{}, errors.New("decimal has no digits")
	}
	integer, fraction, hasFraction := strings.Cut(input, ".")
	if integer == "" || (hasFraction && fraction == "") || strings.Contains(fraction, ".") || !asciiDigits(integer) || !asciiDigits(fraction) {
		return Decimal{}, errors.New("decimal must contain base-10 digits with an optional fraction")
	}
	digits := strings.TrimLeft(integer+fraction, "0")
	scale := len(fraction)
	for len(digits) > 0 && scale > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		scale--
	}
	if digits == "" {
		return Decimal{}, nil
	}
	if len(digits) > 18 || scale > 18 {
		return Decimal{}, errors.New("decimal exceeds portable precision or scale 18")
	}
	if negative {
		digits = "-" + digits
	}
	coefficient, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Decimal{}, fmt.Errorf("invalid decimal: %w", err)
	}
	return NewDecimal(coefficient, uint8(scale))
}

func (value Decimal) Coefficient() int64 { return value.coefficient }
func (value Decimal) Scale() uint8       { return value.scale }

func (value Decimal) String() string {
	coefficient := strconv.FormatInt(value.coefficient, 10)
	negative := strings.HasPrefix(coefficient, "-")
	digits := strings.TrimPrefix(coefficient, "-")
	if value.scale == 0 {
		return coefficient
	}
	width := int(value.scale)
	if len(digits) <= width {
		digits = strings.Repeat("0", width-len(digits)+1) + digits
	}
	point := len(digits) - width
	if negative {
		return "-" + digits[:point] + "." + digits[point:]
	}
	return digits[:point] + "." + digits[point:]
}

// NewDate constructs a date in the portable ISO calendar range 0001-9999.
func NewDate(year int, month time.Month, day int) (Date, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December || day < 1 || day > 31 {
		return Date{}, errors.New("date is outside the portable Gregorian range")
	}
	check := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if check.Year() != year || check.Month() != month || check.Day() != day {
		return Date{}, errors.New("date is not a real Gregorian calendar date")
	}
	return Date{year: int16(year), month: uint8(month), day: uint8(day)}, nil
}

func ParseDate(input string) (Date, error) {
	if len(input) != len("0000-00-00") || input[4] != '-' || input[7] != '-' || !asciiDigits(input[:4]) || !asciiDigits(input[5:7]) || !asciiDigits(input[8:]) {
		return Date{}, errors.New("date must use YYYY-MM-DD")
	}
	year, _ := strconv.Atoi(input[:4])
	month, _ := strconv.Atoi(input[5:7])
	day, _ := strconv.Atoi(input[8:])
	return NewDate(year, time.Month(month), day)
}

func DateFromTime(value time.Time) (Date, error) {
	year, month, day := value.Date()
	return NewDate(year, month, day)
}

func (value Date) Year() int         { return int(value.year) }
func (value Date) Month() time.Month { return time.Month(value.month) }
func (value Date) Day() int          { return int(value.day) }
func (value Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", value.year, value.month, value.day)
}

// NewTime constructs a wall-clock time at exact microsecond precision.
func NewTime(hour, minute, second, microsecond int) (Time, error) {
	if hour < 0 || hour >= 24 || minute < 0 || minute >= 60 || second < 0 || second >= 60 || microsecond < 0 || microsecond >= 1_000_000 {
		return Time{}, errors.New("time is outside [00:00:00, 24:00:00)")
	}
	total := int64(hour)*3_600_000_000 + int64(minute)*60_000_000 + int64(second)*1_000_000 + int64(microsecond)
	return Time{microseconds: total}, nil
}

func ParseTime(input string) (Time, error) {
	clock, fraction, hasFraction := strings.Cut(input, ".")
	if len(clock) != len("00:00:00") || clock[2] != ':' || clock[5] != ':' || !asciiDigits(clock[:2]) || !asciiDigits(clock[3:5]) || !asciiDigits(clock[6:]) {
		return Time{}, errors.New("time must use HH:MM:SS with an optional microsecond fraction")
	}
	microsecond := 0
	if hasFraction {
		if len(fraction) < 1 || len(fraction) > 6 || !asciiDigits(fraction) {
			return Time{}, errors.New("time fraction must contain one to six digits")
		}
		fraction += strings.Repeat("0", 6-len(fraction))
		microsecond, _ = strconv.Atoi(fraction)
	}
	hour, _ := strconv.Atoi(clock[:2])
	minute, _ := strconv.Atoi(clock[3:5])
	second, _ := strconv.Atoi(clock[6:])
	return NewTime(hour, minute, second, microsecond)
}

func (value Time) Microseconds() int64 { return value.microseconds }

func (value Time) Clock() (hour, minute, second, microsecond int) {
	remainder := value.microseconds
	hour = int(remainder / 3_600_000_000)
	remainder %= 3_600_000_000
	minute = int(remainder / 60_000_000)
	remainder %= 60_000_000
	second = int(remainder / 1_000_000)
	microsecond = int(remainder % 1_000_000)
	return
}

func (value Time) String() string {
	hour, minute, second, microsecond := value.Clock()
	base := fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
	if microsecond == 0 {
		return base
	}
	return base + "." + strings.TrimRight(fmt.Sprintf("%06d", microsecond), "0")
}

// JSONPath is an immutable provider-neutral path. Its zero value denotes no
// authored path and is not a spelling for the document root.
type JSONPath struct {
	segments []jsonPathSegmentValue
	present  bool
}

type JSONPathSegment interface {
	jsonPathSegment()
	Key() (string, bool)
	Index() (uint32, bool)
}

type jsonPathSegmentValue struct {
	key     string
	index   uint32
	isIndex bool
	valid   bool
}

func (jsonPathSegmentValue) jsonPathSegment() {}
func (segment jsonPathSegmentValue) Key() (string, bool) {
	return segment.key, segment.valid && !segment.isIndex
}
func (segment jsonPathSegmentValue) Index() (uint32, bool) {
	return segment.index, segment.valid && segment.isIndex
}

func JSONKey(key string) JSONPathSegment {
	return jsonPathSegmentValue{key: key, valid: utf8.ValidString(key)}
}

func JSONIndex(index uint32) JSONPathSegment {
	return jsonPathSegmentValue{index: index, isIndex: true, valid: true}
}

func NewJSONPath(first JSONPathSegment, rest ...JSONPathSegment) JSONPath {
	input := append([]JSONPathSegment{first}, rest...)
	segments := make([]jsonPathSegmentValue, len(input))
	for index, segment := range input {
		segments[index] = copyPathSegment(segment)
	}
	return JSONPath{segments: segments, present: true}
}

func (path JSONPath) Segments() []JSONPathSegment {
	output := make([]JSONPathSegment, len(path.segments))
	for index, segment := range path.segments {
		output[index] = segment
	}
	return output
}

func (path JSONPath) Valid() bool {
	if !path.present || len(path.segments) == 0 {
		return false
	}
	for _, segment := range path.segments {
		if !segment.valid {
			return false
		}
	}
	return true
}

func jsonRootPath() JSONPath { return JSONPath{present: true} }

type JSONValue interface{ jsonValue() }

type JSONScalarValue interface {
	JSONValue
	jsonScalarValue()
}

type JSONOrderedValue interface {
	JSONScalarValue
	jsonOrderedValue()
}

type JSONEqualityOperand interface{ jsonEqualityOperand() }

type JSONStringValue struct {
	value string
	valid bool
}

type JSONNumberValue struct {
	negative    bool
	coefficient string
	exponent    int32
	valid       bool
}

type JSONBoolValue struct {
	value bool
	valid bool
}

type JSONArrayValue struct {
	values []jsonValueData
	valid  bool
}

type JSONObjectValue struct {
	members []jsonMember
	valid   bool
}

type dbNullSentinel uint8
type jsonNullSentinel uint8
type anyNullSentinel uint8

const (
	DBNull   dbNullSentinel   = 1
	JSONNull jsonNullSentinel = 1
	AnyNull  anyNullSentinel  = 1
)

func (JSONStringValue) jsonValue()            {}
func (JSONStringValue) jsonScalarValue()      {}
func (JSONStringValue) jsonOrderedValue()     {}
func (JSONStringValue) jsonEqualityOperand()  {}
func (JSONNumberValue) jsonValue()            {}
func (JSONNumberValue) jsonScalarValue()      {}
func (JSONNumberValue) jsonOrderedValue()     {}
func (JSONNumberValue) jsonEqualityOperand()  {}
func (JSONBoolValue) jsonValue()              {}
func (JSONBoolValue) jsonScalarValue()        {}
func (JSONBoolValue) jsonEqualityOperand()    {}
func (JSONArrayValue) jsonValue()             {}
func (JSONArrayValue) jsonEqualityOperand()   {}
func (JSONObjectValue) jsonValue()            {}
func (JSONObjectValue) jsonEqualityOperand()  {}
func (jsonNullSentinel) jsonValue()           {}
func (jsonNullSentinel) jsonScalarValue()     {}
func (jsonNullSentinel) jsonEqualityOperand() {}
func (dbNullSentinel) jsonEqualityOperand()   {}
func (anyNullSentinel) jsonEqualityOperand()  {}

func JSONString(value string) JSONStringValue {
	return JSONStringValue{value: value, valid: utf8.ValidString(value)}
}

func (value JSONStringValue) Value() string { return value.value }
func (value JSONStringValue) Valid() bool   { return value.valid }

func JSONNumber(value Decimal) JSONNumberValue {
	coefficient := value.coefficient
	negative := coefficient < 0
	digits := strconv.FormatInt(coefficient, 10)
	digits = strings.TrimPrefix(digits, "-")
	return normalizeJSONNumber(negative, digits, -int64(value.scale))
}

func ParseJSONNumber(input string) (JSONNumberValue, error) {
	if !validJSONNumberSyntax(input) {
		return JSONNumberValue{}, errors.New("invalid JSON number")
	}
	negative := strings.HasPrefix(input, "-")
	unsigned := strings.TrimPrefix(input, "-")
	mantissa, exponentText, hasExponent := cutExponent(unsigned)
	exponent := int64(0)
	if hasExponent {
		parsed, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return JSONNumberValue{}, errors.New("JSON number exponent is outside int32 range")
		}
		exponent = parsed
	}
	integer, fraction, _ := strings.Cut(mantissa, ".")
	if int64(len(fraction)) > int64(^uint32(0)>>1) {
		return JSONNumberValue{}, errors.New("JSON number fraction is too long")
	}
	exponent -= int64(len(fraction))
	value := normalizeJSONNumber(negative, integer+fraction, exponent)
	if !value.valid {
		return JSONNumberValue{}, errors.New("normalized JSON number exponent is outside int32 range")
	}
	return value, nil
}

func (value JSONNumberValue) Canonical() string {
	if !value.valid {
		return ""
	}
	prefix := ""
	if value.negative {
		prefix = "-"
	}
	if value.exponent == 0 {
		return prefix + value.coefficient
	}
	return prefix + value.coefficient + "e" + strconv.FormatInt(int64(value.exponent), 10)
}

func (value JSONNumberValue) Parts() (negative bool, coefficient string, exponent int32, ok bool) {
	return value.negative, value.coefficient, value.exponent, value.valid
}

func (value JSONNumberValue) Valid() bool { return value.valid }
func (value JSONBoolValue) Value() bool   { return value.value }
func (value JSONBoolValue) Valid() bool   { return value.valid }

func JSONBool(value bool) JSONBoolValue { return JSONBoolValue{value: value, valid: true} }

func JSONArray(values ...JSONValue) JSONArrayValue {
	output := JSONArrayValue{values: make([]jsonValueData, len(values)), valid: true}
	for index, value := range values {
		data, ok := copyJSONValue(value)
		if !ok {
			output.valid = false
			continue
		}
		output.values[index] = data
	}
	return output
}

func (value JSONArrayValue) Values() []JSONValue {
	output := make([]JSONValue, len(value.values))
	for index, element := range value.values {
		output[index] = publicJSONValue(element.clone())
	}
	return output
}

func (value JSONArrayValue) Valid() bool { return value.valid && validJSONValues(value.values) }

func JSONObject(values map[string]JSONValue) JSONObjectValue {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := JSONObjectValue{members: make([]jsonMember, 0, len(keys)), valid: true}
	for _, key := range keys {
		if !utf8.ValidString(key) {
			output.valid = false
		}
		value, ok := copyJSONValue(values[key])
		if !ok {
			output.valid = false
		}
		output.members = append(output.members, jsonMember{key: key, value: value})
	}
	return output
}

func (value JSONObjectValue) Values() map[string]JSONValue {
	output := make(map[string]JSONValue, len(value.members))
	for _, member := range value.members {
		output[member.key] = publicJSONValue(member.value.clone())
	}
	return output
}

func (value JSONObjectValue) Valid() bool { return value.valid && validJSONMembers(value.members) }

func ParseJSON(input []byte) (JSONValue, error) {
	if !utf8.Valid(input) {
		return nil, errors.New("JSON input is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(append([]byte(nil), input...)))
	decoder.UseNumber()
	value, err := decodeExactJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON input has trailing data")
		}
		return nil, fmt.Errorf("JSON trailing data: %w", err)
	}
	return publicJSONValue(value), nil
}

// CanonicalJSON returns a fresh canonical JSON encoding of value.
func CanonicalJSON(value JSONValue) ([]byte, error) {
	data, ok := copyJSONValue(value)
	if !ok {
		return nil, errors.New("invalid or zero JSON value")
	}
	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, data); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// NewJSONDocument validates and canonicalizes one persisted JSON document for
// a generated JSON field. T is a compile-time application witness; decoding
// remains exact and does not pass through float64.
func NewJSONDocument[T any](input []byte) (JSON[T], error) {
	value, err := ParseJSON(input)
	if err != nil {
		return JSON[T]{}, err
	}
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return JSON[T]{}, err
	}
	return JSON[T]{raw: canonical}, nil
}

func (value JSON[T]) Bytes() []byte { return append([]byte(nil), value.raw...) }

type jsonKind uint8

const (
	jsonKindNull jsonKind = iota + 1
	jsonKindBool
	jsonKindNumber
	jsonKindString
	jsonKindArray
	jsonKindObject
)

type jsonMember struct {
	key   string
	value jsonValueData
}

type jsonValueData struct {
	kind    jsonKind
	boolean bool
	number  JSONNumberValue
	text    string
	array   []jsonValueData
	object  []jsonMember
}

func (value jsonValueData) clone() jsonValueData {
	output := value
	output.array = make([]jsonValueData, len(value.array))
	for index, element := range value.array {
		output.array[index] = element.clone()
	}
	output.object = make([]jsonMember, len(value.object))
	for index, member := range value.object {
		output.object[index] = jsonMember{key: member.key, value: member.value.clone()}
	}
	return output
}

func copyJSONValue(value JSONValue) (jsonValueData, bool) {
	switch value := value.(type) {
	case JSONStringValue:
		return jsonValueData{kind: jsonKindString, text: value.value}, value.valid
	case *JSONStringValue:
		if value == nil {
			return jsonValueData{}, false
		}
		return copyJSONValue(*value)
	case JSONNumberValue:
		return jsonValueData{kind: jsonKindNumber, number: value}, value.valid
	case *JSONNumberValue:
		if value == nil {
			return jsonValueData{}, false
		}
		return copyJSONValue(*value)
	case JSONBoolValue:
		return jsonValueData{kind: jsonKindBool, boolean: value.value}, value.valid
	case *JSONBoolValue:
		if value == nil {
			return jsonValueData{}, false
		}
		return copyJSONValue(*value)
	case JSONArrayValue:
		return jsonValueData{kind: jsonKindArray, array: cloneJSONValues(value.values)}, value.Valid()
	case *JSONArrayValue:
		if value == nil {
			return jsonValueData{}, false
		}
		return copyJSONValue(*value)
	case JSONObjectValue:
		return jsonValueData{kind: jsonKindObject, object: cloneJSONMembers(value.members)}, value.Valid()
	case *JSONObjectValue:
		if value == nil {
			return jsonValueData{}, false
		}
		return copyJSONValue(*value)
	case jsonNullSentinel:
		return jsonValueData{kind: jsonKindNull}, value == JSONNull
	default:
		return jsonValueData{}, false
	}
}

func publicJSONValue(value jsonValueData) JSONValue {
	switch value.kind {
	case jsonKindNull:
		return JSONNull
	case jsonKindBool:
		return JSONBool(value.boolean)
	case jsonKindNumber:
		return value.number
	case jsonKindString:
		return JSONString(value.text)
	case jsonKindArray:
		return JSONArrayValue{values: cloneJSONValues(value.array), valid: true}
	case jsonKindObject:
		return JSONObjectValue{members: cloneJSONMembers(value.object), valid: true}
	default:
		return nil
	}
}

func decodeExactJSON(decoder *json.Decoder) (jsonValueData, error) {
	token, err := decoder.Token()
	if err != nil {
		return jsonValueData{}, fmt.Errorf("invalid JSON: %w", err)
	}
	switch token := token.(type) {
	case nil:
		return jsonValueData{kind: jsonKindNull}, nil
	case bool:
		return jsonValueData{kind: jsonKindBool, boolean: token}, nil
	case string:
		return jsonValueData{kind: jsonKindString, text: token}, nil
	case json.Number:
		number, err := ParseJSONNumber(string(token))
		if err != nil {
			return jsonValueData{}, err
		}
		return jsonValueData{kind: jsonKindNumber, number: number}, nil
	case json.Delim:
		switch token {
		case '[':
			values := make([]jsonValueData, 0)
			for decoder.More() {
				value, err := decodeExactJSON(decoder)
				if err != nil {
					return jsonValueData{}, err
				}
				values = append(values, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return jsonValueData{}, errors.New("invalid JSON array")
			}
			return jsonValueData{kind: jsonKindArray, array: values}, nil
		case '{':
			seen := make(map[string]struct{})
			members := make([]jsonMember, 0)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return jsonValueData{}, fmt.Errorf("invalid JSON object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return jsonValueData{}, errors.New("invalid JSON object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return jsonValueData{}, fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				value, err := decodeExactJSON(decoder)
				if err != nil {
					return jsonValueData{}, err
				}
				members = append(members, jsonMember{key: key, value: value})
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return jsonValueData{}, errors.New("invalid JSON object")
			}
			sort.Slice(members, func(left, right int) bool { return members[left].key < members[right].key })
			return jsonValueData{kind: jsonKindObject, object: members}, nil
		}
	}
	return jsonValueData{}, errors.New("invalid JSON token")
}

func writeCanonicalJSON(output *bytes.Buffer, value jsonValueData) error {
	switch value.kind {
	case jsonKindNull:
		output.WriteString("null")
	case jsonKindBool:
		output.WriteString(strconv.FormatBool(value.boolean))
	case jsonKindNumber:
		if !value.number.valid {
			return errors.New("invalid JSON number")
		}
		output.WriteString(value.number.Canonical())
	case jsonKindString:
		encoded, err := json.Marshal(value.text)
		if err != nil {
			return err
		}
		output.Write(encoded)
	case jsonKindArray:
		output.WriteByte('[')
		for index, element := range value.array {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, element); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case jsonKindObject:
		output.WriteByte('{')
		for index, member := range value.object {
			if index > 0 {
				output.WriteByte(',')
			}
			key, _ := json.Marshal(member.key)
			output.Write(key)
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, member.value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return errors.New("invalid JSON value kind")
	}
	return nil
}

func normalizeJSONNumber(negative bool, digits string, exponent int64) JSONNumberValue {
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return JSONNumberValue{coefficient: "0", valid: true}
	}
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exponent++
	}
	if exponent < -1<<31 || exponent > 1<<31-1 {
		return JSONNumberValue{}
	}
	return JSONNumberValue{negative: negative, coefficient: digits, exponent: int32(exponent), valid: true}
}

func validJSONNumberSyntax(input string) bool {
	if input == "" {
		return false
	}
	index := 0
	if input[index] == '-' {
		index++
		if index == len(input) {
			return false
		}
	}
	if input[index] == '0' {
		index++
		if index < len(input) && input[index] >= '0' && input[index] <= '9' {
			return false
		}
	} else if input[index] >= '1' && input[index] <= '9' {
		for index < len(input) && input[index] >= '0' && input[index] <= '9' {
			index++
		}
	} else {
		return false
	}
	if index < len(input) && input[index] == '.' {
		index++
		start := index
		for index < len(input) && input[index] >= '0' && input[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	if index < len(input) && (input[index] == 'e' || input[index] == 'E') {
		index++
		if index < len(input) && (input[index] == '+' || input[index] == '-') {
			index++
		}
		start := index
		for index < len(input) && input[index] >= '0' && input[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	return index == len(input)
}

func cutExponent(input string) (mantissa, exponent string, ok bool) {
	if index := strings.IndexAny(input, "eE"); index >= 0 {
		return input[:index], input[index+1:], true
	}
	return input, "", false
}

func copyPathSegment(segment JSONPathSegment) jsonPathSegmentValue {
	switch segment := segment.(type) {
	case jsonPathSegmentValue:
		return segment
	case *jsonPathSegmentValue:
		if segment != nil {
			return *segment
		}
	}
	return jsonPathSegmentValue{}
}

func cloneJSONValues(values []jsonValueData) []jsonValueData {
	output := make([]jsonValueData, len(values))
	for index, value := range values {
		output[index] = value.clone()
	}
	return output
}

func cloneJSONMembers(members []jsonMember) []jsonMember {
	output := make([]jsonMember, len(members))
	for index, member := range members {
		output[index] = jsonMember{key: member.key, value: member.value.clone()}
	}
	return output
}

func validJSONValues(values []jsonValueData) bool {
	for _, value := range values {
		if _, ok := copyJSONValue(publicJSONValue(value)); !ok {
			return false
		}
	}
	return true
}

func validJSONMembers(members []jsonMember) bool {
	for _, member := range members {
		if !utf8.ValidString(member.key) {
			return false
		}
		if _, ok := copyJSONValue(publicJSONValue(member.value)); !ok {
			return false
		}
	}
	return true
}

func asciiDigits(input string) bool {
	if input == "" {
		return true
	}
	for index := range input {
		if input[index] < '0' || input[index] > '9' {
			return false
		}
	}
	return true
}

func magnitude(value int64) uint64 {
	if value >= 0 {
		return uint64(value)
	}
	return uint64(-(value + 1)) + 1
}

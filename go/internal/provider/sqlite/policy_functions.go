package sqlite

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	compilerscalar "github.com/eleven-am/golem/go/internal/compiler/scalar"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

const (
	policyASCIIFoldFunction = "golem_policy_ascii_fold"
	policyListFunction      = "golem_policy_list"
	policyJSONFunction      = "golem_policy_json"
)

func sqliteASCIIFold(arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 1 {
		return nil, fmt.Errorf("%s: arity", policyASCIIFoldFunction)
	}
	if arguments[0] == nil {
		return nil, nil
	}
	value, ok := arguments[0].(string)
	if !ok {
		return nil, fmt.Errorf("%s: expected TEXT", policyASCIIFoldFunction)
	}
	return foldPolicyASCII(value), nil
}

func sqlitePolicyList(arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 4 {
		return nil, fmt.Errorf("%s: arity", policyListFunction)
	}
	document, ok := driverText(arguments[0])
	if !ok {
		return int64(0), nil
	}
	wanted, ok := driverText(arguments[1])
	if !ok {
		return int64(0), nil
	}
	typeText, kindOK := driverText(arguments[2])
	operatorNumber, operatorOK := arguments[3].(int64)
	if !kindOK || !operatorOK {
		return int64(0), nil
	}
	typ, typeOK := parsePolicyListType(typeText)
	if !typeOK {
		return int64(0), nil
	}
	kind, operator := typ.kind, ir.OperatorID(operatorNumber)
	decoded, err := parseExactJSON(document)
	if err != nil {
		return int64(0), nil
	}
	values, ok := decoded.([]any)
	if !ok {
		return int64(0), nil
	}

	matched := false
	switch operator {
	case ir.OperatorListEqual:
		candidate, err := parseExactJSON(wanted)
		if err != nil {
			return int64(0), nil
		}
		wantedValues, ok := candidate.([]any)
		matched = ok && typedListEqual(values, wantedValues, typ)
	case ir.OperatorListHas:
		if kind == ir.ValueEnum {
			matched = typedListHas(values, wanted, typ)
		} else {
			candidate, err := parseExactJSON(wanted)
			matched = err == nil && typedListHas(values, candidate, typ)
		}
	case ir.OperatorListHasEvery, ir.OperatorListHasSome:
		candidate, err := parseExactJSON(wanted)
		if err != nil {
			return int64(0), nil
		}
		wantedValues, ok := candidate.([]any)
		if !ok {
			return int64(0), nil
		}
		matched = operator == ir.OperatorListHasEvery
		for _, item := range wantedValues {
			present := typedListHas(values, item, typ)
			if operator == ir.OperatorListHasEvery && !present {
				matched = false
				break
			}
			if operator == ir.OperatorListHasSome && present {
				matched = true
				break
			}
		}
	case ir.OperatorListIsEmpty:
		wantEmpty, err := strconv.ParseBool(wanted)
		// Presence and array shape are the complete contract for IsEmpty. Stored
		// malformed elements still occupy array slots; their value type matters
		// only to element-comparison operators.
		matched = err == nil && (len(values) == 0) == wantEmpty
	default:
		return int64(0), nil
	}
	return policyBool(matched), nil
}

func sqlitePolicyJSON(arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 6 {
		return nil, fmt.Errorf("%s: arity", policyJSONFunction)
	}
	pathText, pathOK := driverText(arguments[1])
	operandText, operandOK := driverText(arguments[2])
	operatorNumber, opOK := arguments[3].(int64)
	modeNumber, modeOK := arguments[4].(int64)
	operandKindNumber, operandKindOK := arguments[5].(int64)
	if !pathOK || !operandOK || !opOK || !modeOK || !operandKindOK {
		return int64(0), nil
	}
	operator := ir.OperatorID(operatorNumber)
	mode := ir.ComparisonMode(modeNumber)

	slot := exactJSONSlot{}
	if document, ok := driverText(arguments[0]); ok {
		decoded, err := parseExactJSON(document)
		if err != nil {
			return int64(0), nil
		}
		slot = exactJSONSlot{present: true, value: decoded}
	}
	path, err := decodePolicyPath(pathText)
	if err != nil {
		return int64(0), nil
	}
	slot = navigateExactJSON(slot, path)

	if ir.OperandKind(operandKindNumber) == ir.OperandJSONNull {
		nullNumber, err := strconv.Atoi(operandText)
		if err != nil {
			return int64(0), nil
		}
		nullKind := ir.JSONNullKind(nullNumber)
		matched := jsonNullSlotMatches(slot, nullKind)
		if operator == ir.OperatorJSONNotEqual {
			switch nullKind {
			case ir.JSONDbNull:
				matched = slot.present
			case ir.JSONDocumentNull, ir.JSONAnyNull:
				matched = slot.present && slot.value != nil
			default:
				matched = false
			}
		}
		return policyBool(matched), nil
	}
	wanted, err := parseExactJSON(operandText)
	if err != nil || !slot.present {
		return int64(0), nil
	}

	matched := false
	switch operator {
	case ir.OperatorJSONEqual:
		matched = sameJSONKind(slot.value, wanted) && exactJSONEqual(slot.value, wanted)
	case ir.OperatorJSONNotEqual:
		matched = sameJSONKind(slot.value, wanted) && !exactJSONEqual(slot.value, wanted)
	case ir.OperatorJSONLessThan, ir.OperatorJSONLessThanOrEqual, ir.OperatorJSONGreaterThan, ir.OperatorJSONGreaterThanOrEqual:
		comparison, comparable := compareExactJSON(slot.value, wanted, mode)
		if comparable {
			matched = operator == ir.OperatorJSONLessThan && comparison < 0 ||
				operator == ir.OperatorJSONLessThanOrEqual && comparison <= 0 ||
				operator == ir.OperatorJSONGreaterThan && comparison > 0 ||
				operator == ir.OperatorJSONGreaterThanOrEqual && comparison >= 0
		}
	case ir.OperatorJSONStringContains, ir.OperatorJSONStringStartsWith, ir.OperatorJSONStringEndsWith:
		left, leftOK := slot.value.(string)
		right, rightOK := wanted.(string)
		if leftOK && rightOK {
			if mode == ir.ComparisonASCIIInsensitive {
				left, right = foldPolicyASCII(left), foldPolicyASCII(right)
			}
			matched = operator == ir.OperatorJSONStringContains && strings.Contains(left, right) ||
				operator == ir.OperatorJSONStringStartsWith && strings.HasPrefix(left, right) ||
				operator == ir.OperatorJSONStringEndsWith && strings.HasSuffix(left, right)
		}
	case ir.OperatorJSONArrayContains:
		array, ok := slot.value.([]any)
		if ok {
			switch wanted.(type) {
			case []any, map[string]any:
				matched = exactJSONContains(slot.value, wanted)
			default:
				for _, item := range array {
					if exactJSONContains(item, wanted) {
						matched = true
						break
					}
				}
			}
		}
	case ir.OperatorJSONArrayStartsWith, ir.OperatorJSONArrayEndsWith:
		array, ok := slot.value.([]any)
		if ok && len(array) > 0 {
			index := 0
			if operator == ir.OperatorJSONArrayEndsWith {
				index = len(array) - 1
			}
			matched = exactJSONEqual(array[index], wanted)
		}
	}
	return policyBool(matched), nil
}

func parseExactJSON(input string) (any, error) {
	canonical, err := compilerscalar.CanonicalJSON([]byte(input))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("trailing JSON")
	}
	return err
}

type pathPart struct {
	key   *string
	index *uint64
}

func decodePolicyPath(input string) ([]pathPart, error) {
	decoded, err := parseExactJSON(input)
	if err != nil {
		return nil, err
	}
	entries, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("path is not an array")
	}
	result := make([]pathPart, len(entries))
	for index, entry := range entries {
		pair, ok := entry.([]any)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("invalid path segment")
		}
		kind, ok := pair[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid path segment kind")
		}
		switch kind {
		case "k":
			key, ok := pair[1].(string)
			if !ok {
				return nil, fmt.Errorf("invalid path key")
			}
			result[index].key = &key
		case "i":
			number, ok := pair[1].(json.Number)
			if !ok {
				return nil, fmt.Errorf("invalid path index")
			}
			value, err := strconv.ParseUint(string(number), 10, 64)
			if err != nil {
				return nil, err
			}
			result[index].index = &value
		default:
			return nil, fmt.Errorf("unknown path segment")
		}
	}
	return result, nil
}

type exactJSONSlot struct {
	present bool
	value   any
}

func navigateExactJSON(slot exactJSONSlot, path []pathPart) exactJSONSlot {
	for _, segment := range path {
		if !slot.present || slot.value == nil {
			return exactJSONSlot{}
		}
		if segment.key != nil {
			object, ok := slot.value.(map[string]any)
			if !ok {
				return exactJSONSlot{}
			}
			value, exists := object[*segment.key]
			if !exists {
				return exactJSONSlot{}
			}
			slot.value = value
		} else {
			array, ok := slot.value.([]any)
			if !ok || *segment.index >= uint64(len(array)) {
				return exactJSONSlot{}
			}
			slot.value = array[*segment.index]
		}
	}
	return slot
}

func jsonNullSlotMatches(slot exactJSONSlot, kind ir.JSONNullKind) bool {
	switch kind {
	case ir.JSONDbNull:
		return !slot.present
	case ir.JSONDocumentNull:
		return slot.present && slot.value == nil
	case ir.JSONAnyNull:
		return !slot.present || slot.value == nil
	default:
		return false
	}
}

type policyListType struct {
	kind             ir.ValueKind
	precision, scale uint16
}

func parsePolicyListType(value string) (policyListType, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return policyListType{}, false
	}
	kind, err1 := strconv.ParseUint(parts[0], 10, 8)
	precision, err2 := strconv.ParseUint(parts[1], 10, 16)
	scale, err3 := strconv.ParseUint(parts[2], 10, 16)
	if err1 != nil || err2 != nil || err3 != nil {
		return policyListType{}, false
	}
	return policyListType{kind: ir.ValueKind(kind), precision: uint16(precision), scale: uint16(scale)}, true
}

func typedListEqual(left, right []any, typ policyListType) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !typedListElementEqual(left[index], right[index], typ) {
			return false
		}
	}
	return true
}
func typedListHas(values []any, wanted any, typ policyListType) bool {
	if !validTypedElement(wanted, typ) {
		return false
	}
	for _, value := range values {
		if typedListElementEqual(value, wanted, typ) {
			return true
		}
	}
	return false
}
func typedListElementEqual(left, right any, typ policyListType) bool {
	if !validTypedElement(left, typ) || !validTypedElement(right, typ) {
		return false
	}
	switch typ.kind {
	case ir.ValueFloat32:
		l, _ := strconv.ParseFloat(string(left.(json.Number)), 32)
		r, _ := strconv.ParseFloat(string(right.(json.Number)), 32)
		return math.Float32bits(float32(l)) == math.Float32bits(float32(r))
	case ir.ValueFloat64:
		l, _ := strconv.ParseFloat(string(left.(json.Number)), 64)
		r, _ := strconv.ParseFloat(string(right.(json.Number)), 64)
		return math.Float64bits(l) == math.Float64bits(r)
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueDecimal:
		return compareJSONNumbers(left.(json.Number), right.(json.Number)) == 0
	default:
		return exactJSONEqual(left, right)
	}
}

func validTypedElement(value any, typ policyListType) bool {
	switch typ.kind {
	case ir.ValueBool:
		_, ok := value.(bool)
		return ok
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		integer, ok := exactInteger(number)
		if !ok {
			return false
		}
		bits := map[ir.ValueKind]int{ir.ValueInt16: 16, ir.ValueInt32: 32, ir.ValueInt64: 64}[typ.kind]
		return integer.IsInt64() && (bits == 64 || integer.Cmp(new(big.Int).Lsh(big.NewInt(1), uint(bits-1))) < 0 && integer.Cmp(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), uint(bits-1)))) >= 0)
	case ir.ValueFloat32:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseFloat(string(number), 32)
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	case ir.ValueFloat64:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseFloat(string(number), 64)
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	case ir.ValueDecimal:
		number, ok := value.(json.Number)
		return ok && validListDecimal(number, typ.precision, typ.scale)
	case ir.ValueString, ir.ValueEnum:
		_, ok := value.(string)
		return ok
	case ir.ValueUUID:
		text, ok := value.(string)
		return ok && validPolicyUUID(text)
	case ir.ValueDate:
		text, ok := value.(string)
		if !ok {
			return false
		}
		parsed, err := time.Parse("2006-01-02", text)
		return err == nil && parsed.Format("2006-01-02") == text
	case ir.ValueTime:
		text, ok := value.(string)
		return ok && validPolicyTime(text, typ.precision)
	case ir.ValueDateTime:
		text, ok := value.(string)
		return ok && validPolicyDateTime(text, typ.precision)
	default:
		return false
	}
}

func validListDecimal(number json.Number, precision, scale uint16) bool {
	coefficient, exponent, _, ok := numberParts(string(number))
	if !ok || precision == 0 || scale > precision {
		return false
	}
	exponent.Add(exponent, big.NewInt(int64(scale)))
	if exponent.Sign() < 0 || !exponent.IsInt64() {
		return false
	}
	return len(coefficient)+int(exponent.Int64()) <= int(precision)
}

func validPolicyUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := 0; index < len(value); index++ {
		current := value[index]
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if current != '-' {
				return false
			}
			continue
		}
		if !(current >= '0' && current <= '9' || current >= 'a' && current <= 'f') {
			return false
		}
	}
	return true
}

func validPolicyTime(value string, precision uint16) bool {
	layout := "15:04:05"
	if precision > 0 {
		layout += "." + strings.Repeat("0", int(precision))
	}
	parsed, err := time.Parse(layout, value)
	return err == nil && parsed.Format(layout) == value
}

func validPolicyDateTime(value string, precision uint16) bool {
	layout := "2006-01-02T15:04:05"
	if precision > 0 {
		layout += "." + strings.Repeat("0", int(precision))
	}
	layout += "Z"
	parsed, err := time.Parse(layout, value)
	return err == nil && parsed.UTC().Format(layout) == value
}

func exactInteger(number json.Number) (*big.Int, bool) {
	coefficient, exponent, negative, ok := numberParts(string(number))
	if !ok {
		return nil, false
	}
	if exponent.Sign() < 0 {
		return nil, false
	}
	if !exponent.IsInt64() || exponent.Int64() > 1_000_000 {
		return nil, false
	}
	value := new(big.Int)
	value.SetString(coefficient, 10)
	value.Mul(value, new(big.Int).Exp(big.NewInt(10), exponent, nil))
	if negative {
		value.Neg(value)
	}
	return value, true
}

func sameJSONKind(left, right any) bool { return jsonType(left) == jsonType(right) }
func jsonType(value any) byte {
	switch value.(type) {
	case nil:
		return 1
	case bool:
		return 2
	case json.Number:
		return 3
	case string:
		return 4
	case []any:
		return 5
	case map[string]any:
		return 6
	default:
		return 0
	}
}
func exactJSONEqual(left, right any) bool {
	if !sameJSONKind(left, right) {
		return false
	}
	switch l := left.(type) {
	case nil:
		return true
	case bool:
		return l == right.(bool)
	case string:
		return l == right.(string)
	case json.Number:
		return compareJSONNumbers(l, right.(json.Number)) == 0
	case []any:
		r := right.([]any)
		if len(l) != len(r) {
			return false
		}
		for index := range l {
			if !exactJSONEqual(l[index], r[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		r := right.(map[string]any)
		if len(l) != len(r) {
			return false
		}
		for key, value := range l {
			other, ok := r[key]
			if !ok || !exactJSONEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func compareExactJSON(left, right any, mode ir.ComparisonMode) (int, bool) {
	if !sameJSONKind(left, right) {
		return 0, false
	}
	switch l := left.(type) {
	case json.Number:
		return compareJSONNumbers(l, right.(json.Number)), true
	case string:
		r := right.(string)
		if mode == ir.ComparisonASCIIInsensitive {
			l, r = foldPolicyASCII(l), foldPolicyASCII(r)
		}
		return strings.Compare(l, r), true
	default:
		return 0, false
	}
}

func compareJSONNumbers(left, right json.Number) int {
	lc, le, ln, lok := numberParts(string(left))
	rc, re, rn, rok := numberParts(string(right))
	if !lok || !rok {
		return 0
	}
	if lc == "0" || rc == "0" {
		if lc == "0" && rc == "0" {
			return 0
		}
		if lc == "0" {
			if rn {
				return 1
			}
			return -1
		}
		if ln {
			return -1
		}
		return 1
	}
	if ln != rn {
		if ln {
			return -1
		}
		return 1
	}
	lm := new(big.Int).Add(big.NewInt(int64(len(lc))), le)
	rm := new(big.Int).Add(big.NewInt(int64(len(rc))), re)
	result := lm.Cmp(rm)
	if result == 0 {
		length := len(lc)
		if len(rc) > length {
			length = len(rc)
		}
		for index := 0; index < length; index++ {
			ld, rd := byte('0'), byte('0')
			if index < len(lc) {
				ld = lc[index]
			}
			if index < len(rc) {
				rd = rc[index]
			}
			if ld < rd {
				result = -1
				break
			}
			if ld > rd {
				result = 1
				break
			}
		}
	}
	if ln {
		return -result
	}
	return result
}

func numberParts(input string) (coefficient string, exponent *big.Int, negative bool, ok bool) {
	negative = strings.HasPrefix(input, "-")
	if negative {
		input = input[1:]
	}
	parts := strings.Split(input, "e")
	if len(parts) > 2 || len(parts) == 0 {
		return "", nil, false, false
	}
	coefficient = parts[0]
	exponent = new(big.Int)
	if len(parts) == 2 {
		if _, ok = exponent.SetString(parts[1], 10); !ok {
			return "", nil, false, false
		}
	}
	for _, digit := range coefficient {
		if digit < '0' || digit > '9' {
			return "", nil, false, false
		}
	}
	return coefficient, exponent, negative, coefficient != ""
}

func exactJSONContains(target, candidate any) bool {
	if !sameJSONKind(target, candidate) {
		return false
	}
	switch wanted := candidate.(type) {
	case []any:
		available := target.([]any)
		for _, item := range wanted {
			found := false
			for _, value := range available {
				if exactJSONContains(value, item) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case map[string]any:
		available := target.(map[string]any)
		for key, item := range wanted {
			value, ok := available[key]
			if !ok || !exactJSONContains(value, item) {
				return false
			}
		}
		return true
	default:
		return exactJSONEqual(target, candidate)
	}
}

func foldPolicyASCII(value string) string {
	bytes := []byte(value)
	changed := false
	for index, current := range bytes {
		if current >= 'A' && current <= 'Z' {
			bytes[index] = current + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return value
	}
	return string(bytes)
}
func driverText(value driver.Value) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}
func policyBool(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

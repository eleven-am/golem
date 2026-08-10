package evaluate

import (
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type jsonSlot struct {
	present bool
	value   ir.JSONValue
}

func evaluateJSON(condition ir.Condition, record Record) (bool, error) {
	fieldID, _ := condition.Field()
	field := record.fields[fieldID]
	operatorID, _ := condition.Operator()
	if operatorID == ir.OperatorJSONIsNull {
		return field.kind == fieldNull, nil
	}
	if operatorID == ir.OperatorJSONIsNotNull {
		return field.kind != fieldNull, nil
	}

	slot := jsonSlot{}
	if field.kind != fieldNull {
		if field.kind != fieldValue || field.value.Kind() != ir.ValueJSON {
			return false, typeFailure(record.model, fieldID, operatorID, "loaded JSON field has an incompatible value")
		}
		value, _ := field.value.JSON()
		slot = jsonSlot{present: true, value: value}
	}
	path, _ := condition.Path()
	slot = navigateJSON(slot, path)
	operand, _ := condition.Operand()
	mode, _ := condition.Mode()

	if nullKind, sentinel := operand.JSONNull(); sentinel {
		matched := jsonNullMatches(slot, nullKind)
		if operatorID == ir.OperatorJSONNotEqual {
			switch nullKind {
			case ir.JSONDbNull:
				return slot.present, nil
			case ir.JSONDocumentNull, ir.JSONAnyNull:
				return slot.present && slot.value.Kind() != ir.JSONNull, nil
			}
		}
		return matched, nil
	}

	wrapper, _ := operand.One()
	wanted, ok := wrapper.JSON()
	if !ok {
		return false, typeFailure(record.model, fieldID, operatorID, "JSON operand is not a JSON value")
	}
	if !slot.present {
		return false, nil
	}
	switch operatorID {
	case ir.OperatorJSONEqual:
		return slot.value.Kind() == wanted.Kind() && jsonEqual(slot.value, wanted), nil
	case ir.OperatorJSONNotEqual:
		return slot.value.Kind() == wanted.Kind() && !jsonEqual(slot.value, wanted), nil
	case ir.OperatorJSONLessThan, ir.OperatorJSONLessThanOrEqual, ir.OperatorJSONGreaterThan, ir.OperatorJSONGreaterThanOrEqual:
		comparison, comparable := compareJSON(slot.value, wanted, mode)
		if !comparable {
			return false, nil
		}
		switch operatorID {
		case ir.OperatorJSONLessThan:
			return comparison < 0, nil
		case ir.OperatorJSONLessThanOrEqual:
			return comparison <= 0, nil
		case ir.OperatorJSONGreaterThan:
			return comparison > 0, nil
		default:
			return comparison >= 0, nil
		}
	case ir.OperatorJSONStringContains, ir.OperatorJSONStringStartsWith, ir.OperatorJSONStringEndsWith:
		left, leftOK := slot.value.Text()
		right, rightOK := wanted.Text()
		if !leftOK || !rightOK {
			return false, nil
		}
		if mode == ir.ComparisonASCIIInsensitive {
			left, right = foldASCII(left), foldASCII(right)
		}
		switch operatorID {
		case ir.OperatorJSONStringContains:
			return strings.Contains(left, right), nil
		case ir.OperatorJSONStringStartsWith:
			return strings.HasPrefix(left, right), nil
		default:
			return strings.HasSuffix(left, right), nil
		}
	case ir.OperatorJSONArrayContains:
		array, arrayOK := slot.value.Array()
		if !arrayOK {
			return false, nil
		}
		if wanted.Kind() != ir.JSONArray && wanted.Kind() != ir.JSONObject {
			for _, element := range array {
				if jsonContainsDeep(element, wanted) {
					return true, nil
				}
			}
			return false, nil
		}
		return jsonContainsDeep(slot.value, wanted), nil
	case ir.OperatorJSONArrayStartsWith, ir.OperatorJSONArrayEndsWith:
		array, arrayOK := slot.value.Array()
		if !arrayOK || len(array) == 0 {
			return false, nil
		}
		index := 0
		if operatorID == ir.OperatorJSONArrayEndsWith {
			index = len(array) - 1
		}
		return jsonEqual(array[index], wanted), nil
	default:
		return false, operatorFailure(record.model, fieldID, operatorID, "unsupported JSON operator")
	}
}

func navigateJSON(slot jsonSlot, path ir.JSONPath) jsonSlot {
	for _, segment := range path.Segments() {
		if !slot.present || slot.value.Kind() == ir.JSONNull {
			return jsonSlot{}
		}
		if key, keySegment := segment.Key(); keySegment {
			members, object := slot.value.Object()
			if !object {
				return jsonSlot{}
			}
			found := false
			for _, member := range members {
				if member.Key() == key {
					slot.value = member.Value()
					found = true
					break
				}
			}
			if !found {
				return jsonSlot{}
			}
			continue
		}
		index, _ := segment.Index()
		values, array := slot.value.Array()
		if !array || index >= uint64(len(values)) {
			return jsonSlot{}
		}
		slot.value = values[index]
	}
	return slot
}

func jsonNullMatches(slot jsonSlot, kind ir.JSONNullKind) bool {
	switch kind {
	case ir.JSONDbNull:
		return !slot.present
	case ir.JSONDocumentNull:
		return slot.present && slot.value.Kind() == ir.JSONNull
	case ir.JSONAnyNull:
		return !slot.present || slot.value.Kind() == ir.JSONNull
	default:
		return false
	}
}

func jsonEqual(left, right ir.JSONValue) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case ir.JSONNull:
		return true
	case ir.JSONBool:
		leftValue, _ := left.Bool()
		rightValue, _ := right.Bool()
		return leftValue == rightValue
	case ir.JSONNumber:
		leftValue, _ := left.Number()
		rightValue, _ := right.Number()
		return compareJSONNumber(leftValue, rightValue) == 0
	case ir.JSONString:
		leftValue, _ := left.Text()
		rightValue, _ := right.Text()
		return leftValue == rightValue
	case ir.JSONArray:
		leftValues, _ := left.Array()
		rightValues, _ := right.Array()
		if len(leftValues) != len(rightValues) {
			return false
		}
		for index := range leftValues {
			if !jsonEqual(leftValues[index], rightValues[index]) {
				return false
			}
		}
		return true
	case ir.JSONObject:
		leftMembers, _ := left.Object()
		rightMembers, _ := right.Object()
		if len(leftMembers) != len(rightMembers) {
			return false
		}
		for index := range leftMembers {
			if leftMembers[index].Key() != rightMembers[index].Key() || !jsonEqual(leftMembers[index].Value(), rightMembers[index].Value()) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func compareJSON(left, right ir.JSONValue, mode ir.ComparisonMode) (int, bool) {
	if left.Kind() != right.Kind() {
		return 0, false
	}
	switch left.Kind() {
	case ir.JSONNumber:
		leftNumber, _ := left.Number()
		rightNumber, _ := right.Number()
		return compareJSONNumber(leftNumber, rightNumber), true
	case ir.JSONString:
		leftText, _ := left.Text()
		rightText, _ := right.Text()
		if mode == ir.ComparisonASCIIInsensitive {
			leftText, rightText = foldASCII(leftText), foldASCII(rightText)
		}
		return strings.Compare(leftText, rightText), true
	default:
		return 0, false
	}
}

func compareJSONNumber(left, right ir.JSONNumberValue) int {
	leftDigits := left.Coefficient()
	rightDigits := right.Coefficient()
	leftZero := string(leftDigits) == "0"
	rightZero := string(rightDigits) == "0"
	if leftZero || rightZero {
		if leftZero && rightZero {
			return 0
		}
		if leftZero {
			if right.Negative() {
				return 1
			}
			return -1
		}
		if left.Negative() {
			return -1
		}
		return 1
	}
	if left.Negative() != right.Negative() {
		if left.Negative() {
			return -1
		}
		return 1
	}
	leftMagnitude := int64(len(leftDigits)) + int64(left.Exponent())
	rightMagnitude := int64(len(rightDigits)) + int64(right.Exponent())
	result := 0
	if leftMagnitude < rightMagnitude {
		result = -1
	} else if leftMagnitude > rightMagnitude {
		result = 1
	} else {
		length := len(leftDigits)
		if len(rightDigits) > length {
			length = len(rightDigits)
		}
		for index := 0; index < length; index++ {
			leftDigit, rightDigit := byte('0'), byte('0')
			if index < len(leftDigits) {
				leftDigit = leftDigits[index]
			}
			if index < len(rightDigits) {
				rightDigit = rightDigits[index]
			}
			if leftDigit < rightDigit {
				result = -1
				break
			}
			if leftDigit > rightDigit {
				result = 1
				break
			}
		}
	}
	if left.Negative() {
		return -result
	}
	return result
}

func jsonContainsDeep(target, candidate ir.JSONValue) bool {
	if target.Kind() != candidate.Kind() {
		return false
	}
	switch target.Kind() {
	case ir.JSONArray:
		targetValues, _ := target.Array()
		candidateValues, _ := candidate.Array()
		for _, wanted := range candidateValues {
			matched := false
			for _, available := range targetValues {
				if jsonContainsDeep(available, wanted) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	case ir.JSONObject:
		targetMembers, _ := target.Object()
		candidateMembers, _ := candidate.Object()
		for _, wanted := range candidateMembers {
			matched := false
			for _, available := range targetMembers {
				if available.Key() == wanted.Key() {
					matched = jsonContainsDeep(available.Value(), wanted.Value())
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	default:
		return jsonEqual(target, candidate)
	}
}

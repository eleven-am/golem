package evaluate

import (
	"bytes"
	"math"
	"math/big"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func scalarOperator(operatorID ir.OperatorID, mode ir.ComparisonMode, subject ir.Value, operand ir.Operand) bool {
	switch operatorID {
	case ir.OperatorEqual, ir.OperatorNotEqual:
		wanted, _ := operand.One()
		matched := valuesEqualMode(subject, wanted, mode)
		if operatorID == ir.OperatorNotEqual {
			matched = !matched
		}
		return matched
	case ir.OperatorIn, ir.OperatorNotIn:
		wanted, _ := operand.Many()
		matched := false
		for _, candidate := range wanted {
			if valuesEqualMode(subject, candidate, mode) {
				matched = true
				break
			}
		}
		if operatorID == ir.OperatorNotIn {
			matched = !matched
		}
		return matched
	case ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual:
		wanted, _ := operand.One()
		comparison, comparable := compareValues(subject, wanted, mode)
		if !comparable {
			return false
		}
		switch operatorID {
		case ir.OperatorLessThan:
			return comparison < 0
		case ir.OperatorLessThanOrEqual:
			return comparison <= 0
		case ir.OperatorGreaterThan:
			return comparison > 0
		default:
			return comparison >= 0
		}
	case ir.OperatorContains, ir.OperatorStartsWith, ir.OperatorEndsWith:
		wanted, _ := operand.One()
		left, leftOK := subject.Text()
		right, rightOK := wanted.Text()
		if !leftOK || !rightOK {
			return false
		}
		if mode == ir.ComparisonASCIIInsensitive {
			left, right = foldASCII(left), foldASCII(right)
		}
		switch operatorID {
		case ir.OperatorContains:
			return strings.Contains(left, right)
		case ir.OperatorStartsWith:
			return strings.HasPrefix(left, right)
		default:
			return strings.HasSuffix(left, right)
		}
	default:
		return false
	}
}

func valuesEqualMode(left, right ir.Value, mode ir.ComparisonMode) bool {
	if mode == ir.ComparisonASCIIInsensitive && left.Kind() == ir.ValueString && right.Kind() == ir.ValueString {
		leftText, _ := left.Text()
		rightText, _ := right.Text()
		return foldASCII(leftText) == foldASCII(rightText)
	}
	return valuesEqual(left, right)
}

func valuesEqual(left, right ir.Value) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case ir.ValueBool:
		leftValue, _ := left.Bool()
		rightValue, _ := right.Bool()
		return leftValue == rightValue
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		leftValue, _ := left.Signed()
		rightValue, _ := right.Signed()
		return leftValue == rightValue
	case ir.ValueFloat32:
		leftValue, _ := left.Float32Bits()
		rightValue, _ := right.Float32Bits()
		return leftValue == rightValue
	case ir.ValueFloat64:
		leftValue, _ := left.Float64Bits()
		rightValue, _ := right.Float64Bits()
		return leftValue == rightValue
	case ir.ValueDecimal:
		leftCoefficient, leftScale, _ := left.Decimal()
		rightCoefficient, rightScale, _ := right.Decimal()
		return leftCoefficient == rightCoefficient && leftScale == rightScale
	case ir.ValueString:
		leftValue, _ := left.Text()
		rightValue, _ := right.Text()
		return leftValue == rightValue
	case ir.ValueBytes:
		leftValue, _ := left.Bytes()
		rightValue, _ := right.Bytes()
		return bytes.Equal(leftValue, rightValue)
	case ir.ValueUUID:
		leftValue, _ := left.UUID()
		rightValue, _ := right.UUID()
		return leftValue == rightValue
	case ir.ValueDate:
		leftYear, leftMonth, leftDay, _ := left.Date()
		rightYear, rightMonth, rightDay, _ := right.Date()
		return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
	case ir.ValueTime:
		leftValue, _ := left.Time()
		rightValue, _ := right.Time()
		return leftValue == rightValue
	case ir.ValueDateTime:
		leftSeconds, leftNanos, _ := left.DateTime()
		rightSeconds, rightNanos, _ := right.DateTime()
		return leftSeconds == rightSeconds && leftNanos == rightNanos
	case ir.ValueEnum:
		leftEnum, leftMember, _ := left.Enum()
		rightEnum, rightMember, _ := right.Enum()
		return leftEnum == rightEnum && leftMember == rightMember
	case ir.ValueJSON:
		leftValue, _ := left.JSON()
		rightValue, _ := right.JSON()
		return jsonEqual(leftValue, rightValue)
	case ir.ValueScalarList:
		leftValues, _ := left.List()
		rightValues, _ := right.List()
		if len(leftValues) != len(rightValues) {
			return false
		}
		for index := range leftValues {
			if !valuesEqual(leftValues[index], rightValues[index]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func compareValues(left, right ir.Value, mode ir.ComparisonMode) (int, bool) {
	if left.Kind() != right.Kind() {
		return 0, false
	}
	switch left.Kind() {
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		leftValue, _ := left.Signed()
		rightValue, _ := right.Signed()
		return compareOrdered(leftValue, rightValue), true
	case ir.ValueFloat32:
		leftBits, _ := left.Float32Bits()
		rightBits, _ := right.Float32Bits()
		return compareOrdered(math.Float32frombits(leftBits), math.Float32frombits(rightBits)), true
	case ir.ValueFloat64:
		leftBits, _ := left.Float64Bits()
		rightBits, _ := right.Float64Bits()
		return compareOrdered(math.Float64frombits(leftBits), math.Float64frombits(rightBits)), true
	case ir.ValueDecimal:
		return compareDecimal(left, right), true
	case ir.ValueString:
		leftValue, _ := left.Text()
		rightValue, _ := right.Text()
		if mode == ir.ComparisonASCIIInsensitive {
			leftValue, rightValue = foldASCII(leftValue), foldASCII(rightValue)
		}
		return strings.Compare(leftValue, rightValue), true
	case ir.ValueDate:
		leftYear, leftMonth, leftDay, _ := left.Date()
		rightYear, rightMonth, rightDay, _ := right.Date()
		if result := compareOrdered(leftYear, rightYear); result != 0 {
			return result, true
		}
		if result := compareOrdered(leftMonth, rightMonth); result != 0 {
			return result, true
		}
		return compareOrdered(leftDay, rightDay), true
	case ir.ValueTime:
		leftValue, _ := left.Time()
		rightValue, _ := right.Time()
		return compareOrdered(leftValue, rightValue), true
	case ir.ValueDateTime:
		leftSeconds, leftNanos, _ := left.DateTime()
		rightSeconds, rightNanos, _ := right.DateTime()
		if result := compareOrdered(leftSeconds, rightSeconds); result != 0 {
			return result, true
		}
		return compareOrdered(leftNanos, rightNanos), true
	default:
		return 0, false
	}
}

func compareDecimal(left, right ir.Value) int {
	leftCoefficient, leftScale, _ := left.Decimal()
	rightCoefficient, rightScale, _ := right.Decimal()
	leftInteger := big.NewInt(leftCoefficient)
	rightInteger := big.NewInt(rightCoefficient)
	if leftScale < rightScale {
		leftInteger.Mul(leftInteger, pow10(int(rightScale-leftScale)))
	} else if rightScale < leftScale {
		rightInteger.Mul(rightInteger, pow10(int(leftScale-rightScale)))
	}
	return leftInteger.Cmp(rightInteger)
}

func pow10(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

func foldASCII(value string) string {
	changed := false
	bytes := []byte(value)
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

func compareOrdered[T ~int16 | ~int64 | ~uint8 | ~uint32 | ~float32 | ~float64](left, right T) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

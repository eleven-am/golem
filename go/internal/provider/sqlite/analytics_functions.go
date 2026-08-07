package sqlite

import (
	"database/sql/driver"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	modernsqlite "modernc.org/sqlite"
)

// These names are versioned because their exact arithmetic and canonical-text
// result contracts are part of the provider ABI, not application SQL.
const (
	AnalyticsIntegerSumFunction     = "golem_analytics_integer_sum_v1"
	AnalyticsDecimalSumFunction     = "golem_analytics_decimal_sum_v1"
	AnalyticsDecimalAvgFunction     = "golem_analytics_decimal_avg_v1"
	AnalyticsNumericCompareFunction = "golem_analytics_numeric_compare_v1"
	AnalyticsNumericCollation       = "golem_analytics_numeric_v1"
)

func init() {
	modernsqlite.MustRegisterFunction(AnalyticsIntegerSumFunction, &modernsqlite.FunctionImpl{
		NArgs: 1, Deterministic: true,
		MakeAggregate: func(modernsqlite.FunctionContext) (modernsqlite.AggregateFunction, error) {
			return &exactIntegerAggregate{}, nil
		},
	})
	modernsqlite.MustRegisterFunction(AnalyticsDecimalSumFunction, &modernsqlite.FunctionImpl{
		NArgs: 2, Deterministic: true,
		MakeAggregate: func(modernsqlite.FunctionContext) (modernsqlite.AggregateFunction, error) {
			return &exactDecimalAggregate{}, nil
		},
	})
	modernsqlite.MustRegisterFunction(AnalyticsDecimalAvgFunction, &modernsqlite.FunctionImpl{
		NArgs: 2, Deterministic: true,
		MakeAggregate: func(modernsqlite.FunctionContext) (modernsqlite.AggregateFunction, error) {
			return &exactDecimalAggregate{average: true}, nil
		},
	})
	modernsqlite.MustRegisterDeterministicScalarFunction(AnalyticsNumericCompareFunction, 2, analyticsNumericCompare)
	modernsqlite.MustRegisterCollationUtf8(AnalyticsNumericCollation, compareAnalyticsNumbers)
}

func analyticsNumericCompare(_ *modernsqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 2 {
		return nil, fmt.Errorf("%s: arity", AnalyticsNumericCompareFunction)
	}
	if arguments[0] == nil || arguments[1] == nil {
		return nil, nil
	}
	left, leftOK := analyticsNumberText(arguments[0])
	right, rightOK := analyticsNumberText(arguments[1])
	if !leftOK || !rightOK {
		return nil, fmt.Errorf("%s: expected exact numeric text", AnalyticsNumericCompareFunction)
	}
	leftValue, leftOK := new(big.Rat).SetString(left)
	rightValue, rightOK := new(big.Rat).SetString(right)
	if !leftOK || !rightOK {
		return nil, fmt.Errorf("%s: invalid exact numeric text", AnalyticsNumericCompareFunction)
	}
	return int64(leftValue.Cmp(rightValue)), nil
}

func analyticsNumberText(value driver.Value) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case int64:
		return new(big.Int).SetInt64(typed).String(), true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	default:
		return "", false
	}
}

func compareAnalyticsNumbers(left, right string) int {
	leftValue, leftOK := new(big.Rat).SetString(left)
	rightValue, rightOK := new(big.Rat).SetString(right)
	if leftOK && rightOK {
		return leftValue.Cmp(rightValue)
	}
	return strings.Compare(left, right)
}

type exactIntegerAggregate struct {
	sum   big.Int
	count uint64
}

func (aggregate *exactIntegerAggregate) Step(_ *modernsqlite.FunctionContext, arguments []driver.Value) error {
	if len(arguments) != 1 {
		return fmt.Errorf("%s: arity", AnalyticsIntegerSumFunction)
	}
	if arguments[0] == nil {
		return nil
	}
	value, ok := arguments[0].(int64)
	if !ok {
		return fmt.Errorf("%s: expected INTEGER", AnalyticsIntegerSumFunction)
	}
	aggregate.sum.Add(&aggregate.sum, big.NewInt(value))
	aggregate.count++
	return nil
}

func (*exactIntegerAggregate) WindowInverse(*modernsqlite.FunctionContext, []driver.Value) error {
	return fmt.Errorf("%s: window use is unsupported", AnalyticsIntegerSumFunction)
}
func (aggregate *exactIntegerAggregate) WindowValue(*modernsqlite.FunctionContext) (driver.Value, error) {
	if aggregate.count == 0 {
		return nil, nil
	}
	return aggregate.sum.String(), nil
}
func (*exactIntegerAggregate) Final(*modernsqlite.FunctionContext) {}

type exactDecimalAggregate struct {
	sum      big.Int
	count    uint64
	scale    int64
	scaleSet bool
	average  bool
}

func (aggregate *exactDecimalAggregate) Step(_ *modernsqlite.FunctionContext, arguments []driver.Value) error {
	if len(arguments) != 2 {
		return fmt.Errorf("SQLite exact decimal aggregate: arity")
	}
	if arguments[0] == nil {
		return nil
	}
	coefficient, coefficientOK := arguments[0].(int64)
	scale, scaleOK := arguments[1].(int64)
	if !coefficientOK || !scaleOK || scale < 0 || scale > 18 {
		return fmt.Errorf("SQLite exact decimal aggregate: invalid coefficient or scale")
	}
	if aggregate.scaleSet && aggregate.scale != scale {
		return fmt.Errorf("SQLite exact decimal aggregate: inconsistent scale")
	}
	aggregate.scale, aggregate.scaleSet = scale, true
	aggregate.sum.Add(&aggregate.sum, big.NewInt(coefficient))
	aggregate.count++
	return nil
}

func (*exactDecimalAggregate) WindowInverse(*modernsqlite.FunctionContext, []driver.Value) error {
	return fmt.Errorf("SQLite exact decimal aggregate: window use is unsupported")
}
func (aggregate *exactDecimalAggregate) WindowValue(*modernsqlite.FunctionContext) (driver.Value, error) {
	if aggregate.count == 0 {
		return nil, nil
	}
	coefficient := new(big.Int).Set(&aggregate.sum)
	if aggregate.average {
		coefficient = divideHalfAwayFromZero(coefficient, aggregate.count)
	}
	return formatScaledInteger(coefficient, int(aggregate.scale)), nil
}
func (*exactDecimalAggregate) Final(*modernsqlite.FunctionContext) {}

func divideHalfAwayFromZero(value *big.Int, divisor uint64) *big.Int {
	denominator := new(big.Int).SetUint64(divisor)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value, denominator, remainder)
	if remainder.Sign() == 0 {
		return quotient
	}
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
	if twiceRemainder.Cmp(denominator) >= 0 {
		if value.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	return quotient
}

func formatScaledInteger(value *big.Int, scale int) string {
	negative := value.Sign() < 0
	digits := new(big.Int).Abs(value).String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		position := len(digits) - scale
		digits = digits[:position] + "." + digits[position:]
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	if digits == "" {
		digits = "0"
	}
	if negative && digits != "0" {
		digits = "-" + digits
	}
	return digits
}

func analyticsProbeSQL() string {
	return "SELECT " + AnalyticsIntegerSumFunction + "(value), " +
		AnalyticsDecimalSumFunction + "(value, 2), " +
		AnalyticsDecimalAvgFunction + "(value, 2), " +
		AnalyticsNumericCompareFunction + "('10', '2'), ('10' COLLATE " + AnalyticsNumericCollation + ") > '2' " +
		"FROM (SELECT 9223372036854775807 AS value UNION ALL SELECT 9223372036854775807)"
}

func validAnalyticsProbe(integer, decimalSum, decimalAverage string, comparison, ordering int) bool {
	return integer == "18446744073709551614" && decimalSum == "184467440737095516.14" && decimalAverage == "92233720368547758.07" && comparison == 1 && ordering == 1
}

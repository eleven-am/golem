package runtime

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestAggregateScalarResultMatrixAndStableOverflowDecode(t *testing.T) {
	scale := uint16(2)
	decimalType := compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Scale: &scale}
	canonicalUUID := "00000000-0000-0000-0000-000000000007"
	expectedUUID, err := golem.ParseUUID(canonicalUUID)
	if err != nil {
		t.Fatal(err)
	}
	decimalMinimum, err := golem.ParseDecimal("-1.25")
	if err != nil {
		t.Fatal(err)
	}
	decimalMaximum, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	dateValue, err := golem.ParseDate("2026-08-07")
	if err != nil {
		t.Fatal(err)
	}
	timeValue, err := golem.ParseTime("12:34:56.123456")
	if err != nil {
		t.Fatal(err)
	}
	datetime := time.Date(2026, 8, 7, 12, 34, 56, 123456000, time.FixedZone("test", 3600))
	tests := []struct {
		name     string
		raw      any
		operator golem.AggregateOperator
		logical  compilerir.LogicalTypeIR
		provider policyir.Provider
		want     any
	}{
		{"count", int64(3), golem.AggregateCountAll, compilerir.LogicalTypeIR{}, policyir.ProviderSQLite, int64(3)},
		{"field count", "2", golem.AggregateCountField, compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, policyir.ProviderPostgreSQL, int64(2)},
		{"bool", int64(1), 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeBool}, policyir.ProviderSQLite, true},
		{"int16", int64(-7), golem.AggregateMinimum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}, policyir.ProviderSQLite, int16(-7)},
		{"int32", int64(8), golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}, policyir.ProviderSQLite, int32(8)},
		{"int64", int64(9), golem.AggregateMinimum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, policyir.ProviderSQLite, int64(9)},
		{"integer sum", "18446744073709551614", golem.AggregateSum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, policyir.ProviderPostgreSQL, golem.MustParseExactInteger("18446744073709551614")},
		{"integer average", 1.5, golem.AggregateAverage, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, policyir.ProviderSQLite, 1.5},
		{"float32", 1.25, golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat32}, policyir.ProviderPostgreSQL, float32(1.25)},
		{"float64", -2.5, golem.AggregateMinimum, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}, policyir.ProviderSQLite, -2.5},
		{"decimal sum", "184467440737095516.14", golem.AggregateSum, decimalType, policyir.ProviderPostgreSQL, golem.MustParseExactDecimal("184467440737095516.14")},
		{"decimal average", "1.51", golem.AggregateAverage, decimalType, policyir.ProviderSQLite, golem.MustParseExactDecimal("1.51")},
		{"decimal sqlite minimum", int64(-125), golem.AggregateMinimum, decimalType, policyir.ProviderSQLite, decimalMinimum},
		{"decimal postgres maximum", "1.25", golem.AggregateMaximum, decimalType, policyir.ProviderPostgreSQL, decimalMaximum},
		{"string", []byte("é"), golem.AggregateMinimum, compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, policyir.ProviderPostgreSQL, "é"},
		{"uuid", canonicalUUID, 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, policyir.ProviderSQLite, expectedUUID},
		{"date", "2026-08-07", golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeDate}, policyir.ProviderPostgreSQL, dateValue},
		{"date postgres driver", time.Date(2026, time.August, 7, 0, 0, 0, 0, time.Local), golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeDate}, policyir.ProviderPostgreSQL, dateValue},
		{"time", "12:34:56.123456", golem.AggregateMinimum, compilerir.LogicalTypeIR{Kind: compilerir.TypeTime}, policyir.ProviderPostgreSQL, timeValue},
		{"datetime postgres", datetime, 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime}, policyir.ProviderPostgreSQL, datetime.UTC()},
		{"datetime sqlite before epoch", int64(-1), 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime}, policyir.ProviderSQLite, time.Unix(-1, 999999000).UTC()},
		{"enum", []byte("OPEN"), 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum}, policyir.ProviderSQLite, "OPEN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeAnalyticsValue(test.raw, golem.FrozenAnalyticsTerm{Operator: test.operator}, test.logical, test.provider)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("decoded %#v (%T), want %#v (%T)", got, got, test.want, test.want)
			}
		})
	}

	overflows := []struct {
		name     string
		raw      any
		operator golem.AggregateOperator
		logical  compilerir.LogicalTypeIR
		provider policyir.Provider
	}{
		{"int16", int64(math.MaxInt16 + 1), golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}, policyir.ProviderSQLite},
		{"int32", int64(math.MaxInt32 + 1), golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}, policyir.ProviderSQLite},
		{"float32", math.MaxFloat64, golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat32}, policyir.ProviderPostgreSQL},
		{"nonfinite", math.Inf(1), golem.AggregateAverage, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}, policyir.ProviderSQLite},
		{"exact envelope", strings.Repeat("9", 129), golem.AggregateSum, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, policyir.ProviderPostgreSQL},
		{"datetime precision", "2026-08-07T12:34:56.123456789Z", golem.AggregateMaximum, compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime}, policyir.ProviderPostgreSQL},
	}
	for _, test := range overflows {
		t.Run("overflow "+test.name, func(t *testing.T) {
			if _, err := decodeAnalyticsValue(test.raw, golem.FrozenAnalyticsTerm{Operator: test.operator}, test.logical, test.provider); err == nil {
				t.Fatal("invalid analytical value was coerced")
			}
		})
	}
}

func TestScopedIntegerDecodeRejectsFractionalNonFiniteAndNarrowingOverflow(t *testing.T) {
	for _, raw := range []any{1.5, math.NaN(), math.Inf(1), float64(9223372036854775808.0)} {
		if _, err := scopedInt64(raw); err == nil {
			t.Fatalf("scopedInt64(%v) accepted an invalid integer", raw)
		}
	}
	for _, test := range []struct {
		name    string
		raw     any
		logical compilerir.LogicalTypeIR
	}{
		{name: "int16-low", raw: int64(math.MinInt16 - 1), logical: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}},
		{name: "int16-high", raw: int64(math.MaxInt16 + 1), logical: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}},
		{name: "int32-low", raw: int64(math.MinInt32 - 1), logical: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}},
		{name: "int32-high", raw: int64(math.MaxInt32 + 1), logical: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := decodeScopedValue(test.raw, golem.FrozenScopedExpression{Kind: golem.ScopedExpressionField}, test.logical, policyir.ProviderSQLite)
			if err == nil || value != nil {
				t.Fatalf("decodeScopedValue(%v)=(%v, %v), want nil overflow error", test.raw, value, err)
			}
		})
	}
}

func TestAggregateOverflowIsStableAndNeverCoerced(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  any
		term golem.FrozenAnalyticsTerm
		typ  compilerir.LogicalTypeIR
	}{
		{"int16", int64(math.MaxInt16 + 1), golem.FrozenAnalyticsTerm{Operator: golem.AggregateMaximum}, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt16}},
		{"int32", int64(math.MaxInt32 + 1), golem.FrozenAnalyticsTerm{Operator: golem.AggregateMaximum}, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt32}},
		{"float32", math.MaxFloat64, golem.FrozenAnalyticsTerm{Operator: golem.AggregateMaximum}, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat32}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := decodeAnalyticsValue(test.raw, test.term, test.typ, policyir.ProviderSQLite)
			if err == nil || value != nil || !strings.Contains(err.Error(), "P6_ANALYTICAL_OVERFLOW") {
				t.Fatalf("overflow value=%#v error=%v", value, err)
			}
		})
	}
}

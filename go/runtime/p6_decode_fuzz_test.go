package runtime

import (
	"math"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// FuzzP6ExactAnalyticsResultDecode exercises the provider-result boundary, not
// the public scalar parsers already covered by the general scalar fuzz suite.
// Inputs are bounded before parsing so a fuzz worker cannot turn one corpus
// value into unbounded allocation work.
func FuzzP6ExactAnalyticsResultDecode(f *testing.F) {
	for _, seed := range []struct {
		kind uint8
		text string
	}{
		{0, "0"}, {0, "18446744073709551614"}, {0, "1.5"},
		{1, "0.0000"}, {1, "-999999999999999999.1250"}, {1, "1e3"},
		{2, "9223372036854775807"}, {2, "9223372036854775808"}, {2, "NaN"},
		{3, "1.5"}, {3, "NaN"}, {3, "+Inf"},
	} {
		f.Add(seed.kind, seed.text)
	}
	f.Fuzz(func(t *testing.T, kind uint8, text string) {
		if len(text) > 512 {
			t.Skip()
		}
		var (
			term    golem.FrozenAnalyticsTerm
			logical compilerir.LogicalTypeIR
		)
		switch kind % 4 {
		case 0:
			term.Operator = golem.AggregateSum
			logical.Kind = compilerir.TypeInt64
		case 1:
			term.Operator = golem.AggregateSum
			logical.Kind = compilerir.TypeDecimal
			scale := uint16(4)
			logical.Scale = &scale
		case 2:
			term.Operator = golem.AggregateCountAll
		case 3:
			term.Operator = golem.AggregateAverage
			logical.Kind = compilerir.TypeFloat64
		}
		value, err := decodeAnalyticsValue([]byte(text), term, logical, policyir.ProviderPostgreSQL)
		if err != nil {
			return
		}
		switch typed := value.(type) {
		case golem.ExactInteger:
			roundTrip, parseErr := golem.ParseExactInteger(typed.String())
			if parseErr != nil || roundTrip.Cmp(typed) != 0 {
				t.Fatalf("exact integer did not round-trip: %q, %v", typed.String(), parseErr)
			}
		case golem.ExactDecimal:
			roundTrip, parseErr := golem.ParseExactDecimal(typed.String())
			if parseErr != nil || roundTrip.Cmp(typed) != 0 {
				t.Fatalf("exact decimal did not round-trip: %q, %v", typed.String(), parseErr)
			}
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				t.Fatalf("non-finite analytical result escaped decode: %v", typed)
			}
		case int64:
		default:
			t.Fatalf("unexpected decoded analytical type %T", value)
		}
	})
}

package decode

import (
	"bytes"
	"math"
	"testing"
	"time"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestCorrelatedTextBoundaryPreservesExactScalarValues(t *testing.T) {
	maximum, err := correlatedRaw(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, "9223372036854775807")
	if err != nil || maximum != int64(math.MaxInt64) {
		t.Fatalf("max int64=%v err=%v", maximum, err)
	}
	minimum, err := correlatedRaw(policyir.ProviderSQLite, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, "-9223372036854775808")
	if err != nil || minimum != int64(math.MinInt64) {
		t.Fatalf("min int64=%v err=%v", minimum, err)
	}
	decimal, err := correlatedRaw(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal}, "-999999999999.000001")
	if err != nil || decimal != "-999999999999.000001" {
		t.Fatalf("decimal=%v err=%v", decimal, err)
	}
	scaled, err := correlatedRaw(policyir.ProviderSQLite, compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal}, "-999999999999000001")
	if err != nil || scaled != int64(-999999999999000001) {
		t.Fatalf("scaled decimal=%v err=%v", scaled, err)
	}
	negativeZero, err := correlatedRaw(policyir.ProviderSQLite, compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}, "-0")
	if err != nil || !math.Signbit(negativeZero.(float64)) {
		t.Fatalf("negative zero=%v err=%v", negativeZero, err)
	}
	binary, err := correlatedRaw(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: compilerir.TypeBytes}, "0001feff")
	if err != nil || !bytes.Equal(binary.([]byte), []byte{0, 1, 254, 255}) {
		t.Fatalf("bytes=%x err=%v", binary, err)
	}
	instant, err := correlatedRaw(policyir.ProviderPostgreSQL, compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime}, "1969-12-31T23:59:59.123456Z")
	wantInstant := time.Date(1969, 12, 31, 23, 59, 59, 123456000, time.UTC)
	if err != nil || !instant.(time.Time).Equal(wantInstant) {
		t.Fatalf("instant=%v err=%v", instant, err)
	}
}

func TestCorrelatedTextBoundaryRejectsMalformedScalars(t *testing.T) {
	for _, test := range []struct {
		provider policyir.Provider
		kind     compilerir.LogicalTypeKind
		value    string
	}{
		{policyir.ProviderSQLite, compilerir.TypeBool, "yes"},
		{policyir.ProviderPostgreSQL, compilerir.TypeInt64, "9007199254740993.0"},
		{policyir.ProviderPostgreSQL, compilerir.TypeBytes, "xyz"},
		{policyir.ProviderPostgreSQL, compilerir.TypeDateTime, "2024-01-01T00:00:00Z"},
	} {
		if _, err := correlatedRaw(test.provider, compilerir.LogicalTypeIR{Kind: test.kind}, test.value); err == nil {
			t.Errorf("kind=%s value=%q was accepted", test.kind, test.value)
		}
	}
}

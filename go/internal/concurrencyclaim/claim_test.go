package concurrencyclaim_test

import (
	"encoding"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/concurrencyclaim"
)

func TestPublicExistingVersionConvertsToClosedInternalInspection(t *testing.T) {
	for _, test := range []struct {
		name  string
		input int64
		want  int64
		valid bool
	}{
		{name: "minimum", input: 1, want: 1, valid: true},
		{name: "maximum", input: math.MaxInt64, want: math.MaxInt64, valid: true},
		{name: "zero", input: 0},
		{name: "negative", input: -1},
		{name: "minimum_int64", input: math.MinInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := golem.ExpectVersion(test.input)
			claim := concurrencyclaim.ExistingVersion(public)
			got, valid := concurrencyclaim.InspectExistingVersion(claim)
			if got != test.want || valid != test.valid {
				t.Fatalf("InspectExistingVersion(ExpectVersion(%d)) = (%d, %t), want (%d, %t)", test.input, got, valid, test.want, test.valid)
			}
		})
	}

	var zero golem.ExistingVersion
	if _, valid := concurrencyclaim.InspectExistingVersion(concurrencyclaim.ExistingVersion(zero)); valid {
		t.Fatal("the public zero ExistingVersion must convert to an invalid internal claim")
	}
}

func TestPublicExpectationConvertsToClosedInternalDiscrimination(t *testing.T) {
	absent := concurrencyclaim.ConcurrencyExpectation(golem.ExpectAbsent())
	if !concurrencyclaim.IsAbsent(absent) || !concurrencyclaim.ValidExpectation(absent) {
		t.Fatal("ExpectAbsent did not convert to the one valid absent state")
	}
	if _, existing := concurrencyclaim.InspectExistingExpectation(absent); existing {
		t.Fatal("the absent state was misclassified as an existing-version state")
	}

	for _, value := range []int64{1, 17, math.MaxInt64} {
		expectation := concurrencyclaim.ConcurrencyExpectation(golem.ExpectExisting(value))
		got, existing := concurrencyclaim.InspectExistingExpectation(expectation)
		if got != value || !existing || concurrencyclaim.IsAbsent(expectation) || !concurrencyclaim.ValidExpectation(expectation) {
			t.Fatalf("ExpectExisting(%d) converted to value=%d existing=%t absent=%t valid=%t", value, got, existing, concurrencyclaim.IsAbsent(expectation), concurrencyclaim.ValidExpectation(expectation))
		}
	}

	for _, value := range []int64{math.MinInt64, -1, 0} {
		expectation := concurrencyclaim.ConcurrencyExpectation(golem.ExpectExisting(value))
		if got, existing := concurrencyclaim.InspectExistingExpectation(expectation); got != 0 || existing {
			t.Fatalf("ExpectExisting(%d) converted to forgeable existing state (%d, %t)", value, got, existing)
		}
		if concurrencyclaim.IsAbsent(expectation) || concurrencyclaim.ValidExpectation(expectation) {
			t.Fatalf("ExpectExisting(%d) converted to absent or valid state", value)
		}
	}

	var zero golem.ConcurrencyExpectation
	convertedZero := concurrencyclaim.ConcurrencyExpectation(zero)
	if concurrencyclaim.IsAbsent(convertedZero) || concurrencyclaim.ValidExpectation(convertedZero) {
		t.Fatal("the public zero expectation must convert to the invalid internal state")
	}
}

func TestPublicClaimsRemainComparableDeepCopyValuesWithoutAuthorityAPI(t *testing.T) {
	version := golem.ExpectVersion(41)
	versionCopy := version
	if versionCopy != version {
		t.Fatal("copying ExistingVersion changed its value")
	}

	expectation := golem.ExpectExisting(41)
	expectationCopy := expectation
	if expectationCopy != expectation {
		t.Fatal("copying ConcurrencyExpectation changed its value")
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(version), reflect.TypeOf(expectation)} {
		if !typ.Comparable() {
			t.Fatalf("%s is not comparable", typ)
		}
		if typ.NumMethod() != 0 || reflect.PointerTo(typ).NumMethod() != 0 {
			t.Fatalf("%s exposes public authority methods on its value or pointer method set", typ)
		}
		for index := 0; index < typ.NumField(); index++ {
			if typ.Field(index).IsExported() {
				t.Fatalf("%s exposes field %q", typ, typ.Field(index).Name)
			}
		}
		for _, authorityInterface := range []reflect.Type{
			reflect.TypeOf((*json.Marshaler)(nil)).Elem(),
			reflect.TypeOf((*json.Unmarshaler)(nil)).Elem(),
			reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem(),
			reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem(),
			reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem(),
			reflect.TypeOf((*encoding.BinaryUnmarshaler)(nil)).Elem(),
		} {
			if typ.Implements(authorityInterface) || reflect.PointerTo(typ).Implements(authorityInterface) {
				t.Fatalf("%s exposes %s through its value or pointer API", typ, authorityInterface)
			}
		}
	}
}

package golem_test

import (
	"math"
	"reflect"
	"testing"

	golem "github.com/eleven-am/golem/go/golem"
)

type concurrencyModel struct{}

func TestOptimisticConcurrencyExpectationsRetainOnlyClosedImmutableState(t *testing.T) {
	for _, expectationType := range []reflect.Type{
		reflect.TypeOf(golem.ExistingVersion{}),
		reflect.TypeOf(golem.ConcurrencyExpectation{}),
	} {
		if !expectationType.Comparable() {
			t.Fatalf("%s must remain a copyable equality token", expectationType)
		}
		for fieldIndex := 0; fieldIndex < expectationType.NumField(); fieldIndex++ {
			field := expectationType.Field(fieldIndex)
			if field.IsExported() {
				t.Fatalf("%s exposes forgeable field %q", expectationType, field.Name)
			}
			switch field.Type.Kind() {
			case reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
				t.Fatalf("%s retains mutable or authority-bearing field %q of type %s", expectationType, field.Name, field.Type)
			}
		}
	}

	valid := golem.ExpectVersion(7)
	if valid != golem.ExpectVersion(7) || valid == golem.ExpectVersion(8) {
		t.Fatal("existing-version equality does not preserve its opaque token")
	}

	for _, value := range []int64{math.MinInt64, -1, 0} {
		invalid := golem.ExpectVersion(value)
		if invalid != (golem.ExistingVersion{}) || invalid == valid {
			t.Fatalf("ExpectVersion(%d) did not retain the closed invalid token", value)
		}
	}
	if golem.ExpectVersion(math.MaxInt64) == golem.ExpectVersion(0) {
		t.Fatal("MaxInt64 expectation aliases the invalid zero token")
	}
}

func TestConcurrencyExpectationDistinguishesAbsentExistingAndInvalid(t *testing.T) {
	absent := golem.ExpectAbsent()
	existing := golem.ExpectExisting(11)
	if absent == existing || existing != golem.ExpectExisting(11) || existing == golem.ExpectExisting(12) {
		t.Fatal("closed concurrency variants do not preserve equality-only semantics")
	}

	invalidExisting := golem.ExpectExisting(-9)
	if invalidExisting != (golem.ConcurrencyExpectation{}) || invalidExisting == existing || invalidExisting == absent {
		t.Fatal("invalid existing expectation did not retain the closed invalid state")
	}

	var zero golem.ConcurrencyExpectation
	if zero == absent || zero == existing {
		t.Fatal("zero concurrency expectation aliases a valid constructor result")
	}
	for _, expectationType := range []reflect.Type{reflect.TypeOf(golem.ExistingVersion{}), reflect.TypeOf(golem.ConcurrencyExpectation{})} {
		if expectationType.NumMethod() != 0 || reflect.PointerTo(expectationType).NumMethod() != 0 {
			t.Fatalf("%s exposes value/pointer methods (%d/%d); opaque tokens must be compared only by equality", expectationType, expectationType.NumMethod(), reflect.PointerTo(expectationType).NumMethod())
		}
	}
}

func TestOptimisticConcurrencyDeclarationShellIsModelAndInt64Typed(t *testing.T) {
	var option func(golem.ScalarColumn[concurrencyModel, int64]) golem.ModelOption[concurrencyModel] = golem.OptimisticConcurrency[concurrencyModel]
	version := golem.GeneratedOrderedField[concurrencyModel, int64](golem.FieldID{0x01})
	_ = golem.DefineModel(option(version))

	functionType := reflect.TypeOf(golem.OptimisticConcurrency[concurrencyModel])
	if functionType.NumIn() != 1 || functionType.NumOut() != 1 {
		t.Fatalf("OptimisticConcurrency signature = %s, want one field and one model option", functionType)
	}
	wantField := reflect.TypeOf((*golem.ScalarColumn[concurrencyModel, int64])(nil)).Elem()
	wantOption := reflect.TypeOf((*golem.ModelOption[concurrencyModel])(nil)).Elem()
	if functionType.In(0) != wantField || functionType.Out(0) != wantOption {
		t.Fatalf("OptimisticConcurrency signature = %s, want func(%s) %s", functionType, wantField, wantOption)
	}
}

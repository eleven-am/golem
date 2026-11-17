package graphql

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestPreparedNumericConversionRejectsNarrowingOverflow(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  any
		target reflect.Type
	}{
		{"positive int16", int32(math.MaxInt16 + 1), reflect.TypeFor[int16]()},
		{"negative int16", int32(math.MinInt16 - 1), reflect.TypeFor[int16]()},
		{"int32", int64(math.MaxInt32 + 1), reflect.TypeFor[int32]()},
		{"uint16", uint32(math.MaxUint16 + 1), reflect.TypeFor[uint16]()},
		{"float32", math.MaxFloat64, reflect.TypeFor[float32]()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if converted, err := coercePreparedValue(test.value, test.target); err == nil {
				t.Fatalf("overflow silently became %v", converted.Interface())
			}
		})
	}
	for _, value := range []int32{math.MinInt16, 0, math.MaxInt16} {
		got, err := coercePrepared[int16](value)
		if err != nil || int32(got) != value {
			t.Fatalf("valid boundary %d became %d: %v", value, got, err)
		}
	}
}

func TestGeneratedNestedInputOverflowIsBadUserInput(t *testing.T) {
	type Input struct{ Values []int16 }
	_, err := generatedArguments[struct{ Input Input }]([]ComputedArgument{{Name: "input", Value: map[string]any{"values": []any{int32(32768)}}}})
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || public.Reason != golem.ReasonInvalidValue || public.InputField != "input" {
		t.Fatalf("input overflow was not classified as a client error: %v", err)
	}
}

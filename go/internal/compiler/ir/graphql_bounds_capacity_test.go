package ir

import (
	"math"
	"testing"
)

func capacityType(element GraphQLTypeIR, maxima ...uint32) GraphQLTypeIR {
	for index := len(maxima) - 1; index >= 0; index-- {
		inner := element
		element = GraphQLTypeIR{Kind: GraphQLTypeList, MaxItems: maxima[index], Element: &inner}
	}
	return element
}

func TestCompositeListCapacityMultipliesEveryLevelAndSaturates(t *testing.T) {
	model := GraphQLTypeIR{Kind: GraphQLTypeModel, Name: "Post"}
	scalar := GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String"}
	for _, testCase := range []struct {
		name      string
		value     GraphQLTypeIR
		capacity  int
		composite bool
	}{
		{"single level", capacityType(model, 7), 7, true},
		{"nested levels", capacityType(model, 2, 5), 10, true},
		{"two maximal levels saturate", capacityType(model, math.MaxUint32, math.MaxUint32), math.MaxInt, true},
		{"three maximal levels stay saturated", capacityType(model, math.MaxUint32, math.MaxUint32, math.MaxUint32), math.MaxInt, true},
		{"scalar elements are leaves", capacityType(scalar, 4), 4, false},
		{"a single model is not a list", model, 0, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capacity, composite := testCase.value.CompositeListCapacity()
			if capacity != testCase.capacity || composite != testCase.composite {
				t.Fatalf("capacity = %d/%v, want %d/%v", capacity, composite, testCase.capacity, testCase.composite)
			}
		})
	}
}

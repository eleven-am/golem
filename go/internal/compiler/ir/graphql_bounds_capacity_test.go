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

func TestPageMaximumIsTheSmallestSetPageLimit(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		limits LimitContractIR
		want   uint32
	}{
		{"nothing set", LimitContractIR{}, 0},
		{"page only", LimitContractIR{MaxPageSize: 500}, 500},
		{"take only", LimitContractIR{MaxTake: 30}, 30},
		{"take below page", LimitContractIR{MaxPageSize: 500, MaxTake: 30}, 30},
		{"page below take", LimitContractIR{MaxPageSize: 20, MaxTake: 30}, 20},
	} {
		if got := testCase.limits.PageMaximum(); got != testCase.want {
			t.Fatalf("%s: PageMaximum = %d, want %d", testCase.name, got, testCase.want)
		}
	}
}

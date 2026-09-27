package contract

import (
	"math"
	"testing"
)

func TestWeightsStayWithinThePortableDatabaseRange(t *testing.T) {
	index := Index{Name: "content", Folding: FoldingDiacritics, Fields: []Field{{ID: "body", Weight: 1}}}
	for _, weight := range []float64{MinimumWeight, 1, MaximumWeight} {
		index.Fields[0].Weight = weight
		if _, err := Encode(index); err != nil {
			t.Fatalf("weight %g rejected: %v", weight, err)
		}
	}
	for _, weight := range []float64{math.SmallestNonzeroFloat64, math.Inf(1), math.NaN()} {
		index.Fields[0].Weight = weight
		if _, err := Encode(index); err == nil {
			t.Fatalf("weight %g accepted", weight)
		}
	}
	index.Fields[0].Weight = math.Nextafter(MaximumWeight, math.Inf(1))
	if _, err := Encode(index); err == nil {
		t.Fatalf("weight above %g accepted", MaximumWeight)
	}
}

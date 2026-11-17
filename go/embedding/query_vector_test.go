package embedding

import (
	"math"
	"reflect"
	"testing"
)

func TestQueryVectorOwnsValuesAndEmbeddingSpecification(t *testing.T) {
	specification, err := NewSpecification("provider", "model", "revision", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	values := []float32{1, 0, 0}
	vector, err := NewVector(values)
	if err != nil {
		t.Fatal(err)
	}
	query, err := NewQueryVector(specification, vector)
	if err != nil {
		t.Fatal(err)
	}
	values[0] = 0
	copy := query.Vector().Values()
	copy[0] = 0
	if query.Specification() != specification || !reflect.DeepEqual(query.Vector().Values(), []float32{1, 0, 0}) || query.Validate() != nil {
		t.Fatal("query vector lost its specification or immutable values")
	}
}

func TestQueryVectorRejectsInvalidCosineInputs(t *testing.T) {
	specification, err := NewSpecification("provider", "model", "revision", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	// Package-local invalid values cover deserialization/provider corruption as
	// well as the public constructors' finite-value checks.
	for _, values := range [][]float32{nil, {1}, {0, 0, 0}, {1, float32(math.NaN()), 0}, {1, float32(math.Inf(1)), 0}} {
		if _, err := NewQueryVector(specification, Vector{values: values}); err == nil {
			t.Fatalf("invalid query accepted: %v", values)
		}
	}
	if _, err := NewQueryVector(Specification{}, Vector{values: []float32{1, 0, 0}}); err == nil {
		t.Fatal("missing query specification accepted")
	}
	if (QueryVector{}).Validate() == nil {
		t.Fatal("zero query value accepted")
	}
}

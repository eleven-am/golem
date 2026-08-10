package embedding

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

type testProvider struct{ specification Specification }

func (provider testProvider) Specification() Specification            { return provider.specification }
func (testProvider) Embed(context.Context, []Input) ([]Vector, error) { return nil, nil }

type panicSpecificationProvider struct{}

func (*panicSpecificationProvider) Specification() Specification { panic("secret specification panic") }
func (*panicSpecificationProvider) Embed(context.Context, []Input) ([]Vector, error) {
	return nil, nil
}

func TestSpecificationAndVectorAreValidatedAndCopyIsolated(t *testing.T) {
	specification, err := NewSpecification("openai", "text-embedding-3-small", "2024-01", 3, 64)
	if err != nil {
		t.Fatal(err)
	}
	if specification.Provider() != "openai" || specification.Model() != "text-embedding-3-small" || specification.Revision() != "2024-01" || specification.Dimensions() != 3 || specification.MaximumBatch() != 64 {
		t.Fatalf("unexpected specification: %#v", specification)
	}
	values := []float32{1, 2, 3}
	vector, err := NewVector(values)
	if err != nil {
		t.Fatal(err)
	}
	values[0] = 99
	copy := vector.Values()
	copy[1] = 99
	if got := vector.Values(); !reflect.DeepEqual(got, []float32{1, 2, 3}) {
		t.Fatalf("vector was not copy isolated: %v", got)
	}
}

func TestInvalidAndMismatchedResultsFailClosed(t *testing.T) {
	specification, _ := NewSpecification("test", "model", "v1", 2, 2)
	input, _ := NewInput("one", "title\nbody")
	valid, _ := NewVector([]float32{1, 2})
	wrong, _ := NewVector([]float32{1})
	if err := ValidateResult(specification, []Input{input}, []Vector{valid}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResult(specification, []Input{input}, nil); err == nil {
		t.Fatal("missing result accepted")
	}
	if err := ValidateResult(specification, []Input{input}, []Vector{wrong}); err == nil {
		t.Fatal("wrong dimensions accepted")
	}
	zero, _ := NewVector([]float32{0, 0})
	if err := ValidateResult(specification, []Input{input}, []Vector{zero}); err == nil {
		t.Fatal("zero-norm cosine vector accepted")
	}
	if _, err := NewVector([]float32{float32(math.Inf(1))}); err == nil {
		t.Fatal("infinite vector accepted")
	}
}

func TestClosedErrorsRetainTrustedCauseWithoutDisclosingIt(t *testing.T) {
	cause := errors.New("secret provider response")
	err := NewError(CodeUnavailable, cause)
	if err.Error() != string(CodeUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("closed error contract failed: %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != CodeUnavailable {
		t.Fatalf("code = %q, %v", code, ok)
	}
}

func TestRegistryValidatesAndOrdersNamedSpaces(t *testing.T) {
	specification, _ := NewSpecification("test", "model", "v1", 3, 8)
	registry, err := NewRegistry(map[string]Provider{
		"secondary": testProvider{specification: specification},
		"content":   testProvider{specification: specification},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"content", "secondary"}) {
		t.Fatalf("names=%v", got)
	}
	if _, ok := registry.Lookup("content"); !ok {
		t.Fatal("configured space is absent")
	}
	if _, err := NewRegistry(map[string]Provider{"Content": testProvider{specification: specification}}); err == nil {
		t.Fatal("non-portable space name was accepted")
	}
	if _, err := NewRegistry(map[string]Provider{"content": nil}); err == nil {
		t.Fatal("nil provider was accepted")
	}
	var typedNil *panicSpecificationProvider
	if _, err := NewRegistry(map[string]Provider{"content": typedNil}); err == nil {
		t.Fatal("typed-nil provider was accepted")
	}
	if _, err := NewRegistry(map[string]Provider{"content": &panicSpecificationProvider{}}); err == nil || err.Error() != `EMBEDDING_REGISTRY_INVALID: space "content" has an invalid provider specification` {
		t.Fatalf("panicking specification error=%v", err)
	}
}

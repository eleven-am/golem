package embedding

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
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

func TestDetailedErrorsPrintDetailWithoutDisclosingCause(t *testing.T) {
	cause := errors.New("secret provider response")
	err := Failf(CodeInvalidInput, cause, "text is %d bytes, which exceeds the %d byte limit", 32, 16)
	expected := string(CodeInvalidInput) + ": text is 32 bytes, which exceeds the 16 byte limit"
	if err.Error() != expected {
		t.Fatalf("error = %q, want %q", err.Error(), expected)
	}
	if strings.Contains(err.Error(), "secret provider response") {
		t.Fatalf("detailed error disclosed its cause: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("detailed error lost its trusted cause: %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != CodeInvalidInput {
		t.Fatalf("code = %q, %v", code, ok)
	}
	if plain := Failf(CodeProvider, cause, ""); plain.Error() != string(CodeProvider) {
		t.Fatalf("empty detail = %q, want %q", plain.Error(), string(CodeProvider))
	}
}

func TestInvalidValuesNameTheSingleReasonTheyWereRefused(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		fragment string
	}{
		{"empty key", inputError("", "text"), "key is empty"},
		{"long key", inputError(strings.Repeat("k", 513), "text"), "513 bytes"},
		{"nul key", inputError("k\x00", "text"), "NUL"},
		{"empty text", inputError("key", ""), "text is empty"},
		{"invalid text", inputError("key", "\xff"), "UTF-8"},
		{"provider identity", specificationError("Bad", "model", "v1", 3, 8), "provider"},
		{"model identity", specificationError("provider", "Bad", "v1", 3, 8), "model"},
		{"revision identity", specificationError("provider", "model", "V1", 3, 8), "revision"},
		{"dimensions", specificationError("provider", "model", "v1", 0, 8), "dimensions"},
		{"batch", specificationError("provider", "model", "v1", 3, 0), "batch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("invalid value was accepted")
			}
			if !strings.Contains(test.err.Error(), test.fragment) {
				t.Fatalf("error = %q, want it to name %q", test.err.Error(), test.fragment)
			}
		})
	}
	if err := specificationError("Bad", "Bad", "V1", 3, 8); err == nil || strings.Contains(err.Error(), "model") || strings.Contains(err.Error(), "revision") {
		t.Fatalf("compound specification message survived: %v", err)
	}
}

func inputError(key, text string) error {
	_, err := NewInput(key, text)
	return err
}

func specificationError(provider, model, revision string, dimensions, maximumBatch int) error {
	_, err := NewSpecification(provider, model, revision, dimensions, maximumBatch)
	return err
}

func TestVectorAndBatchRefusalsNameOneReasonAndTheActualCount(t *testing.T) {
	_, emptyVector := NewVector(nil)
	if emptyVector == nil {
		t.Fatal("empty vector accepted")
	}
	if !strings.Contains(emptyVector.Error(), "vector is empty") {
		t.Fatalf("empty vector error = %q, want it to name emptiness", emptyVector.Error())
	}
	if strings.Contains(emptyVector.Error(), "limit") {
		t.Fatalf("empty vector error folded the ceiling in: %q", emptyVector.Error())
	}

	_, oversizedVector := NewVector(make([]float32, MaximumDimensions+1))
	if oversizedVector == nil {
		t.Fatal("oversized vector accepted")
	}
	for _, fragment := range []string{"2001 dimensions", "2000 dimension limit"} {
		if !strings.Contains(oversizedVector.Error(), fragment) {
			t.Fatalf("oversized vector error = %q, want it to name %q", oversizedVector.Error(), fragment)
		}
	}
	if strings.Contains(oversizedVector.Error(), "empty") {
		t.Fatalf("oversized vector error folded emptiness in: %q", oversizedVector.Error())
	}

	specification, _ := NewSpecification("test", "model", "v1", 2, 2)
	input, _ := NewInput("one", "title\nbody")
	vector, _ := NewVector([]float32{1, 2})

	emptyBatch := ValidateResult(specification, nil, nil)
	if emptyBatch == nil {
		t.Fatal("empty batch accepted")
	}
	if !strings.Contains(emptyBatch.Error(), "batch is empty") {
		t.Fatalf("empty batch error = %q, want it to name emptiness", emptyBatch.Error())
	}
	if strings.Contains(emptyBatch.Error(), "limit") {
		t.Fatalf("empty batch error folded the batch limit in: %q", emptyBatch.Error())
	}

	oversizedBatch := ValidateResult(specification, []Input{input, input, input}, []Vector{vector, vector, vector})
	if oversizedBatch == nil {
		t.Fatal("oversized batch accepted")
	}
	for _, fragment := range []string{"input count is 3", "batch limit of 2"} {
		if !strings.Contains(oversizedBatch.Error(), fragment) {
			t.Fatalf("oversized batch error = %q, want it to name %q", oversizedBatch.Error(), fragment)
		}
	}
	if strings.Contains(oversizedBatch.Error(), "empty") {
		t.Fatalf("oversized batch error folded emptiness in: %q", oversizedBatch.Error())
	}
}

// Package embedding defines the provider-neutral boundary between a generated
// Golem semantic index and an application-selected embedding service.
//
// Providers translate canonical text into finite float32 vectors. They do not
// receive model names, database identities, actors, policies, or raw records.
// Golem owns durable indexing, storage, authorization, and similarity queries.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaximumDimensions is the portable v1 ceiling. PostgreSQL pgvector's
	// single-precision vector type has the same indexed dimension ceiling.
	MaximumDimensions = 2000
	MaximumBatchSize  = 2048
	MaximumInputBytes = 16 << 20
)

// Provider embeds one ordered batch. Results must preserve input order and
// contain exactly Specification.Dimensions finite values each. Embed may be
// called concurrently and must honor context cancellation; implementations
// own any concurrency limiting required by their upstream service.
type Provider interface {
	Specification() Specification
	Embed(ctx context.Context, inputs []Input) ([]Vector, error)
}

// Registry is an immutable application-owned mapping from schema space names
// to embedding providers. The zero value is a valid empty registry.
type Registry struct {
	providers      map[string]Provider
	specifications map[string]Specification
}

func NewRegistry(providers map[string]Provider) (Registry, error) {
	result := Registry{providers: make(map[string]Provider, len(providers)), specifications: make(map[string]Specification, len(providers))}
	for name, provider := range providers {
		if !portableIdentity(name) {
			return Registry{}, fmt.Errorf("EMBEDDING_REGISTRY_INVALID: space name %q is not portable", name)
		}
		if provider == nil || nilProvider(provider) {
			return Registry{}, fmt.Errorf("EMBEDDING_REGISTRY_INVALID: space %q has no provider", name)
		}
		specification, err := safeSpecification(provider)
		if err != nil {
			return Registry{}, fmt.Errorf("EMBEDDING_REGISTRY_INVALID: space %q has an invalid provider specification", name)
		}
		result.providers[name] = provider
		result.specifications[name] = specification
	}
	return result, nil
}

func (registry Registry) Specification(name string) (Specification, bool) {
	specification, ok := registry.specifications[name]
	return specification, ok
}

func (registry Registry) Lookup(name string) (Provider, bool) {
	provider, ok := registry.providers[name]
	return provider, ok
}

func (registry Registry) Names() []string {
	result := make([]string, 0, len(registry.providers))
	for name := range registry.providers {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// Specification is the immutable identity of an embedding space. Changing
// any value creates a different space and makes existing projections stale.
type Specification struct {
	provider   string
	model      string
	revision   string
	dimensions int
	maximum    int
}

func NewSpecification(provider, model, revision string, dimensions, maximumBatch int) (Specification, error) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	revision = strings.TrimSpace(revision)
	if !portableIdentity(provider) || !portableIdentity(model) || !portableIdentity(revision) {
		return Specification{}, fmt.Errorf("EMBEDDING_SPEC_INVALID: provider, model, and revision must be closed portable identities")
	}
	if dimensions < 1 || dimensions > MaximumDimensions {
		return Specification{}, fmt.Errorf("EMBEDDING_SPEC_INVALID: dimensions must be between 1 and %d", MaximumDimensions)
	}
	if maximumBatch < 1 || maximumBatch > MaximumBatchSize {
		return Specification{}, fmt.Errorf("EMBEDDING_SPEC_INVALID: maximum batch must be between 1 and %d", MaximumBatchSize)
	}
	return Specification{provider: provider, model: model, revision: revision, dimensions: dimensions, maximum: maximumBatch}, nil
}

func (value Specification) Provider() string  { return value.provider }
func (value Specification) Model() string     { return value.model }
func (value Specification) Revision() string  { return value.revision }
func (value Specification) Dimensions() int   { return value.dimensions }
func (value Specification) MaximumBatch() int { return value.maximum }

func (value Specification) Validate() error {
	_, err := NewSpecification(value.provider, value.model, value.revision, value.dimensions, value.maximum)
	return err
}

// FingerprintInput is a length-delimited, stable description suitable for a
// caller-owned hash. It deliberately does not choose a hashing algorithm.
func (value Specification) FingerprintInput() string {
	return fmt.Sprintf("embedding-space:v1\x00%d:%s\x00%d:%s\x00%d:%s\x00%d\x00%d", len(value.provider), value.provider, len(value.model), value.model, len(value.revision), value.revision, value.dimensions, value.maximum)
}

// Input is one canonical document. Key is an opaque job-local correlation key;
// providers must not interpret or retain it.
type Input struct {
	key  string
	text string
}

func NewInput(key, text string) (Input, error) {
	if key == "" || len(key) > 512 || strings.IndexByte(key, 0) >= 0 {
		return Input{}, fmt.Errorf("EMBEDDING_INPUT_INVALID: key must contain 1..512 non-NUL bytes")
	}
	if len(text) == 0 || len(text) > MaximumInputBytes || !utf8.ValidString(text) {
		return Input{}, fmt.Errorf("EMBEDDING_INPUT_INVALID: text must contain 1..%d bytes of UTF-8", MaximumInputBytes)
	}
	return Input{key: key, text: text}, nil
}

func (value Input) Key() string  { return value.key }
func (value Input) Text() string { return value.text }

// Vector is an immutable finite single-precision embedding.
type Vector struct{ values []float32 }

func NewVector(values []float32) (Vector, error) {
	if len(values) == 0 || len(values) > MaximumDimensions {
		return Vector{}, fmt.Errorf("EMBEDDING_VECTOR_INVALID: dimensions must be between 1 and %d", MaximumDimensions)
	}
	result := append([]float32(nil), values...)
	for _, value := range result {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return Vector{}, fmt.Errorf("EMBEDDING_VECTOR_INVALID: elements must be finite")
		}
	}
	return Vector{values: result}, nil
}

func (value Vector) Dimensions() int   { return len(value.values) }
func (value Vector) Values() []float32 { return append([]float32(nil), value.values...) }

// ValidateResult verifies the closed positional batch contract without
// exposing provider errors or input contents in the returned diagnostic.
func ValidateResult(specification Specification, inputs []Input, vectors []Vector) error {
	if err := specification.Validate(); err != nil {
		return err
	}
	if len(inputs) == 0 || len(inputs) > specification.MaximumBatch() {
		return fmt.Errorf("EMBEDDING_BATCH_INVALID: input count is outside the provider batch limit")
	}
	if len(vectors) != len(inputs) {
		return fmt.Errorf("EMBEDDING_RESULT_INVALID: result count does not match input count")
	}
	for index, input := range inputs {
		if _, err := NewInput(input.key, input.text); err != nil {
			return fmt.Errorf("EMBEDDING_BATCH_INVALID: input %d is invalid", index)
		}
		if len(vectors[index].values) != specification.Dimensions() {
			return fmt.Errorf("EMBEDDING_RESULT_INVALID: vector %d has the wrong dimensions", index)
		}
		var norm float64
		for _, value := range vectors[index].values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("EMBEDDING_RESULT_INVALID: vector %d contains a non-finite element", index)
			}
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return fmt.Errorf("EMBEDDING_RESULT_INVALID: vector %d has zero cosine norm", index)
		}
	}
	return nil
}

type Code string

const (
	CodeInvalidInput Code = "EMBEDDING_INVALID_INPUT"
	CodeUnavailable  Code = "EMBEDDING_UNAVAILABLE"
	CodeProvider     Code = "EMBEDDING_PROVIDER"
)

type Error struct {
	code Code
	err  error
}

func NewError(code Code, cause error) error {
	if code != CodeInvalidInput && code != CodeUnavailable && code != CodeProvider {
		code = CodeProvider
	}
	return &Error{code: code, err: cause}
}

func (err *Error) Error() string { return string(err.code) }
func (err *Error) Unwrap() error { return err.err }

func CodeOf(err error) (Code, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.code, true
}

func portableIdentity(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func nilProvider(provider Provider) bool {
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func safeSpecification(provider Provider) (specification Specification, err error) {
	defer func() {
		if recover() != nil {
			specification = Specification{}
			err = fmt.Errorf("embedding provider specification panicked")
		}
	}()
	specification = provider.Specification()
	if err := specification.Validate(); err != nil {
		return Specification{}, err
	}
	return specification, nil
}

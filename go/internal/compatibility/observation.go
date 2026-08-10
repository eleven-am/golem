package compatibility

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type ObservationInventory struct {
	FormatVersion                 uint16                 `json:"formatVersion"`
	AdapterABI                    string                 `json:"adapterABI"`
	Attributes                    []ObservationAttribute `json:"attributes"`
	Slog                          ObservationSlog        `json:"slog"`
	OTel                          ObservationOTel        `json:"otel"`
	ProviderIndependentOperations []string               `json:"providerIndependentOperations"`
	Coverage                      []ObservationCoverage  `json:"coverage"`
}

type ObservationAttribute struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ObservationSlog struct {
	Message string `json:"message"`
	Level   string `json:"level"`
}

type ObservationOTel struct {
	InstrumentationScope string              `json:"instrumentationScope"`
	Span                 string              `json:"span"`
	Metrics              []ObservationMetric `json:"metrics"`
}

type ObservationMetric struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Unit string `json:"unit"`
}

type ObservationCoverage struct {
	Kind       string   `json:"kind"`
	Operations []string `json:"operations"`
}

func ParseObservationInventory(encoded []byte) (ObservationInventory, error) {
	if err := rejectDuplicateJSONKeys(encoded); err != nil {
		return ObservationInventory{}, fail(ReasonInvalidEncoding)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value ObservationInventory
	if err := decoder.Decode(&value); err != nil {
		return ObservationInventory{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || !canonicalObservation(value) {
		return ObservationInventory{}, fail(ReasonInvalidEncoding)
	}
	return value, nil
}

// encoding/json deliberately accepts duplicate object names. Compatibility
// inventories are a closed wire format, so accepting the final occurrence
// would let a human reviewer and a machine consume different apparent values.
func rejectDuplicateJSONKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("invalid JSON object name")
			}
			if _, duplicate := seen[name]; duplicate {
				return errors.New("duplicate JSON object name")
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func CompareObservation(previous, current ObservationInventory) LayerChange {
	if !canonicalObservation(previous) || !canonicalObservation(current) || previous.FormatVersion != current.FormatVersion || previous.AdapterABI != current.AdapterABI || previous.Slog != current.Slog || previous.OTel.InstrumentationScope != current.OTel.InstrumentationScope || previous.OTel.Span != current.OTel.Span {
		return LayerBreaking
	}
	change := LayerUnchanged
	if !retainsNamed(previous.Attributes, current.Attributes, func(value ObservationAttribute) string { return value.Name }) || !retainsNamed(previous.OTel.Metrics, current.OTel.Metrics, func(value ObservationMetric) string { return value.Name }) {
		return LayerBreaking
	}
	if len(previous.Attributes) != len(current.Attributes) || len(previous.OTel.Metrics) != len(current.OTel.Metrics) {
		change = LayerAdditive
	}
	if !observationContainsAll(current.ProviderIndependentOperations, previous.ProviderIndependentOperations) {
		return LayerBreaking
	}
	if len(previous.ProviderIndependentOperations) != len(current.ProviderIndependentOperations) {
		change = LayerAdditive
	}
	currentCoverage := make(map[string][]string, len(current.Coverage))
	for _, coverage := range current.Coverage {
		currentCoverage[coverage.Kind] = coverage.Operations
	}
	for _, before := range previous.Coverage {
		after, ok := currentCoverage[before.Kind]
		if !ok || !observationContainsAll(after, before.Operations) {
			return LayerBreaking
		}
		if len(after) != len(before.Operations) {
			change = LayerAdditive
		}
	}
	if len(current.Coverage) != len(previous.Coverage) {
		change = LayerAdditive
	}
	return change
}

func canonicalObservation(value ObservationInventory) bool {
	if value.FormatVersion != 1 || value.AdapterABI == "" || value.Slog.Message == "" || value.Slog.Level == "" || value.OTel.InstrumentationScope == "" || value.OTel.Span == "" || len(value.Attributes) == 0 || len(value.Coverage) == 0 {
		return false
	}
	if !uniqueNamed(value.Attributes, func(value ObservationAttribute) string { return value.Name }, func(value ObservationAttribute) bool { return value.Name != "" && value.Type != "" }) ||
		!uniqueNamed(value.OTel.Metrics, func(value ObservationMetric) string { return value.Name }, func(value ObservationMetric) bool { return value.Name != "" && value.Kind != "" && value.Unit != "" }) ||
		hasDuplicate(value.ProviderIndependentOperations) {
		return false
	}
	kinds := map[string]bool{}
	operations := map[string]bool{}
	for _, coverage := range value.Coverage {
		if coverage.Kind == "" || kinds[coverage.Kind] || len(coverage.Operations) == 0 || hasDuplicate(coverage.Operations) {
			return false
		}
		kinds[coverage.Kind] = true
		for _, operation := range coverage.Operations {
			if operation == "" || operations[operation] {
				return false
			}
			operations[operation] = true
		}
	}
	for _, operation := range value.ProviderIndependentOperations {
		if !operations[operation] {
			return false
		}
	}
	return true
}

func retainsNamed[T comparable](previous, current []T, name func(T) string) bool {
	byName := make(map[string]T, len(current))
	for _, value := range current {
		byName[name(value)] = value
	}
	for _, value := range previous {
		if next, ok := byName[name(value)]; !ok || next != value {
			return false
		}
	}
	return true
}

func uniqueNamed[T any](values []T, name func(T) string, valid func(T) bool) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		identity := name(value)
		if !valid(value) || seen[identity] {
			return false
		}
		seen[identity] = true
	}
	return true
}

func hasDuplicate(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func observationContainsAll(values, expected []string) bool {
	available := make(map[string]bool, len(values))
	for _, value := range values {
		available[value] = true
	}
	for _, value := range expected {
		if !available[value] {
			return false
		}
	}
	return true
}

package observe

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type telemetryManifest struct {
	FormatVersion int    `json:"formatVersion"`
	AdapterABI    string `json:"adapterABI"`
	Attributes    []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"attributes"`
	Slog struct {
		Message string `json:"message"`
		Level   string `json:"level"`
	} `json:"slog"`
	OTel struct {
		InstrumentationScope string `json:"instrumentationScope"`
		Span                 string `json:"span"`
		Metrics              []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Unit string `json:"unit"`
		} `json:"metrics"`
	} `json:"otel"`
	ProviderIndependentOperations []string `json:"providerIndependentOperations"`
	Coverage                      []struct {
		Kind       string   `json:"kind"`
		Operations []string `json:"operations"`
	} `json:"coverage"`
}

func TestTelemetryManifestExactlyCoversClosedObservationInventory(t *testing.T) {
	contents, err := os.ReadFile("telemetry-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest telemetryManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 1 || manifest.AdapterABI != "golem.observe.adapter.v1" || manifest.Slog.Message != "golem.observation.v1" || manifest.Slog.Level != "INFO" || manifest.OTel.InstrumentationScope != "github.com/eleven-am/golem/go/observe/otel" || manifest.OTel.Span != "golem.operation.v1" {
		t.Fatalf("unexpected manifest identity: %#v", manifest)
	}
	kinds, operations := closedStringConstants(t)
	coveredKinds := make(map[string]bool)
	coveredOperations := make(map[string]bool)
	for _, family := range manifest.Coverage {
		if !kinds[family.Kind] || coveredKinds[family.Kind] {
			t.Fatalf("unknown or duplicate kind %q", family.Kind)
		}
		coveredKinds[family.Kind] = true
		for _, operation := range family.Operations {
			if !operations[operation] || coveredOperations[operation] {
				t.Fatalf("unknown or duplicate operation %q", operation)
			}
			coveredOperations[operation] = true
		}
	}
	if len(coveredKinds) != len(kinds) || len(coveredOperations) != len(operations) {
		t.Fatalf("manifest coverage kinds=%d/%d operations=%d/%d", len(coveredKinds), len(kinds), len(coveredOperations), len(operations))
	}
	wantProviderIndependent := []string{"event.publisher_retry", "event.publisher_block", "event.retention", "event.transport_reconnect"}
	if !slices.Equal(manifest.ProviderIndependentOperations, wantProviderIndependent) {
		t.Fatalf("provider-independent operations=%v want=%v", manifest.ProviderIndependentOperations, wantProviderIndependent)
	}
	for _, operation := range manifest.ProviderIndependentOperations {
		if !coveredOperations[operation] {
			t.Fatalf("provider-independent operation %q is absent from coverage", operation)
		}
	}
	wantAttributes := []string{"golem.kind", "golem.phase", "golem.outcome", "golem.reason", "golem.provider", "golem.operation", "golem.model_id", "golem.duration_ns", "golem.statement_count", "golem.attempt", "golem.queue_depth", "golem.queue_limit", "golem.aggregate_count"}
	if len(manifest.Attributes) != len(wantAttributes) {
		t.Fatalf("attributes=%d want %d", len(manifest.Attributes), len(wantAttributes))
	}
	for index, name := range wantAttributes {
		if manifest.Attributes[index].Name != name {
			t.Fatalf("attribute %d=%q want %q", index, manifest.Attributes[index].Name, name)
		}
	}
	wantMetrics := []string{"golem.observation.records", "golem.observation.duration_ns", "golem.observation.statement_count", "golem.observation.attempt", "golem.observation.queue_depth", "golem.observation.queue_limit", "golem.observation.aggregate_count"}
	if len(manifest.OTel.Metrics) != len(wantMetrics) {
		t.Fatalf("metrics=%d want %d", len(manifest.OTel.Metrics), len(wantMetrics))
	}
	for index, name := range wantMetrics {
		if manifest.OTel.Metrics[index].Name != name {
			t.Fatalf("metric %d=%q want %q", index, manifest.OTel.Metrics[index].Name, name)
		}
	}
}

func closedStringConstants(t *testing.T) (map[string]bool, map[string]bool) {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "observe.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]bool)
	operations := make(map[string]bool)
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(name.Name, "Kind"):
				kinds[value] = true
			case strings.HasPrefix(name.Name, "Operation"):
				operations[value] = true
			}
		}
		return true
	})
	return kinds, operations
}

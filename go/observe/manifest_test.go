package observe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
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

type coverageManifest struct {
	FormatVersion int `json:"formatVersion"`
	Occurrences   []struct {
		Symbol string `json:"symbol"`
		Source string `json:"source"`
	} `json:"occurrences"`
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

func TestP8ObservationCoverageManifestStructuralOccurrences(t *testing.T) {
	contents, err := os.ReadFile("coverage-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest coverageManifest
	if err := json.Unmarshal(contents, &manifest); err != nil || manifest.FormatVersion != 1 {
		t.Fatalf("decode coverage manifest: version=%d err=%v", manifest.FormatVersion, err)
	}
	symbols := closedOperationSymbols(t)
	seen := make(map[string]bool)
	for _, occurrence := range manifest.Occurrences {
		if _, ok := symbols[occurrence.Symbol]; !ok || seen[occurrence.Symbol] {
			t.Fatalf("unknown or duplicate operation symbol %q", occurrence.Symbol)
		}
		if filepath.IsAbs(occurrence.Source) || strings.Contains(occurrence.Source, "..") || strings.Contains(occurrence.Source, "observe/") || strings.HasSuffix(occurrence.Source, "_test.go") {
			t.Fatalf("operation %s has non-production source %q", occurrence.Symbol, occurrence.Source)
		}
		if !sourceUsesObserveSymbol(t, filepath.Join("..", occurrence.Source), occurrence.Symbol) {
			t.Fatalf("operation %s has no observe-qualified production occurrence in %s", occurrence.Symbol, occurrence.Source)
		}
		seen[occurrence.Symbol] = true
	}
	if len(seen) != len(symbols) {
		t.Fatalf("coverage occurrences=%d operations=%d", len(seen), len(symbols))
	}
}

func TestP8ObservationCoverageManifest(t *testing.T) {
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") != "1" {
		t.Skip("GOLEM_P8_REQUIRE_POSTGRESQL=1 is required for the exact dynamic coverage gate")
	}
	path := filepath.Join(t.TempDir(), "production-observations.tsv")
	commands := [][]string{
		{"test", "./runtime", "-run", `^(TestP8ObservationCoverageMutationHookAndSystemTransactionEdges|TestP8ObservationCoverageEventFaultEdges)$`, "-count=1", "-v"},
		{"test", "./internal/p8oracle", "-run", `^TestP8(HookPhaseAndResultCrossSurfaceOracle|ComputedAndBatchedDependencyDisclosureOracle|AfterCommitFailureDoesNotChangeCommittedResult|ReadCrossEntryPointIndependentOracle|ReadMaskErrorAndPaginationParity|CustomQueryCannotChangeAuthorizationOrSystemCapability|CallerTransactionReadParity)$`, "-count=1", "-v"},
		{"test", "./internal/p8oracle/mutation", "-run", `^TestP8`, "-count=1", "-v"},
		{"test", "./internal/p8oracle/analytics", "-run", `^TestP8`, "-count=1", "-v"},
		{"test", "./internal/p8oracle/event", "-run", `^TestP8`, "-count=1", "-v"},
		{"test", "./internal/generate/pipeline", "-run", `^TestFreshGeneratedSemantic(SQLiteApplicationOwnsEmbeddingLifecycle|PostgreSQLApplicationOwnsPGVectorLifecycle)$`, "-count=1", "-v"},
	}
	for _, arguments := range commands {
		command := exec.Command("go", arguments...)
		command.Dir = ".."
		command.Env = p8CoverageEnvironment(os.Environ(), "P8_OBSERVATION_COVERAGE_FILE", path)
		command.Env = p8CoverageEnvironment(command.Env, "GOLEM_REQUIRE_PGVECTOR", "1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("dynamic production coverage command failed: go %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		if strings.Contains(string(output), "--- SKIP:") {
			t.Fatalf("dynamic production coverage command skipped a required profile: go %s\n%s", strings.Join(arguments, " "), output)
		}
	}
	manifestContents, err := os.ReadFile("telemetry-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest telemetryManifest
	if err := json.Unmarshal(manifestContents, &manifest); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool)
	for _, family := range manifest.Coverage {
		for _, operation := range family.Operations {
			want[operation] = true
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	seen := make(map[string]map[string]bool)
	providers := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			t.Fatalf("invalid dynamic observation coverage record %q", scanner.Text())
		}
		provider, operation := fields[0], fields[1]
		if provider != "sqlite" && provider != "postgresql" {
			t.Fatalf("invalid dynamic observation provider %q", provider)
		}
		if !want[operation] {
			t.Fatalf("dynamic production path emitted operation absent from manifest %q", operation)
		}
		providers[provider] = true
		if seen[operation] == nil {
			seen[operation] = make(map[string]bool)
		}
		seen[operation][provider] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !providers["sqlite"] || !providers["postgresql"] {
		t.Fatalf("dynamic coverage providers=%v; SQLite and PostgreSQL are mandatory", providers)
	}
	providerIndependent := make(map[string]bool, len(manifest.ProviderIndependentOperations))
	for _, operation := range manifest.ProviderIndependentOperations {
		providerIndependent[operation] = true
	}
	var missing []string
	for operation := range want {
		providersForOperation := seen[operation]
		if providerIndependent[operation] {
			if len(providersForOperation) == 0 {
				missing = append(missing, operation+"@provider-independent")
			}
			continue
		}
		if !providersForOperation["sqlite"] {
			missing = append(missing, operation+"@sqlite")
		}
		if !providersForOperation["postgresql"] {
			missing = append(missing, operation+"@postgresql")
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		t.Fatalf("telemetry manifest operations without a dynamic production occurrence: %v", missing)
	}
}

func p8CoverageEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, fmt.Sprintf("%s=%s", name, value))
}

func closedOperationSymbols(t *testing.T) map[string]string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "observe.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string)
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Operation") || index >= len(spec.Values) {
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
			result[name.Name] = value
		}
		return true
	})
	return result
}

func sourceUsesObserveSymbol(t *testing.T, path, symbol string) bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := make(map[string]bool)
	for _, specification := range parsed.Imports {
		pathValue, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if pathValue != "github.com/eleven-am/golem/go/observe" {
			continue
		}
		name := "observe"
		if specification.Name != nil {
			name = specification.Name.Name
		}
		aliases[name] = true
	}
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != symbol {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && aliases[identifier.Name] {
			found = true
		}
		return true
	})
	return found
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

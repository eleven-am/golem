package mutationverify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestP6MutationCatalogContainsEveryFrozenLedgerLabelExactlyOnce(t *testing.T) {
	want := strings.Fields(`AGGREGATE_IN_GO POLICY_AFTER_GROUP SKIP_MEASURE_CLASSIFICATION COUNT_FIELD_AS_COUNT_ALL MASK_AGGREGATE DISCHARGE_BY_SAMPLE DECIMAL_TO_REAL INTEGER_SUM_INT64 NATIVE_COLLATION_GROUP NULL_SUM_ZERO SILENT_PROGRAMMATIC_CAP SILENT_GRAPHQL_TRUNCATION LIMIT_BEFORE_HAVING DROP_ORDER_TIEBREAK RELATION_TWO_PHASE_MERGE RELATION_TARGET_UNSCOPED LEFT_POLICY_IN_WHERE IMPLICIT_RELATION_DEDUP ALLOW_RAW_NODE MIX_SCOPE_NONCE AUDIT_ONLY_SUCCESS AUDIT_RAW_SQL_OR_VALUES GRAPHQL_SECOND_ENGINE EMIT_ANALYTICS_BY_RESERVED_NAME RUN_AGGREGATE_HOOKS`)
	got := make([]string, 0, len(Catalog()))
	seen := map[string]bool{}
	for _, mutation := range Catalog() {
		if mutation.Label == "" || seen[mutation.Label] {
			t.Fatalf("empty or duplicate mutation label %q", mutation.Label)
		}
		seen[mutation.Label] = true
		got = append(got, mutation.Label)
		if mutation.Covered() == (mutation.Remaining != "") {
			t.Fatalf("mutation %s must be either covered or carry a remaining reason", mutation.Label)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog labels = %v, want %v", got, want)
	}
}

func TestP6MutationPatchesAreNarrowAndApplyToCurrentSource(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range Catalog() {
		if !mutation.Covered() {
			continue
		}
		t.Run(mutation.Label, func(t *testing.T) {
			temporary := t.TempDir()
			for _, patch := range mutation.Patches {
				source := filepath.Join(root, filepath.FromSlash(patch.Path))
				destination := filepath.Join(temporary, filepath.FromSlash(patch.Path))
				content, readErr := os.ReadFile(source)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o755); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
				if writeErr := os.WriteFile(destination, content, 0o644); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			for _, patch := range mutation.Patches {
				if patchErr := applyPatch(temporary, patch); patchErr != nil {
					t.Fatal(patchErr)
				}
			}
		})
	}
}

func TestP6MutationRunnerClassifiesNamedFailureAndSurvival(t *testing.T) {
	module := t.TempDir()
	write := func(path, content string) {
		path = filepath.Join(module, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/mutant\n\ngo 1.24\n")
	write("sample.go", "package sample\nconst answer = 42\n")
	write("sample_test.go", "package sample\nimport \"testing\"\nfunc TestOracle(t *testing.T) { if answer != 42 { t.Fatalf(\"answer=%d\", answer) } }\n")
	runner := Runner{ModuleDir: module}
	killed, err := runner.Run(context.Background(), Mutation{Label: "KILLED", Patches: []Patch{{Path: "sample.go", Before: "answer = 42", After: "answer = 41"}}, Tests: []Test{{Package: ".", Name: "TestOracle"}}})
	if err != nil || killed.Status != StatusKilled || killed.Test != "TestOracle" {
		t.Fatalf("killed=%#v err=%v", killed, err)
	}
	survived, err := runner.Run(context.Background(), Mutation{Label: "SURVIVED", Patches: []Patch{{Path: "sample.go", Before: "answer = 42", After: "answer = 42 // unchanged"}}, Tests: []Test{{Package: ".", Name: "TestOracle"}}})
	if err != nil || survived.Status != StatusSurvived {
		t.Fatalf("survived=%#v err=%v", survived, err)
	}
}

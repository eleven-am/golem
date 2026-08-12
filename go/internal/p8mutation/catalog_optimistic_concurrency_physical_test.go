package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyPhysicalMutationCatalogIsIsolatedApplicableAndKilled(t *testing.T) {
	want := []string{"CONCURRENCY_PHYSICAL_OMITS_CANONICAL_POINTER", "CONCURRENCY_POSTGRES_OMITS_CATALOG_RECONCILIATION", "CONCURRENCY_REVIEWED_EDGE_USES_MUTABLE_PLANNER", "CONCURRENCY_POSTGRES_INITIALIZES_TWO", "CONCURRENCY_POSTGRES_PROVES_TWO", "CONCURRENCY_POSTGRES_INVENTS_DATABASE_DEFAULT", "CONCURRENCY_SQLITE_COPIES_TWO", "CONCURRENCY_BOOTSTRAP_IGNORES_PHYSICAL_DISAGREEMENT", "CONCURRENCY_PHYSICAL_ACCEPTS_DATABASE_DEFAULT"}
	mutations := optimisticConcurrencyPhysicalMutations()
	if len(mutations) != len(want) {
		t.Fatalf("physical concurrency mutations=%d want=%d", len(mutations), len(want))
	}
	for index := range want {
		if mutations[index].Label != want[index] {
			t.Fatalf("mutation %d=%q want=%q", index, mutations[index].Label, want[index])
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	if err := ValidatePatchSites(repository, mutations); err != nil {
		t.Fatal(err)
	}
	if !isolatedMutationExecutionEnabled() {
		return
	}
	for _, mutation := range mutations {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result=%#v want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

func optimisticConcurrencyPhysicalRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyGeneratedGoMutationCatalogIsIsolatedApplicableAndExact(t *testing.T) {
	mutations := optimisticConcurrencyGeneratedGoMutations()
	want := []string{
		"CONCURRENCY_GENERATED_REEMITS_AUTHORED_SETTER",
		"CONCURRENCY_GENERATED_REEMITS_ROOT_BATCH",
		"CONCURRENCY_GENERATED_ONE_FAMILY_OMITS_EXPECTATION",
		"CONCURRENCY_GENERATED_CALLS_LEGACY_RUNTIME",
		"CONCURRENCY_GENERATED_REEMITS_UNSAFE_NESTED_TARGET_UPDATE",
		"CONCURRENCY_GENERATED_REEMITS_VERSIONED_ROOT_RELATION_UPDATE",
		"CONCURRENCY_GENERATED_DISCOVERY_SHELL_LOSES_ESCAPE",
	}
	if len(mutations) != len(want) {
		t.Fatalf("optimistic-concurrency generated-Go mutation count = %d, want %d", len(mutations), len(want))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyGeneratedGoRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestOptimisticConcurrencyGeneratedGoMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyGeneratedGoRepository(t)
	for _, mutation := range optimisticConcurrencyGeneratedGoMutations() {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutation.Gate.Test)
			}
			t.Logf("%s %s in %s", result.Mutation, result.Status, result.Duration)
		})
	}
}

func optimisticConcurrencyGeneratedGoRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve generated-Go mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

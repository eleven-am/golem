package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyModelMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	mutations := optimisticConcurrencyModelMutations()
	want := []string{
		"CONCURRENCY_MODEL_CURRENT_FRAMING_ACCEPTS_V1",
		"CONCURRENCY_MODEL_V1_ACCEPTS_V2_MEMBER",
		"CONCURRENCY_MODEL_V1_FINGERPRINTS_PROJECTED_V2",
		"CONCURRENCY_POLICY_ACTIVE_ACCEPTS_MODEL_V1",
	}
	if len(mutations) != len(want) {
		t.Fatalf("optimistic-concurrency ModelIR mutation count = %d, want %d", len(mutations), len(want))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyModelRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestOptimisticConcurrencyModelMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyModelRepository(t)
	for _, mutation := range optimisticConcurrencyModelMutations() {
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

func optimisticConcurrencyModelRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

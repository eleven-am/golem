package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyContractMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	mutations := optimisticConcurrencyContractMutations()
	want := []string{
		"CONCURRENCY_CONTRACT_OMITS_V6_PROJECTION",
		"CONCURRENCY_CURRENT_FRAMING_ACCEPTS_V5",
		"CONCURRENCY_V5_DECODER_ACCEPTS_V6",
		"CONCURRENCY_V5_ROUTES_THROUGH_CURRENT_FRAMING",
		"CONCURRENCY_CURRENT_FRAMING_ACCEPTS_NONCANONICAL",
		"CONCURRENCY_CONTRACT_ACCEPTS_MODEL_DISAGREEMENT",
		"CONCURRENCY_EXPORTS_NONAUTHORITATIVE_CURRENT_FRAMING",
	}
	if len(mutations) != len(want) {
		t.Fatalf("optimistic-concurrency ContractIR mutation count = %d, want %d", len(mutations), len(want))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyContractRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestOptimisticConcurrencyContractMutationsAreKilled(t *testing.T) {
	if !globalCatalogTestExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyContractRepository(t)
	for _, mutation := range optimisticConcurrencyContractMutations() {
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

func optimisticConcurrencyContractRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

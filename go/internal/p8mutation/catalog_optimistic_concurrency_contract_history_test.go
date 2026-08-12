package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyContractHistoryMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	mutations := optimisticConcurrencyContractHistoryMutations()
	want := []string{
		"CONCURRENCY_CONTRACT_V5_RETAINS_OLD_IN_MEMORY_VERSION",
		"CONCURRENCY_CONTRACT_V5_REUSES_MUTABLE_EXACT_JSON",
		"CONCURRENCY_CONTRACT_V5_ACCEPTS_CURRENT_ONLY_FIELD",
		"CONCURRENCY_CONTRACT_V5_BOOTSTRAP_ROUTE_OMITTED",
		"CONCURRENCY_CONTRACT_V5_ACTIVE_ROUTE_OPENS",
		"CONCURRENCY_CONTRACT_V5_SKIPS_ORIGINAL_FINGERPRINT",
		"CONCURRENCY_HISTORICAL_PHYSICAL_V2_ROUTES_CURRENT",
		"CONCURRENCY_HISTORICAL_PHYSICAL_SKIPS_ORIGINAL_FINGERPRINT",
		"CONCURRENCY_HISTORICAL_PHYSICAL_ACCEPTS_RELABELLED_V2",
		"CONCURRENCY_HISTORICAL_PHYSICAL_V1_ROUTE_BYPASSED",
		"CONCURRENCY_HISTORICAL_PHYSICAL_V3_ROUTES_CURRENT",
		"CONCURRENCY_HISTORICAL_PHYSICAL_SKIPS_SYSTEM_FINGERPRINT",
	}
	if len(mutations) != len(want) {
		t.Fatalf("ContractIR history mutation count = %d, want %d", len(mutations), len(want))
	}
	for index := range want {
		if mutations[index].Label != want[index] {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, want[index])
		}
		if _, published := Find(want[index]); published {
			t.Fatalf("isolated ContractIR history mutation %q entered the global catalog", want[index])
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyContractHistoryRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestOptimisticConcurrencyContractHistoryMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyContractHistoryRepository(t)
	for _, mutation := range optimisticConcurrencyContractHistoryMutations() {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

func optimisticConcurrencyContractHistoryRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve ContractIR history mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

package p8mutation

import (
	"context"
	"testing"
)

func TestOptimisticConcurrencyMigrationHistoryMutationCatalogIsIsolatedApplicableAndKilled(t *testing.T) {
	want := []string{
		"CONCURRENCY_MIGRATION_ENTRY_CHANGES_DOMAIN",
		"CONCURRENCY_MIGRATION_ENTRY_OMITS_OPERATION_KIND",
		"CONCURRENCY_MIGRATION_ENTRY_REOPENS_REFLECTION",
		"CONCURRENCY_MIGRATION_ENTRY_ACCEPTS_FUTURE_AUTHORITY",
		"CONCURRENCY_MIGRATION_ENTRY_SKIPS_PREV3_FACT_VALIDATION",
		"CONCURRENCY_MIGRATION_ENTRY_OMITS_V3_CONCURRENCY_IDENTITY",
		"CONCURRENCY_MIGRATION_PREVIEW_REHASHES_HISTORICAL_MODEL",
		"CONCURRENCY_MIGRATION_NEW_REENCODES_HISTORICAL_MODEL",
		"CONCURRENCY_MIGRATION_BACKFILL_REENCODES_HISTORICAL_MODEL",
		"CONCURRENCY_MIGRATION_BACKFILL_TRUSTS_UNBOUND_BEFORE_MODEL",
	}
	mutations := optimisticConcurrencyMigrationHistoryMutations()
	if len(mutations) != len(want) {
		t.Fatalf("migration-history concurrency mutations=%d want=%d", len(mutations), len(want))
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

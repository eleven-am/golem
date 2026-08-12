package p8mutation

import (
	"context"
	"testing"
)

func TestPhysicalV1V3UpgradeMutationCatalogIsIsolatedApplicableAndKilled(t *testing.T) {
	want := []string{
		"PHYSICAL_V1_V3_UPGRADE_OMITS_DISPATCH",
		"PHYSICAL_V1_V3_UPGRADE_SKIPS_V2_PROJECTION",
		"PHYSICAL_V1_V3_UPGRADE_RETAINS_LEG_PUBLICATIONS",
		"PHYSICAL_V1_V3_UPGRADE_INTERLEAVES_FROZEN_LEGS",
		"PHYSICAL_V1_V3_UPGRADE_RENDERER_FORGETS_REPRESENTATION_LEG",
	}
	mutations := physicalV1V3UpgradeMutations()
	if len(mutations) != len(want) {
		t.Fatalf("physical v1-to-v3 mutations=%d want=%d", len(mutations), len(want))
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

package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompatibilityManifestHistoryMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	mutations := compatibilityManifestHistoryMutations()
	want := []string{
		"COMPATIBILITY_MANIFEST_CURRENT_RELABELED_V1",
		"COMPATIBILITY_MANIFEST_ACTIVE_ACCEPTS_V1",
		"COMPATIBILITY_MANIFEST_V1_ACCEPTS_CURRENT_GRAPHQL_MEMBER",
		"COMPATIBILITY_MANIFEST_V1_OMITS_CURRENT_ONLY_GRAPHQL_PROJECTION",
		"COMPATIBILITY_MANIFEST_V1_SKIPS_CANONICAL_BYTES",
		"COMPATIBILITY_MANIFEST_V1_ACCEPTS_NULL_EMPTY_INVENTORY",
		"COMPATIBILITY_MANIFEST_V1_SKIPS_TRUST_DIGEST",
		"COMPATIBILITY_MANIFEST_V1_REUSES_CURRENT_VALIDATOR",
		"COMPATIBILITY_MANIFEST_V1_MISPROJECTS_OBSERVATION_DIGEST",
	}
	if len(mutations) != len(want) {
		t.Fatalf("compatibility manifest history mutation count=%d want=%d", len(mutations), len(want))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("mutation %d=%q want=%q", index, mutations[index].Label, label)
		}
		if _, published := Find(label); published {
			t.Fatalf("isolated compatibility manifest mutation %q entered the global catalog", label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(compatibilityManifestHistoryRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityManifestHistoryMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := compatibilityManifestHistoryRepository(t)
	for _, mutation := range compatibilityManifestHistoryMutations() {
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

func compatibilityManifestHistoryRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve compatibility manifest mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMigrationGuideMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	mutations := migrationGuideMutations()
	want := []string{"MIGRATION_GUIDE_MANIFEST_AUTHORITY_ABSENT", "MIGRATION_GUIDE_TRUST_DIGEST_SKIPPED", "MIGRATION_GUIDE_FROM_TAG_RELABELED", "MIGRATION_GUIDE_ACTION_ONLY_ACCEPTED", "MIGRATION_GUIDE_CORPUS_TREE_DRIFT", "MIGRATION_GUIDE_V1_FUTURE_HISTORY_REJECTED"}
	if len(mutations) != len(want) {
		t.Fatalf("count=%d", len(mutations))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("label %d=%s", index, mutations[index].Label)
		}
		if _, ok := Find(label); ok {
			t.Fatalf("isolated mutation %s published", label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(migrationGuideRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationGuideMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := migrationGuideRepository(t)
	for _, mutation := range migrationGuideMutations() {
		mutation := mutation
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func migrationGuideRepository(t *testing.T) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

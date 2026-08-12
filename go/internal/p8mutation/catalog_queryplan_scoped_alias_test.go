package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQueryPlanScopedAliasMutationsAreIsolatedApplicableAndKilled(t *testing.T) {
	mutations := queryPlanScopedAliasMutations()
	if len(mutations) != 9 {
		t.Fatalf("scoped alias mutations = %#v", mutations)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	if err := ValidatePatchSites(repository, mutations); err != nil {
		t.Fatal(err)
	}
	if !globalCatalogTestExecutionEnabled() {
		return
	}
	for _, mutation := range mutations {
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

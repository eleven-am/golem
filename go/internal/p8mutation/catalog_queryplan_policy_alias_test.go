package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQueryPlanPolicyAliasMutationIsIsolatedApplicableAndKilled(t *testing.T) {
	mutations := queryPlanPolicyAliasMutations()
	if len(mutations) != 1 || mutations[0].Label != "QUERYPLAN_POLICY_ALIAS_REGISTRATION_OMITTED" {
		t.Fatalf("policy alias mutations = %#v", mutations)
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
	result, err := (Runner{Repository: repository}).Run(context.Background(), mutations[0])
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusKilled || result.Test != mutations[0].Gate.Test {
		t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutations[0].Gate.Test)
	}
}

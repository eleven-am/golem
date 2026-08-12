package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestQueryPlanRuntimeMutationsAreIsolatedApplicableAndKilled(t *testing.T) {
	mutations := queryPlanRuntimeMutations()
	if len(mutations) != 5 {
		t.Fatalf("runtime query-plan mutations = %#v", mutations)
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

func TestQueryPlanRuntimeAndExplicitPolicyRoleMutationsAreEightOfEightKilled(t *testing.T) {
	if !globalCatalogTestExecutionEnabled() {
		return
	}
	mutations := append([]Mutation{}, queryPlanRuntimeMutations()...)
	for _, candidates := range [][]Mutation{queryPlanReadAliasMutations(), queryPlanAnalyticsAliasMutations(), queryPlanScopedAliasMutations()} {
		for _, mutation := range candidates {
			if mutation.Label == "QUERYPLAN_READ_ALIAS_MISCLASSIFIES_POLICY_ROLE" || mutation.Label == "QUERYPLAN_ANALYTICS_ALIAS_MISCLASSIFIES_POLICY_ROLE" || mutation.Label == "QUERYPLAN_SCOPED_ALIAS_MISCLASSIFIES_POLICY_ROLE" {
				mutations = append(mutations, mutation)
			}
		}
	}
	if len(mutations) != 8 {
		t.Fatalf("runtime and policy-role mutations = %#v", mutations)
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

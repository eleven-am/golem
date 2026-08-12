package p8mutation

import (
	"context"
	"testing"
)

func TestQueryPlanGeneratedGoMutationCatalogIsIsolatedApplicableAndExact(t *testing.T) {
	mutations := queryPlanGeneratedGoMutations()
	want := []string{
		"QUERYPLAN_GENERATED_OMITS_CALLER_FIND_MANY",
		"QUERYPLAN_GENERATED_FIND_MANY_CALLS_COUNT",
		"QUERYPLAN_GENERATED_FIND_FIRST_CALLS_FIND_MANY",
		"QUERYPLAN_GENERATED_FIND_UNIQUE_RETURNS_ZERO_REPORT",
		"QUERYPLAN_GENERATED_COUNT_CALLS_FIND_MANY",
		"QUERYPLAN_GENERATED_AGGREGATE_RETURNS_ZERO_REPORT",
		"QUERYPLAN_GENERATED_GROUP_BY_RETURNS_ZERO_REPORT",
		"QUERYPLAN_GENERATED_EXPOSES_SYSTEM",
		"QUERYPLAN_GENERATED_EXPOSES_CALLER_TX",
		"QUERYPLAN_GENERATED_EXPOSES_MUTATION_EXPLAIN",
		"QUERYPLAN_GENERATED_OMITS_RELATION_GROUP",
		"QUERYPLAN_GENERATED_UNCONDITIONAL_RELATION_GROUP",
		"QUERYPLAN_GENERATED_OMITS_SCOPED",
		"QUERYPLAN_GENERATED_UNCONDITIONAL_SCOPED",
		"QUERYPLAN_GENERATED_DISCOVERY_SHELL_LOSES_SUPERSET",
		"QUERYPLAN_GENERATED_DISCOVERY_SHELL_LEAKS_CALLER_TX",
	}
	if len(mutations) != len(want) {
		t.Fatalf("generated query-plan mutation count = %d, want %d", len(mutations), len(want))
	}
	for index, label := range want {
		if mutations[index].Label != label {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, label)
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyGeneratedGoRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestQueryPlanGeneratedGoMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyGeneratedGoRepository(t)
	for _, mutation := range queryPlanGeneratedGoMutations() {
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

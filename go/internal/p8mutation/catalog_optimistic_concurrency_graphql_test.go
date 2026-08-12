package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyGraphQLMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	want := []string{
		"CONCURRENCY_GRAPHQL_EXPOSES_AUTHORED_TOKEN",
		"CONCURRENCY_GRAPHQL_UPDATE_OMITS_EXPECTED_VERSION",
		"CONCURRENCY_GRAPHQL_DELETE_OMITS_EXPECTED_VERSION",
		"CONCURRENCY_GRAPHQL_REEMITS_BATCH_ROOTS",
		"CONCURRENCY_GRAPHQL_ACCEPTS_MALFORMED_EXPECTATION",
		"CONCURRENCY_GRAPHQL_ACCEPTS_AUTHORED_TOKEN_MAP",
		"CONCURRENCY_GRAPHQL_REQUEST_ALIASES_EXISTING_CLAIM",
		"CONCURRENCY_GRAPHQL_REQUEST_ALIASES_UPSERT_EXPECTATION",
		"CONCURRENCY_GRAPHQL_NONVERSIONED_CLAIM_GAINS_AUTHORITY",
		"CONCURRENCY_GRAPHQL_CUSTOM_LIST_ACCEPTS_UPDATE_MANY",
		"CONCURRENCY_GRAPHQL_FORGED_CUSTOM_ACCEPTS_UPDATE_MANY",
		"CONCURRENCY_GRAPHQL_REEMITS_UNSAFE_INVERSE_MEMBERSHIP",
		"CONCURRENCY_GRAPHQL_CUSTOM_SELECTOR_MANUFACTURES_DELETE",
		"CONCURRENCY_GRAPHQL_EMITS_EMPTY_NESTED_HELPER",
		"CONCURRENCY_GRAPHQL_EMITS_ORPHAN_ROOT_UPDATE_HELPERS",
		"CONCURRENCY_GRAPHQL_LEGACY_REQUEST_REGAINS_DISPATCH",
	}
	mutations := optimisticConcurrencyGraphQLMutations()
	if len(mutations) != len(want) {
		t.Fatalf("GraphQL concurrency mutations=%d want=%d", len(mutations), len(want))
	}
	for index := range want {
		if mutations[index].Label != want[index] {
			t.Fatalf("mutation %d=%q want=%q", index, mutations[index].Label, want[index])
		}
		if _, globallyPublished := Find(want[index]); globallyPublished {
			t.Fatalf("isolated GraphQL mutation %q was added to the global catalog", want[index])
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyGraphQLRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestOptimisticConcurrencyGraphQLMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	mutations := optimisticConcurrencyGraphQLMutations()
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	repository := optimisticConcurrencyGraphQLRepository(t)
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
				t.Fatalf("mutation result=%#v want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

func optimisticConcurrencyGraphQLRepository(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve GraphQL mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

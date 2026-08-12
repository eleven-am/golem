package p8mutation

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptimisticConcurrencyRuntimeMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	want := []string{
		"CONCURRENCY_RUNTIME_CREATE_INITIALIZES_TWO",
		"CONCURRENCY_RUNTIME_OMITS_INCREMENT",
		"CONCURRENCY_RUNTIME_OMITS_EQUALITY_CAS",
		"CONCURRENCY_RUNTIME_OMITS_OVERFLOW_GUARD",
		"CONCURRENCY_RUNTIME_ORDINARY_RENDER_GAINS_AUTHORITY",
		"CONCURRENCY_RUNTIME_PROGRAM_OMITS_PRECHECK_CAPABILITY",
		"CONCURRENCY_RUNTIME_SKIPS_CLAIM_EQUALITY",
		"CONCURRENCY_RUNTIME_ACCEPTS_MAX_TOKEN",
		"CONCURRENCY_RUNTIME_LEGACY_ROOT_BYPASS",
		"CONCURRENCY_RUNTIME_NESTED_OWNER_BYPASS",
		"CONCURRENCY_RUNTIME_OMITS_HOOK_OWNED_CREATE_PREFLIGHT",
		"CONCURRENCY_RUNTIME_RETRY_REUSES_ORDINAL_ONE",
		"CONCURRENCY_RUNTIME_REPLAYS_SCOPED_BINDING",
		"CONCURRENCY_RUNTIME_RETRIES_UNIQUE",
		"CONCURRENCY_RUNTIME_SUPPRESSES_ABORT_FAILURE",
		"CONCURRENCY_RUNTIME_RECLASSIFIES_DIRTY_SAVEPOINT",
		"CONCURRENCY_RUNTIME_MULTIPLE_ROOT_OBSERVATIONS",
		"CONCURRENCY_RUNTIME_REPREPARES_ABSENT_RETRY_VALUES",
		"CONCURRENCY_RUNTIME_PRIVATE_PROBE_PRECEDES_AUTHORIZED_PROBE",
		"CONCURRENCY_RUNTIME_BEFORE_HOOK_PRECEDES_EXPECTATION_CHECK",
		"CONCURRENCY_RUNTIME_RESELECTS_AFTER_BEFORE_HOOK",
		"CONCURRENCY_RUNTIME_REPREPARES_EXISTING_RETRY_VALUES",
		"CONCURRENCY_RUNTIME_ACCEPTS_BEFORE_RETARGET",
		"CONCURRENCY_RUNTIME_OMITS_HOOK_MUTABLE_GRANTS",
		"CONCURRENCY_RUNTIME_DROPS_PRIVATE_TOKEN_PREIMAGE",
		"CONCURRENCY_RUNTIME_UNGROUPS_POSTCONDITION_OR",
	}
	mutations := optimisticConcurrencyRuntimeMutations()
	if len(mutations) != len(want) {
		t.Fatalf("runtime concurrency mutations=%d want=%d", len(mutations), len(want))
	}
	for index := range want {
		if mutations[index].Label != want[index] {
			t.Fatalf("mutation %d=%q want=%q", index, mutations[index].Label, want[index])
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime mutation catalog source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	if err := ValidatePatchSites(repository, mutations); err != nil {
		t.Fatal(err)
	}
	_ = repository
}

func TestOptimisticConcurrencyRuntimeMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	mutations := optimisticConcurrencyRuntimeMutations()
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime mutation catalog source")
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
				t.Fatalf("mutation result=%#v want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

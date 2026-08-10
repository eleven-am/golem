package p8mutation

import (
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"testing"
)

func TestP8RuntimeResourceMutationLabelsAreExactlyAssignedAndPatchSitesApply(t *testing.T) {
	mutations := runtimeResourceMutations()
	got := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		got = append(got, mutation.Label)
	}
	sort.Strings(got)
	want := []string{
		"AFTER_COMMIT_ERROR_REWRITES_SUCCESS",
		"CANCEL_LEAKS_GOROUTINE_OR_CONNECTION",
		"COMPUTED_PRIVATE_DEPENDENCY_ESCAPES",
		"HOOK_RESULT_BEFORE_VERIFICATION",
		"RELATION_LOAD_N_PLUS_ONE",
		"SLOW_SUBSCRIBER_DROPS_AND_CONTINUES",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("runtime/resource mutation labels = %#v, want %#v", got, want)
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
}

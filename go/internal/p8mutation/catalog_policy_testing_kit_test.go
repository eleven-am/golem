package p8mutation

import (
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"testing"
)

func TestPolicyTestingKitMutationLabelsAreExactlyAssignedAndPatchSitesApply(t *testing.T) {
	mutations := policyTestingKitMutations()
	got := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		got = append(got, mutation.Label)
	}
	sort.Strings(got)
	want := []string{
		"POLICY_KIT_ACCEPT_FOREIGN_GENERATION_DESCRIPTOR",
		"POLICY_KIT_ACCEPT_FOREIGN_GENERATION_FIELD",
		"POLICY_KIT_ACCEPT_WIDENED_REACH",
		"POLICY_KIT_CONDITIONAL_ACCESS_ALWAYS",
		"POLICY_KIT_DROP_DENY_RULE",
		"POLICY_KIT_EXPOSE_FACTORY_PANIC_PAYLOAD",
		"POLICY_KIT_OMIT_RELATION_DEPENDENCY",
		"POLICY_KIT_REUSE_ACTOR_POLICY_SET",
		"POLICY_KIT_UNPROVED_CONDITIONAL_DISCHARGED",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("policy testing kit mutation labels = %#v, want %#v", got, want)
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

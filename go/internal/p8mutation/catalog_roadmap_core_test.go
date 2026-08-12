package p8mutation

import "testing"

func TestRoadmapCoreMutationCatalogIsClosed(t *testing.T) {
	values := roadmapCoreMutations()
	want := []string{
		"QUERYPLAN_EXPOSES_POINTER_AUTHORITY",
		"CONCURRENCY_CLAIM_ACCEPTS_ZERO_VERSION",
		"CONCURRENCY_CLAIM_ABSENT_IS_INVALID",
		"CONCURRENCY_CLAIM_EXPOSES_VALUE_METHOD",
	}
	if len(values) != len(want) {
		t.Fatalf("roadmap core mutation count=%d want=%d", len(values), len(want))
	}
	seen := map[string]bool{}
	for _, value := range values {
		seen[value.Label] = true
	}
	for _, label := range want {
		if !seen[label] {
			t.Fatalf("missing roadmap core mutation %s", label)
		}
	}
	if err := ValidateCatalog(values); err != nil {
		t.Fatal(err)
	}
}

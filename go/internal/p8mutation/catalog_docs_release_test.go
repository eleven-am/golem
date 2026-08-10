package p8mutation

import (
	"slices"
	"sort"
	"testing"
)

func TestP8DocsReleaseCompatibilityMutationLabelsAreExactlyAssigned(t *testing.T) {
	got := make([]string, 0, len(docsReleaseCompatibilityMutations()))
	for _, mutation := range docsReleaseCompatibilityMutations() {
		got = append(got, mutation.Label)
	}
	sort.Strings(got)
	want := []string{
		"DOCUMENT_UNSUPPORTED_FEATURE",
		"DOC_SNIPPET_NOT_COMPILED",
		"EXAMPLE_HANDWRITES_CRUD_RESOLVER",
		"EXAMPLE_USES_LOCAL_REPLACE",
		"PATCH_BREAKS_GENERATED_ABI",
		"PATCH_BREAKS_GRAPHQL_SCHEMA",
		"RELEASE_FROM_MOVING_BRANCH",
		"RELEASE_TAG_MODULE_MISMATCH",
		"REPLACE_EXISTING_RELEASE_BYTES",
		"UNKNOWN_CODEC_BEST_EFFORT_DECODE",
		"UPGRADE_ADVANCES_LEDGER_BEFORE_VERIFY",
		"UPGRADE_REWRITES_EVENT_ID",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("docs/release mutation labels = %#v, want %#v", got, want)
	}
}

package mutationtest

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestReleaseGuideMutationCatalogIsExactAndEveryPatchSiteExists(t *testing.T) {
	want := []string{
		"RELEASE_GUIDE_ACCEPTS_PRIOR_BOUND_FIRST_RELEASE",
		"RELEASE_GUIDE_TRUSTS_SELF_COMPUTED_DIGEST",
		"RELEASE_GUIDE_BYPASSES_ENDPOINT_AND_ACTION_BINDING",
		"RELEASE_GUIDE_SKIPS_CURRENT_CORPUS_TREE",
		"RELEASE_GUIDE_BUILD_OMITS_ARTIFACT",
		"RELEASE_GUIDE_PUBLISH_SKIPS_ARTIFACT_BINDING",
	}
	got := make([]string, len(GuideCatalog()))
	for index, mutation := range GuideCatalog() {
		got[index] = mutation.Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release guide mutation labels=%v; want=%v", got, want)
	}
	if err := p8mutation.ValidateCatalog(GuideCatalog()); err != nil {
		t.Fatal(err)
	}
	if err := p8mutation.ValidatePatchSites(repositoryRoot(t), GuideCatalog()); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseGuideMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if os.Getenv("GOLEM_RUN_RELEASE_GUIDE_MUTATIONS") != "1" {
		return
	}
	runner := p8mutation.Runner{Repository: repositoryRoot(t), Keep: false}
	for _, mutation := range GuideCatalog() {
		mutation := mutation
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := runner.Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != p8mutation.StatusKilled {
				t.Fatalf("status=%s detail=%s baseline=%s mutant=%s", result.Status, result.Detail, result.BaselineEventSHA256, result.MutantEventSHA256)
			}
		})
	}
}

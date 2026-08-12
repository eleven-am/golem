package mutationtest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestReleaseTransitionMutationCatalogIsExactAndEveryPatchSiteExists(t *testing.T) {
	want := []string{
		"RELEASE_TRANSITION_SELECTS_LEXICALLY_EARLIER_BASELINE",
		"RELEASE_TRANSITION_IGNORES_COMPETITOR_SIGNATURE",
		"RELEASE_TRANSITION_SKIPS_CURRENT_EVIDENCE",
		"RELEASE_TRANSITION_IGNORES_CORPUS_DIGEST_BINDING",
		"RELEASE_TRANSITION_ACCEPTS_DIVERGENT_BASELINE",
		"RELEASE_TRANSITION_BYPASSES_COMPARE_RELEASE",
		"RELEASE_TRANSITION_CLI_DIGEST_CLAIMS_UNCHANGED",
		"RELEASE_TRANSITION_OBSERVATION_CLAIMS_UNCHANGED",
	}
	got := make([]string, len(Catalog()))
	for index, mutation := range Catalog() {
		got[index] = mutation.Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release transition mutation labels=%v; want=%v", got, want)
	}
	if err := p8mutation.ValidateCatalog(Catalog()); err != nil {
		t.Fatal(err)
	}
	if err := p8mutation.ValidatePatchSites(repositoryRoot(t), Catalog()); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseTransitionMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if os.Getenv("GOLEM_RUN_RELEASE_TRANSITION_MUTATIONS") != "1" {
		return
	}
	runner := p8mutation.Runner{Repository: repositoryRoot(t), Keep: false}
	for _, mutation := range Catalog() {
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

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve release transition mutation source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
}

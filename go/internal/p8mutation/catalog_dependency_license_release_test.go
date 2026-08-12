package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestDependencyLicenseReleaseMutationCatalogIsExactAndApplicable(t *testing.T) {
	want := []string{
		"DEPENDENCY_LICENSE_ACCEPTS_NOTICE_DIGEST_MISMATCH",
		"DEPENDENCY_LICENSE_ACCEPTS_TRUNCATED_COMPOSITE_TEXT",
		"RELEASE_LICENSE_IGNORES_SELECTED_MODULE_VERSION",
		"RELEASE_ARCHIVE_OMITS_THIRD_PARTY_NOTICES",
		"RELEASE_SOURCE_SPDX_OMITS_DECLARED_LICENSE",
		"RELEASE_SOURCE_SPDX_OMITS_EXTRACTED_LICENSE_TEXT",
		"RELEASE_BINARY_SPDX_OMITS_PROJECT_LICENSE",
		"RELEASE_BINARY_SPDX_OMITS_EXTRACTED_LICENSE_TEXT",
		"RELEASE_PROVENANCE_OMITS_LICENSE_AUTHORITY",
		"RELEASE_PUBLISH_IGNORES_STAGED_LICENSE_EVIDENCE",
	}
	mutations := dependencyLicenseReleaseMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependency license release mutation labels=%v want %v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyLicenseReleaseMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_DEPENDENCY_LICENSE_RELEASE_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range dependencyLicenseReleaseMutations() {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository, Keep: false}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result=%#v want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

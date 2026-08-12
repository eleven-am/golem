package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestSocialNATSHostMutationCatalogIsIsolatedExactAndApplicable(t *testing.T) {
	want := []string{
		"SOCIAL_HOST_DEFAULTS_NATS",
		"SOCIAL_HOST_IGNORES_STRAY_NATS_CONFIG",
		"SOCIAL_HOST_ALLOWS_SQLITE_NATS",
		"SOCIAL_HOST_OMITS_NATS_OBSERVER",
		"SOCIAL_HOST_LEAKS_RECONNECT_CONTEXT",
		"SOCIAL_HOST_IGNORES_TRANSPORT_READINESS",
		"SOCIAL_HOST_LISTENS_BEFORE_READINESS",
		"SOCIAL_HOST_IGNORES_EARLY_PUBLISHER_EXIT",
		"SOCIAL_HOST_CREATES_SERVER_AFTER_READINESS_TIMEOUT",
		"SOCIAL_HOST_IGNORES_HTTP_TERMINATION",
		"SOCIAL_HOST_IGNORES_PUBLISHER_TERMINATION",
		"SOCIAL_HOST_CLOSES_AFTER_HTTP_SHUTDOWN_FAILURE",
		"SOCIAL_HOST_CLOSES_AFTER_GRAPHQL_SHUTDOWN_FAILURE",
		"SOCIAL_HOST_CLOSES_AFTER_PUBLISHER_TIMEOUT",
		"SOCIAL_HOST_CLOSES_DATABASE_AFTER_TRANSPORT_FAILURE",
		"SOCIAL_HOST_LEAKS_CLEANUP_ERROR",
	}
	mutations := socialNATSHostMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("social NATS host mutation labels=%v want %v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestSocialNATSHostMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_SOCIAL_NATS_HOST_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range socialNATSHostMutations() {
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

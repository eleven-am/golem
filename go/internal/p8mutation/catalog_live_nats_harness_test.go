package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLiveNATSHarnessMutationCatalogIsIsolatedExactAndApplicable(t *testing.T) {
	want := []string{
		"LIVE_NATS_HARNESS_REPLACES_PINNED_IMAGE_DIGEST",
		"LIVE_NATS_HARNESS_LOWERS_BROKER_MAX_PAYLOAD",
		"LIVE_NATS_HARNESS_REMOVES_CONTAINER_OWNERSHIP",
		"LIVE_NATS_HARNESS_ALLOWS_OPTIONAL_POSTGRESQL",
		"LIVE_NATS_HARNESS_INCLUDES_SQLITE_PROFILE",
		"LIVE_NATS_ORACLE_OMITS_BOUNDARY_PLUS_ONE",
		"LIVE_NATS_ORACLE_IGNORES_OUTAGE_UNAVAILABILITY",
		"LIVE_NATS_ORACLE_IGNORES_DUPLICATE_BYTES",
		"LIVE_NATS_ORACLE_ROUTES_BY_GENERATION",
		"LIVE_NATS_ORACLE_ACCEPTS_CORE_REPLAY",
	}
	mutations := liveNATSHarnessMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live NATS harness mutation labels=%v want %v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestLiveNATSHarnessMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_LIVE_NATS_HARNESS_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range liveNATSHarnessMutations() {
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

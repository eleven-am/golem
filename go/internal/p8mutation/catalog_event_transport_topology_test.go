package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestEventTransportTopologyMutationCatalogIsExactAndApplicable(t *testing.T) {
	want := []string{
		"EVENT_TOPOLOGY_ACCEPTS_SQLITE_CROSS_PROCESS",
		"EVENT_TOPOLOGY_ACCEPTS_CROSS_PROCESS_WITHOUT_BINDING",
		"EVENT_TOPOLOGY_IGNORES_STARTUP_UNAVAILABILITY",
		"EVENT_TOPOLOGY_IGNORES_PREBIND_AVAILABILITY_CHANGE",
		"EVENT_TOPOLOGY_REPORTS_STALE_AVAILABILITY",
		"EVENT_TOPOLOGY_SKIPS_RUNTIME_BINDING",
		"EVENT_TOPOLOGY_ACCEPTS_MISSING_PAYLOAD_LIMIT",
		"EVENT_TOPOLOGY_ACCEPTS_UNDERSIZED_PAYLOAD_LIMIT",
	}
	mutations := eventTransportTopologyMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event transport topology mutation labels = %v, want %v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestEventTransportTopologyMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_EVENT_TOPOLOGY_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range eventTransportTopologyMutations() {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository, Keep: false}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestNATSTransportMutationCatalogIsIsolatedExactAndApplicable(t *testing.T) {
	want := []string{
		"NATS_TRANSPORT_DIALS_SQLITE",
		"NATS_TRANSPORT_DEFAULTS_SUBJECT_PREFIX",
		"NATS_TRANSPORT_ROUTES_BY_GENERATION",
		"NATS_TRANSPORT_OMITS_PUBLISH_FLUSH",
		"NATS_TRANSPORT_OMITS_SUBSCRIBE_FLUSH",
		"NATS_TRANSPORT_REBINDS_DECODER",
		"NATS_TRANSPORT_ACCEPTS_FOREIGN_EVENT_SCHEMA",
		"NATS_TRANSPORT_ACCEPTS_FOREIGN_MODEL",
		"NATS_TRANSPORT_USES_UNBOUNDED_STREAM_CAPACITY",
		"NATS_TRANSPORT_IGNORES_STREAM_BYTE_BOUND",
		"NATS_TRANSPORT_GROWS_OBSERVER_QUEUE",
		"NATS_TRANSPORT_REORDERS_OBSERVATIONS",
		"NATS_TRANSPORT_OBSERVES_UNDER_CALLBACK_LOCK",
		"NATS_TRANSPORT_IGNORES_CALLER_CANCELLATION",
		"NATS_TRANSPORT_REVIVES_TERMINAL_CLOSE",
		"NATS_TRANSPORT_IGNORES_RECONNECT_PAYLOAD",
		"NATS_TRANSPORT_IGNORES_INITIAL_PAYLOAD",
		"NATS_TRANSPORT_MISREPORTS_PAYLOAD_LIMIT",
		"NATS_TRANSPORT_DROPS_CONCURRENT_DISCONNECT",
		"NATS_TRANSPORT_DISCONNECT_REMAINS_AVAILABLE",
		"NATS_TRANSPORT_OBSERVER_GAINS_SUPPRESSION_LABEL",
		"NATS_TRANSPORT_LEAKS_BROKER_ERROR",
		"NATS_TRANSPORT_CLOSE_IS_NOT_IDEMPOTENT",
	}
	mutations := natsTransportMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NATS mutation labels=%v want=%v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestNATSTransportMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_NATS_TRANSPORT_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range natsTransportMutations() {
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

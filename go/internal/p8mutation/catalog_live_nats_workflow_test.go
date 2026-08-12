package p8mutation

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLiveNATSWorkflowMutationCatalogIsIsolatedExactAndApplicable(t *testing.T) {
	want := []string{
		"LIVE_NATS_WORKFLOW_OPTIONAL_MODE",
		"LIVE_NATS_WORKFLOW_TRUSTS_OTHER_IMAGE",
		"LIVE_NATS_WORKFLOW_TRUSTS_OTHER_REPO_DIGEST",
		"LIVE_NATS_WORKFLOW_OMITS_FIXED_NAME_PREFLIGHT",
		"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_OWNER",
		"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_IMAGE",
		"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_ORDER",
		"LIVE_NATS_WORKFLOW_OMITS_OUTAGE_C_PROFILE",
		"LIVE_NATS_WORKFLOW_OMITS_OUTAGE_LINGUISTIC_PROFILE",
		"LIVE_NATS_WORKFLOW_OMITS_DUPLICATE_C_PROFILE",
		"LIVE_NATS_WORKFLOW_OMITS_DUPLICATE_LINGUISTIC_PROFILE",
		"LIVE_NATS_WORKFLOW_TOOLCHAIN_OMITS_EXECUTION",
		"LIVE_NATS_WORKFLOW_PROVIDER_OMITS_EXECUTION",
		"LIVE_NATS_WORKFLOW_HARDENING_OMITS_EXECUTION",
		"LIVE_NATS_WORKFLOW_ALLOWS_JOB_OVERRIDE",
	}
	mutations := liveNATSWorkflowMutations()
	got := make([]string, len(mutations))
	for index := range mutations {
		got[index] = mutations[index].Label
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live NATS workflow mutation labels=%v want %v", got, want)
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestLiveNATSWorkflowMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() && os.Getenv("GOLEM_RUN_LIVE_NATS_WORKFLOW_MUTATIONS") != "1" {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range liveNATSWorkflowMutations() {
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

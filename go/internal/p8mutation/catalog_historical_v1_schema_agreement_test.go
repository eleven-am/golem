package p8mutation

import (
	"context"
	"testing"
)

func TestHistoricalV1SchemaAgreementMutationCatalogIsIsolatedAndApplicable(t *testing.T) {
	want := []string{
		"HISTORICAL_V1_SCHEMA_AGREEMENT_ACCEPTS_V2_V3_TEXT",
		"HISTORICAL_V1_SCHEMA_AGREEMENT_OMITS_CHECK_PROOF",
		"HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_FIELD",
		"HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_LENGTH",
		"HISTORICAL_V1_SCHEMA_AGREEMENT_IGNORES_CHECK_OPERATOR",
		"HISTORICAL_V1_SCHEMA_AGREEMENT_USES_ACTIVE_SOCIAL",
	}
	mutations := historicalV1SchemaAgreementMutations()
	if len(mutations) != len(want) {
		t.Fatalf("historical v1 schema agreement mutation count = %d, want %d", len(mutations), len(want))
	}
	for index := range want {
		if mutations[index].Label != want[index] {
			t.Fatalf("mutation %d = %q, want %q", index, mutations[index].Label, want[index])
		}
	}
	if err := ValidateCatalog(mutations); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchSites(optimisticConcurrencyPhysicalRepository(t), mutations); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalV1SchemaAgreementMutationsAreKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	repository := optimisticConcurrencyPhysicalRepository(t)
	for _, mutation := range historicalV1SchemaAgreementMutations() {
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := (Runner{Repository: repository}).Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
				t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutation.Gate.Test)
			}
		})
	}
}

func TestHistoricalV1SchemaAgreementFormatEscapeMutationIsKilled(t *testing.T) {
	if !isolatedMutationExecutionEnabled() {
		return
	}
	mutation := historicalV1SchemaAgreementMutations()[0]
	result, err := (Runner{Repository: optimisticConcurrencyPhysicalRepository(t)}).Run(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusKilled || result.Test != mutation.Gate.Test {
		t.Fatalf("mutation result = %#v, want KILLED by %s", result, mutation.Gate.Test)
	}
	t.Logf("%s %s in %s", result.Mutation, result.Status, result.Duration)
}

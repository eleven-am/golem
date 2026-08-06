package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestMutationLimitsDefaultToPortableHardCeilings(t *testing.T) {
	got, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got.nestedDepth != MaxMutationNestedDepth || got.touchedRows != MaxMutationTouchedRows || got.facts != MaxMutationFacts || got.outboxBytes != MaxMutationOutboxBytes || got.statementParameters != MaxMutationStatementParameters || got.upsertAttempts != MaxMutationUpsertAttempts {
		t.Fatalf("defaults=%+v", got)
	}
}

func TestMutationLimitsAcceptLowerValuesAndRejectInvalidValues(t *testing.T) {
	want := MutationLimits{MaxNestedDepth: 2, MaxTouchedRows: 10, MaxFacts: 8, MaxOutboxBytes: 1024, MaxStatementParameters: 50, MaxUpsertAttempts: 1}
	got, err := normalizeMutationLimits(want)
	if err != nil {
		t.Fatal(err)
	}
	if got.nestedDepth != 2 || got.touchedRows != 10 || got.facts != 8 || got.outboxBytes != 1024 || got.statementParameters != 50 || got.upsertAttempts != 1 {
		t.Fatalf("normalized=%+v", got)
	}
	invalid := []MutationLimits{
		{MaxNestedDepth: -1},
		{MaxNestedDepth: MaxMutationNestedDepth + 1},
		{MaxTouchedRows: MaxMutationTouchedRows + 1},
		{MaxFacts: MaxMutationFacts + 1},
		{MaxOutboxBytes: MaxMutationOutboxBytes + 1},
		{MaxStatementParameters: MaxMutationStatementParameters + 1},
		{MaxUpsertAttempts: MaxMutationUpsertAttempts + 1},
	}
	for index, value := range invalid {
		if _, err := normalizeMutationLimits(value); err == nil {
			t.Fatalf("invalid mutation limits %d accepted: %+v", index, value)
		}
	}
}

func TestMutationLimitsExactBoundariesAndOpenValidation(t *testing.T) {
	ceilings := MutationLimits{
		MaxNestedDepth: MaxMutationNestedDepth, MaxTouchedRows: MaxMutationTouchedRows,
		MaxFacts: MaxMutationFacts, MaxOutboxBytes: MaxMutationOutboxBytes,
		MaxStatementParameters: MaxMutationStatementParameters, MaxUpsertAttempts: MaxMutationUpsertAttempts,
	}
	if _, err := normalizeMutationLimits(ceilings); err != nil {
		t.Fatalf("exact portable mutation ceilings were refused: %v", err)
	}
	invalid := []MutationLimits{
		{MaxNestedDepth: MaxMutationNestedDepth + 1},
		{MaxTouchedRows: MaxMutationTouchedRows + 1},
		{MaxFacts: MaxMutationFacts + 1},
		{MaxOutboxBytes: MaxMutationOutboxBytes + 1},
		{MaxStatementParameters: MaxMutationStatementParameters + 1},
		{MaxUpsertAttempts: MaxMutationUpsertAttempts + 1},
	}
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		fixture := profile.fixture
		for index, limits := range invalid {
			_, err := Open(context.Background(), Config[mutationResultPrincipal, mutationResultActor]{
				DB: fixture.app.database, Provider: profile.provider, Bundle: fixture.schema.Bundle,
				Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors, MutationLimits: limits,
				ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
					return mutationResultActor{}, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "P4_RUNTIME_CONFIG: invalid mutation limits") {
				t.Fatalf("Open accepted invalid mutation limits %d: %+v err=%v", index, limits, err)
			}
		}
	})
}

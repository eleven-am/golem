package runtime

import (
	"fmt"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

const (
	MaxMutationNestedDepth         = 5
	MaxMutationTouchedRows         = 1_000
	MaxMutationFacts               = eventprovider.MaximumCausationFacts
	MaxMutationOutboxBytes         = 1 << 20
	MaxMutationStatementParameters = 999
	MaxMutationUpsertAttempts      = 3
)

// MutationLimits bounds the complete touched graph and commit-derived fact
// buffer of one outer mutation transaction. Zero selects the portable default,
// which is also the hard ceiling. Applications may lower but never raise it.
type MutationLimits struct {
	MaxNestedDepth         int
	MaxTouchedRows         int
	MaxFacts               int
	MaxOutboxBytes         int
	MaxStatementParameters int
	MaxUpsertAttempts      int
}

type normalizedMutationLimits struct {
	nestedDepth         int
	touchedRows         int
	facts               int
	outboxBytes         int
	statementParameters int
	upsertAttempts      int
}

func normalizeMutationLimits(value MutationLimits) (normalizedMutationLimits, error) {
	if value.MaxNestedDepth < 0 || value.MaxTouchedRows < 0 || value.MaxFacts < 0 || value.MaxOutboxBytes < 0 || value.MaxStatementParameters < 0 || value.MaxUpsertAttempts < 0 {
		return normalizedMutationLimits{}, fmt.Errorf("mutation limits cannot be negative")
	}
	result := normalizedMutationLimits{
		nestedDepth:         MaxMutationNestedDepth,
		touchedRows:         MaxMutationTouchedRows,
		facts:               MaxMutationFacts,
		outboxBytes:         MaxMutationOutboxBytes,
		statementParameters: MaxMutationStatementParameters,
		upsertAttempts:      MaxMutationUpsertAttempts,
	}
	type limit struct {
		name        string
		configured  int
		ceiling     int
		destination *int
	}
	limits := []limit{
		{name: "MaxNestedDepth", configured: value.MaxNestedDepth, ceiling: MaxMutationNestedDepth, destination: &result.nestedDepth},
		{name: "MaxTouchedRows", configured: value.MaxTouchedRows, ceiling: MaxMutationTouchedRows, destination: &result.touchedRows},
		{name: "MaxFacts", configured: value.MaxFacts, ceiling: MaxMutationFacts, destination: &result.facts},
		{name: "MaxOutboxBytes", configured: value.MaxOutboxBytes, ceiling: MaxMutationOutboxBytes, destination: &result.outboxBytes},
		{name: "MaxStatementParameters", configured: value.MaxStatementParameters, ceiling: MaxMutationStatementParameters, destination: &result.statementParameters},
		{name: "MaxUpsertAttempts", configured: value.MaxUpsertAttempts, ceiling: MaxMutationUpsertAttempts, destination: &result.upsertAttempts},
	}
	for _, item := range limits {
		if item.configured > item.ceiling {
			return normalizedMutationLimits{}, fmt.Errorf("%s exceeds provider-neutral ceiling %d", item.name, item.ceiling)
		}
		if item.configured != 0 {
			*item.destination = item.configured
		}
	}
	return result, nil
}

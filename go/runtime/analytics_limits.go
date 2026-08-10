package runtime

import "fmt"

type AnalyticsLimits struct {
	MaxMeasures             int
	MaxDimensions           int
	MaxRelationDepth        int
	MaxContributionRows     int
	MaxIntermediateGroups   int
	MaxProgrammaticGroups   int
	MaxScopedJoins          int
	MaxScopedSelections     int
	MaxScopedPredicateNodes int
}

type normalizedAnalyticsLimits AnalyticsLimits

func normalizeAnalyticsLimits(value AnalyticsLimits) (normalizedAnalyticsLimits, error) {
	type limit struct {
		name       string
		value      *int
		defaultVal int
		hardMax    int
	}
	limits := []limit{
		{"MaxMeasures", &value.MaxMeasures, 64, 256},
		{"MaxDimensions", &value.MaxDimensions, 16, 64},
		{"MaxRelationDepth", &value.MaxRelationDepth, 4, 8},
		{"MaxContributionRows", &value.MaxContributionRows, 1_000_000, 10_000_000},
		{"MaxIntermediateGroups", &value.MaxIntermediateGroups, 250_000, 1_000_000},
		{"MaxProgrammaticGroups", &value.MaxProgrammaticGroups, 100_000, 1_000_000},
		{"MaxScopedJoins", &value.MaxScopedJoins, 16, 64},
		{"MaxScopedSelections", &value.MaxScopedSelections, 128, 512},
		{"MaxScopedPredicateNodes", &value.MaxScopedPredicateNodes, 2_048, 8_192},
	}
	for _, item := range limits {
		if *item.value < 0 {
			return normalizedAnalyticsLimits{}, fmt.Errorf("analytics limit %s cannot be negative", item.name)
		}
		if *item.value == 0 {
			*item.value = item.defaultVal
		}
		if *item.value > item.hardMax {
			return normalizedAnalyticsLimits{}, fmt.Errorf("analytics limit %s exceeds hard maximum %d", item.name, item.hardMax)
		}
	}
	return normalizedAnalyticsLimits(value), nil
}

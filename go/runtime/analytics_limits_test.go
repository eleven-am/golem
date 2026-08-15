package runtime

import (
	"strings"
	"testing"
)

func TestAnalyticsLimitsFrozenDefaultsAndHardMaxima(t *testing.T) {
	defaults, err := normalizeAnalyticsLimits(AnalyticsLimits{})
	if err != nil {
		t.Fatalf("normalize zero limits: %v", err)
	}
	wantDefaults := normalizedAnalyticsLimits{
		MaxMeasures:             64,
		MaxDimensions:           16,
		MaxRelationDepth:        4,
		MaxContributionRows:     1_000_000,
		MaxIntermediateGroups:   250_000,
		MaxProgrammaticGroups:   100_000,
		MaxScopedJoins:          16,
		MaxScopedSelections:     128,
		MaxScopedPredicateNodes: 2_048,
	}
	if defaults != wantDefaults {
		t.Fatalf("zero-value defaults = %#v, want %#v", defaults, wantDefaults)
	}

	maxima := AnalyticsLimits{
		MaxMeasures:             256,
		MaxDimensions:           64,
		MaxRelationDepth:        8,
		MaxContributionRows:     10_000_000,
		MaxIntermediateGroups:   1_000_000,
		MaxProgrammaticGroups:   1_000_000,
		MaxScopedJoins:          64,
		MaxScopedSelections:     512,
		MaxScopedPredicateNodes: 8_192,
	}
	if _, err := normalizeAnalyticsLimits(maxima); err != nil {
		t.Fatalf("hard maxima must be accepted: %v", err)
	}

	tests := []struct {
		name   string
		limits AnalyticsLimits
	}{
		{"MaxMeasures", AnalyticsLimits{MaxMeasures: 257}},
		{"MaxDimensions", AnalyticsLimits{MaxDimensions: 65}},
		{"MaxRelationDepth", AnalyticsLimits{MaxRelationDepth: 9}},
		{"MaxContributionRows", AnalyticsLimits{MaxContributionRows: 10_000_001}},
		{"MaxIntermediateGroups", AnalyticsLimits{MaxIntermediateGroups: 1_000_001}},
		{"MaxProgrammaticGroups", AnalyticsLimits{MaxProgrammaticGroups: 1_000_001}},
		{"MaxScopedJoins", AnalyticsLimits{MaxScopedJoins: 65}},
		{"MaxScopedSelections", AnalyticsLimits{MaxScopedSelections: 513}},
		{"MaxScopedPredicateNodes", AnalyticsLimits{MaxScopedPredicateNodes: 8_193}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeAnalyticsLimits(test.limits)
			if err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("normalize above %s hard maximum error = %v", test.name, err)
			}
		})
	}
}

func TestAnalyticsLimitsRejectEveryNegativeField(t *testing.T) {
	tests := []struct {
		name   string
		limits AnalyticsLimits
	}{
		{"MaxMeasures", AnalyticsLimits{MaxMeasures: -1}},
		{"MaxDimensions", AnalyticsLimits{MaxDimensions: -1}},
		{"MaxRelationDepth", AnalyticsLimits{MaxRelationDepth: -1}},
		{"MaxContributionRows", AnalyticsLimits{MaxContributionRows: -1}},
		{"MaxIntermediateGroups", AnalyticsLimits{MaxIntermediateGroups: -1}},
		{"MaxProgrammaticGroups", AnalyticsLimits{MaxProgrammaticGroups: -1}},
		{"MaxScopedJoins", AnalyticsLimits{MaxScopedJoins: -1}},
		{"MaxScopedSelections", AnalyticsLimits{MaxScopedSelections: -1}},
		{"MaxScopedPredicateNodes", AnalyticsLimits{MaxScopedPredicateNodes: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeAnalyticsLimits(test.limits)
			if err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("normalize negative %s error = %v", test.name, err)
			}
		})
	}
}

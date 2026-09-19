package events

import (
	"testing"
	"time"
)

func TestRetentionDisabledSurvivesNormalization(t *testing.T) {
	normalized, err := NormalizeLimits(Limits{RetentionEvery: RetentionDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RetentionEvery != RetentionDisabled || normalized.RetentionEnabled() {
		t.Fatalf("RetentionEvery=%s enabled=%t", normalized.RetentionEvery, normalized.RetentionEnabled())
	}
	if !(Limits{}).RetentionEnabled() || !DefaultLimits().RetentionEnabled() {
		t.Fatal("default limits disabled retention")
	}
	if _, err := NormalizeLimits(Limits{RetentionEvery: RetentionDisabled - time.Nanosecond}); err == nil {
		t.Fatal("a negative interval other than RetentionDisabled was accepted")
	}
}

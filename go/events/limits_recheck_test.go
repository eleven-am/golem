package events

import (
	"testing"
	"time"
)

func TestRevalidateEveryIsOffOrAtLeastTheMinimum(t *testing.T) {
	for _, every := range []time.Duration{0, MinimumRecheckInterval, time.Hour} {
		limits, err := NormalizeLimits(Limits{RevalidateEvery: every})
		if err != nil || limits.RevalidateEvery != every {
			t.Fatalf("RevalidateEvery %s: limits=%s err=%v", every, limits.RevalidateEvery, err)
		}
	}
	for _, every := range []time.Duration{time.Nanosecond, MinimumRecheckInterval - time.Millisecond, -time.Second, 25 * time.Hour} {
		if _, err := NormalizeLimits(Limits{RevalidateEvery: every}); err == nil {
			t.Fatalf("RevalidateEvery %s was accepted", every)
		} else if code, _ := CodeOf(err); code != CodeEventConfig {
			t.Fatalf("RevalidateEvery %s code=%s", every, code)
		}
	}
}

func TestMaxSubscriptionsPerPrincipalIsOffByDefaultAndBounded(t *testing.T) {
	limits, err := NormalizeLimits(Limits{})
	if err != nil || limits.MaxSubscriptionsPerPrincipal != 0 || limits.RevalidateEvery != 0 {
		t.Fatalf("defaults=%+v err=%v", limits, err)
	}
	for _, value := range []int{-1, MaximumLimits().MaxSubscriptionsPerPrincipal + 1} {
		if _, err := NormalizeLimits(Limits{MaxSubscriptionsPerPrincipal: value}); err == nil {
			t.Fatalf("MaxSubscriptionsPerPrincipal %d was accepted", value)
		}
	}
}

package queue_test

import (
	"math"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/queue"
)

func TestBackoffDelayIsTotal(t *testing.T) {
	rows := []struct {
		name    string
		backoff queue.Backoff
		attempt int
		bound   time.Duration
	}{
		{name: "zero is the default", backoff: queue.Backoff{}, attempt: 1, bound: 5 * time.Second},
		{name: "negative base and cap are the defaults", backoff: queue.Backoff{Base: -1, Cap: -1}, attempt: 1, bound: 5 * time.Second},
		{name: "most negative base and cap are the defaults", backoff: queue.Backoff{Base: math.MinInt64, Cap: math.MinInt64}, attempt: 3, bound: 20 * time.Second},
		{name: "negative base is the default base", backoff: queue.Backoff{Base: -time.Second, Cap: time.Hour}, attempt: 3, bound: 20 * time.Second},
		{name: "negative cap is the default cap", backoff: queue.Backoff{Base: time.Second, Cap: -time.Second}, attempt: 30, bound: 5 * time.Minute},
		{name: "base above cap is held to the cap", backoff: queue.Backoff{Base: time.Minute, Cap: time.Second}, attempt: 1, bound: time.Second},
		{name: "base above negative cap is held to the default cap", backoff: queue.Backoff{Base: time.Hour, Cap: -1}, attempt: 1, bound: 5 * time.Minute},
		{name: "zero attempt is the first", backoff: queue.Backoff{Base: time.Second, Cap: time.Minute}, attempt: 0, bound: time.Second},
		{name: "most negative attempt is the first", backoff: queue.Backoff{Base: time.Second, Cap: time.Minute}, attempt: math.MinInt, bound: time.Second},
		{name: "huge attempt is held to the cap", backoff: queue.Backoff{Base: time.Second, Cap: time.Hour}, attempt: math.MaxInt, bound: time.Hour},
		{name: "one doubling past the duration range is held to the cap", backoff: queue.Backoff{Base: 1 << 62, Cap: math.MaxInt64}, attempt: 3, bound: math.MaxInt64},
		{name: "doubling past the duration range is held to the cap", backoff: queue.Backoff{Base: 1 << 62, Cap: math.MaxInt64}, attempt: math.MaxInt, bound: math.MaxInt64},
		{name: "maximum base and cap", backoff: queue.Backoff{Base: math.MaxInt64, Cap: math.MaxInt64}, attempt: math.MaxInt, bound: math.MaxInt64},
		{name: "one nanosecond window", backoff: queue.Backoff{Base: 1, Cap: 1}, attempt: math.MaxInt, bound: 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			distinct := make(map[time.Duration]struct{})
			for sample := 0; sample < 64; sample++ {
				delay := row.backoff.Delay(row.attempt)
				if delay < 0 || delay >= row.bound {
					t.Fatalf("delay %s is outside [0,%s)", delay, row.bound)
				}
				distinct[delay] = struct{}{}
			}
			if row.bound > time.Microsecond && len(distinct) < 2 {
				t.Fatalf("a %s window produced %d distinct delays", row.bound, len(distinct))
			}
		})
	}
}

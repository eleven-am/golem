package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSteadySmokeDeliversEveryExpectedEvent(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not set")
	}
	environment, err := newEnvironment(dsn, "../..", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := Execute(context.Background(), environment, RunSpec{Kind: KindSteady, Users: 10, Seed: 1000, Warmup: 2 * time.Second, Measure: 8 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Passed() {
		t.Fatalf("violations: %v", detail.Violations)
	}
	if detail.Delivery.Expected == 0 {
		t.Fatal("the run produced no expected events")
	}
}

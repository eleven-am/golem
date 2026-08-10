// Package providertest contains the provider-neutral P7 delivery state-machine
// conformance harness. Provider packages invoke it against live databases; it
// does not calculate expected state through provider SQL.
package providertest

import (
	"context"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

// RunCoordinatorContract expects exactly one pending causal group in the
// supplied isolated coordinator fixture.
func RunCoordinatorContract(t testing.TB, coordinator eventprovider.Coordinator, causation string, factRows int) {
	t.Helper()
	ctx := context.Background()
	state, err := coordinator.Inspect(ctx, causation)
	if err != nil || state.Status != eventprovider.StatusPending || state.ImmutableFactRows != factRows {
		t.Fatalf("virtual pending=%#v error=%v", state, err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 {
		t.Fatalf("initial claim=%#v error=%v", leases, err)
	}
	assertWholeCausation(t, leases[0], causation, factRows)
	firstToken := leases[0].Delivery.LeaseToken
	if changed, err := coordinator.Retry(ctx, causation, firstToken, 0, "provider-harness-retry"); err != nil || !changed {
		t.Fatalf("retry changed=%t error=%v", changed, err)
	}
	leases, err = coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 || leases[0].Delivery.LeaseToken == firstToken {
		t.Fatalf("reowned claim=%#v error=%v", leases, err)
	}
	secondToken := leases[0].Delivery.LeaseToken
	staleTransitions := []struct {
		name string
		call func() (bool, error)
	}{
		{name: "renew", call: func() (bool, error) { return coordinator.Renew(ctx, causation, firstToken, time.Second) }},
		{name: "ack", call: func() (bool, error) { return coordinator.Acknowledge(ctx, causation, firstToken) }},
		{name: "retry", call: func() (bool, error) { return coordinator.Retry(ctx, causation, firstToken, 0, "stale") }},
		{name: "block", call: func() (bool, error) { return coordinator.Block(ctx, causation, firstToken, "stale") }},
		{name: "release", call: func() (bool, error) { return coordinator.Release(ctx, causation, firstToken) }},
	}
	for _, transition := range staleTransitions {
		if changed, transitionErr := transition.call(); transitionErr != nil || changed {
			t.Fatalf("stale %s changed=%t error=%v", transition.name, changed, transitionErr)
		}
	}
	if changed, err := coordinator.Block(ctx, causation, secondToken, "provider-harness-block"); err != nil || !changed {
		t.Fatalf("owner block changed=%t error=%v", changed, err)
	}
	state, err = coordinator.Inspect(ctx, causation)
	if err != nil || state.Status != eventprovider.StatusBlocked || state.LastFailureCode != "provider-harness-block" {
		t.Fatalf("blocked=%#v error=%v", state, err)
	}
	if changed, err := coordinator.Resume(ctx, causation); err != nil || !changed {
		t.Fatalf("resume changed=%t error=%v", changed, err)
	}
	leases, err = coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 {
		t.Fatalf("resumed claim=%#v error=%v", leases, err)
	}
	if changed, err := coordinator.Block(ctx, causation, leases[0].Delivery.LeaseToken, "provider-harness-retire"); err != nil || !changed {
		t.Fatalf("pre-retire block changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Retire(ctx, causation); err != nil || !changed {
		t.Fatalf("retire changed=%t error=%v", changed, err)
	}
	state, err = coordinator.Inspect(ctx, causation)
	if err != nil || state.Status != eventprovider.StatusRetired || state.RetiredAt == nil {
		t.Fatalf("retired=%#v error=%v", state, err)
	}
}

func assertWholeCausation(t testing.TB, lease eventprovider.Lease, causation string, rows int) {
	t.Helper()
	if lease.Delivery.CausationID != causation || lease.Delivery.Status != eventprovider.StatusLeased || len(lease.Facts) != rows {
		t.Fatalf("causal lease=%#v", lease)
	}
	for index, fact := range lease.Facts {
		if fact.CausationID != causation || fact.TransactionOrdinal != int64(index+1) {
			t.Fatalf("fact %d is outside exact ordinal sequence: %#v", index, fact)
		}
	}
}

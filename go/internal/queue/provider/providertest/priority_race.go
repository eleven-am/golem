package providertest

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

type priorityChangeOutcome struct {
	changed bool
	err     error
}

type priorityClaimOutcome struct {
	records []queueprovider.Record
	err     error
}

func assertPriorityPayloadIdentity(t testing.TB, before, after queueprovider.Record) {
	t.Helper()
	if before.ID != after.ID || before.Type != after.Type || !bytes.Equal(before.Payload, after.Payload) || before.MaxAttempts != after.MaxAttempts || before.DedupeKey != after.DedupeKey || before.ExclusiveKey != after.ExclusiveKey || !before.EnqueuedAt.Equal(after.EnqueuedAt) {
		t.Fatalf("priority transition changed job identity/payload: before=%#v after=%#v", before, after)
	}
}

// PriorityPromotionRacesClaim permits exactly two serial outcomes: the normal
// claimant owns the original job, or promotion wins and only the interactive
// claimant may own it. Later promotion cannot rewrite that lease or its budget.
func PriorityPromotionRacesClaim(t testing.TB, fixture Fixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for round := 0; round < 12; round++ {
		name := fmt.Sprintf("gate.priority.claim.%d", round)
		input := newRequest(t, name, request{dedupe: name, exclusive: name})
		stored, err := fixture.Store.Enqueue(ctx, nil, input)
		if err != nil {
			t.Fatal(err)
		}
		before := inspect(t, fixture, stored.ID)
		resource := &queueprovider.ClaimResource{Name: name, Concurrency: 2, Costs: map[string]int64{name: 2}}
		options := queueprovider.ClaimOptions{Types: []string{name}, Limit: 1, LeaseDuration: longLease, Resource: resource}
		start := make(chan struct{})
		promotion := make(chan priorityChangeOutcome, 1)
		claiming := make(chan priorityClaimOutcome, 1)
		go func() {
			<-start
			changed, err := fixture.Store.Promote(ctx, nil, stored.ID, 1, false)
			promotion <- priorityChangeOutcome{changed, err}
		}()
		go func() {
			<-start
			records, err := fixture.Store.Claim(ctx, options)
			claiming <- priorityClaimOutcome{records, err}
		}()
		close(start)
		promoted, claimed := <-promotion, <-claiming
		if promoted.err != nil || claimed.err != nil {
			t.Fatalf("round %d: promotion=%v claim=%v", round, promoted.err, claimed.err)
		}
		wantPriority := 0
		if promoted.changed {
			wantPriority = 1
			if len(claimed.records) != 0 {
				t.Fatalf("normal claim consumed promoted work: %#v", claimed.records)
			}
			pending := inspect(t, fixture, stored.ID)
			if pending.State != queueprovider.StatePending || pending.Priority != 1 || pending.AttemptCount != 0 || pending.LeaseToken != "" || !pending.AvailableAt.Equal(before.AvailableAt) {
				t.Fatalf("promotion did not preserve unclaimed state: %#v", pending)
			}
			options.Priority = 1
			claimed.records, claimed.err = fixture.Store.Claim(ctx, options)
		}
		if claimed.err != nil || len(claimed.records) != 1 {
			t.Fatalf("winning lane failed to claim: %#v error=%v", claimed.records, claimed.err)
		}
		lease := claimed.records[0]
		if lease.ID != stored.ID || lease.Priority != wantPriority || lease.State != queueprovider.StateLeased || lease.AttemptCount != 1 || lease.LeaseToken == "" || lease.LeaseUntil == nil {
			t.Fatalf("claim/promotion has no permitted serial outcome: promoted=%t lease=%#v", promoted.changed, lease)
		}
		assertPriorityPayloadIdentity(t, before, lease)
		if changed, err := fixture.Store.Promote(ctx, nil, lease.ID, 1, true); err != nil || changed {
			t.Fatalf("leased job was promoted: changed=%t error=%v", changed, err)
		}
		after := inspect(t, fixture, stored.ID)
		if after.Priority != lease.Priority || after.AttemptCount != lease.AttemptCount || after.LeaseToken != lease.LeaseToken || after.LeaseUntil == nil || !after.LeaseUntil.Equal(*lease.LeaseUntil) || !after.AvailableAt.Equal(lease.AvailableAt) || !after.UpdatedAt.Equal(lease.UpdatedAt) {
			t.Fatalf("refused promotion rewrote active lease: before=%#v after=%#v", lease, after)
		}
		duplicate := input
		duplicate.ID, _ = queueprovider.NewIdentifier()
		duplicate.Priority = 1
		if result, err := fixture.Store.Enqueue(ctx, nil, duplicate); err != nil || result.ID != stored.ID || result.Inserted {
			t.Fatalf("race changed dedupe ownership: %#v error=%v", result, err)
		}
		// No exclusive key on this competitor: only the original weighted
		// lease can block it. Promotion must not clear its resource snapshot.
		competitor := newRequest(t, name, request{})
		competitor.Priority = 1
		if _, err := fixture.Store.Enqueue(ctx, nil, competitor); err != nil {
			t.Fatal(err)
		}
		options.Priority = 1
		if records, err := fixture.Store.Claim(ctx, options); err != nil || len(records) != 0 {
			t.Fatalf("promotion lost active resource charge: records=%#v error=%v", records, err)
		}
		if changed, err := fixture.Store.Succeed(ctx, lease.ID, lease.LeaseToken, ""); err != nil || !changed {
			t.Fatalf("winning token lost transition authority: changed=%t error=%v", changed, err)
		}
		if records, err := fixture.Store.Claim(ctx, options); err != nil || len(records) != 1 || records[0].ID != competitor.ID {
			t.Fatalf("terminal transition did not release resource: records=%#v error=%v", records, err)
		}
	}
}

func PriorityPromotionRacesCancel(t testing.TB, fixture Fixture) {
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	for round := 0; round < 12; round++ {
		name := fmt.Sprintf("gate.priority.cancel.%d", round)
		id := enqueue(t, fixture, name, request{delay: time.Hour, dedupe: name, exclusive: name})
		before := inspect(t, fixture, id)
		start := make(chan struct{})
		promotion := make(chan priorityChangeOutcome, 1)
		cancellation := make(chan priorityChangeOutcome, 1)
		go func() {
			<-start
			changed, err := fixture.Store.Promote(ctx, nil, id, 1, true)
			promotion <- priorityChangeOutcome{changed, err}
		}()
		go func() {
			<-start
			result, err := fixture.Store.Cancel(ctx, nil, id)
			cancellation <- priorityChangeOutcome{result.Changed && result.Terminal, err}
		}()
		close(start)
		promoted, canceled := <-promotion, <-cancellation
		if promoted.err != nil || canceled.err != nil || !canceled.changed {
			t.Fatalf("race promotion=%#v cancel=%#v", promoted, canceled)
		}
		after := inspect(t, fixture, id)
		wantPriority := 0
		if promoted.changed {
			wantPriority = 1
		}
		if after.State != queueprovider.StateCanceled || after.Priority != wantPriority || after.AttemptCount != 0 || after.LeaseToken != "" || after.LeaseUntil != nil || after.FinishedAt == nil || after.LastCode != queueprovider.CodeCanceled {
			t.Fatalf("promotion resurrected canceled work: promoted=%t after=%#v", promoted.changed, after)
		}
		assertPriorityPayloadIdentity(t, before, after)
		if changed, err := fixture.Store.Promote(ctx, nil, id, 1, true); err != nil || changed {
			t.Fatalf("terminal promotion changed work: changed=%t error=%v", changed, err)
		}
		for _, lane := range []int{0, 1} {
			if records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Priority: lane, Types: []string{name}, Limit: 1, LeaseDuration: longLease}); err != nil || len(records) != 0 {
				t.Fatalf("canceled work is claimable in lane %d: %#v error=%v", lane, records, err)
			}
		}
	}
}

func ConcurrentPriorityPromotionsAreMonotonic(t testing.TB, fixture Fixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for round := 0; round < 12; round++ {
		name := fmt.Sprintf("gate.priority.concurrent.%d", round)
		id := enqueue(t, fixture, name, request{delay: time.Hour, dedupe: name, exclusive: name})
		before := inspect(t, fixture, id)
		start := make(chan struct{})
		results := make(chan priorityChangeOutcome, 2)
		for range 2 {
			go func() {
				<-start
				changed, err := fixture.Store.Promote(ctx, nil, id, 1, true)
				results <- priorityChangeOutcome{changed, err}
			}()
		}
		close(start)
		changes := 0
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.changed {
				changes++
			}
		}
		after := inspect(t, fixture, id)
		if changes != 1 || after.Priority != 1 || after.State != queueprovider.StatePending || after.AttemptCount != 0 || after.LeaseToken != "" || after.LeaseUntil != nil || !after.AvailableAt.Before(before.AvailableAt) {
			t.Fatalf("concurrent promotion changed=%d state=%#v", changes, after)
		}
		assertPriorityPayloadIdentity(t, before, after)
		if changed, err := fixture.Store.Promote(ctx, nil, id, 0, true); err != nil || changed {
			t.Fatalf("lower priority rewrote winning promotion: changed=%t error=%v", changed, err)
		}
		records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{name}, Limit: 1, LeaseDuration: longLease})
		if err != nil || len(records) != 1 || records[0].AttemptCount != 1 {
			t.Fatalf("promoted work was not immediately claimable once: %#v error=%v", records, err)
		}
	}
}

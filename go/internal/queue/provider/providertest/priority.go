package providertest

import (
	"context"
	"reflect"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

// PriorityPromotion tests the durable state machine through the existing
// provider harness; no expected transition is computed through provider SQL.
func PriorityPromotion(t testing.TB, fixture Fixture) {
	ctx := context.Background()
	request := newRequest(t, "gate.priority", request{delay: time.Hour, dedupe: "gate.priority"})
	stored, err := fixture.Store.Enqueue(ctx, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	before, err := fixture.Store.Inspect(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := request
	duplicate.ID, _ = queueprovider.NewIdentifier()
	duplicate.Priority, duplicate.Delay = 1, 0
	if coalesced, err := fixture.Store.Enqueue(ctx, nil, duplicate); err != nil || coalesced.ID != stored.ID || coalesced.Inserted {
		t.Fatalf("coalescing=%#v error=%v", coalesced, err)
	}
	unchanged, err := fixture.Store.Inspect(ctx, stored.ID)
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatalf("dedupe changed existing work: before=%#v after=%#v error=%v", before, unchanged, err)
	}
	if changed, err := fixture.Store.Promote(ctx, nil, stored.ID, 1, false); err != nil || !changed {
		t.Fatalf("promotion=%t error=%v", changed, err)
	}
	promoted, err := fixture.Store.Inspect(ctx, stored.ID)
	if err != nil || promoted.Priority != 1 || !promoted.AvailableAt.Equal(before.AvailableAt) || promoted.AttemptCount != 0 || promoted.State != queueprovider.StatePending {
		t.Fatalf("promotion lost delay or state: %#v error=%v", promoted, err)
	}
	if changed, err := fixture.Store.Promote(ctx, nil, stored.ID, 0, true); err != nil || changed {
		t.Fatalf("lower-priority ReadyNow changed work: %t error=%v", changed, err)
	}
	if records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{request.Type}, Limit: 1, LeaseDuration: longLease}); err != nil || len(records) != 0 {
		t.Fatalf("promotion erased delay: records=%v error=%v", records, err)
	}
	if changed, err := fixture.Store.Promote(ctx, nil, stored.ID, 1, true); err != nil || !changed {
		t.Fatalf("ReadyNow=%t error=%v", changed, err)
	}
	if records := claim(t, fixture, request.Type, longLease, 1); len(records) != 0 {
		t.Fatalf("normal lane claimed interactive work: %#v", records)
	}
	records, err := fixture.Store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{request.Type}, Limit: 1, LeaseDuration: longLease})
	if err != nil || len(records) != 1 || records[0].ID != stored.ID || records[0].Priority != 1 || records[0].AttemptCount != 1 {
		t.Fatalf("interactive claim=%#v error=%v", records, err)
	}
	expireLeases(t, fixture, stored.ID)
	if changed, err := fixture.Store.Promote(ctx, nil, stored.ID, 1, true); err != nil || changed {
		t.Fatalf("expired leased work was promoted: %t error=%v", changed, err)
	}
}

func PriorityPromotionRollsBack(t testing.TB, fixture Fixture) {
	ctx := context.Background()
	id := enqueue(t, fixture, "gate.priority.rollback", request{delay: time.Hour})
	before, err := fixture.Store.Inspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.Database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if changed, err := fixture.Store.Promote(ctx, tx, id, 1, true); err != nil || !changed {
		t.Fatalf("transactional promotion=%t error=%v", changed, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.Store.Inspect(ctx, id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback changed work: before=%#v after=%#v error=%v", before, after, err)
	}
}

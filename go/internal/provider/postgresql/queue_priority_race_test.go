package postgresql

import (
	"context"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestPriorityPromotionRacesClaim(t *testing.T) {
	runQueueGate(t, providertest.PriorityPromotionRacesClaim)
}

func TestPriorityPromotionRacesCancel(t *testing.T) {
	runQueueGate(t, providertest.PriorityPromotionRacesCancel)
}

func TestConcurrentPriorityPromotionsAreMonotonic(t *testing.T) {
	runQueueGate(t, providertest.ConcurrentPriorityPromotionsAreMonotonic)
}

// Discovery does not lock a row. Promotion between that observation and the
// real lock query must remove it from the normal lane's eligible candidates.
func TestPriorityPromotionBetweenDiscoveryAndLockRevalidatesLane(t *testing.T) {
	runQueueGate(t, func(t testing.TB, fixture providertest.Fixture) {
		ctx := context.Background()
		store := fixture.Store.(*queueStore)
		input := queueprovider.EnqueueRequest{ID: "promotion-between-discovery-and-lock", Type: "gate.priority.lock", Payload: []byte(`{"value":1}`), MaxAttempts: 3}
		if _, err := store.Enqueue(ctx, nil, input); err != nil {
			t.Fatal(err)
		}
		predicate := `job."type"=$1 AND job."priority"=$2`
		arguments := []any{input.Type, 0}
		discovery, discoveryArguments := postgresqlClaimDiscovery(store.table(), predicate, append([]any(nil), arguments...), nil, 1)
		var candidates []postgresqlClaimCandidate
		if err := fixture.Database.SelectContext(ctx, &candidates, discovery, discoveryArguments...); err != nil || len(candidates) != 1 || candidates[0].ID != input.ID {
			t.Fatalf("initial normal discovery=%#v error=%v", candidates, err)
		}
		if changed, err := store.Promote(ctx, nil, input.ID, 1, false); err != nil || !changed {
			t.Fatalf("intervening promotion=%t error=%v", changed, err)
		}
		transaction, err := fixture.Database.BeginTxx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		lock, lockArguments := postgresqlClaimLock(store.table(), predicate, append([]any(nil), arguments...), []string{candidates[0].ID})
		var locked []postgresqlClaimCandidate
		if err := transaction.SelectContext(ctx, &locked, lock, lockArguments...); err != nil || len(locked) != 0 {
			t.Fatalf("stale normal discovery locked promoted work: %#v error=%v", locked, err)
		}
		if err := transaction.Rollback(); err != nil {
			t.Fatal(err)
		}
		records, err := store.Claim(ctx, queueprovider.ClaimOptions{Priority: 1, Types: []string{input.Type}, Limit: 1, LeaseDuration: time.Minute})
		if err != nil || len(records) != 1 || records[0].ID != input.ID || records[0].Priority != 1 || records[0].AttemptCount != 1 {
			t.Fatalf("interactive lane lost promoted candidate: %#v error=%v", records, err)
		}
	})
}

package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestClaimIsExclusiveUnderConcurrency(t *testing.T) {
	providertest.ClaimIsExclusiveUnderConcurrency(t, newQueueFixture(t))
}

func TestSharedResourceCapacityIsAtomicAndWeighted(t *testing.T) {
	providertest.SharedResourceCapacityIsAtomicAndWeighted(t, newQueueFixture(t))
}

func TestStaleTokenCannotTransition(t *testing.T) {
	providertest.StaleTokenCannotTransition(t, newQueueFixture(t))
}

func TestExpiredLeaseIsReclaimedByOrdinaryClaim(t *testing.T) {
	providertest.ExpiredLeaseIsReclaimedByOrdinaryClaim(t, newQueueFixture(t))
}

func TestExpiredLeaseCannotBeRenewed(t *testing.T) {
	providertest.ExpiredLeaseCannotBeRenewed(t, newQueueFixture(t))
}

func TestExpiredLeaseCannotTransition(t *testing.T) {
	providertest.ExpiredLeaseCannotTransition(t, newQueueFixture(t))
}

func TestUncountedRetryPreservesAttempt(t *testing.T) {
	providertest.UncountedRetryPreservesAttempt(t, newQueueFixture(t))
}

func TestExpiredFinalAttemptFailsWithoutReexecution(t *testing.T) {
	providertest.ExpiredFinalAttemptFailsWithoutReexecution(t, newQueueFixture(t))
}

func TestExpiredCanceledLeaseIsTerminalWithoutReexecution(t *testing.T) {
	providertest.ExpiredCanceledLeaseIsTerminalWithoutReexecution(t, newQueueFixture(t))
}

func TestExpiredLeaseCancellationIsImmediate(t *testing.T) {
	providertest.ExpiredLeaseCancellationIsImmediate(t, newQueueFixture(t))
}

func TestRetentionIsStateSelectiveAndPreservesLiveRows(t *testing.T) {
	providertest.RetentionIsStateSelectiveAndPreservesLiveRows(t, newQueueFixture(t))
}

func TestFailedJobsCanBeDiscoveredAndRecovered(t *testing.T) {
	providertest.FailedJobsCanBeDiscoveredAndRecovered(t, newQueueFixture(t))
}

func TestJobsCanBeListedCountedAndCanceledInBulk(t *testing.T) {
	providertest.JobsCanBeListedCountedAndCanceledInBulk(t, newQueueFixture(t))
}

func TestCancellationIsDurableAndIdempotent(t *testing.T) {
	providertest.CancellationIsDurableAndIdempotent(t, newQueueFixture(t))
}

func TestExclusiveKeyBlocksOnlyLiveHolders(t *testing.T) {
	providertest.ExclusiveKeyBlocksOnlyLiveHolders(t, newQueueFixture(t))
}

func TestExclusiveKeyNeverDoubleLeasesUnderRace(t *testing.T) {
	providertest.ExclusiveKeyNeverDoubleLeasesUnderRace(t, newQueueFixture(t))
}

func TestDedupeCoalescesActiveAndReleasesOnTerminal(t *testing.T) {
	providertest.DedupeCoalescesActiveAndReleasesOnTerminal(t, newQueueFixture(t))
}

func TestTransactionalEnqueueIsAtomicWithCallerTransaction(t *testing.T) {
	providertest.TransactionalEnqueueIsAtomicWithCallerTransaction(t, newQueueFixture(t))
}

func TestQueueSchemaBootstrapIsIdempotent(t *testing.T) {
	fixture := newQueueFixture(t)
	if err := fixture.Store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Store.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{ID: identity, Type: "gate.bootstrap", Payload: []byte(`{}`), MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestExistingQueueSchemaAddsResourceLeaseSnapshots(t *testing.T) {
	fixture := newQueueFixture(t)
	for _, column := range []string{"resource_name", "resource_cost", "resource_capacity"} {
		if _, err := fixture.Database.ExecContext(context.Background(), `ALTER TABLE `+sqliteQueueTable+` DROP COLUMN "`+column+`"`); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.Store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := fixture.Database.GetContext(context.Background(), &columns, `SELECT COUNT(*) FROM pragma_table_info('golem_queue') WHERE "name" IN ('resource_name','resource_cost','resource_capacity')`); err != nil || columns != 3 {
		t.Fatalf("resource snapshot columns=%d error=%v", columns, err)
	}
}

func newQueueFixture(t *testing.T) providertest.Fixture {
	t.Helper()
	provider := New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := provider.QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return providertest.Fixture{Store: store, Database: database}
}

// TestQueueStorageIsToleratedByDriftDetection proves the lowered unmanaged
// allowlist is what admits the queue store's own objects into a reviewed
// schema. Removing it turns the queue table into drift.
func TestQueueStorageIsToleratedByDriftDetection(t *testing.T) {
	ctx := context.Background()
	provider := New()
	bare := normalizeMigrationFixture(t, incrementalFixtureSchema(t, false))
	allowlisted := incrementalFixtureSchema(t, false)
	allowlisted.Unmanaged = physical.QueueUnmanagedObjects()
	allowlisted = normalizeMigrationFixture(t, allowlisted)
	database := openMigrationFixture(t, provider, allowlisted, "queue-drift.db")
	store, err := provider.QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, allowlisted); err != nil {
		t.Fatalf("allowlisted queue storage reported drift: %v", err)
	}
	if err := provider.Verify(ctx, database, bare); err == nil {
		t.Fatal("queue storage was tolerated without the unmanaged allowlist")
	}
}

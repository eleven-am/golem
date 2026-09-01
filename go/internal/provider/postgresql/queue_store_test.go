package postgresql

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestClaimIsExclusiveUnderConcurrency(t *testing.T) {
	runQueueGate(t, providertest.ClaimIsExclusiveUnderConcurrency)
}

func TestSharedResourceCapacityIsAtomicAndWeighted(t *testing.T) {
	runQueueGate(t, providertest.SharedResourceCapacityIsAtomicAndWeighted)
}

func TestStaleTokenCannotTransition(t *testing.T) {
	runQueueGate(t, providertest.StaleTokenCannotTransition)
}

func TestExpiredLeaseIsReclaimedByOrdinaryClaim(t *testing.T) {
	runQueueGate(t, providertest.ExpiredLeaseIsReclaimedByOrdinaryClaim)
}

func TestExpiredLeaseCannotBeRenewed(t *testing.T) {
	runQueueGate(t, providertest.ExpiredLeaseCannotBeRenewed)
}

func TestExpiredLeaseCannotTransition(t *testing.T) {
	runQueueGate(t, providertest.ExpiredLeaseCannotTransition)
}

func TestUncountedRetryPreservesAttempt(t *testing.T) {
	runQueueGate(t, providertest.UncountedRetryPreservesAttempt)
}

func TestExpiredFinalAttemptFailsWithoutReexecution(t *testing.T) {
	runQueueGate(t, providertest.ExpiredFinalAttemptFailsWithoutReexecution)
}

func TestExpiredCanceledLeaseIsTerminalWithoutReexecution(t *testing.T) {
	runQueueGate(t, providertest.ExpiredCanceledLeaseIsTerminalWithoutReexecution)
}

func TestExpiredLeaseCancellationIsImmediate(t *testing.T) {
	runQueueGate(t, providertest.ExpiredLeaseCancellationIsImmediate)
}

func TestRetentionIsStateSelectiveAndPreservesLiveRows(t *testing.T) {
	runQueueGate(t, providertest.RetentionIsStateSelectiveAndPreservesLiveRows)
}

func TestFailedJobsCanBeDiscoveredAndRecovered(t *testing.T) {
	runQueueGate(t, providertest.FailedJobsCanBeDiscoveredAndRecovered)
}

func TestJobsCanBeListedCountedAndCanceledInBulk(t *testing.T) {
	runQueueGate(t, providertest.JobsCanBeListedCountedAndCanceledInBulk)
}

func TestCancellationIsDurableAndIdempotent(t *testing.T) {
	runQueueGate(t, providertest.CancellationIsDurableAndIdempotent)
}

func TestExclusiveKeyBlocksOnlyLiveHolders(t *testing.T) {
	runQueueGate(t, providertest.ExclusiveKeyBlocksOnlyLiveHolders)
}

func TestExclusiveKeyNeverDoubleLeasesUnderRace(t *testing.T) {
	runQueueGate(t, providertest.ExclusiveKeyNeverDoubleLeasesUnderRace)
}

func TestDedupeCoalescesActiveAndReleasesOnTerminal(t *testing.T) {
	runQueueGate(t, providertest.DedupeCoalescesActiveAndReleasesOnTerminal)
}

func TestTransactionalEnqueueIsAtomicWithCallerTransaction(t *testing.T) {
	runQueueGate(t, providertest.TransactionalEnqueueIsAtomicWithCallerTransaction)
}

func TestEnqueueReportsInsertedAndCoalescedState(t *testing.T) {
	runQueueGate(t, providertest.EnqueueReportsInsertedAndCoalescedState)
}

func TestQueueSchemaBootstrapIsIdempotent(t *testing.T) {
	runQueueGate(t, func(t testing.TB, fixture providertest.Fixture) {
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
	})
}

func TestExistingQueueSchemaAddsResourceLeaseSnapshots(t *testing.T) {
	runQueueGate(t, func(t testing.TB, fixture providertest.Fixture) {
		store, ok := fixture.Store.(*queueStore)
		if !ok {
			t.Fatal("queue store has unexpected type")
		}
		for _, column := range []string{"resource_name", "resource_cost", "resource_capacity"} {
			if _, err := fixture.Database.ExecContext(context.Background(), `ALTER TABLE `+store.table()+` DROP COLUMN "`+column+`"`); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.Store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		var columns int
		if err := fixture.Database.GetContext(context.Background(), &columns, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='golem_queue' AND column_name IN ('resource_name','resource_cost','resource_capacity')`, string(store.namespace)); err != nil || columns != 3 {
			t.Fatalf("resource snapshot columns=%d error=%v", columns, err)
		}
	})
}

func TestCancelUsesTimeAfterRowLock(t *testing.T) {
	runQueueGate(t, func(t testing.TB, fixture providertest.Fixture) {
		store, ok := fixture.Store.(*queueStore)
		if !ok {
			t.Fatal("queue store has unexpected type")
		}
		ctx := context.Background()
		identity, err := queueprovider.NewIdentifier()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: identity, Type: "gate.cancel.lock", Payload: []byte(`{}`), MaxAttempts: 2}); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"gate.cancel.lock"}, Limit: 1, LeaseDuration: 150 * time.Millisecond})
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim=%#v error=%v", claimed, err)
		}
		transaction, err := fixture.Database.Beginx()
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		var locked string
		if err := transaction.GetContext(ctx, &locked, `SELECT "id" FROM `+store.table()+` WHERE "id"=$1 FOR UPDATE`, identity); err != nil {
			t.Fatal(err)
		}
		result := make(chan queueprovider.CancelResult, 1)
		failure := make(chan error, 1)
		go func() {
			canceled, cancelErr := store.Cancel(ctx, identity)
			result <- canceled
			failure <- cancelErr
		}()
		time.Sleep(300 * time.Millisecond)
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-failure; err != nil {
			t.Fatal(err)
		}
		if canceled := <-result; !canceled.Changed || !canceled.Terminal {
			t.Fatalf("post-lock cancellation=%#v", canceled)
		}
	})
}

func runQueueGate(t *testing.T, gate func(testing.TB, providertest.Fixture)) {
	t.Helper()
	for _, profile := range []struct{ name, environment string }{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.environment))
			if dsn == "" {
				t.Skip(profile.environment + " is not configured; queue store evidence requires this live profile")
			}
			gate(t, newQueueFixture(t, dsn))
		})
	}
}

func newQueueFixture(t *testing.T, dsn string) providertest.Fixture {
	t.Helper()
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	identity, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	namespace := physical.PhysicalName("golem_queue_gate_" + strings.ReplaceAll(identity, "-", ""))
	if _, err := database.ExecContext(ctx, `CREATE SCHEMA "`+string(namespace)+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "` + string(namespace) + `" CASCADE`)
	})
	store, err := provider.QueueStoreAt(database, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return providertest.Fixture{Store: store, Database: database}
}

// TestQueueStorageIsToleratedByDriftDetection proves the lowered unmanaged
// allowlist admits the queue store's own relation into the reviewed system
// namespace. Removing it turns the queue table into drift.
func TestQueueStorageIsToleratedByDriftDetection(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not configured; queue drift evidence requires this live profile")
	}
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const namespace = "golem_queue_drift"
	drop := func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "` + namespace + `" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}
	drop()
	defer drop()
	allowlisted, err := provider.Lower(ctx, fixtureModel(), physical.LowerOptions{Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	bare := allowlisted
	bare.Unmanaged = nil
	if err := provider.ApplyInitial(ctx, database, allowlisted); err != nil {
		t.Fatal(err)
	}
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

func TestIdentityBoundIsOwnedByTheQueueContract(t *testing.T) {
	runQueueGate(t, providertest.IdentityBoundIsOwnedByTheQueueContract)
}

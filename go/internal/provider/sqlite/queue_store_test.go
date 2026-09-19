package sqlite

import (
	"context"
	"path/filepath"
	"strings"
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

func TestEnqueueReportsInsertedAndCoalescedState(t *testing.T) {
	providertest.EnqueueReportsInsertedAndCoalescedState(t, newQueueFixture(t))
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
	return providertest.Fixture{Store: store, Database: database, ReplaceIndex: func(ctx context.Context, shape providertest.IndexShape) error {
		if _, err := database.ExecContext(ctx, `DROP INDEX IF EXISTS "main"."`+shape.Name+`"`); err != nil {
			return err
		}
		statement := `CREATE `
		if shape.Unique {
			statement += `UNIQUE `
		}
		statement += `INDEX "main"."` + shape.Name + `" ON "golem_queue" ("` + strings.Join(shape.Columns, `","`) + `")`
		if shape.Predicate != "" {
			statement += ` WHERE ` + shape.Predicate
		}
		_, err := database.ExecContext(ctx, statement)
		return err
	}}
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

func TestIdentityBoundIsOwnedByTheQueueContract(t *testing.T) {
	providertest.IdentityBoundIsOwnedByTheQueueContract(t, newQueueFixture(t))
}

func TestMalformedDedupeIndexIsRefused(t *testing.T) {
	providertest.MalformedDedupeIndexIsRefused(t, newQueueFixture(t))
}

func TestDedupedEnqueueSurvivesConcurrentTerminalTransition(t *testing.T) {
	providertest.DedupedEnqueueSurvivesConcurrentTerminalTransition(t, newQueueFixture(t))
}

func TestQueueTableWithoutIdentityKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keyless := strings.Replace(sqliteQueueSchema[0], `"id" TEXT PRIMARY KEY NOT NULL`, `"id" TEXT NOT NULL`, 1)
	if keyless == sqliteQueueSchema[0] {
		t.Fatal("queue table contract no longer declares its identity key inline")
	}
	if _, err := database.ExecContext(ctx, keyless); err != nil {
		t.Fatal(err)
	}
	store, err := provider.QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Fatalf("bootstrap accepted a queue table without its identity key: %v", err)
	}
}

func TestReleasedQueueSchemaUpgradesInPlace(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS "main"."golem_queue" ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BLOB NOT NULL,"status" TEXT NOT NULL,"attempt_count" INTEGER NOT NULL DEFAULT 0,"max_attempts" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,"lease_until" INTEGER,"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" INTEGER,"last_code" TEXT,"enqueued_at" INTEGER NOT NULL,"finished_at" INTEGER,"updated_at" INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS "main"."golem_queue_claim" ON "golem_queue" ("status","available_at","type")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "main"."golem_queue_dedupe" ON "golem_queue" ("dedupe_key") WHERE "status" IN ('pending','leased')`,
		`CREATE INDEX IF NOT EXISTS "main"."golem_queue_exclusive" ON "golem_queue" ("exclusive_key") WHERE "status"='leased'`,
		`INSERT INTO "main"."golem_queue" ("id","type","payload","status","max_attempts","available_at","dedupe_key","enqueued_at","updated_at") VALUES ('legacy-live','gate.legacy',X'7B7D','pending',5,1,'legacy-live',1,1)`,
		`INSERT INTO "main"."golem_queue" ("id","type","payload","status","attempt_count","max_attempts","available_at","dedupe_key","last_code","enqueued_at","finished_at","updated_at") VALUES ('legacy-done','gate.legacy',X'7B7D','succeeded',1,5,1,'legacy-done','done',1,2,2)`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	store, err := provider.QueueStore(database)
	if err != nil {
		t.Fatal(err)
	}
	providertest.LegacySchemaUpgradesInPlace(t, providertest.Fixture{Store: store, Database: database}, "legacy-live", "legacy-done")
}

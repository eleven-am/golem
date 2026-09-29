package sqlite

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func sqliteClaimIndexCount(t *testing.T, database *sqlx.DB) int {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM "main"."sqlite_master" WHERE "type"='index' AND "name"='golem_outbox_delivery_claim'`); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSQLiteClaimRebuildsADroppedClaimIndexInsteadOfFailing(t *testing.T) {
	ctx := context.Background()
	database := openOutboxSystemTables(t)
	coordinator := admittedSQLiteCoordinator(t, database)
	claim := func() error {
		_, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
		return err
	}
	if err := claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP INDEX "main"."golem_outbox_delivery_claim"`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := claim(); err != nil {
			t.Fatalf("claim %d after the claim index was dropped: %v", attempt, err)
		}
	}
	if count := sqliteClaimIndexCount(t, database); count != 1 {
		t.Fatalf("the dropped claim index was not rebuilt: %d", count)
	}
	if _, err := database.Exec(`DROP INDEX "main"."golem_outbox_delivery_claim"`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE INDEX "main"."golem_outbox_delivery_claim" ON "_golem_outbox_delivery" ("first_recorded_at")`); err != nil {
		t.Fatal(err)
	}
	err := claim()
	if err == nil || !strings.Contains(err.Error(), "drop it") {
		t.Fatalf("a foreign index that replaced the claim index was accepted: %v", err)
	}
}

func TestSQLiteConcurrentNodesAllCreateTheClaimIndex(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/outbox.db"
	openOutboxSystemTablesAt(t, path)
	database := sqlx.MustOpen("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_txlock=immediate")
	t.Cleanup(func() { database.Close() })
	const nodes = 8
	coordinators := make([]*eventCoordinator, nodes)
	for index := range coordinators {
		built, err := New().EventCoordinatorAdmitting(database, physical.OutboxDeliveryUnmanagedObjects())
		if err != nil {
			t.Fatal(err)
		}
		coordinators[index] = built.(*eventCoordinator)
	}
	var group sync.WaitGroup
	failures := make(chan error, nodes)
	for _, coordinator := range coordinators {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute}); err != nil {
				failures <- err
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("a node racing another to create the claim index failed its claim: %v", err)
	}
	if count := sqliteClaimIndexCount(t, database); count != 1 {
		t.Fatalf("claim index count=%d", count)
	}
}

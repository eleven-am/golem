package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func restoreTestSystem() physical.SystemSchema {
	return physical.SystemSchema{Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{physical.OutboxSystemObjectV1(), physical.OutboxDeliverySystemObjectV1()}}
}

func TestRestoreWalkIncludesMissingDeliveryWithoutMutation(t *testing.T) {
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	insertDeliveryFact(t, database, deliveryUUID(1), deliveryUUID(11), 1, 1)
	insertDeliveryFact(t, database, deliveryUUID(2), deliveryUUID(21), 1, 2)
	var groups []eventprovider.Lease
	if err := provider.WalkRestoreFacts(context.Background(), database, restoreTestSystem(), func(group eventprovider.Lease) error { groups = append(groups, group); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Delivery.CausationID != deliveryUUID(1) || len(groups[0].Facts) != 1 {
		t.Fatalf("groups=%#v", groups)
	}
	var count int
	if err := database.Get(&count, `SELECT COUNT(*) FROM _golem_outbox_delivery`); err != nil || count != 0 {
		t.Fatalf("walker wrote delivery state: count=%d err=%v", count, err)
	}
	stop := errors.New("stop validation")
	if err := provider.WalkRestoreFacts(context.Background(), database, restoreTestSystem(), func(eventprovider.Lease) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("callback error=%v", err)
	}
	if err := provider.ReconcileRestoreWork(context.Background(), database, restoreTestSystem(), RestoreAbandonAndResync); err != nil {
		t.Fatal(err)
	}
	var retired int
	if err := database.Get(&retired, `SELECT COUNT(*) FROM _golem_outbox_delivery WHERE status='retired'`); err != nil || retired != 2 {
		t.Fatalf("retired=%d err=%v", retired, err)
	}
	var after []eventprovider.Lease
	if err := provider.WalkRestoreFacts(context.Background(), database, restoreTestSystem(), func(group eventprovider.Lease) error { after = append(after, group); return nil }); err != nil {
		t.Fatal(err)
	}
	for index := range groups {
		if !reflect.DeepEqual(groups[index].Facts, after[index].Facts) {
			t.Fatal("restore changed immutable facts")
		}
	}
}

func TestRestoreWalkRejectsBoundsAndOrdinalGaps(t *testing.T) {
	for _, scenario := range []string{"evidence", "header", "type", "gap", "count", "orphan"} {
		t.Run(scenario, func(t *testing.T) {
			provider := New()
			database := openEventDeliveryFixture(t, provider)
			insertDeliveryFact(t, database, deliveryUUID(1), deliveryUUID(11), 1, 1)
			// Simulate a corrupted archive. Production restore first validates schema
			// and integrity; the walker independently protects materialization.
			if _, err := database.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			var statement string
			switch scenario {
			case "evidence":
				statement = `UPDATE _golem_outbox SET metadata=zeroblob(1048577)`
			case "header":
				statement = `UPDATE _golem_outbox SET codec_identity=printf('%0300d',1)`
			case "type":
				// STRICT prevents corrupt storage types through normal writes;
				// an untrusted archive can carry a substituted table.
				for _, sql := range []string{`CREATE TABLE restore_bad_facts AS SELECT * FROM _golem_outbox`, `DROP TABLE _golem_outbox`, `ALTER TABLE restore_bad_facts RENAME TO _golem_outbox`} {
					if _, err := database.Exec(sql); err != nil {
						t.Fatal(err)
					}
				}
				statement = `UPDATE _golem_outbox SET metadata='text-not-blob'`
			case "gap":
				statement = `UPDATE _golem_outbox SET transaction_ordinal=2`
			case "count":
				for i := 2; i <= eventprovider.MaximumCausationFacts+1; i++ {
					insertDeliveryFact(t, database, deliveryUUID(1), deliveryUUID(100+i), i, 1)
				}
			case "orphan":
				statement = `INSERT INTO _golem_outbox_delivery(causation_id,status,first_recorded_at,attempt_count,available_at,updated_at) VALUES('00000000-0000-4000-8000-000000000099','pending',1,0,1,1)`
			}
			if statement != "" {
				if _, err := database.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			visits := 0
			err := provider.WalkRestoreFacts(context.Background(), database, restoreTestSystem(), func(eventprovider.Lease) error { visits++; return nil })
			if err == nil || visits != 0 {
				t.Fatalf("invalid archive admitted: visits=%d err=%v", visits, err)
			}
		})
	}
}

func TestRestoreQueuePreservesHistoryAndFinalization(t *testing.T) {
	for _, mode := range []RestoreWorkMode{RestoreResumeAtLeastOnce, RestoreAbandonAndResync} {
		t.Run(string(mode), func(t *testing.T) {
			store, database := legacyQueueStorageFixture(t, false, true)
			ctx := context.Background()
			if _, err := store.UpgradeStorage(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE golem_queue SET priority=1`); err != nil {
				t.Fatal(err)
			}
			before := queueStorageRows(t, database, false)
			system := physical.SystemSchema{Namespace: physical.Namespace{Name: "main"}}
			if err := New().ReconcileRestoreWork(ctx, database, system, mode); err != nil {
				t.Fatal(err)
			}
			after := queueStorageRows(t, database, false)
			for i, row := range before {
				for _, column := range []string{"id", "type", "payload", "attempt_count", "max_attempts", "dedupe_key", "exclusive_key", "cancel_requested_at", "enqueued_at"} {
					if !reflect.DeepEqual(row[column], after[i][column]) {
						t.Fatalf("%s changed %s", row["id"], column)
					}
				}
				if row["status"] == "succeeded" || mode == RestoreResumeAtLeastOnce && row["status"] == "pending" {
					if !reflect.DeepEqual(row, after[i]) {
						t.Fatalf("inactive row changed: before=%#v after=%#v", row, after[i])
					}
				}
				if row["status"] == "leased" {
					if after[i]["lease_token"] != nil || after[i]["resource_name"] != nil {
						t.Fatal("old lease retained")
					}
					want := "leased"
					if mode == RestoreAbandonAndResync {
						want = "canceled"
					}
					if after[i]["status"] != want {
						t.Fatalf("cancel intent lost: %#v", after[i])
					}
				}
			}
			var changed int
			if err := database.Get(&changed, `SELECT COUNT(*) FROM golem_queue WHERE priority<>1`); err != nil || changed != 0 {
				t.Fatalf("priority changed: %v", err)
			}
		})
	}
}

func TestRestoreReconciliationRollsBackAndRefusesLegacy(t *testing.T) {
	store, database := legacyQueueStorageFixture(t, false, true)
	system := physical.SystemSchema{Namespace: physical.Namespace{Name: "main"}}
	before := queueStorageRows(t, database, false)
	if err := New().ReconcileRestoreWork(context.Background(), database, system, RestoreAbandonAndResync); err == nil {
		t.Fatal("legacy queue upgraded implicitly")
	}
	if !reflect.DeepEqual(before, queueStorageRows(t, database, false)) {
		t.Fatal("legacy queue mutated")
	}
	if _, err := store.UpgradeStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = queueStorageRows(t, database, false)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().ReconcileRestoreWork(canceled, database, system, RestoreAbandonAndResync); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if !reflect.DeepEqual(before, queueStorageRows(t, database, false)) {
		t.Fatal("canceled restore mutated queue")
	}
	err := restoreImmediate(context.Background(), database, func(connection *sqlx.Conn) error {
		if _, err := connection.ExecContext(context.Background(), `UPDATE golem_queue SET attempt_count=99`); err != nil {
			return err
		}
		return errors.New("injected rollback")
	})
	if err == nil || !reflect.DeepEqual(before, queueStorageRows(t, database, false)) {
		t.Fatal("restore transaction did not roll back")
	}
}

func TestRestoreRotationRequiresExistingCanonicalIdentity(t *testing.T) {
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	system := restoreTestSystem()
	system.Objects = append(system.Objects, physical.DataIncarnationSystemObjectV1())
	if _, err := database.Exec(renderDataIncarnation("_golem_data_incarnation")); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RotateRestoreIncarnation(context.Background(), database, system); err == nil {
		t.Fatal("missing identity bootstrapped")
	}
	const previous = "12345678901234567890123456789012"
	if _, err := database.Exec(`INSERT INTO _golem_data_incarnation(singleton,value) VALUES(1,?)`, previous); err != nil {
		t.Fatal(err)
	}
	next, err := provider.RotateRestoreIncarnation(context.Background(), database, system)
	if err != nil || !next.Valid() || next.String() == previous {
		t.Fatalf("rotation=%v err=%v", next, err)
	}
	actual, err := provider.ReadDataIncarnation(context.Background(), database, system)
	if err != nil || actual != next {
		t.Fatalf("durable identity=%v err=%v", actual, err)
	}
	if _, err := database.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE _golem_data_incarnation SET value='NOT-CANONICAL'`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RotateRestoreIncarnation(context.Background(), database, system); err == nil {
		t.Fatal("malformed identity repaired")
	}
	var unchanged string
	if err := database.Get(&unchanged, `SELECT value FROM _golem_data_incarnation`); err != nil || unchanged != "NOT-CANONICAL" {
		t.Fatalf("invalid identity changed: %q %v", unchanged, err)
	}
}

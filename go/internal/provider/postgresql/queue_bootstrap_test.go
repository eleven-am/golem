package postgresql

import (
	"context"
	"strings"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
)

func TestPostgreSQLQueueBootstrapFailureRollsBackAndRetries(t *testing.T) {
	for _, history := range []bool{false, true} {
		name := "fresh"
		if history {
			name = "history"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, database := legacyPostgreSQLQueueStorageFixture(t, false, false)
			want := queueprovider.StorageAbsent
			condition := "TG_TAG='CREATE INDEX'"
			if history {
				if _, err := store.UpgradeStorage(ctx); err != nil {
					t.Fatal(err)
				}
				store.history = true
				want = queueprovider.StoragePriorityV1
				condition += ` AND position('"golem_queue_history"' in current_query())>0`
			} else if _, err := database.Exec(`DROP TABLE ` + store.table()); err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`CREATE FUNCTION public.queue_bootstrap_fault() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF ` + condition + ` THEN RAISE EXCEPTION 'queue bootstrap test index fault'; END IF; END $$`,
				`CREATE EVENT TRIGGER queue_bootstrap_fault ON ddl_command_start EXECUTE FUNCTION public.queue_bootstrap_fault()`,
			} {
				if _, err := database.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			bootstrapErr := store.EnsureSchema(ctx)
			if _, err := database.Exec(`DROP EVENT TRIGGER queue_bootstrap_fault; DROP FUNCTION public.queue_bootstrap_fault()`); err != nil {
				t.Fatal(err)
			}
			if bootstrapErr == nil || !strings.Contains(bootstrapErr.Error(), "queue bootstrap test index fault") {
				t.Fatalf("expected real index fault, got %v", bootstrapErr)
			}
			state, err := store.InspectStorage(ctx)
			if err != nil || state.Version != want || state.HistoryIndexes {
				t.Fatalf("failed bootstrap left partial storage: %+v error=%v", state, err)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatalf("retry after failed bootstrap: %v", err)
			}
			state, err = store.InspectStorage(ctx)
			if err != nil || state.Version != queueprovider.StoragePriorityV1 || state.HistoryIndexes != history {
				t.Fatalf("retry storage=%+v error=%v", state, err)
			}
		})
	}
}

func TestPostgreSQLQueueConcurrentBootstrapSerializesBeforeInspection(t *testing.T) {
	store, database := legacyPostgreSQLQueueStorageFixture(t, false, false)
	if _, err := database.Exec(`DROP TABLE ` + store.table()); err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if err := store.lockStorage(ctx, gate); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		candidate := &queueStore{database: database, namespace: store.namespace, history: true}
		go func() { results <- candidate.EnsureSchema(ctx) }()
	}
	// Observe both real database sessions waiting for the exact lock held by
	// this transaction. No elapsed-delay assertion substitutes for contention.
	for {
		var waiting int
		err := gate.GetContext(ctx, &waiting, `SELECT count(*) FROM pg_catalog.pg_locks waiting JOIN pg_catalog.pg_locks held USING(locktype,database,classid,objid,objsubid) WHERE held.pid=pg_backend_pid() AND held.locktype='advisory' AND held.granted AND NOT waiting.granted`)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case err := <-results:
			t.Fatalf("bootstrap escaped the namespace lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent bootstrap: %v", err)
		}
	}
	state, err := store.InspectStorage(ctx)
	if err != nil || state.Version != queueprovider.StoragePriorityV1 || !state.HistoryIndexes {
		t.Fatalf("concurrent bootstrap storage=%+v error=%v", state, err)
	}
}

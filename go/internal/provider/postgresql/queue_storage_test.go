package postgresql

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func legacyPostgreSQLQueueStorageFixture(t *testing.T, preResource, history bool) (*queueStore, *sqlx.DB) {
	t.Helper()
	root, dsn := os.Getenv("GOLEM_POLICY_HISTORY_PG_ROOT"), os.Getenv("GOLEM_POLICY_HISTORY_PG_DSN")
	if root == "" && dsn == "" {
		testenv.SkipMissingPostgreSQL(t, "requires the explicitly owned private PostgreSQL root and DSN")
	}
	dsn = testenv.DisposablePostgreSQLInRoot(t, root, dsn)
	database, _, err := New().Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(`CREATE SCHEMA queue_storage_gate`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range releasedPostgreSQLQueueSchema("queue_storage_gate") {
		if preResource {
			statement = strings.Replace(statement, `"resource_name" TEXT,"resource_cost" BIGINT,"resource_capacity" BIGINT,`, "", 1)
		}
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store := &queueStore{database: database, namespace: "queue_storage_gate"}
	if history {
		for _, statement := range []string{`CREATE INDEX golem_queue_enqueued ON ` + store.table() + `(enqueued_at,id)`, `CREATE INDEX golem_queue_history ON ` + store.table() + `(status,enqueued_at,id)`, `CREATE INDEX golem_queue_terminal ON ` + store.table() + `(status,finished_at,id)`} {
			if _, err := database.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		`INSERT INTO ` + store.table() + `(id,type,payload,status,attempt_count,max_attempts,available_at,dedupe_key,exclusive_key,enqueued_at,updated_at) VALUES('pending','gate.job','\x000102','pending',1,5,'2000-01-01','pending-dedupe','pending-exclusive','2000-01-01','2000-01-02')`,
		`INSERT INTO ` + store.table() + `(id,type,payload,status,attempt_count,max_attempts,available_at,lease_token,lease_until,cancel_requested_at,dedupe_key,exclusive_key,enqueued_at,updated_at) VALUES('leased','gate.job','\x030405','leased',2,5,'2100-01-01','lease-token','2100-01-01','2000-01-03','lease-dedupe','lease-exclusive','2000-01-01','2000-01-02')`,
		`INSERT INTO ` + store.table() + `(id,type,payload,status,attempt_count,max_attempts,available_at,last_code,enqueued_at,finished_at,updated_at) VALUES('done','gate.job','\x060708','succeeded',3,5,'2000-01-01','complete','2000-01-01','2000-01-04','2000-01-04')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if !preResource {
		if _, err := database.Exec(`UPDATE ` + store.table() + ` SET resource_name='network',resource_cost=2,resource_capacity=7 WHERE id='leased'`); err != nil {
			t.Fatal(err)
		}
	}
	return store, database
}

func postgresqlQueueStorageRows(t *testing.T, store *queueStore, preResource bool) []string {
	t.Helper()
	expression := `to_jsonb(q)-'priority'`
	if preResource {
		expression += `-ARRAY['resource_name','resource_cost','resource_capacity']`
	}
	var rows []string
	if err := store.database.Select(&rows, `SELECT (`+expression+`)::text FROM `+store.table()+` q ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPostgreSQLQueueStorageExplicitUpgradePreservesLegacyJobs(t *testing.T) {
	for _, test := range []struct {
		name                 string
		preResource, history bool
	}{{"v088", false, false}, {"v088_history", false, true}, {"pre_resource", true, false}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, database := legacyPostgreSQLQueueStorageFixture(t, test.preResource, test.history)
			before := postgresqlQueueStorageRows(t, store, test.preResource)
			state, err := store.InspectStorage(ctx)
			want := queueprovider.StorageLegacyV088
			if test.preResource {
				want = queueprovider.StorageLegacyPreResources
			}
			if err != nil || state.Version != want || state.HistoryIndexes != test.history {
				t.Fatalf("legacy inspection=%+v error=%v", state, err)
			}
			if err := store.EnsureSchema(ctx); !errors.Is(err, queueprovider.ErrStorageUpgradeRequired) {
				t.Fatalf("startup upgrade refusal=%v", err)
			}
			state, err = store.UpgradeStorage(ctx)
			if err != nil || state.Version != queueprovider.StoragePriorityV1 || state.HistoryIndexes != test.history {
				t.Fatalf("upgrade=%+v error=%v", state, err)
			}
			if !reflect.DeepEqual(before, postgresqlQueueStorageRows(t, store, test.preResource)) {
				t.Fatal("upgrade changed jobs, leases, attempts, resource snapshots, or history")
			}
			var normal int
			if err := database.Get(&normal, `SELECT count(*) FROM `+store.table()+` WHERE priority=0`); err != nil || normal != 3 {
				t.Fatalf("normal jobs=%d error=%v", normal, err)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpgradeStorage(ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, postgresqlQueueStorageRows(t, store, test.preResource)) {
				t.Fatal("repeated admission changed jobs")
			}
		})
	}
}

func TestPostgreSQLQueueStorageUpgradeRollsBackColumnAndIndexTogether(t *testing.T) {
	store, database := legacyPostgreSQLQueueStorageFixture(t, true, true)
	before := postgresqlQueueStorageRows(t, store, true)
	for _, statement := range []string{
		`CREATE FUNCTION public.queue_upgrade_deny_drop() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF TG_TAG='DROP INDEX' THEN RAISE EXCEPTION 'test blocks index replacement'; END IF; END $$`,
		`CREATE EVENT TRIGGER queue_upgrade_deny_drop ON ddl_command_start EXECUTE FUNCTION public.queue_upgrade_deny_drop()`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_, upgradeErr := store.UpgradeStorage(context.Background())
	if _, err := database.Exec(`DROP EVENT TRIGGER queue_upgrade_deny_drop; DROP FUNCTION public.queue_upgrade_deny_drop()`); err != nil {
		t.Fatal(err)
	}
	if upgradeErr == nil {
		t.Fatal("upgrade ignored actual index replacement failure")
	}
	state, err := store.InspectStorage(context.Background())
	if err != nil || state.Version != queueprovider.StorageLegacyPreResources || !state.HistoryIndexes {
		t.Fatalf("partial upgrade survived: %+v error=%v", state, err)
	}
	if !reflect.DeepEqual(before, postgresqlQueueStorageRows(t, store, true)) {
		t.Fatal("failed upgrade changed jobs")
	}
}

func TestPostgreSQLQueueStorageRefusesPartialOrForeignShapes(t *testing.T) {
	for _, change := range []string{
		`ADD COLUMN priority BIGINT NOT NULL DEFAULT 0`,
		`DROP COLUMN resource_capacity`,
		`ADD CONSTRAINT extra_check CHECK(attempt_count>=0)`,
		`ENABLE ROW LEVEL SECURITY`,
	} {
		t.Run(change, func(t *testing.T) {
			store, database := legacyPostgreSQLQueueStorageFixture(t, false, false)
			if _, err := database.Exec(`ALTER TABLE ` + store.table() + ` ` + change); err != nil {
				t.Fatal(err)
			}
			before := postgresqlQueueStorageRows(t, store, false)
			if _, err := store.InspectStorage(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown inspection=%v", err)
			}
			if err := store.EnsureSchema(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown startup=%v", err)
			}
			if _, err := store.UpgradeStorage(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown upgrade=%v", err)
			}
			if !reflect.DeepEqual(before, postgresqlQueueStorageRows(t, store, false)) {
				t.Fatal("unknown storage changed jobs")
			}
		})
	}
}

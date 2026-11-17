package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

func legacyQueueStorageFixture(t *testing.T, preResource, history bool) (*queueStore, *sqlx.DB) {
	t.Helper()
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, statement := range releasedQueueSchemaV030 {
		if preResource {
			statement = strings.Replace(statement, `"resource_name" TEXT,"resource_cost" INTEGER,"resource_capacity" INTEGER,`, "", 1)
		}
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if history {
		for _, statement := range sqliteQueueHistorySchema {
			if _, err := database.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		`INSERT INTO golem_queue(id,type,payload,status,attempt_count,max_attempts,available_at,dedupe_key,exclusive_key,enqueued_at,updated_at) VALUES('pending','gate.job',X'000102','pending',1,5,100,'pending-dedupe','pending-exclusive',10,20)`,
		`INSERT INTO golem_queue(id,type,payload,status,attempt_count,max_attempts,available_at,lease_token,lease_until,cancel_requested_at,dedupe_key,exclusive_key,enqueued_at,updated_at) VALUES('leased','gate.job',X'030405','leased',2,5,900,'lease-token',900,30,'lease-dedupe','lease-exclusive',10,20)`,
		`INSERT INTO golem_queue(id,type,payload,status,attempt_count,max_attempts,available_at,last_code,enqueued_at,finished_at,updated_at) VALUES('done','gate.job',X'060708','succeeded',3,5,100,'complete',10,40,40)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if !preResource {
		if _, err := database.Exec(`UPDATE golem_queue SET resource_name='network',resource_cost=2,resource_capacity=7 WHERE id='leased'`); err != nil {
			t.Fatal(err)
		}
	}
	return &queueStore{database: database}, database
}

func queueStorageRows(t *testing.T, database *sqlx.DB, preResource bool) []map[string]any {
	t.Helper()
	rows, err := database.Queryx(`SELECT * FROM golem_queue ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		value := map[string]any{}
		if err := rows.MapScan(value); err != nil {
			t.Fatal(err)
		}
		delete(value, "priority")
		if preResource {
			for _, name := range []string{"resource_name", "resource_cost", "resource_capacity"} {
				delete(value, name)
			}
		}
		for name, item := range value {
			if binary, ok := item.([]byte); ok {
				value[name] = append([]byte(nil), binary...)
			}
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestQueueStorageExplicitUpgradePreservesLegacyJobs(t *testing.T) {
	for _, test := range []struct {
		name                 string
		preResource, history bool
	}{{"v088", false, false}, {"v088_history", false, true}, {"pre_resource", true, false}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, database := legacyQueueStorageFixture(t, test.preResource, test.history)
			before := queueStorageRows(t, database, test.preResource)
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
			if !reflect.DeepEqual(before, queueStorageRows(t, database, test.preResource)) {
				t.Fatal("refused startup changed jobs")
			}
			state, err = store.UpgradeStorage(ctx)
			if err != nil || state.Version != queueprovider.StoragePriorityV1 || state.HistoryIndexes != test.history {
				t.Fatalf("upgrade=%+v error=%v", state, err)
			}
			if !reflect.DeepEqual(before, queueStorageRows(t, database, test.preResource)) {
				t.Fatal("upgrade changed payloads, leases, attempts, resource snapshots, or history")
			}
			var normal int
			if err := database.Get(&normal, `SELECT count(*) FROM golem_queue WHERE priority=0`); err != nil || normal != 3 {
				t.Fatalf("legacy normal jobs=%d error=%v", normal, err)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpgradeStorage(ctx); err != nil {
				t.Fatalf("already-current explicit upgrade: %v", err)
			}
			if !reflect.DeepEqual(before, queueStorageRows(t, database, test.preResource)) {
				t.Fatal("repeated admission changed jobs")
			}
		})
	}
}

func TestQueueStorageUpgradeRollsBackColumnAndIndexTogether(t *testing.T) {
	store, database := legacyQueueStorageFixture(t, true, true)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	before := queueStorageRows(t, database, true)
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error {
		return raw.(sqlitedriver.Conn).Raw().SetAuthorizer(func(action sqlite3.AuthorizerActionCode, first, second, namespace, inner string) sqlite3.AuthorizerReturnCode {
			if action == sqlite3.AUTH_DROP_INDEX && first == "golem_queue_claim" {
				return sqlite3.AUTH_DENY
			}
			return sqlite3.AUTH_OK
		})
	})
	if closeErr := connection.Close(); err != nil || closeErr != nil {
		t.Fatalf("authorizer setup=%v close=%v", err, closeErr)
	}
	_, upgradeErr := store.UpgradeStorage(context.Background())
	connection, err = database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error { return raw.(sqlitedriver.Conn).Raw().SetAuthorizer(nil) })
	if closeErr := connection.Close(); err != nil || closeErr != nil {
		t.Fatalf("authorizer cleanup=%v close=%v", err, closeErr)
	}
	if upgradeErr == nil {
		t.Fatal("upgrade ignored the denied index replacement")
	}
	state, err := store.InspectStorage(context.Background())
	if err != nil || state.Version != queueprovider.StorageLegacyPreResources || !state.HistoryIndexes {
		t.Fatalf("partial upgrade survived rollback: %+v error=%v", state, err)
	}
	if !reflect.DeepEqual(before, queueStorageRows(t, database, true)) {
		t.Fatal("failed upgrade changed jobs")
	}
}

func TestQueueStorageRefusesPartialOrForeignShapesBeforeStartupWrites(t *testing.T) {
	for _, statement := range []string{
		`ALTER TABLE golem_queue ADD COLUMN priority INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE golem_queue DROP COLUMN resource_capacity`,
		`CREATE INDEX golem_queue_invented ON golem_queue(type)`,
		`CREATE TRIGGER queue_extra AFTER UPDATE ON golem_queue BEGIN SELECT 1; END`,
		`CREATE INDEX golem_queue_history ON golem_queue(status,enqueued_at,id)`,
		`DROP INDEX golem_queue_claim`,
	} {
		t.Run(statement, func(t *testing.T) {
			store, database := legacyQueueStorageFixture(t, false, false)
			if _, err := database.Exec(statement); err != nil {
				t.Fatal(err)
			}
			var before []string
			if err := database.Select(&before, `SELECT type||':'||name||':'||coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.InspectStorage(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown inspection=%v", err)
			}
			if err := store.EnsureSchema(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown startup=%v", err)
			}
			if _, err := store.UpgradeStorage(context.Background()); !errors.Is(err, queueprovider.ErrStorageUnrecognized) {
				t.Fatalf("unknown upgrade=%v", err)
			}
			var after []string
			if err := database.Select(&after, `SELECT type||':'||name||':'||coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("unknown storage was repaired or partially upgraded")
			}
		})
	}
}

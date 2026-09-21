package postgresql

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func openQueueHistoryNamespace(t *testing.T, namespace string, admitted bool) (queueprovider.Store, *sqlx.DB) {
	t.Helper()
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	database, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+namespace+`" CASCADE`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `CREATE SCHEMA "`+namespace+`"`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+namespace+`" CASCADE`)
		database.Close()
	})
	unmanaged := physical.QueueUnmanagedObjects()
	if !admitted {
		unmanaged = unmanaged[:4]
	}
	store, err := New().QueueStoreAt(database, physical.PhysicalName(namespace), unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return store, database
}

func queueIndexDefinitions(t *testing.T, database *sqlx.DB, namespace string) map[string]string {
	t.Helper()
	var rows []struct {
		Name       string `db:"indexname"`
		Definition string `db:"indexdef"`
	}
	if err := database.Select(&rows, `SELECT indexname,indexdef FROM pg_catalog.pg_indexes WHERE schemaname=$1 AND tablename='golem_queue' ORDER BY indexname`, namespace); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.Name] = row.Definition
	}
	return result
}

func TestPostgreSQLQueueHistoryIndexesFollowTheAllowlist(t *testing.T) {
	const admittedNamespace = "queue_history_admitted"
	_, database := openQueueHistoryNamespace(t, admittedNamespace, true)
	indexes := queueIndexDefinitions(t, database, admittedNamespace)
	for name, columns := range map[string]string{
		"golem_queue_enqueued": "(enqueued_at, id)",
		"golem_queue_history":  "(status, enqueued_at, id)",
		"golem_queue_terminal": "(status, finished_at, id)",
	} {
		definition, created := indexes[name]
		if !created {
			t.Fatalf("admitted store did not create %s: %v", name, indexes)
		}
		if !strings.Contains(definition, columns) {
			t.Fatalf("%s definition=%q want columns %s", name, definition, columns)
		}
	}
}

func TestPostgreSQLQueueHistoryIsAbsentWhenUnadmitted(t *testing.T) {
	const plainNamespace = "queue_history_plain"
	_, database := openQueueHistoryNamespace(t, plainNamespace, false)
	indexes := queueIndexDefinitions(t, database, plainNamespace)
	for _, name := range []string{"golem_queue_enqueued", "golem_queue_history", "golem_queue_terminal"} {
		if _, created := indexes[name]; created {
			t.Fatalf("unadmitted store created %s", name)
		}
	}
	for _, name := range []string{"golem_queue_claim", "golem_queue_dedupe", "golem_queue_exclusive"} {
		if _, created := indexes[name]; !created {
			t.Fatalf("unadmitted store skipped its own %s", name)
		}
	}
}

func TestPostgreSQLQueueHistoryIsIdempotentAcrossRestarts(t *testing.T) {
	const namespace = "queue_history_restart"
	store, database := openQueueHistoryNamespace(t, namespace, true)
	before := queueIndexDefinitions(t, database, namespace)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("a second start refused its own storage: %v", err)
	}
	after := queueIndexDefinitions(t, database, namespace)
	if len(before) != len(after) {
		t.Fatalf("index set changed across restart: %v then %v", before, after)
	}
	for name, definition := range before {
		if after[name] != definition {
			t.Fatalf("index %s was redefined across restart: %q then %q", name, definition, after[name])
		}
	}
}

func queueStoreOf(t *testing.T, store queueprovider.Store) *queueStore {
	t.Helper()
	value, ok := store.(*queueStore)
	if !ok {
		t.Fatalf("store is %T", store)
	}
	return value
}

func TestPostgreSQLVerifiesEveryQueueIndexNotJustDedupe(t *testing.T) {
	ctx := context.Background()
	for _, probe := range []struct {
		name       string
		namespace  string
		redefine   string
		wantNamed  string
		wantAction string
	}{
		{name: "enqueued", namespace: "queue_verify_enqueued", redefine: `CREATE INDEX "golem_queue_enqueued" ON %s ("enqueued_at")`, wantNamed: "golem_queue_enqueued", wantAction: "drop it"},
		{name: "history", namespace: "queue_verify_history", redefine: `CREATE INDEX "golem_queue_history" ON %s ("status","id")`, wantNamed: "golem_queue_history", wantAction: "drop it"},
		{name: "terminal", namespace: "queue_verify_terminal", redefine: `CREATE INDEX "golem_queue_terminal" ON %s ("finished_at")`, wantNamed: "golem_queue_terminal", wantAction: "drop it"},
		{name: "claim", namespace: "queue_verify_claim", redefine: `CREATE INDEX "golem_queue_claim" ON %s ("status")`, wantNamed: "golem_queue_claim", wantAction: "drop it"},
		{name: "exclusive", namespace: "queue_verify_exclusive", redefine: `CREATE INDEX "golem_queue_exclusive" ON %s ("exclusive_key")`, wantNamed: "golem_queue_exclusive", wantAction: "drop it"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			store, database := openQueueHistoryNamespace(t, probe.namespace, true)
			table := `"` + probe.namespace + `"."golem_queue"`
			if _, err := database.ExecContext(ctx, `DROP INDEX "`+probe.namespace+`"."`+probe.wantNamed+`"`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, fmt.Sprintf(probe.redefine, table)); err != nil {
				t.Fatal(err)
			}
			err := queueStoreOf(t, store).verifyQueueGuarantees(ctx)
			if err == nil {
				t.Fatalf("a wrong-shaped %s was accepted", probe.wantNamed)
			}
			for _, fragment := range []string{probe.wantNamed, probe.wantAction} {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("refusal must name the object and say what to do, got: %v", err)
				}
			}
		})
	}
}

func TestPostgreSQLRefusesAUserIndexOnTheQueueTable(t *testing.T) {
	ctx := context.Background()
	store, database := openQueueHistoryNamespace(t, "queue_verify_foreign", true)
	if _, err := database.ExecContext(ctx, `CREATE INDEX "tenant_queue_by_type" ON "queue_verify_foreign"."golem_queue" ("type")`); err != nil {
		t.Fatal(err)
	}
	err := queueStoreOf(t, store).verifyQueueGuarantees(ctx)
	if err == nil {
		t.Fatal("a user-authored index on golem_queue was tolerated")
	}
	for _, fragment := range []string{"tenant_queue_by_type", "drop it"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("refusal must name the object and say what to do, got: %v", err)
		}
	}
}

func TestPostgreSQLReleasedQueueDatabasePassesVerification(t *testing.T) {
	ctx := context.Background()
	for _, admitted := range []bool{false, true} {
		store, _ := openQueueHistoryNamespace(t, fmt.Sprintf("queue_released_%t", admitted), admitted)
		if err := queueStoreOf(t, store).verifyQueueGuarantees(ctx); err != nil {
			t.Fatalf("admitted=%v: a queue golem created itself failed verification: %v", admitted, err)
		}
	}
}

// releasedPostgreSQLQueueSchema is the durable-queue DDL shipped by go/v0.3.0
// through go/v0.4.0, verified byte-identical against the module cache. A
// database an upgrading user already has must satisfy the new verification.
func releasedPostgreSQLQueueSchema(namespace string) []string {
	table := `"` + namespace + `"."golem_queue"`
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + table + ` ("id" TEXT PRIMARY KEY NOT NULL,"type" TEXT NOT NULL,"payload" BYTEA NOT NULL,"status" TEXT NOT NULL,"attempt_count" BIGINT NOT NULL DEFAULT 0,"max_attempts" BIGINT NOT NULL,"available_at" TIMESTAMPTZ NOT NULL,"lease_token" TEXT,"lease_until" TIMESTAMPTZ,"resource_name" TEXT,"resource_cost" BIGINT,"resource_capacity" BIGINT,"dedupe_key" TEXT,"exclusive_key" TEXT,"cancel_requested_at" TIMESTAMPTZ,"last_code" TEXT,"enqueued_at" TIMESTAMPTZ NOT NULL,"finished_at" TIMESTAMPTZ,"updated_at" TIMESTAMPTZ NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_claim" ON ` + table + ` ("status","available_at","type")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "golem_queue_dedupe" ON ` + table + ` ("dedupe_key") WHERE "status" IN ('pending','leased')`,
		`CREATE INDEX IF NOT EXISTS "golem_queue_exclusive" ON ` + table + ` ("exclusive_key") WHERE "status"='leased'`,
	}
}

func TestAPostgreSQLQueueFromV030ThroughV040PassesIndexVerification(t *testing.T) {
	ctx := context.Background()
	for _, admitted := range []bool{false, true} {
		namespace := fmt.Sprintf("queue_released_ddl_%t", admitted)
		dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
		database, err := sqlx.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close() })
		if _, err := database.ExecContext(ctx, `CREATE SCHEMA "`+namespace+`"`); err != nil {
			t.Fatal(err)
		}
		for _, statement := range releasedPostgreSQLQueueSchema(namespace) {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create released queue storage %q: %v", statement, err)
			}
		}
		unmanaged := physical.QueueUnmanagedObjects()
		if !admitted {
			unmanaged = unmanaged[:4]
		}
		store, err := New().QueueStoreAt(database, physical.PhysicalName(namespace), unmanaged)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatalf("admitted=%v: a released PostgreSQL queue database was refused on upgrade: %v", admitted, err)
		}
		if err := queueStoreOf(t, store).verifyQueueGuarantees(ctx); err != nil {
			t.Fatalf("admitted=%v: released PostgreSQL queue indexes failed verification: %v", admitted, err)
		}
	}
}

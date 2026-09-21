package postgresql

import (
	"context"
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

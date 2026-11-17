package compatibility

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func TestReleasedV088RefusesPrioritySQLiteQueueBeforeService(t *testing.T) {
	ctx := context.Background()
	dir := preparePriorRelease(t)
	path := filepath.Join(dir, "current-queue.db")
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := provider.QueueStore(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	seedQueueCompatibility(t, ctx, store)
	before := queueCompatibilityEvidence(t, database)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	// The child uses the actual released provider and startup verifier. It
	// must refuse the new claim index before any job servicing is attempted.
	runPriorRelease(t, dir, "TestReleasedQueueStartup")
	reopened, _, err := provider.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := queueCompatibilityEvidence(t, reopened)
	if !bytes.Equal(before, after) {
		t.Fatal("released startup refusal changed queued jobs or the SQLite schema")
	}
	current, err := provider.QueueStore(reopened, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.EnsureSchema(ctx); err != nil {
		t.Fatalf("released refusal damaged current startup: %v", err)
	}
}

func seedQueueCompatibility(t *testing.T, ctx context.Context, store queueprovider.Store) {
	t.Helper()
	for _, input := range []queueprovider.EnqueueRequest{
		{ID: "normal", Type: "compat.pending", Payload: []byte(`{"value":"normal"}`), MaxAttempts: 5, DedupeKey: "normal-dedupe", ExclusiveKey: "normal-exclusive"},
		{ID: "promoted", Type: "compat.pending", Payload: []byte(`{"value":"promoted"}`), MaxAttempts: 4, DedupeKey: "promoted-dedupe"},
		{ID: "leased", Type: "compat.lease", Payload: []byte(`{"value":"leased"}`), MaxAttempts: 3, ExclusiveKey: "leased-exclusive"},
	} {
		if _, err := store.Enqueue(ctx, nil, input); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := store.Promote(ctx, nil, "promoted", 1, false); err != nil || !changed {
		t.Fatalf("real current promotion: changed=%v error=%v", changed, err)
	}
	claimed, err := store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"compat.lease"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(claimed) != 1 || claimed[0].ID != "leased" || claimed[0].AttemptCount != 1 {
		t.Fatalf("real current claim: jobs=%+v error=%v", claimed, err)
	}
}

func TestReleasedV088RefusesPriorityPostgreSQLQueueBeforeService(t *testing.T) {
	root, dsn := os.Getenv("GOLEM_POLICY_HISTORY_PG_ROOT"), os.Getenv("GOLEM_POLICY_HISTORY_PG_DSN")
	if root == "" && dsn == "" {
		testenv.SkipMissingPostgreSQL(t, "requires the root-owned disposable Unix-socket PostgreSQL cluster")
	}
	target := testenv.DisposablePostgreSQLInRoot(t, root, dsn)
	control := testenv.DisposablePostgreSQLInRoot(t, root, dsn)
	dir := preparePriorRelease(t)
	fixture, err := json.Marshal(map[string]string{"target": target, "control": control})
	if err != nil || len(fixture) > 16<<10 {
		t.Fatal("invalid or oversized private PostgreSQL queue fixture")
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres-queue.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	provider := postgresql.New()
	database, _, err := provider.Open(ctx, target)
	if err != nil {
		t.Fatal("current PostgreSQL queue fixture open failed")
	}
	defer database.Close()
	store, err := provider.QueueStoreAt(database, "public", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	seedQueueCompatibility(t, ctx, store)
	before := postgreSQLQueueCompatibilityEvidence(t, database)
	// The child receives only the two disposable identities in its private
	// fixture, never the parent cluster's administrative DSN through env.
	t.Setenv("GOLEM_POLICY_HISTORY_PG_DSN", "")
	t.Setenv("GOLEM_POLICY_HISTORY_PG_ROOT", "")
	runPriorRelease(t, dir, "TestReleasedPostgreSQLQueueStartup")
	after := postgreSQLQueueCompatibilityEvidence(t, database)
	if !bytes.Equal(before, after) {
		t.Fatal("released startup refusal changed queued jobs or the public PostgreSQL schema")
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("released refusal damaged current PostgreSQL startup: %v", err)
	}
}

func postgreSQLQueueCompatibilityEvidence(t *testing.T, database *sqlx.DB) []byte {
	t.Helper()
	return queueCompatibilityQueryEvidence(t, database, []string{
		`SELECT * FROM public.golem_queue ORDER BY id`,
		`SELECT * FROM information_schema.columns WHERE table_schema='public' ORDER BY table_name,ordinal_position`,
		`SELECT c.relname, pg_catalog.pg_get_indexdef(i.indexrelid) AS definition, to_jsonb(i)::text AS properties FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' ORDER BY c.relname`,
		`SELECT c.conname, c.conrelid, pg_catalog.pg_get_constraintdef(c.oid) AS definition, to_jsonb(c)::text AS properties FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public' ORDER BY c.conrelid,c.conname`,
	})
}

func queueCompatibilityEvidence(t *testing.T, database *sqlx.DB) []byte {
	t.Helper()
	return queueCompatibilityQueryEvidence(t, database, []string{
		`SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`,
		`SELECT * FROM golem_queue ORDER BY id`,
	})
}

func queueCompatibilityQueryEvidence(t *testing.T, database *sqlx.DB, statements []string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var evidence [][]map[string]any
	for _, statement := range statements {
		rows, err := database.QueryxContext(ctx, statement)
		if err != nil {
			t.Fatal(err)
		}
		var values []map[string]any
		for rows.Next() {
			value := make(map[string]any)
			if err := rows.MapScan(value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for key, item := range value {
				if data, ok := item.([]byte); ok {
					value[key] = bytes.Clone(data)
				}
			}
			values = append(values, value)
			if len(values) > 128 {
				rows.Close()
				t.Fatal("queue evidence row bound exceeded")
			}
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("bounded queue evidence: read=%v close=%v", readErr, closeErr)
		}
		evidence = append(evidence, values)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > 64<<10 {
		t.Fatalf("queue evidence exceeded bound or failed encoding: %v", err)
	}
	return encoded
}

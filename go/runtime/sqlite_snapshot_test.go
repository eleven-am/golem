package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestSQLiteSnapshotPublicAPIReopensReviewedCopyWithPendingFacts(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true})
	fixture.mutate(t, golem.EventCreated, 91, 1, "before snapshot")
	fixture.mutate(t, golem.EventUpdated, 91, 1, "copied version")
	before := sqliteSnapshotRuntimeState(t, fixture.app.database)
	if len(before.facts) != 2 || len(before.deliveries) != 2 || before.counter == 0 {
		t.Fatalf("fixture lacks actual durable mutation state: %+v", before)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	if err := publicsqlite.Snapshot(fixture.ctx, fixture.applicationConfig.Database, fixture.schema.Bundle, destination); err != nil {
		t.Fatalf("public snapshot: %v", err)
	}
	fixture.mutate(t, golem.EventUpdated, 91, 1, "later source version")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := publicsqlite.Open(ctx, publicsqlite.Config{DataSourceName: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	config := fixture.applicationConfig
	config.Database = database
	app, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("normal startup refused the verified copied artifact: %v", err)
	}
	if app.EventCapabilities().PublisherRunning() {
		t.Fatal("snapshot reopen started a publisher")
	}
	after := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot altered versions or pending evidence: before=%+v after=%+v", before, after)
	}
	var title string
	if err := database.UnsafeSQLX().GetContext(ctx, &title, "SELECT title FROM posts WHERE id=?", (golem.UUID{15: 91}).String()); err != nil || title != "copied version" {
		t.Fatalf("snapshot followed later source write: title=%q error=%v", title, err)
	}
	if current := sqliteSnapshotRuntimeState(t, fixture.app.database); len(current.facts) != 3 || current.counter <= before.counter {
		t.Fatalf("source stopped progressing: %+v", current)
	}
}

type sqliteSnapshotMutationState struct {
	counter           int64
	facts, deliveries []string
	versions          []int64
}

func sqliteSnapshotRuntimeState(t *testing.T, database *sqlx.DB) sqliteSnapshotMutationState {
	t.Helper()
	var state sqliteSnapshotMutationState
	if err := database.Get(&state.counter, `SELECT value FROM "`+string(physical.RowVersionObject)+`"`); err != nil {
		t.Fatal(err)
	}
	if err := database.Select(&state.facts, `SELECT event_id || ':' || fact_version || ':' || codec_identity || ':' || generation_fingerprint || ':' || hex(metadata) || ':' || hex(delete_snapshot) || ':' || recorded_at FROM _golem_outbox ORDER BY event_id`); err != nil {
		t.Fatal(err)
	}
	if err := database.Select(&state.deliveries, `SELECT causation_id || ':' || status || ':' || attempt_count || ':' || available_at || ':' || updated_at FROM _golem_outbox_delivery ORDER BY causation_id`); err != nil {
		t.Fatal(err)
	}
	if err := database.Select(&state.versions, `SELECT "`+string(physical.RowVersionColumn)+`" FROM posts ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	return state
}

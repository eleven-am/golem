package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/ncruces/go-sqlite3/driver"
)

func sqliteFullTextIndexName(t *testing.T, database *sqlx.DB) string {
	t.Helper()
	var name string
	if err := database.Get(&name, `SELECT name FROM pragma_table_list WHERE schema='main' AND type='virtual'`); err != nil {
		t.Fatal(err)
	}
	return name
}

func sqliteFullTextDataPlan(t *testing.T, database *sqlx.DB, index string) string {
	t.Helper()
	var steps []struct {
		ID     int    `db:"id"`
		Parent int    `db:"parent"`
		Unused int    `db:"notused"`
		Detail string `db:"detail"`
	}
	if err := database.Select(&steps, `EXPLAIN QUERY PLAN SELECT block FROM "`+index+`_data" WHERE id>=? AND id<=?`, 1, 2); err != nil {
		t.Fatal(err)
	}
	details := make([]string, len(steps))
	for position, step := range steps {
		details[position] = step.Detail
	}
	return strings.Join(details, "\n")
}

func sqliteShadowStatistics(t *testing.T, database *sqlx.DB) int {
	t.Helper()
	var present int
	if err := database.Get(&present, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`); err != nil {
		t.Fatal(err)
	}
	if present == 0 {
		return 0
	}
	total := 0
	for _, table := range []string{"sqlite_stat1", "sqlite_stat4"} {
		var exists int
		if err := database.Get(&exists, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			continue
		}
		var rows int
		if err := database.Get(&rows, `SELECT count(*) FROM `+table+` WHERE tbl IN (SELECT name FROM pragma_table_list WHERE schema='main' AND type='shadow')`); err != nil {
			t.Fatal(err)
		}
		total += rows
	}
	return total
}

func requireRowIDRangeSearch(t *testing.T, plan string) {
	t.Helper()
	if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, "INTEGER PRIMARY KEY") {
		t.Fatalf("full-text storage is read by a scan instead of a rowid range, so every write gets slower as the index grows:\n%s", plan)
	}
}

func TestMigrationAnalysisLeavesFullTextShadowTablesUnanalyzed(t *testing.T) {
	database := sqliteFullTextWriteBenchmarkDatabase(t, true)
	defer database.Close()
	database.SetMaxOpenConns(1)
	index := sqliteFullTextIndexName(t, database)
	for row := range 10 {
		key := fmt.Sprintf("%016d", row)
		if _, err := database.Exec(`INSERT INTO documents(id,slug,subject,participants,body) VALUES(?,?,?,?,?)`, key, "slug-"+key, "subject", "sender", "alpha beta"); err != nil {
			t.Fatal(err)
		}
	}

	if err := analyzeSQLitePlanner(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if rows := sqliteShadowStatistics(t, database); rows != 0 {
		t.Fatalf("migration analysis recorded %d statistics rows for full-text storage", rows)
	}
	var owner int
	if err := database.Get(&owner, `SELECT count(*) FROM sqlite_stat1 WHERE tbl='documents'`); err != nil {
		t.Fatal(err)
	}
	if owner == 0 {
		t.Fatal("migration analysis skipped the ordinary table it exists to describe")
	}
	requireRowIDRangeSearch(t, sqliteFullTextDataPlan(t, database, index))
}

func TestMigrationAnalysisHealsStatisticsAnEarlierReleaseWrote(t *testing.T) {
	database := sqliteFullTextWriteBenchmarkDatabase(t, true)
	defer database.Close()
	if _, err := database.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	if rows := sqliteShadowStatistics(t, database); rows == 0 {
		t.Fatal("setup did not reproduce the statistics an unrestricted ANALYZE writes")
	}
	if err := analyzeSQLitePlanner(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if rows := sqliteShadowStatistics(t, database); rows != 0 {
		t.Fatalf("migration analysis left %d stale full-text statistics rows in place", rows)
	}
}

func TestOpeningADatabaseHealsPoisonedFullTextStatistics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poisoned.db")
	created := sqliteFullTextWriteBenchmarkDatabaseAt(t, path, true)
	if _, err := created.Exec(`ANALYZE`); err != nil {
		created.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := driver.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	poisoned := sqlx.NewDb(raw, "sqlite3")
	if rows := sqliteShadowStatistics(t, poisoned); rows == 0 {
		poisoned.Close()
		t.Fatal("setup did not leave stale full-text statistics on disk")
	}
	if err := poisoned.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, _, err := New().Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if rows := sqliteShadowStatistics(t, reopened); rows != 0 {
		t.Fatalf("opening left %d stale full-text statistics rows in place", rows)
	}
	index := sqliteFullTextIndexName(t, reopened)
	for range VerifiedPoolWidth * 2 {
		requireRowIDRangeSearch(t, sqliteFullTextDataPlan(t, reopened, index))
	}
}

type recordingStatisticsExecutor struct {
	database *sqlx.DB
	writes   []string
}

func (recorder *recordingStatisticsExecutor) ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error) {
	recorder.writes = append(recorder.writes, query)
	return recorder.database.ExecContext(ctx, query, arguments...)
}

func (recorder *recordingStatisticsExecutor) QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error) {
	return recorder.database.QueryContext(ctx, query, arguments...)
}

func TestForgettingShadowStatisticsDoesNotWriteWhenNoneAreStale(t *testing.T) {
	database := sqliteFullTextWriteBenchmarkDatabase(t, true)
	defer database.Close()
	if _, err := database.Exec(`ANALYZE documents`); err != nil {
		t.Fatal(err)
	}
	if rows := sqliteShadowStatistics(t, database); rows != 0 {
		t.Fatalf("setup recorded %d shadow statistics rows", rows)
	}
	recorder := &recordingStatisticsExecutor{database: database}
	forgotten, err := ForgetShadowTableStatistics(context.Background(), recorder)
	if err != nil {
		t.Fatal(err)
	}
	if forgotten != 0 || len(recorder.writes) != 0 {
		t.Fatalf("a database with no stale statistics was written to: forgotten=%d writes=%q", forgotten, recorder.writes)
	}
}

func TestOpeningReplacesPooledConnectionsEvenWhenTheDiskIsAlreadyClean(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "raced.db")
	created := sqliteFullTextWriteBenchmarkDatabaseAt(t, path, true)
	if _, err := created.Exec(`ANALYZE`); err != nil {
		created.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := driver.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pool := sqlx.NewDb(raw, "sqlite3")
	defer pool.Close()
	pool.SetMaxOpenConns(VerifiedPoolWidth)
	pool.SetMaxIdleConns(VerifiedPoolWidth)
	index := sqliteFullTextIndexName(t, pool)
	held := make([]*sqlx.Conn, 0, VerifiedPoolWidth)
	for range VerifiedPoolWidth {
		connection, err := pool.Connx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := connection.QueryxContext(ctx, `EXPLAIN QUERY PLAN SELECT block FROM "`+index+`_data" WHERE id>=? AND id<=?`, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		held = append(held, connection)
	}
	for _, connection := range held {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}

	cleaner, err := driver.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := sqlx.NewDb(cleaner, "sqlite3")
	if _, err := ForgetShadowTableStatistics(ctx, cleaned); err != nil {
		cleaned.Close()
		t.Fatal(err)
	}
	if err := cleaned.Close(); err != nil {
		t.Fatal(err)
	}

	if err := healShadowTableStatistics(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for range VerifiedPoolWidth * 2 {
		requireRowIDRangeSearch(t, sqliteFullTextDataPlan(t, pool, index))
	}
}

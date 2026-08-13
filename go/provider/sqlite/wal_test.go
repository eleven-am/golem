package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
	ncrucesdriver "github.com/ncruces/go-sqlite3/driver"
)

func TestSQLitePublicOpenEnablesAndVerifiesWALOnEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal_pool.sqlite")
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	pool := database.UnsafeSQLX()
	connections := make([]*sqlx.Conn, 0, 4)
	for index := 0; index < 4; index++ {
		connection, err := pool.Connx(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", index, err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	if stats := pool.Stats(); stats.OpenConnections != 4 || stats.InUse != 4 {
		t.Fatalf("pool was not exhausted: %+v", stats)
	}
	for index, connection := range connections {
		var journal string
		var synchronous, foreignKeys, busyTimeout int
		if err := connection.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
			t.Fatalf("connection %d journal_mode: %v", index, err)
		}
		if err := connection.GetContext(ctx, &synchronous, "PRAGMA synchronous"); err != nil {
			t.Fatalf("connection %d synchronous: %v", index, err)
		}
		if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if err := connection.GetContext(ctx, &busyTimeout, "PRAGMA busy_timeout"); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if strings.ToLower(journal) != "wal" || synchronous != 2 || foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d journal_mode=%q synchronous=%d foreign_keys=%d busy_timeout=%d", index, journal, synchronous, foreignKeys, busyTimeout)
		}
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("write-ahead log sidecar is missing: %v", err)
	}
}

func TestSQLitePublicOpenPreservesFullSynchronousAndExistingDataWhenEnablingWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy_delete.sqlite")
	seeded := seedLegacyRollbackJournalDatabase(t, path)

	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatal(err)
	}
	if observed := readLegacyRows(t, database.UnsafeSQLX()); !equalLegacyRows(observed, seeded) {
		t.Fatalf("WAL transition changed the logical snapshot:\nwant %#v\ngot  %#v", seeded, observed)
	}
	assertEveryPooledConnection(t, database.UnsafeSQLX(), func(index int, connection *sqlx.Conn) {
		var journal string
		var synchronous int
		if err := connection.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
			t.Fatalf("connection %d journal_mode: %v", index, err)
		}
		if err := connection.GetContext(ctx, &synchronous, "PRAGMA synchronous"); err != nil {
			t.Fatalf("connection %d synchronous: %v", index, err)
		}
		if strings.ToLower(journal) != "wal" || synchronous != 2 {
			t.Fatalf("connection %d journal_mode=%q synchronous=%d", index, journal, synchronous)
		}
	})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatalf("reopening an existing WAL database is not idempotent: %v", err)
	}
	defer reopened.Close()
	if observed := readLegacyRows(t, reopened.UnsafeSQLX()); !equalLegacyRows(observed, seeded) {
		t.Fatalf("reopen changed the logical snapshot:\nwant %#v\ngot  %#v", seeded, observed)
	}
	assertEveryPooledConnection(t, reopened.UnsafeSQLX(), func(index int, connection *sqlx.Conn) {
		var journal string
		var synchronous int
		if err := connection.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
			t.Fatalf("connection %d journal_mode: %v", index, err)
		}
		if err := connection.GetContext(ctx, &synchronous, "PRAGMA synchronous"); err != nil {
			t.Fatalf("connection %d synchronous: %v", index, err)
		}
		if strings.ToLower(journal) != "wal" || synchronous != 2 {
			t.Fatalf("reopened connection %d journal_mode=%q synchronous=%d", index, journal, synchronous)
		}
	})
}

func TestSQLitePublicOpenRejectsEveryWALAndSynchronousOverrideSpelling(t *testing.T) {
	const secret = "wal-dsn-canary"
	directory := t.TempDir()
	for _, suffix := range []string{
		"?_pragma=journal_mode(delete)",
		"?_pragma=journal_mode(DELETE)",
		"?_pragma=JOURNAL_MODE(wal)",
		"?_pragma=journal_mode=delete",
		"?_pragma=%6aournal_mode(delete)",
		"?_pragma=journal%5fmode(delete)",
		"?_pragma=%20journal_mode%20(delete)",
		"?_pragma=main.journal_mode(delete)",
		"?_pragma=%22main%22.journal_mode(delete)",
		"?_%70ragma=journal_mode(delete)",
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(delete)",
		"?_pragma=synchronous(0)",
		"?_pragma=synchronous(NORMAL)",
		"?_pragma=SYNCHRONOUS%3dOFF",
		"?_pragma=%20synchronous%09(1)",
		"?_pragma=temp.synchronous(0)",
		"?_%70ragma=sync%68ronous(0)",
		"?mode=ro",
		"?mode=RO",
		"?immutable=1",
		"?immutable=true",
	} {
		dataSourceName := "file:" + filepath.Join(directory, "override.sqlite") + suffix + "&password=" + secret
		database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
		if database != nil {
			_ = database.Close()
			t.Fatalf("DSN %q unexpectedly opened", dataSourceName)
		}
		if err == nil {
			t.Fatalf("DSN %q unexpectedly opened", dataSourceName)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), dataSourceName) {
			t.Fatalf("DSN %q disclosed its canary: %v", dataSourceName, err)
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
			t.Fatalf("DSN %q code=%q known=%t", dataSourceName, code, ok)
		}
	}
}

func TestSQLiteNamedSharedMemoryIsExplicitlyNotWALProductionEvidence(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:wal_named_shared?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	assertEveryPooledConnection(t, database.UnsafeSQLX(), func(index int, connection *sqlx.Conn) {
		var journal string
		var synchronous, foreignKeys, busyTimeout int
		if err := connection.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
			t.Fatalf("connection %d journal_mode: %v", index, err)
		}
		if err := connection.GetContext(ctx, &synchronous, "PRAGMA synchronous"); err != nil {
			t.Fatalf("connection %d synchronous: %v", index, err)
		}
		if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if err := connection.GetContext(ctx, &busyTimeout, "PRAGMA busy_timeout"); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if strings.ToLower(journal) != "memory" {
			t.Fatalf("connection %d reported journal_mode=%q; the named shared in-memory profile is not WAL evidence", index, journal)
		}
		if synchronous != 2 || foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d synchronous=%d foreign_keys=%d busy_timeout=%d", index, synchronous, foreignKeys, busyTimeout)
		}
		var location string
		if err := connection.GetContext(ctx, &location, "SELECT file FROM pragma_database_list WHERE name = 'main'"); err != nil {
			t.Fatalf("connection %d database location: %v", index, err)
		}
		if location != "" {
			t.Fatalf("connection %d reported a file-backed location %q for an in-memory profile", index, location)
		}
	})
}

func TestSQLiteWALReaderWriterContentionIsBoundedAndPoolIsReleased(t *testing.T) {
	ctx := context.Background()
	baselineGoroutines := runtime.NumGoroutine()
	path := filepath.Join(t.TempDir(), "wal_contention.sqlite")
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:" + path})
	if err != nil {
		t.Fatal(err)
	}
	pool := database.UnsafeSQLX()
	if _, err := pool.ExecContext(ctx, `CREATE TABLE authors (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `CREATE TABLE books (id INTEGER PRIMARY KEY, author INTEGER NOT NULL REFERENCES authors(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO authors (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO books (id, author) VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}

	const readers = 3
	var holding sync.WaitGroup
	var finished sync.WaitGroup
	holding.Add(readers)
	finished.Add(readers)
	release := make(chan struct{})
	snapshots := make([]int, readers)
	failures := make([]error, readers)
	for index := 0; index < readers; index++ {
		go func(slot int) {
			defer finished.Done()
			transaction, err := pool.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				failures[slot] = fmt.Errorf("reader %d begin: %w", slot, err)
				holding.Done()
				return
			}
			var before int
			if err := transaction.GetContext(ctx, &before, `SELECT count(*) FROM books`); err != nil {
				failures[slot] = fmt.Errorf("reader %d first read: %w", slot, err)
				_ = transaction.Rollback()
				holding.Done()
				return
			}
			holding.Done()
			<-release
			var during int
			if err := transaction.GetContext(ctx, &during, `SELECT count(*) FROM books`); err != nil {
				failures[slot] = fmt.Errorf("reader %d read beside writer: %w", slot, err)
				_ = transaction.Rollback()
				return
			}
			snapshots[slot] = during
			if err := transaction.Rollback(); err != nil {
				failures[slot] = fmt.Errorf("reader %d rollback: %w", slot, err)
			}
		}(index)
	}
	holding.Wait()
	for _, failure := range failures {
		if failure != nil {
			t.Fatal(failure)
		}
	}

	started := time.Now()
	writer, err := pool.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("write transaction blocked behind open readers after %s: %v", time.Since(started), err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO books (id, author) VALUES (2, 1)`); err != nil {
		t.Fatalf("write beside open readers failed after %s: %v", time.Since(started), err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit beside open readers failed after %s: %v", time.Since(started), err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the writer serialised behind open readers for %s; readers still block the writer", elapsed)
	}
	close(release)
	finished.Wait()
	for _, failure := range failures {
		if failure != nil {
			t.Fatal(failure)
		}
	}
	for slot, snapshot := range snapshots {
		if snapshot != 1 {
			t.Fatalf("reader %d observed %d rows inside its open snapshot, want 1", slot, snapshot)
		}
	}
	var committed int
	if err := pool.GetContext(ctx, &committed, `SELECT count(*) FROM books`); err != nil {
		t.Fatal(err)
	}
	if committed != 2 {
		t.Fatalf("committed row count=%d, want 2", committed)
	}

	first, err := pool.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	contested := time.Now()
	second, err := pool.BeginTxx(bounded, nil)
	if err == nil {
		_ = second.Rollback()
		_ = first.Rollback()
		t.Fatal("two immediate write transactions were granted at once; writes are no longer serialized")
	}
	if waited := time.Since(contested); waited > 3*time.Second {
		t.Fatalf("the competing writer waited %s, beyond the bounded busy timeout", waited)
	}
	if err := first.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := pool.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("the write lock was not released: %v", err)
	}
	if err := after.Rollback(); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.ExecContext(ctx, `INSERT INTO books (id, author) VALUES (3, 404)`); err == nil {
		t.Fatal("foreign keys are no longer enforced under WAL")
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := pool.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
		t.Fatalf("close left pooled connections: %+v", stats)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baselineGoroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - baselineGoroutines; leaked > 0 {
		t.Fatalf("close left %d goroutines running", leaked)
	}
}

type legacyRow struct {
	ID       int64
	Name     string
	Payload  []byte
	Ratio    float64
	Optional sql.NullString
}

func seedLegacyRollbackJournalDatabase(t *testing.T, path string) []legacyRow {
	t.Helper()
	ctx := context.Background()
	standard, err := ncrucesdriver.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := sqlx.NewDb(standard, "sqlite3")
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE legacy (id INTEGER PRIMARY KEY, name TEXT NOT NULL, payload BLOB NOT NULL, ratio REAL NOT NULL, optional TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := []legacyRow{
		{ID: 1, Name: "ascii", Payload: []byte{0x00, 0x01, 0xff}, Ratio: 0.5, Optional: sql.NullString{String: "present", Valid: true}},
		{ID: 2, Name: "unicode é中\U0001f600", Payload: []byte{}, Ratio: -1.25, Optional: sql.NullString{}},
		{ID: 3, Name: strings.Repeat("wide", 512), Payload: []byte("\n\t\x00binary"), Ratio: 1e308, Optional: sql.NullString{String: "", Valid: true}},
	}
	for _, row := range rows {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO legacy (id, name, payload, ratio, optional) VALUES (?, ?, ?, ?, ?)`, row.ID, row.Name, row.Payload, row.Ratio, row.Optional); err != nil {
			t.Fatal(err)
		}
	}
	var journal string
	if err := legacy.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journal) != "delete" {
		t.Fatalf("seed database journal_mode=%q, want delete", journal)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func readLegacyRows(t *testing.T, pool *sqlx.DB) []legacyRow {
	t.Helper()
	cursor, err := pool.QueryxContext(context.Background(), `SELECT id, name, ratio, optional, payload FROM legacy ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close()
	var rows []legacyRow
	for cursor.Next() {
		var row legacyRow
		if err := cursor.Scan(&row.ID, &row.Name, &row.Ratio, &row.Optional, &row.Payload); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := cursor.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func equalLegacyRows(left, right []legacyRow) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Name != right[index].Name {
			return false
		}
		if left[index].Ratio != right[index].Ratio || left[index].Optional != right[index].Optional {
			return false
		}
		if string(left[index].Payload) != string(right[index].Payload) {
			return false
		}
	}
	return true
}

func assertEveryPooledConnection(t *testing.T, pool *sqlx.DB, check func(int, *sqlx.Conn)) {
	t.Helper()
	ctx := context.Background()
	connections := make([]*sqlx.Conn, 0, 4)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < 4; index++ {
		connection, err := pool.Connx(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", index, err)
		}
		connections = append(connections, connection)
	}
	if stats := pool.Stats(); stats.InUse != 4 {
		t.Fatalf("pool was not exhausted: %+v", stats)
	}
	for index, connection := range connections {
		check(index, connection)
	}
}

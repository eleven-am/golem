package sqlite_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestP8SQLitePublicOpenAppliesInvariantToEveryPooledConnection(t *testing.T) {
	profiles := map[string]string{
		"file":         "file:" + filepath.Join(t.TempDir(), "p8_sqlite_public.sqlite"),
		"named-memory": "file:p8_sqlite_public?mode=memory&cache=shared",
	}
	for name, dataSourceName := range profiles {
		t.Run(name, func(t *testing.T) {
			assertSQLitePublicPoolProfile(t, dataSourceName)
			if name == "file" {
				assertSQLiteImmediateWriteMode(t, dataSourceName)
			}
		})
	}
}

func assertSQLitePublicPoolProfile(t *testing.T, dataSourceName string) {
	t.Helper()
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if database.Pool().MaximumOpen() != 4 || database.Pool().MaximumIdle() != 4 {
		t.Fatalf("pool=%+v", database.Pool())
	}
	db := database.UnsafeSQLX()
	if db == nil {
		t.Fatal("unsafe pool is nil")
	}
	connections := make([]*sqlx.Conn, 0, 4)
	for index := 0; index < 4; index++ {
		connection, err := db.Connx(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", index, err)
		}
		connections = append(connections, connection)
	}
	t.Cleanup(func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	if stats := db.Stats(); stats.OpenConnections != 4 || stats.InUse != 4 {
		t.Fatalf("pool was not exhausted: %+v", stats)
	}
	for index, connection := range connections {
		var foreignKeys, busyTimeout int
		if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if err := connection.GetContext(ctx, &busyTimeout, "PRAGMA busy_timeout"); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d foreign_keys=%d busy_timeout=%d", index, foreignKeys, busyTimeout)
		}
	}
	features := database.Capabilities().Features()
	if !contains(features, provider.FeatureForeignKeys) || !contains(features, provider.FeatureAnalyticsExact) {
		t.Fatalf("features=%#v", features)
	}
}

func TestP8SQLiteRefusesPrivateMemoryAndProviderPragmaOverride(t *testing.T) {
	for _, dataSourceName := range []string{
		":memory:",
		"file:p8_private?mode=memory",
		"file:p8_private?mo%64e=mem%6fry",
		"file:?mode=memory&cache=shared",
		"file://?mode=memory&cache=shared",
		"file::memory:?cache=shared",
		"file::memory:?mode=memory&cache=shared",
		"file:shared?mode=memory&cache=shared#fragment",
		"file:p8_private?mode=memory&cache=private&cache=shared",
		"file:p8_override.db?_pragma=foreign_keys(0)",
		"file:p8_override.db?_pragma=busy_timeout(1)",
		"file:p8_override.db?_%70ragma=foreign%5fkeys(0)",
		"file:p8_override.db?_pragma=BUSY%5fTIMEOUT%3d1",
		"file:p8_override.db?_txlock=deferred",
		"file:p8_override.db?_%74xlock=exclusive",
	} {
		database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
		if database != nil {
			_ = database.Close()
			t.Fatalf("DSN %q unexpectedly opened", dataSourceName)
		}
		if err == nil || strings.Contains(err.Error(), dataSourceName) {
			t.Fatalf("DSN %q error=%v", dataSourceName, err)
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
			t.Fatalf("DSN %q code=%q known=%t", dataSourceName, code, ok)
		}
	}
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:p8_encoded_shared?mo%64e=mem%6fry&ca%63he=sha%72ed"})
	if err != nil {
		t.Fatalf("encoded named shared-memory DSN rejected: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestP8SQLitePublicOpenUsesImmediateWriteTransactions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "p8_sqlite_immediate.sqlite")
	assertSQLiteImmediateWriteMode(t, "file:"+databasePath)
}

func assertSQLiteImmediateWriteMode(t *testing.T, dataSourceName string) {
	t.Helper()
	databasePath := strings.TrimPrefix(dataSourceName, "file:")
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.UnsafeSQLX().ExecContext(context.Background(), `CREATE TABLE p8_immediate_lock (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.UnsafeSQLX().BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	competing := exec.Command(os.Args[0], "-test.run=^TestSQLiteImmediateLockSubprocessHelper$")
	competing.Env = append(os.Environ(), "GOLEM_P8_SQLITE_LOCK_HELPER=file:"+databasePath)
	if output, err := competing.CombinedOutput(); err == nil {
		_ = transaction.Rollback()
		t.Fatalf("a statement-free transaction did not reserve the write lock; deferred mode is active: %s", output)
	} else if text := strings.ToLower(string(output)); !strings.Contains(text, "locked") && !strings.Contains(text, "busy") {
		_ = transaction.Rollback()
		t.Fatalf("competing writer failed for a reason other than the reserved write lock: %v\n%s", err, output)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	afterRollback := exec.Command(os.Args[0], "-test.run=^TestSQLiteImmediateLockSubprocessHelper$")
	afterRollback.Env = append(os.Environ(), "GOLEM_P8_SQLITE_LOCK_HELPER=file:"+databasePath)
	if output, err := afterRollback.CombinedOutput(); err != nil {
		t.Fatalf("write lock remained after rollback: %v\n%s", err, output)
	}
}

func TestSQLiteImmediateLockSubprocessHelper(t *testing.T) {
	dataSourceName := os.Getenv("GOLEM_P8_SQLITE_LOCK_HELPER")
	if dataSourceName == "" {
		return
	}
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	connection, err := database.UnsafeSQLX().Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `INSERT INTO p8_immediate_lock (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
}

func TestP8SQLiteOpenFailureClosesAllResourcesAndRedactsDSN(t *testing.T) {
	const secret = "p8-sqlite-password-canary"
	for _, dataSourceName := range []string{
		"file:p8_redaction?_pragma=foreign_keys(0)&password=" + secret,
		"file:p8_redaction?_txlock=deferred&password=" + secret,
		"file:" + filepath.Join(t.TempDir(), "readonly.sqlite") + "?mode=ro&password=" + secret,
	} {
		database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dataSourceName})
		if database != nil {
			_ = database.Close()
			t.Fatalf("failed open returned a database for %q", dataSourceName)
		}
		if err == nil {
			t.Fatalf("DSN %q unexpectedly opened", dataSourceName)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), dataSourceName) {
			t.Fatalf("public failure disclosed its DSN or credential canary: %v", err)
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
			t.Fatalf("DSN %q code=%q known=%t", dataSourceName, code, ok)
		}
	}
}

func contains(values []provider.Feature, target provider.Feature) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

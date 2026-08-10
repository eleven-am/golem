package gentest

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestStartTempSQLiteUsesTestDirectoryAndCleansUp(t *testing.T) {
	var closed atomic.Bool
	var startedIn string
	t.Run("lifecycle", func(t *testing.T) {
		target := StartTempSQLite(t, context.Background(), FakeSQLiteLifecycle{
			StartFunc: func(_ context.Context, directory string) (SQLiteTarget, error) {
				startedIn = directory
				return SQLiteTarget{
					DatabasePath: filepath.Join(directory, "schema.sqlite"),
					Close: func(context.Context) error {
						closed.Store(true)
						return nil
					},
				}, nil
			},
		})
		if filepath.Dir(target.DatabasePath) != startedIn {
			t.Fatalf("database path %q is not under %q", target.DatabasePath, startedIn)
		}
		if closed.Load() {
			t.Fatal("target closed before test cleanup")
		}
	})
	if !closed.Load() {
		t.Fatal("SQLite target was not closed by cleanup")
	}
}

func TestStartPostgreSQLUsesAdapterBoundaryAndCleansUp(t *testing.T) {
	var closed atomic.Bool
	t.Run("service", func(t *testing.T) {
		target := StartPostgreSQL(t, context.Background(), FakePostgreSQLService{
			StartFunc: func(context.Context) (PostgreSQLTarget, error) {
				return PostgreSQLTarget{
					ConnectionURL: "postgres://fixture.invalid/golem",
					Close: func(context.Context) error {
						closed.Store(true)
						return nil
					},
				}, nil
			},
		})
		if target.ConnectionURL != "postgres://fixture.invalid/golem" {
			t.Fatalf("connection URL = %q", target.ConnectionURL)
		}
		if closed.Load() {
			t.Fatal("target closed before test cleanup")
		}
	})
	if !closed.Load() {
		t.Fatal("PostgreSQL target was not closed by cleanup")
	}
}

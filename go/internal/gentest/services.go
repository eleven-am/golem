package gentest

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// SQLiteTarget describes an isolated filesystem location. Opening a database,
// selecting a driver, and applying pragmas belong to the provider adapter.
type SQLiteTarget struct {
	DatabasePath string
	Close        func(context.Context) error
}

// SQLiteLifecycle creates an isolated SQLite target beneath directory.
type SQLiteLifecycle interface {
	Start(context.Context, string) (SQLiteTarget, error)
}

// StartTempSQLite allocates a test-owned directory, starts the adapter, and
// registers cleanup. No live driver is selected by this helper.
func StartTempSQLite(t testing.TB, ctx context.Context, lifecycle SQLiteLifecycle) SQLiteTarget {
	t.Helper()
	if lifecycle == nil {
		t.Fatal("SQLite lifecycle is nil")
	}
	directory := t.TempDir()
	target, err := lifecycle.Start(ctx, directory)
	if err != nil {
		t.Fatalf("start temporary SQLite target: %v", err)
	}
	if target.DatabasePath == "" {
		t.Fatal("SQLite lifecycle returned an empty database path")
	}
	relative, err := filepath.Rel(directory, target.DatabasePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("SQLite lifecycle returned path outside temporary directory: %q", target.DatabasePath)
	}
	if target.Close != nil {
		t.Cleanup(func() {
			if err := target.Close(context.Background()); err != nil {
				t.Errorf("close temporary SQLite target: %v", err)
			}
		})
	}
	return target
}

// PostgreSQLTarget is a driver-neutral service endpoint. ConnectionURL may be
// consumed by a separately selected database/sql adapter and must not be placed
// in diagnostics or goldens because it may contain credentials.
type PostgreSQLTarget struct {
	ConnectionURL string
	Close         func(context.Context) error
}

// PostgreSQLService is the service/container boundary for future live tests.
// W1-C defines the seam only and does not choose a driver or container library.
type PostgreSQLService interface {
	Start(context.Context) (PostgreSQLTarget, error)
}

// StartPostgreSQL starts the configured service adapter and registers cleanup.
func StartPostgreSQL(t testing.TB, ctx context.Context, service PostgreSQLService) PostgreSQLTarget {
	t.Helper()
	if service == nil {
		t.Fatal("PostgreSQL service is nil")
	}
	target, err := service.Start(ctx)
	if err != nil {
		t.Fatalf("start PostgreSQL service: %v", err)
	}
	if target.ConnectionURL == "" {
		t.Fatal("PostgreSQL service returned an empty connection URL")
	}
	if target.Close != nil {
		t.Cleanup(func() {
			if err := target.Close(context.Background()); err != nil {
				t.Errorf("close PostgreSQL service: %v", err)
			}
		})
	}
	return target
}

// FakeSQLiteLifecycle is a minimal fake for lifecycle and cleanup tests.
type FakeSQLiteLifecycle struct {
	StartFunc func(context.Context, string) (SQLiteTarget, error)
}

func (fake FakeSQLiteLifecycle) Start(ctx context.Context, directory string) (SQLiteTarget, error) {
	if fake.StartFunc == nil {
		return SQLiteTarget{}, fmt.Errorf("fake SQLite StartFunc is nil")
	}
	return fake.StartFunc(ctx, directory)
}

// FakePostgreSQLService is a minimal fake for service and cleanup tests.
type FakePostgreSQLService struct {
	StartFunc func(context.Context) (PostgreSQLTarget, error)
}

func (fake FakePostgreSQLService) Start(ctx context.Context) (PostgreSQLTarget, error) {
	if fake.StartFunc == nil {
		return PostgreSQLTarget{}, fmt.Errorf("fake PostgreSQL StartFunc is nil")
	}
	return fake.StartFunc(ctx)
}

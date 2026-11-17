package sqlite_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestSQLiteSnapshotRequiresVerifiedOpenLocalHandle(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"nil", "zero", "closed", "unverified", "wrong_provider", "network_rollback", "network_wal"} {
		t.Run(kind, func(t *testing.T) {
			var database *provider.Database
			var err error
			config := providersqlite.Config{DataSourceName: filepath.Join(t.TempDir(), "source.db")}
			switch kind {
			case "zero":
				database = &provider.Database{}
			case "closed":
				database, err = providersqlite.Open(ctx, config)
				if err == nil {
					err = database.Close()
				}
			case "unverified", "wrong_provider":
				providerKind := golem.SQLite
				if kind == "wrong_provider" {
					providerKind = golem.PostgreSQL
				}
				database = (*provider.Database)(providerhandle.AdoptUnverifiedForTest(nil, providerhandle.TestMetadata{Provider: providerKind}))
			case "network_rollback":
				database, err = providersqlite.OpenNetworkFilesystem(ctx, config)
			case "network_wal":
				database, err = providersqlite.OpenNetworkFilesystemWAL(ctx, config)
			}
			if err != nil {
				t.Fatal(err)
			}
			if database != nil {
				t.Cleanup(func() { _ = database.Close() })
			}
			directory := t.TempDir()
			destination := filepath.Join(directory, "PRIVATE_SNAPSHOT_DESTINATION")
			err = providersqlite.Snapshot(ctx, database, golem.SchemaBundle{}, destination)
			if code, ok := provider.CodeOf(err); !ok || code != provider.CodeMaintenance {
				t.Fatalf("refusal code=(%q,%v) error=%v", code, ok, err)
			}
			if strings.Contains(err.Error(), "PRIVATE_SNAPSHOT_DESTINATION") || strings.Contains(err.Error(), config.DataSourceName) {
				t.Fatalf("refusal disclosed storage identity: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refused admission created artifacts: entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestSQLiteSnapshotRejectsInvalidExpectationAndPreservesCancellation(t *testing.T) {
	ctx := context.Background()
	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: filepath.Join(t.TempDir(), "source.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	directory := t.TempDir()
	destination := filepath.Join(directory, "snapshot.db")
	if err := providersqlite.Snapshot(ctx, database, golem.SchemaBundle{}, destination); err == nil {
		t.Fatal("missing expected bundle accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := providersqlite.Snapshot(canceled, database, golem.SchemaBundle{}, destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if err := providersqlite.Snapshot(nil, database, golem.SchemaBundle{}, destination); err == nil {
		t.Fatal("nil context accepted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid preflight created artifacts: entries=%v error=%v", entries, err)
	}
	if err := database.UnsafeSQLX().PingContext(ctx); err != nil {
		t.Fatalf("refused snapshot closed source: %v", err)
	}
}

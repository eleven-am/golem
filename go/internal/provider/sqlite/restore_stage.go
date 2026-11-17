package sqlite

import (
	"context"
	"net/url"

	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// OpenRestoreArchive reads an immutable, standalone archive. Its owner has
// checked the file and sidecar namespace; this never opens a live database.
func (*Provider) OpenRestoreArchive(ctx context.Context, path string) (*sqlx.DB, error) {
	return openRestoreFile(ctx, path, true)
}

// OpenRestoreStage opens the private DELETE-journal copy without converting it
// to the normal serving WAL profile. No worker or publisher runs on this pool.
func (*Provider) OpenRestoreStage(ctx context.Context, path string) (*sqlx.DB, error) {
	return openRestoreFile(ctx, path, false)
}

func openRestoreFile(ctx context.Context, path string, archive bool) (*sqlx.DB, error) {
	values := url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)"}}
	if archive {
		values.Set("mode", "ro")
		values.Set("immutable", "1")
		values.Add("_pragma", "query_only(1)")
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}
	standard, err := sqlitedriver.Open(uri.String())
	if err != nil {
		return nil, err
	}
	database := sqlx.NewDb(standard, "sqlite3")
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// VerifyRestoreStage reuses snapshot verification on an exclusively owned,
// closed staging file. SQLite owns journal normalization and checkpointing.
func (provider *Provider) VerifyRestoreStage(ctx context.Context, path string, expected physical.PhysicalSchema, manifest migration.Manifest) error {
	if err := prepareSQLiteSnapshotVerification(ctx, path); err != nil {
		return err
	}
	return provider.verifySnapshot(ctx, path, expected, manifest)
}

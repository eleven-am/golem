package sqlite

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/ncruces/go-sqlite3/driver"
)

// CheckpointForBackup performs the bounded provider-owned WAL checkpoint that
// the supported backup procedure requires before the database file is copied.
// It is recovery/backup code, not runtime code: the caller must already have
// stopped application writes and closed the Golem provider handle. The
// checkpoint runs through a maintenance connection this call owns, requires the
// truncating checkpoint to leave no busy frames and no residual log, and never
// deletes or truncates a sidecar file itself. A busy reader fails the call so
// the operator does not copy an incomplete database.
func (provider *Provider) CheckpointForBackup(ctx context.Context, dataSourceName string) error {
	configured, err := configureDataSourceName(dataSourceName)
	if err != nil {
		return err
	}
	standard, err := driver.Open(configured, initializeProviderConnection)
	if err != nil {
		return fmt.Errorf("sqlite checkpoint: %w", err)
	}
	database := sqlx.NewDb(standard, "sqlite3")
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	connection, err := database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("sqlite checkpoint: maintenance connection: %w", err)
	}
	defer connection.Close()
	required, err := requiredJournalMode(ctx, connection)
	if err != nil {
		return err
	}
	if required != journalModeWAL {
		return fmt.Errorf("sqlite checkpoint: in-memory databases have no write-ahead log to checkpoint")
	}
	observed, err := observedJournalMode(ctx, connection)
	if err != nil {
		return err
	}
	if observed != journalModeWAL {
		return fmt.Errorf("sqlite checkpoint: database reported journal mode %q, want %q", observed, journalModeWAL)
	}
	var busy, log, checkpointed int
	if err := connection.QueryRowxContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return fmt.Errorf("sqlite checkpoint: truncating checkpoint failed: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("sqlite checkpoint: truncating checkpoint was blocked by a concurrent reader or writer")
	}
	if log != 0 || checkpointed != 0 {
		return fmt.Errorf("sqlite checkpoint: write-ahead log retained %d frames of which %d were checkpointed", log, checkpointed)
	}
	return nil
}

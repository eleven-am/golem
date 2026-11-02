package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/ncruces/go-sqlite3/driver"
)

const (
	databaseHeaderLength            = 100
	databaseHeaderWriteVersion      = 18
	databaseHeaderReadVersion       = 19
	databaseHeaderWriteAheadVersion = 2
)

var databaseHeaderMagic = []byte("SQLite format 3\x00")

func convertWriteAheadLogForRollbackJournal(ctx context.Context, dataSourceName string) error {
	location, fileBacked, err := dataSourceFileLocation(dataSourceName)
	if err != nil || !fileBacked {
		return err
	}
	writeAheadLog, err := databaseHeaderDeclaresWriteAheadLog(location)
	if err != nil || !writeAheadLog {
		return err
	}
	if err := refuseSharedMemoryIndexFor(location, "the rollback-journal profile refuses to convert it from write-ahead logging"); err != nil {
		return err
	}
	if err := runRollbackJournalConversion(ctx, dataSourceName); err != nil {
		return fmt.Errorf("sqlite open: convert write-ahead logging database %s to rollback journaling: %w", location, err)
	}
	stillWriteAheadLog, err := databaseHeaderDeclaresWriteAheadLog(location)
	if err != nil {
		return err
	}
	if stillWriteAheadLog {
		return fmt.Errorf("sqlite open: database %s still declares write-ahead logging after rollback-journal conversion", location)
	}
	return nil
}

func runRollbackJournalConversion(ctx context.Context, dataSourceName string) error {
	configured, err := configureDataSourceNameForProfile(dataSourceName, DurabilityNetworkFilesystemWAL)
	if err != nil {
		return err
	}
	standard, err := driver.Open(configured, initializeExclusiveWALConnection)
	if err != nil {
		return err
	}
	database := sqlx.NewDb(standard, "sqlite3")
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	connection, err := database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("exclusive conversion connection: %w", err)
	}
	defer connection.Close()
	var lockingMode string
	if err := connection.GetContext(ctx, &lockingMode, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		return fmt.Errorf("exclusive locking: %w", err)
	}
	if normalizeJournalMode(lockingMode) != lockingModeExclusive {
		return fmt.Errorf("conversion connection reported locking mode %q, want %q", lockingMode, lockingModeExclusive)
	}
	observed, err := observedJournalMode(ctx, connection)
	if err != nil {
		return err
	}
	if observed != journalModeWAL {
		return fmt.Errorf("conversion connection reported journal mode %q, want %q", observed, journalModeWAL)
	}
	var busy, log, checkpointed int
	if err := connection.QueryRowxContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return fmt.Errorf("truncating checkpoint: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("truncating checkpoint was blocked by a concurrent reader or writer")
	}
	if log != 0 || checkpointed != 0 {
		return fmt.Errorf("truncating checkpoint retained %d frames of which %d were checkpointed", log, checkpointed)
	}
	var applied string
	if err := connection.GetContext(ctx, &applied, "PRAGMA journal_mode=DELETE"); err != nil {
		return fmt.Errorf("journal mode transition: %w", err)
	}
	if normalizeJournalMode(applied) != journalModeDelete {
		return fmt.Errorf("journal mode transition reported journal mode %q, want %q", applied, journalModeDelete)
	}
	var integrity []string
	if err := connection.SelectContext(ctx, &integrity, "PRAGMA integrity_check"); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if len(integrity) != 1 || integrity[0] != "ok" {
		return fmt.Errorf("integrity check reported %q, want %q", strings.Join(integrity, "; "), "ok")
	}
	if err := connection.Close(); err != nil {
		return fmt.Errorf("close conversion connection: %w", err)
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close conversion database: %w", err)
	}
	return nil
}

func dataSourceFileLocation(dataSourceName string) (string, bool, error) {
	base, rawQuery, _ := strings.Cut(dataSourceName, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", false, fmt.Errorf("sqlite open: data source name query is invalid")
	}
	for key, values := range query {
		if !strings.EqualFold(strings.TrimSpace(key), "mode") {
			continue
		}
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(value), "memory") {
				return "", false, nil
			}
		}
	}
	path := strings.TrimPrefix(base, "file:")
	if strings.HasPrefix(path, "//") {
		authority, rest, _ := strings.Cut(strings.TrimPrefix(path, "//"), "/")
		if authority != "" && !strings.EqualFold(authority, "localhost") {
			return "", false, fmt.Errorf("sqlite open: data source name authority %q is unsupported", authority)
		}
		path = "/" + rest
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return "", false, fmt.Errorf("sqlite open: data source name path is invalid")
	}
	if decoded == "" || decoded == ":memory:" {
		return "", false, nil
	}
	return decoded, true, nil
}

func databaseHeaderDeclaresWriteAheadLog(location string) (bool, error) {
	file, err := os.Open(location)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sqlite open: inspect database header %s: %w", location, err)
	}
	defer file.Close()
	header := make([]byte, databaseHeaderLength)
	if _, err := io.ReadFull(file, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, fmt.Errorf("sqlite open: inspect database header %s: %w", location, err)
	}
	if !bytes.Equal(header[:len(databaseHeaderMagic)], databaseHeaderMagic) {
		return false, nil
	}
	return header[databaseHeaderWriteVersion] == databaseHeaderWriteAheadVersion && header[databaseHeaderReadVersion] == databaseHeaderWriteAheadVersion, nil
}

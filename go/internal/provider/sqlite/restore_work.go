package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

// ErrRestoreQueueStorage identifies malformed or unsupported archived job
// storage without exposing its values through the public restore boundary.
var ErrRestoreQueueStorage = errors.New("sqlite restore queue storage is invalid")

// RestoreWorkMode is an explicit decision about work in an offline archive.
type RestoreWorkMode string

const (
	RestoreResumeAtLeastOnce RestoreWorkMode = "resume-at-least-once"
	RestoreAbandonAndResync  RestoreWorkMode = "abandon-and-resync"
	restoreAbandonedCode                     = "restore-abandoned"
)

// restoreImmediate owns only a private staging database. The caller must stop
// the old fleet and prevent restart; a SQLite transaction cannot prove that.
func restoreImmediate(ctx context.Context, database *sqlx.DB, apply func(*sqlx.Conn) error) error {
	if ctx == nil || database == nil {
		return fmt.Errorf("sqlite restore database/context is nil")
	}
	connection, err := database.Connx(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err = connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := sqliteCleanupContext(ctx)
		defer cancel()
		_, _ = connection.ExecContext(cleanup, "ROLLBACK")
	}()
	if err = apply(connection); err != nil {
		return err
	}
	_, err = connection.ExecContext(ctx, "COMMIT")
	return err
}

// ReconcileRestoreWork runs once, before staged application reconciliation.
// Calling it at publication would wrongly abandon newly reconciled work.
// Full catalog/archive verification belongs to the restore session owner.
func (*Provider) ReconcileRestoreWork(ctx context.Context, database *sqlx.DB, system physical.SystemSchema, mode RestoreWorkMode) error {
	if mode != RestoreResumeAtLeastOnce && mode != RestoreAbandonAndResync {
		return fmt.Errorf("sqlite restore work mode is invalid")
	}
	return restoreImmediate(ctx, database, func(connection *sqlx.Conn) error {
		version, err := requireRestoreQueueStorage(ctx, connection)
		if err != nil {
			return err
		}
		outbox, delivery, err := restoreOutboxPresence(ctx, connection, system)
		if err != nil {
			return err
		}
		if outbox && !delivery {
			return fmt.Errorf("sqlite restore requires declared delivery storage for archived outbox")
		}
		if delivery {
			if err := restoreDeliveryOrphans(ctx, connection); err != nil {
				return err
			}
		}
		if version == queueprovider.StoragePriorityV1 {
			var statement string
			if mode == RestoreResumeAtLeastOnce {
				// Claim deliberately recognizes cancel intent and failure finalization
				// only on expired leased rows. Keep that state without its old token.
				finalizer := `("cancel_requested_at" IS NOT NULL OR "attempt_count">="max_attempts" OR ` + sqliteFinalizerMark + `)`
				statement = `UPDATE ` + sqliteQueueTable + ` SET "status"=CASE WHEN ` + finalizer + ` THEN 'leased' ELSE 'pending' END,"lease_token"=NULL,"lease_until"=CASE WHEN ` + finalizer + ` THEN ` + sqliteDatabaseMicros + ` ELSE NULL END,"available_at"=` + sqliteDatabaseMicros + `,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"updated_at"=` + sqliteDatabaseMicros + ` WHERE "status"='leased'`
			} else {
				statement = `UPDATE ` + sqliteQueueTable + ` SET "status"='canceled',"lease_token"=NULL,"lease_until"=NULL,"resource_name"=NULL,"resource_cost"=NULL,"resource_capacity"=NULL,"last_code"='` + restoreAbandonedCode + `',"finished_at"=` + sqliteDatabaseMicros + `,"updated_at"=` + sqliteDatabaseMicros + ` WHERE "status" IN ('pending','leased')`
			}
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if !delivery {
			return nil
		}
		if mode == RestoreResumeAtLeastOnce {
			_, err := connection.ExecContext(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='pending',"lease_token"=NULL,"lease_until"=NULL,"available_at"=`+sqliteDatabaseMicros+`,"updated_at"=`+sqliteDatabaseMicros+` WHERE "status"='leased'`)
			return err
		}
		// Missing legacy state must be explicitly retired, never inferred pending
		// by the publisher when the restored application starts.
		if _, err := connection.ExecContext(ctx, `INSERT INTO "main"."_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","retired_at","last_failure_code","updated_at") SELECT o."causation_id",'retired',MIN(o."recorded_at"),0,MIN(o."recorded_at"),`+sqliteDatabaseMicros+`,'`+restoreAbandonedCode+`',`+sqliteDatabaseMicros+` FROM "main"."_golem_outbox" o WHERE NOT EXISTS (SELECT 1 FROM "main"."_golem_outbox_delivery" d WHERE d."causation_id"=o."causation_id") GROUP BY o."causation_id"`); err != nil {
			return err
		}
		_, err = connection.ExecContext(ctx, `UPDATE "main"."_golem_outbox_delivery" SET "status"='retired',"lease_token"=NULL,"lease_until"=NULL,"blocked_at"=NULL,"retired_at"=`+sqliteDatabaseMicros+`,"last_failure_code"='`+restoreAbandonedCode+`',"updated_at"=`+sqliteDatabaseMicros+` WHERE "status" IN ('pending','leased','blocked')`)
		return err
	})
}

// VerifyRestoreQueueStorage checks the mutable stage again without repeating
// archived-work reconciliation or changing newly enqueued domain work.
func (*Provider) VerifyRestoreQueueStorage(ctx context.Context, database *sqlx.DB) (bool, error) {
	transaction, err := database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer transaction.Rollback()
	version, err := requireRestoreQueueStorage(ctx, transaction)
	return version == queueprovider.StoragePriorityV1, err
}

type restoreQueueQuery interface {
	catalogQuerier
	sqlx.QueryerContext
}

func requireRestoreQueueStorage(ctx context.Context, query restoreQueueQuery) (_ queueprovider.StorageVersion, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("%w: %w", ErrRestoreQueueStorage, resultErr)
		}
	}()
	inspection, err := inspectSQLiteQueueStorage(ctx, query)
	if err != nil {
		return "", err
	}
	if inspection.Version != queueprovider.StorageAbsent && inspection.Version != queueprovider.StoragePriorityV1 {
		return "", fmt.Errorf("sqlite restore requires explicit reviewed queue storage upgrade")
	}
	if inspection.Version == queueprovider.StoragePriorityV1 {
		if err := validateRestoreQueueRows(ctx, query); err != nil {
			return "", err
		}
	}
	return inspection.Version, nil
}

// RotateRestoreIncarnation changes exactly the already-valid identity. It
// cannot initialize a missing singleton or repair a malformed identity.
func (*Provider) RotateRestoreIncarnation(ctx context.Context, database *sqlx.DB, system physical.SystemSchema) (dataincarnation.Value, error) {
	var next dataincarnation.Value
	err := restoreImmediate(ctx, database, func(connection *sqlx.Conn) error {
		previous, err := readDataIncarnation(ctx, connection, system)
		if err != nil {
			return err
		}
		next, err = rotateDataIncarnation(ctx, connection, system, previous)
		return err
	})
	if err != nil {
		return dataincarnation.Value{}, err
	}
	return next, nil
}

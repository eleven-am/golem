package upsert

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	ncrucsqlite "github.com/ncruces/go-sqlite3"
	moderncsqlite "modernc.org/sqlite"
)

const (
	postgresUniqueViolation              = "23505"
	postgresSerializationFailure         = "40001"
	postgresDeadlockDetected             = "40P01"
	postgresTriggeredDataChangeViolation = "27000"
)

const (
	sqlitePrimaryKeyConstraintCode = 1555
	sqliteUniqueConstraintCode     = 2067
	sqliteBusyCode                 = 5
	sqliteLockedCode               = 6
	sqlitePrimaryResultCodeMask    = 0xff
)

// ProviderFault is the single classification of a driver error that mutation
// execution is allowed to act on. Only classes a mutation can respond to are
// named; every other provider failure stays opaque so driver text, constraint
// names, SQL, and values never reach a public boundary.
type ProviderFault uint8

const (
	ProviderFaultNone ProviderFault = iota
	ProviderFaultUniqueCollision
	ProviderFaultInterference
)

// ClassifyProviderFault is the one owner of provider-code interpretation for
// every mutation path. Callers translate the fault into their own vocabulary;
// no caller may decide for itself what a provider code means.
func ClassifyProviderFault(err error) ProviderFault {
	if providerUniqueCollision(err) {
		return ProviderFaultUniqueCollision
	}
	if providerInterference(err) {
		return ProviderFaultInterference
	}
	return ProviderFaultNone
}

func providerUniqueCollision(err error) bool {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && postgres.Code == postgresUniqueViolation {
		return true
	}
	var ncruces *ncrucsqlite.Error
	if errors.As(err, &ncruces) {
		switch ncruces.ExtendedCode() {
		case ncrucsqlite.CONSTRAINT_PRIMARYKEY, ncrucsqlite.CONSTRAINT_UNIQUE:
			return true
		}
	}
	var sqlite *moderncsqlite.Error
	if errors.As(err, &sqlite) {
		code := sqlite.Code()
		return code == sqlitePrimaryKeyConstraintCode || code == sqliteUniqueConstraintCode
	}
	return false
}

func providerInterference(err error) bool {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case postgresSerializationFailure, postgresDeadlockDetected, postgresTriggeredDataChangeViolation:
			return true
		}
	}
	var ncruces *ncrucsqlite.Error
	if errors.As(err, &ncruces) {
		switch ncruces.Code() {
		case ncrucsqlite.BUSY, ncrucsqlite.LOCKED:
			return true
		}
	}
	var sqlite *moderncsqlite.Error
	if errors.As(err, &sqlite) {
		primary := sqlite.Code() & sqlitePrimaryResultCodeMask
		return primary == sqliteBusyCode || primary == sqliteLockedCode
	}
	return false
}

func untrustedRetryCause(err error) bool {
	var untrusted interface{ UntrustedRetryCause() }
	return errors.As(err, &untrusted)
}

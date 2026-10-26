package rowlock

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/jmoiron/sqlx"
)

type refusingQueryer struct{ t *testing.T }

func (queryer refusingQueryer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	queryer.t.Fatal("a statement ran for a refused lock")
	return nil, nil
}

func (queryer refusingQueryer) QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error) {
	queryer.t.Fatal("a statement ran for a refused lock")
	return nil, nil
}

func (queryer refusingQueryer) QueryRowxContext(context.Context, string, ...any) *sqlx.Row {
	queryer.t.Fatal("a statement ran for a refused lock")
	return nil
}

func TestApplicationKeysSortBetweenGuardsAndRows(t *testing.T) {
	row := testKey(t, 0, KeyShare, policyir.UUIDValue([16]byte{}))
	highest, lowest := AppKey("\xff\xff\xff"), AppKey("\x01")
	guard := guardOrder([32]byte{0xff, 0xff, 0xff, 0xff})
	if !(guard < lowest.order && lowest.order < highest.order && highest.order < row.order) {
		t.Fatal("application lock keys do not sort after every guard and before every row")
	}
	if highest.mode != Update {
		t.Fatal("an application lock key is not recorded as an exclusive lock")
	}
	high, low := AppLockID(lowest)
	againHigh, againLow := AppLockID(AppKey("\x01"))
	if high != againHigh || low != againLow {
		t.Fatal("an application lock key does not map to one stable advisory lock")
	}
	sum := sha256.Sum256([]byte(lowest.order))
	if high != int32(binary.BigEndian.Uint32(sum[0:4])) || low != int32(binary.BigEndian.Uint32(sum[4:8])) {
		t.Fatal("an application lock key does not use 64 bits of its digest")
	}
	otherHigh, otherLow := AppLockID(highest)
	if otherHigh == high && otherLow == low {
		t.Fatal("two application lock keys map to one advisory lock")
	}
}

func TestPlanWaitsForApplicationKeysOnlyInLedgerOrder(t *testing.T) {
	first, second := AppKey("\x01a"), AppKey("\x01b")
	guard := Key{order: guardOrder([32]byte{1}), mode: Update}
	row := testKey(t, 1, Update, policyir.UUIDValue([16]byte{15: 1}))
	afterGuard := NewLedger()
	afterGuard.record([]Key{guard})
	pending := plan(afterGuard, []Key{second, first})
	if len(pending) != 2 || pending[0].key.order != first.order || pending[0].nowait || pending[1].nowait {
		t.Fatal("application keys after a guard are not sorted and taken waiting")
	}
	afterKey := NewLedger()
	afterKey.record([]Key{second})
	if pending := plan(afterKey, []Key{first}); len(pending) != 1 || !pending[0].nowait {
		t.Fatal("an application key that sorts before a held key waits")
	}
	if pending := plan(afterKey, []Key{second}); len(pending) != 0 {
		t.Fatal("a held application key was locked again")
	}
	if pending := plan(afterKey, []Key{guard}); len(pending) != 1 || !pending[0].nowait {
		t.Fatal("a guard taken after an application key waits")
	}
	afterRow := NewLedger()
	afterRow.record([]Key{row})
	if pending := plan(afterRow, []Key{first}); len(pending) != 1 || !pending[0].nowait {
		t.Fatal("an application key taken after a row lock waits")
	}
	rolled := NewLedger()
	mark := rolled.Mark()
	rolled.record([]Key{first})
	rolled.RollbackTo(mark)
	if pending := plan(rolled, []Key{first}); len(pending) != 1 || pending[0].nowait {
		t.Fatal("a savepoint rollback left the application key recorded")
	}
}

func TestLockAppRefusesKeysThatAreNotApplicationKeys(t *testing.T) {
	session := Session{Ledger: NewLedger(), Queryer: refusingQueryer{t: t}, Registry: &schema.Registry{}, Provider: policyir.ProviderPostgreSQL, MaxParameters: 1}
	row := testKey(t, 1, Update, policyir.UUIDValue([16]byte{15: 1}))
	if err := session.LockApp(t.Context(), []Key{AppKey("\x01a"), row}); err == nil {
		t.Fatal("a row key was taken as an application lock")
	}
	sqlite := session
	sqlite.Provider = policyir.ProviderSQLite
	if err := sqlite.LockApp(t.Context(), []Key{AppKey("\x01a")}); err == nil {
		t.Fatal("an application lock was taken outside PostgreSQL")
	}
}

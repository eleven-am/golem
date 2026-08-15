package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestSQLiteAllocatedProbeFailureClosesConnection(t *testing.T) {
	var connects, closes atomic.Int64
	raw := sql.OpenDB(sqliteCleanupConnector{connects: &connects, closes: &closes})
	database := sqlx.NewDb(raw, "p8-cleanup")

	opened, _, err := New().verifyOpenedDatabase(context.Background(), database)
	if opened != nil || err == nil {
		t.Fatalf("failed probe result database=%p error=%v", opened, err)
	}
	if openedCount, closedCount := connects.Load(), closes.Load(); openedCount == 0 || closedCount != openedCount {
		t.Fatalf("allocated driver connections opened=%d closed=%d", openedCount, closedCount)
	}
	if stats := raw.Stats(); stats.OpenConnections != 0 {
		t.Fatalf("failed probe retained open connections: %+v", stats)
	}
}

type sqliteCleanupConnector struct {
	connects *atomic.Int64
	closes   *atomic.Int64
}

func (connector sqliteCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	connector.connects.Add(1)
	return &sqliteCleanupConnection{closes: connector.closes}, nil
}
func (connector sqliteCleanupConnector) Driver() driver.Driver {
	return sqliteCleanupDriver{connector: connector}
}

type sqliteCleanupDriver struct{ connector sqliteCleanupConnector }

func (value sqliteCleanupDriver) Open(string) (driver.Conn, error) {
	return value.connector.Connect(context.Background())
}

type sqliteCleanupConnection struct{ closes *atomic.Int64 }

func (*sqliteCleanupConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("p8 sqlite probe failure")
}
func (connection *sqliteCleanupConnection) Close() error {
	connection.closes.Add(1)
	return nil
}
func (*sqliteCleanupConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions unsupported")
}
func (*sqliteCleanupConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("p8 sqlite probe failure")
}

var _ driver.Connector = sqliteCleanupConnector{}
var _ driver.QueryerContext = (*sqliteCleanupConnection)(nil)

package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestP8PostgreSQLAllocatedProbeFailureClosesConnection(t *testing.T) {
	var connects, closes atomic.Int64
	raw := sql.OpenDB(postgreSQLCleanupConnector{connects: &connects, closes: &closes})
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

type postgreSQLCleanupConnector struct {
	connects *atomic.Int64
	closes   *atomic.Int64
}

func (connector postgreSQLCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	connector.connects.Add(1)
	return &postgreSQLCleanupConnection{closes: connector.closes}, nil
}
func (connector postgreSQLCleanupConnector) Driver() driver.Driver {
	return postgreSQLCleanupDriver{connector: connector}
}

type postgreSQLCleanupDriver struct{ connector postgreSQLCleanupConnector }

func (value postgreSQLCleanupDriver) Open(string) (driver.Conn, error) {
	return value.connector.Connect(context.Background())
}

type postgreSQLCleanupConnection struct{ closes *atomic.Int64 }

func (*postgreSQLCleanupConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("p8 postgresql probe failure")
}
func (connection *postgreSQLCleanupConnection) Close() error {
	connection.closes.Add(1)
	return nil
}
func (*postgreSQLCleanupConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions unsupported")
}
func (*postgreSQLCleanupConnection) Ping(context.Context) error { return nil }
func (*postgreSQLCleanupConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("p8 postgresql probe failure")
}

var _ driver.Connector = postgreSQLCleanupConnector{}
var _ driver.Pinger = (*postgreSQLCleanupConnection)(nil)
var _ driver.QueryerContext = (*postgreSQLCleanupConnection)(nil)

package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
)

var p3CountingDriverIdentity atomic.Uint64

type p3CountingSQLiteDriver struct {
	inner   driver.Driver
	queries *atomic.Int64
}

func (value *p3CountingSQLiteDriver) Open(name string) (driver.Conn, error) {
	connection, err := value.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &p3CountingSQLiteConnection{Conn: connection, queries: value.queries}, nil
}

type p3CountingSQLiteConnection struct {
	driver.Conn
	queries *atomic.Int64
}

func (value *p3CountingSQLiteConnection) QueryContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := value.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	value.queries.Add(1)
	return queryer.QueryContext(ctx, query, arguments)
}

func (value *p3CountingSQLiteConnection) ExecContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	executor, ok := value.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return executor.ExecContext(ctx, query, arguments)
}

func openP3CountingSQLite(dataSourceName string) (*sqlx.DB, *atomic.Int64, error) {
	queries := &atomic.Int64{}
	name := fmt.Sprintf("golem_p3_counting_sqlite_%d", p3CountingDriverIdentity.Add(1))
	registered, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, err
	}
	inner := registered.Driver()
	_ = registered.Close()
	sql.Register(name, &p3CountingSQLiteDriver{inner: inner, queries: queries})
	configured := dataSourceName + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	database, err := sqlx.Open(name, configured)
	if err != nil {
		return nil, nil, err
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	return database, queries, nil
}

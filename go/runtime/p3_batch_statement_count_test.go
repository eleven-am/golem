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

func (value *p3CountingSQLiteConnection) Prepare(query string) (driver.Stmt, error) {
	statement, err := value.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &p3CountingSQLiteStatement{Stmt: statement, queries: value.queries}, nil
}

func (value *p3CountingSQLiteConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if contextual, ok := value.Conn.(driver.ConnPrepareContext); ok {
		statement, err := contextual.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &p3CountingSQLiteStatement{Stmt: statement, queries: value.queries}, nil
	}
	return value.Prepare(query)
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

type p3CountingSQLiteStatement struct {
	driver.Stmt
	queries *atomic.Int64
}

func (value *p3CountingSQLiteStatement) CheckNamedValue(argument *driver.NamedValue) error {
	if checker, ok := value.Stmt.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(argument)
	}
	return driver.ErrSkip
}

func (value *p3CountingSQLiteStatement) Query(arguments []driver.Value) (driver.Rows, error) {
	value.queries.Add(1)
	return value.Stmt.Query(arguments)
}

func (value *p3CountingSQLiteStatement) QueryContext(ctx context.Context, arguments []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := value.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	value.queries.Add(1)
	return queryer.QueryContext(ctx, arguments)
}

func openP3CountingSQLite(dataSourceName string) (*sqlx.DB, *atomic.Int64, error) {
	queries := &atomic.Int64{}
	name := fmt.Sprintf("golem_p3_counting_sqlite_%d", p3CountingDriverIdentity.Add(1))
	registered, err := sql.Open("sqlite3", ":memory:")
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

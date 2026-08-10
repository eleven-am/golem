package runtime

import (
	"context"
	"database/sql/driver"
	"sync"
)

type p5SQLTrace struct {
	lock       sync.Mutex
	statements []string
}

func (trace *p5SQLTrace) record(statement string) {
	trace.lock.Lock()
	trace.statements = append(trace.statements, statement)
	trace.lock.Unlock()
}

func (trace *p5SQLTrace) reset() {
	trace.lock.Lock()
	trace.statements = nil
	trace.lock.Unlock()
}

func (trace *p5SQLTrace) snapshot() []string {
	trace.lock.Lock()
	defer trace.lock.Unlock()
	return append([]string(nil), trace.statements...)
}

type p5TraceConnector struct {
	base  driver.Connector
	trace *p5SQLTrace
}

func (connector p5TraceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &p5TraceConn{Conn: connection, trace: connector.trace}, nil
}

func (connector p5TraceConnector) Driver() driver.Driver { return connector.base.Driver() }

type p5DriverConnector struct {
	driver driver.Driver
	dsn    string
}

func (connector p5DriverConnector) Connect(context.Context) (driver.Conn, error) {
	return connector.driver.Open(connector.dsn)
}

func (connector p5DriverConnector) Driver() driver.Driver { return connector.driver }

type p5TraceConn struct {
	driver.Conn
	trace *p5SQLTrace
}

func (connection *p5TraceConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := connection.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &p5TraceStmt{Stmt: statement, query: query, trace: connection.trace}, nil
}

func (connection *p5TraceConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if contextual, ok := connection.Conn.(driver.ConnPrepareContext); ok {
		statement, err := contextual.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &p5TraceStmt{Stmt: statement, query: query, trace: connection.trace}, nil
	}
	return connection.Prepare(query)
}

func (connection *p5TraceConn) ExecContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	executor, ok := connection.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	connection.trace.record(query)
	return executor.ExecContext(ctx, query, arguments)
}

func (connection *p5TraceConn) QueryContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := connection.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	connection.trace.record(query)
	return queryer.QueryContext(ctx, query, arguments)
}

func (connection *p5TraceConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := connection.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, options)
	}
	return connection.Conn.Begin()
}

func (connection *p5TraceConn) Ping(ctx context.Context) error {
	if pinger, ok := connection.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (connection *p5TraceConn) ResetSession(ctx context.Context) error {
	if resetter, ok := connection.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (connection *p5TraceConn) IsValid() bool {
	if validator, ok := connection.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (connection *p5TraceConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := connection.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

type p5TraceStmt struct {
	driver.Stmt
	query string
	trace *p5SQLTrace
}

func (statement *p5TraceStmt) Exec(arguments []driver.Value) (driver.Result, error) {
	statement.trace.record(statement.query)
	return statement.Stmt.Exec(arguments)
}

func (statement *p5TraceStmt) Query(arguments []driver.Value) (driver.Rows, error) {
	statement.trace.record(statement.query)
	return statement.Stmt.Query(arguments)
}

func (statement *p5TraceStmt) ExecContext(ctx context.Context, arguments []driver.NamedValue) (driver.Result, error) {
	if contextual, ok := statement.Stmt.(driver.StmtExecContext); ok {
		statement.trace.record(statement.query)
		return contextual.ExecContext(ctx, arguments)
	}
	return nil, driver.ErrSkip
}

func (statement *p5TraceStmt) QueryContext(ctx context.Context, arguments []driver.NamedValue) (driver.Rows, error) {
	if contextual, ok := statement.Stmt.(driver.StmtQueryContext); ok {
		statement.trace.record(statement.query)
		return contextual.QueryContext(ctx, arguments)
	}
	return nil, driver.ErrSkip
}

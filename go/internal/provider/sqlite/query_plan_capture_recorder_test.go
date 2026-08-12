package sqlite_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

func TestQueryPlanSQLiteNeverExecutesDataQueryAndClosesRows(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "post_alias" }, fixture.Post, golem.RelationID{}, nil, queryplancapture.AliasPhysicalAccess)
	if err != nil {
		t.Fatal(err)
	}
	recorder, database := newSQLitePlanRecorder("SCAN post_alias", nil)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	statement := "SELECT private_sql FROM private_table WHERE private_column = ?"
	plan, err := sqliteprovider.CaptureQueryPlan(context.Background(), connection, statement, []any{"private-value"}, fixture.Registry, queryplancapture.NewAliasMap(fact))
	if err != nil {
		t.Fatal(err)
	}
	if root := plan.Root(); root.AccessKind() != queryplancapture.AccessFullScan {
		t.Fatalf("access=%v, want full scan", root.AccessKind())
	}
	if recorder.query != "EXPLAIN QUERY PLAN "+statement || recorder.queries != 1 || recorder.executes != 0 {
		t.Fatalf("query=%q queries=%d executes=%d", recorder.query, recorder.queries, recorder.executes)
	}
	if !recorder.rowsClosed {
		t.Fatal("EXPLAIN rows remained open at observation boundary")
	}
}

func TestQueryPlanSQLiteDerivedAliasCannotBecomePhysicalAccess(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "derived_alias" }, fixture.Post, golem.RelationID{}, []golem.FieldID{fixture.PostTitle}, queryplancapture.AliasAggregate)
	if err != nil {
		t.Fatal(err)
	}
	_, database := newSQLitePlanRecorder("SCAN derived_alias", nil)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	plan, err := sqliteprovider.CaptureQueryPlan(context.Background(), connection, "SELECT derived", nil, fixture.Registry, queryplancapture.NewAliasMap(fact))
	if err != nil {
		t.Fatal(err)
	}
	if root := plan.Root(); root.Kind() != queryplancapture.NodeAggregate || root.AccessKind() != queryplancapture.AccessNone {
		t.Fatalf("derived alias became provider access: kind=%v access=%v", root.Kind(), root.AccessKind())
	}
}

func TestQueryPlanSQLiteRejectsMultiStatementLookingInputBeforeProvider(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	recorder, database := newSQLitePlanRecorder("SCAN private", nil)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_, err = sqliteprovider.CaptureQueryPlan(context.Background(), connection, "SELECT 1; SELECT private", nil, fixture.Registry, queryplancapture.AliasMap{})
	if code, ok := queryplancapture.CodeOf(err); !ok || code != queryplancapture.ErrorInvalid {
		t.Fatalf("code=(%v,%v), want invalid", code, ok)
	}
	if recorder.queries != 0 || recorder.executes != 0 {
		t.Fatalf("unsafe input reached provider: queries=%d executes=%d", recorder.queries, recorder.executes)
	}
}

func TestQueryPlanSQLiteOversizeAndProviderFailureAreSanitized(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	tests := []struct {
		name     string
		detail   string
		queryErr error
		wantCode queryplancapture.ErrorCode
	}{
		{name: "oversize detail", detail: strings.Repeat("private", queryplancapture.MaxRawBytes), wantCode: queryplancapture.ErrorTooComplex},
		{name: "provider error", queryErr: errors.New("private dsn and provider diagnostic"), wantCode: queryplancapture.ErrorUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, database := newSQLitePlanRecorder(test.detail, test.queryErr)
			connection, err := database.Connx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// A leak mutant leaves database/sql waiting forever for its
				// driver row. Do not hide that named failure in cleanup timeout.
				if test.queryErr == nil && !recorder.rowsClosed {
					return
				}
				_ = connection.Close()
				_ = database.Close()
			})
			_, err = sqliteprovider.CaptureQueryPlan(context.Background(), connection, "SELECT never_executed", nil, fixture.Registry, queryplancapture.AliasMap{})
			code, ok := queryplancapture.CodeOf(err)
			if !ok || code != test.wantCode {
				t.Fatalf("code=(%v,%v), want %v; err=%v", code, ok, test.wantCode, err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "dsn") {
				t.Fatalf("raw provider input escaped refusal: %q", err)
			}
			if test.queryErr == nil && !recorder.rowsClosed {
				t.Fatal("rows remained open on oversized refusal")
			}
		})
	}
}

type sqlitePlanRecorder struct {
	detail     string
	queryErr   error
	query      string
	queries    int
	executes   int
	rowsClosed bool
}

type sqlitePlanConnector struct{ recorder *sqlitePlanRecorder }

func (connector sqlitePlanConnector) Connect(context.Context) (driver.Conn, error) {
	return &sqlitePlanRecorderConn{recorder: connector.recorder}, nil
}
func (connector sqlitePlanConnector) Driver() driver.Driver { return sqlitePlanRecorderDriver{} }

type sqlitePlanRecorderDriver struct{}

func (sqlitePlanRecorderDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("direct open unavailable")
}

type sqlitePlanRecorderConn struct{ recorder *sqlitePlanRecorder }

func (*sqlitePlanRecorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unavailable")
}
func (*sqlitePlanRecorderConn) Close() error { return nil }
func (*sqlitePlanRecorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transaction unavailable")
}
func (connection *sqlitePlanRecorderConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.recorder.query = query
	connection.recorder.queries++
	if connection.recorder.queryErr != nil {
		return nil, connection.recorder.queryErr
	}
	return &sqlitePlanRecorderRows{recorder: connection.recorder}, nil
}
func (connection *sqlitePlanRecorderConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.recorder.executes++
	return nil, errors.New("unexpected data execution")
}

type sqlitePlanRecorderRows struct {
	recorder *sqlitePlanRecorder
	done     bool
}

func (*sqlitePlanRecorderRows) Columns() []string {
	return []string{"id", "parent", "notused", "detail"}
}
func (rows *sqlitePlanRecorderRows) Close() error {
	rows.recorder.rowsClosed = true
	return nil
}
func (rows *sqlitePlanRecorderRows) Next(values []driver.Value) error {
	if rows.done {
		return io.EOF
	}
	rows.done = true
	values[0], values[1], values[2], values[3] = int64(2), int64(0), int64(0), rows.recorder.detail
	return nil
}

func newSQLitePlanRecorder(detail string, queryErr error) (*sqlitePlanRecorder, *sqlx.DB) {
	recorder := &sqlitePlanRecorder{detail: detail, queryErr: queryErr}
	standard := sql.OpenDB(sqlitePlanConnector{recorder: recorder})
	return recorder, sqlx.NewDb(standard, "sqlite3")
}

var _ driver.QueryerContext = (*sqlitePlanRecorderConn)(nil)
var _ driver.ExecerContext = (*sqlitePlanRecorderConn)(nil)

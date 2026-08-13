package postgresql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

const expectedPostgresExplainPrefix = "EXPLAIN (FORMAT JSON, ANALYZE FALSE, VERBOSE FALSE, COSTS FALSE, SETTINGS FALSE, BUFFERS FALSE, WAL FALSE, SUMMARY FALSE) "

func TestQueryPlanPostgreSQLMapsScanJoinSortAndIndexWithoutRawJSON(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	userTable := postgresPhysicalTable(t, fixture.PostgreSQL, fixture.User)
	if userTable.PrimaryKey == nil {
		t.Fatal("user primary key absent")
	}
	document := []any{map[string]any{"Plan": map[string]any{
		"Node Type": "Sort", "Total Cost": 999999, "Sort Key": []string{"private_column"},
		"Plans": []any{map[string]any{
			"Node Type": "Nested Loop", "Join Filter": "private_predicate",
			"Plans": []any{
				map[string]any{"Node Type": "Seq Scan", "Relation Name": "private_table", "Alias": "post_alias", "Plan Rows": 12345},
				map[string]any{"Node Type": "Index Scan", "Relation Name": "another_private_table", "Alias": "user_alias", "Index Name": string(userTable.PrimaryKey.Name), "Index Cond": "private_value"},
			},
		}},
	}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	recorder, database := newPlanRecorder(raw)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	postFact := mustAliasFact(t, "post_alias", fixture.Post)
	userFact := mustAliasFact(t, "user_alias", fixture.User)
	aliases := queryplancapture.NewAliasMap(postFact, userFact)
	statement := "SELECT private_sql FROM private_table WHERE private_value = $1"

	plan, err := postgresprovider.CaptureQueryPlan(context.Background(), connection, statement, []any{"private-bind-value"}, fixture.Registry, aliases)
	if err != nil {
		t.Fatal(err)
	}
	root := plan.Root()
	if root.Kind() != queryplancapture.NodeSort || len(root.Children()) != 1 || root.Children()[0].Kind() != queryplancapture.NodeJoin {
		t.Fatalf("closed root shape mismatch: %#v", plan.RootInput())
	}
	joinChildren := root.Children()[0].Children()
	if len(joinChildren) != 2 || joinChildren[0].AccessKind() != queryplancapture.AccessFullScan || joinChildren[1].AccessKind() != queryplancapture.AccessPrimaryKey {
		t.Fatalf("closed access shape mismatch: %#v", plan.RootInput())
	}
	if model, ok := joinChildren[0].ModelID(); !ok || model != fixture.Post {
		t.Fatalf("scan model=(%v,%v), want Post", model, ok)
	}
	if model, ok := joinChildren[1].ModelID(); !ok || model != fixture.User {
		t.Fatalf("index model=(%v,%v), want User", model, ok)
	}
	if _, ok := joinChildren[1].IndexID(); !ok {
		t.Fatal("stable primary-key identity absent")
	}
	wantSQL := expectedPostgresExplainPrefix + statement
	if recorder.query != wantSQL || recorder.queries != 1 || recorder.executes != 0 {
		t.Fatalf("provider boundary query=%q queries=%d executes=%d", recorder.query, recorder.queries, recorder.executes)
	}
	if strings.Contains(recorder.query, "ANALYZE TRUE") || !strings.Contains(recorder.query, "ANALYZE FALSE") {
		t.Fatalf("unsafe explain options: %q", recorder.query)
	}
	if !recorder.rowsClosed {
		t.Fatal("EXPLAIN rows remained open at observation boundary")
	}
}

func TestQueryPlanPostgreSQLDerivedAliasCannotBecomePhysicalAccess(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	raw := mustJSON(t, []any{map[string]any{"Plan": map[string]any{"Node Type": "Seq Scan", "Relation Name": "private_derived_relation", "Alias": "derived_alias"}}})
	_, database := newPlanRecorder(raw)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "derived_alias" }, fixture.Post, golem.RelationID{}, []golem.FieldID{fixture.PostTitle}, queryplancapture.AliasAggregate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := postgresprovider.CaptureQueryPlan(context.Background(), connection, "SELECT derived", nil, fixture.Registry, queryplancapture.NewAliasMap(fact))
	if err != nil {
		t.Fatal(err)
	}
	if root := plan.Root(); root.Kind() != queryplancapture.NodeAggregate || root.AccessKind() != queryplancapture.AccessNone {
		t.Fatalf("derived alias became provider access: kind=%v access=%v", root.Kind(), root.AccessKind())
	}
}

func TestQueryPlanPostgreSQLRejectsMultiStatementLookingInputBeforeProvider(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	recorder, database := newPlanRecorder(mustJSON(t, []any{map[string]any{"Plan": map[string]any{"Node Type": "Result"}}}))
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_, err = postgresprovider.CaptureQueryPlan(context.Background(), connection, "SELECT 1; SELECT private", nil, fixture.Registry, queryplancapture.AliasMap{})
	if code, ok := queryplancapture.CodeOf(err); !ok || code != queryplancapture.ErrorInvalid {
		t.Fatalf("code=(%v,%v), want invalid", code, ok)
	}
	if recorder.queries != 0 || recorder.executes != 0 {
		t.Fatalf("unsafe input reached provider: queries=%d executes=%d", recorder.queries, recorder.executes)
	}
}

func TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution(t *testing.T) {
	profiles := []struct {
		name        string
		environment string
		linguistic  bool
	}{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", linguistic: true},
	}
	required := os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1"
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.environment))
			if dsn == "" {
				if required {
					t.Fatalf("required PostgreSQL query-plan profile %s is not configured", profile.environment)
				}
				t.Skipf("%s is not configured", profile.environment)
			}
			fixture := schematest.NewIndexed(t)
			table := postgresPhysicalTable(t, fixture.PostgreSQL, fixture.Post)
			if table.PrimaryKey == nil {
				t.Fatal("post primary key absent")
			}
			idColumn := postgresPhysicalColumn(t, table, fixture.PostID)
			titleColumn := postgresPhysicalColumn(t, table, fixture.PostTitle)
			database, _, err := postgresprovider.New().Open(context.Background(), dsn)
			if err != nil {
				t.Fatalf("PostgreSQL query-plan profile %s is unavailable", profile.name)
			}
			databaseClosed := false
			t.Cleanup(func() {
				if !databaseClosed {
					_ = database.Close()
				}
			})
			connection, err := database.Connx(context.Background())
			if err != nil {
				t.Fatalf("acquire PostgreSQL query-plan profile %s connection", profile.name)
			}
			connectionClosed := false
			t.Cleanup(func() {
				if !connectionClosed {
					_ = connection.Close()
				}
			})

			var collation, characterType string
			if err := connection.QueryRowxContext(context.Background(), `SELECT datcollate, datctype FROM pg_catalog.pg_database WHERE datname = current_database()`).Scan(&collation, &characterType); err != nil {
				t.Fatalf("inspect PostgreSQL query-plan profile %s", profile.name)
			}
			if !profile.linguistic && (collation != "C" || characterType != "C") {
				t.Fatalf("C query-plan profile has collation=%q ctype=%q", collation, characterType)
			}
			if profile.linguistic && (collation == "C" || characterType == "C") {
				t.Fatalf("linguistic query-plan profile requires non-C collation and ctype; got collation=%q ctype=%q", collation, characterType)
			}

			const bindCanary = "00000000-0000-0000-0000-000000000001"
			if _, err := connection.ExecContext(context.Background(), `CREATE TEMP SEQUENCE golem_query_plan_execution_probe MINVALUE 0 START WITH 0`); err != nil {
				t.Fatal("create query-plan execution probe")
			}
			if _, err := connection.ExecContext(context.Background(), fmt.Sprintf("CREATE TEMP TABLE %q (%q uuid NOT NULL, %q text NOT NULL, CONSTRAINT %q PRIMARY KEY (%q))", table.Name, idColumn.Name, titleColumn.Name, table.PrimaryKey.Name, idColumn.Name)); err != nil {
				t.Fatal("create query-plan table")
			}
			if _, err := connection.ExecContext(context.Background(), fmt.Sprintf("INSERT INTO %q (%q, %q) VALUES ($1, $2)", table.Name, idColumn.Name, titleColumn.Name), bindCanary, "private-title-canary"); err != nil {
				t.Fatal("seed query-plan execution probe")
			}
			if _, err := connection.ExecContext(context.Background(), `SET enable_seqscan = off`); err != nil {
				t.Fatal("stabilize query-plan access selection")
			}
			fact := mustAliasFact(t, "post_alias", fixture.Post)
			statement := fmt.Sprintf("SELECT %q, nextval('golem_query_plan_execution_probe') FROM %q AS post_alias WHERE %q = $1", titleColumn.Name, table.Name, idColumn.Name)
			plan, err := postgresprovider.CaptureQueryPlan(context.Background(), connection, statement, []any{bindCanary}, fixture.Registry, queryplancapture.NewAliasMap(fact))
			if err != nil {
				t.Fatal("capture live PostgreSQL query plan")
			}
			root := plan.Root()
			if root.Kind() != queryplancapture.NodeAccess || root.AccessKind() != queryplancapture.AccessPrimaryKey {
				t.Fatalf("live closed access=(kind=%v access=%v), want primary-key access", root.Kind(), root.AccessKind())
			}
			if model, ok := root.ModelID(); !ok || model != fixture.Post {
				t.Fatalf("live access model=(%v,%v), want Post", model, ok)
			}
			if _, ok := root.IndexID(); !ok {
				t.Fatal("live primary-key identity absent")
			}
			closed := fmt.Sprintf("%#v", plan.RootInput())
			for _, private := range []string{bindCanary, "private-title-canary", "post_alias", string(table.Name), string(titleColumn.Name)} {
				if strings.Contains(closed, private) {
					t.Fatalf("live closed plan disclosed private input %q", private)
				}
			}
			var lastValue int64
			var wasCalled bool
			if err := connection.QueryRowxContext(context.Background(), `SELECT last_value, is_called FROM golem_query_plan_execution_probe`).Scan(&lastValue, &wasCalled); err != nil {
				t.Fatal("inspect query-plan execution probe")
			}
			if lastValue != 0 || wasCalled {
				t.Fatalf("EXPLAIN executed the planned statement: sequence=(%d,%t)", lastValue, wasCalled)
			}
			if err := connection.Close(); err != nil {
				t.Fatal("close query-plan connection")
			}
			connectionClosed = true
			if inUse := database.Stats().InUse; inUse != 0 {
				t.Fatalf("query-plan connection remained checked out: in-use=%d", inUse)
			}
			if err := database.Close(); err != nil {
				t.Fatal("close query-plan database")
			}
			databaseClosed = true
		})
	}
}

func TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	tests := []struct {
		name     string
		raw      []byte
		queryErr error
		wantCode queryplancapture.ErrorCode
		wantPlan queryplancapture.NodeKind
	}{
		{name: "unknown alias", raw: mustJSON(t, []any{map[string]any{"Plan": map[string]any{"Node Type": "Seq Scan", "Relation Name": "known_but_must_not_override_alias", "Alias": "private_unknown_alias"}}}), wantPlan: queryplancapture.NodeUnknown},
		{name: "oversize", raw: []byte(`[{"Plan":{"Node Type":"Seq Scan","Alias":"` + strings.Repeat("private", queryplancapture.MaxRawBytes) + `"}}]`), wantCode: queryplancapture.ErrorTooComplex},
		{name: "oversize precedes token decode", raw: []byte(`private-invalid-json` + strings.Repeat("x", queryplancapture.MaxRawBytes)), wantCode: queryplancapture.ErrorTooComplex},
		{name: "invalid json", raw: []byte(`private provider error with dsn=secret`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate node type", raw: []byte(`[{"Plan":{"Node Type":"Result","Node Type":"Seq Scan"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate outer plan", raw: []byte(`[{"Plan":{"Node Type":"Result"},"Plan":{"Node Type":"Seq Scan"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate alias", raw: []byte(`[{"Plan":{"Node Type":"Seq Scan","Alias":"first","Alias":"second"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "escaped duplicate alias", raw: []byte(`[{"Plan":{"Node Type":"Seq Scan","Alias":"first","\u0041lias":"second"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate index name", raw: []byte(`[{"Plan":{"Node Type":"Index Scan","Index Name":"first","Index Name":"second"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate plans", raw: []byte(`[{"Plan":{"Node Type":"Result","Plans":[],"Plans":[]}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "duplicate unknown key", raw: []byte(`[{"Plan":{"Node Type":"Result","Unknown":"first","Unknown":"second"}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "nested duplicate node type", raw: []byte(`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Result","Node Type":"Seq Scan"}]}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "nested duplicate unknown key", raw: []byte(`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Result","NestedUnknown":1,"NestedUnknown":2}]}}]`), wantCode: queryplancapture.ErrorUnavailable},
		{name: "provider error", queryErr: errors.New("private dsn and provider diagnostic"), wantCode: queryplancapture.ErrorUnavailable},
		{name: "depth", raw: deepPostgresPlan(t, queryplancapture.MaxDepth+1), wantCode: queryplancapture.ErrorTooComplex},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, database := newPlanRecorder(test.raw)
			recorder.queryErr = test.queryErr
			connection, err := database.Connx(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// A leak mutant leaves database/sql waiting forever for its
				// driver row. Preserve the named assertion failure instead.
				if test.queryErr == nil && !recorder.rowsClosed {
					return
				}
				_ = connection.Close()
				_ = database.Close()
			})
			plan, err := postgresprovider.CaptureQueryPlan(context.Background(), connection, "SELECT never_executed", nil, fixture.Registry, queryplancapture.AliasMap{})
			if test.wantCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if plan.Root().Kind() != test.wantPlan {
					t.Fatalf("unknown provider identity guessed: %v", plan.Root().Kind())
				}
			} else {
				code, ok := queryplancapture.CodeOf(err)
				if !ok || code != test.wantCode {
					t.Fatalf("code=(%v,%v), want %v; err=%v", code, ok, test.wantCode, err)
				}
				if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "dsn") {
					t.Fatalf("raw provider input escaped refusal: %q", err)
				}
			}
			if test.queryErr == nil && !recorder.rowsClosed {
				t.Fatal("rows remained open on refusal")
			}
		})
	}
}

func mustAliasFact(t *testing.T, alias string, model golem.ModelID) queryplancapture.AliasFact {
	t.Helper()
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == alias }, model, golem.RelationID{}, nil, queryplancapture.AliasPhysicalAccess)
	if err != nil {
		t.Fatal(err)
	}
	return fact
}

func postgresPhysicalTable(t *testing.T, value physical.PhysicalSchema, model golem.ModelID) physical.PhysicalTable {
	t.Helper()
	for _, table := range value.Tables {
		if string(table.ID) == hex.EncodeToString(model[:]) {
			return table
		}
	}
	t.Fatalf("physical table for model %x not found", model)
	return physical.PhysicalTable{}
}

func postgresPhysicalColumn(t *testing.T, table physical.PhysicalTable, field golem.FieldID) physical.PhysicalColumn {
	t.Helper()
	for _, column := range table.Columns {
		if string(column.ID) == hex.EncodeToString(field[:]) {
			return column
		}
	}
	t.Fatalf("physical column for field %x not found", field)
	return physical.PhysicalColumn{}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func deepPostgresPlan(t *testing.T, depth int) []byte {
	t.Helper()
	var node map[string]any
	for index := 0; index < depth; index++ {
		current := map[string]any{"Node Type": "Limit"}
		if node != nil {
			current["Plans"] = []any{node}
		}
		node = current
	}
	return mustJSON(t, []any{map[string]any{"Plan": node}})
}

type planRecorder struct {
	raw        []byte
	queryErr   error
	query      string
	queries    int
	executes   int
	rowsClosed bool
}

type planConnector struct{ recorder *planRecorder }

func (connector planConnector) Connect(context.Context) (driver.Conn, error) {
	return &planRecorderConn{recorder: connector.recorder}, nil
}
func (connector planConnector) Driver() driver.Driver { return planRecorderDriver{} }

type planRecorderDriver struct{}

func (planRecorderDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("direct open unavailable")
}

type planRecorderConn struct{ recorder *planRecorder }

func (*planRecorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unavailable")
}
func (*planRecorderConn) Close() error { return nil }
func (*planRecorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transaction unavailable")
}
func (connection *planRecorderConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.recorder.query = query
	connection.recorder.queries++
	if connection.recorder.queryErr != nil {
		return nil, connection.recorder.queryErr
	}
	return &planRecorderRows{recorder: connection.recorder, raw: append([]byte(nil), connection.recorder.raw...)}, nil
}
func (connection *planRecorderConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.recorder.executes++
	return nil, errors.New("unexpected data execution")
}

type planRecorderRows struct {
	recorder *planRecorder
	raw      []byte
	done     bool
}

func (*planRecorderRows) Columns() []string { return []string{"QUERY PLAN"} }
func (rows *planRecorderRows) Close() error {
	rows.recorder.rowsClosed = true
	return nil
}
func (rows *planRecorderRows) Next(values []driver.Value) error {
	if rows.done {
		return io.EOF
	}
	rows.done = true
	values[0] = append([]byte(nil), rows.raw...)
	return nil
}

func newPlanRecorder(raw []byte) (*planRecorder, *sqlx.DB) {
	recorder := &planRecorder{raw: append([]byte(nil), raw...)}
	standard := sql.OpenDB(planConnector{recorder: recorder})
	return recorder, sqlx.NewDb(standard, "postgres")
}

var _ driver.QueryerContext = (*planRecorderConn)(nil)
var _ driver.ExecerContext = (*planRecorderConn)(nil)

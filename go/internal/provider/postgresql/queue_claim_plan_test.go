package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type postgresqlPlanNode struct {
	NodeType     string               `json:"Node Type"`
	RelationName string               `json:"Relation Name"`
	Alias        string               `json:"Alias"`
	ActualRows   float64              `json:"Actual Rows"`
	ActualLoops  float64              `json:"Actual Loops"`
	Plans        []postgresqlPlanNode `json:"Plans"`
}

func (node postgresqlPlanNode) walk(visit func(postgresqlPlanNode)) {
	visit(node)
	for _, child := range node.Plans {
		child.walk(visit)
	}
}

func analyzedPostgreSQLPlan(tb testing.TB, database *sqlx.DB, statement string, arguments []any) (postgresqlPlanNode, string) {
	tb.Helper()
	transaction, err := database.Beginx()
	if err != nil {
		tb.Fatal(err)
	}
	defer transaction.Rollback()
	var raw string
	if err := transaction.Get(&raw, `EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, FORMAT JSON) `+statement, arguments...); err != nil {
		tb.Fatalf("explain %q: %v", statement, err)
	}
	var plans []struct {
		Plan postgresqlPlanNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &plans); err != nil || len(plans) != 1 {
		tb.Fatalf("plan %s: %v", raw, err)
	}
	return plans[0].Plan, raw
}

func openClaimNamespace(tb testing.TB, namespace string) (*queueStore, *sqlx.DB) {
	tb.Helper()
	dsn := testenv.DisposablePostgreSQL(tb, testenv.PostgreSQLDSNVariable)
	database, err := sqlx.Open("pgx", dsn)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE SCHEMA "` + namespace + `"`); err != nil {
		tb.Fatal(err)
	}
	store, err := New().QueueStoreAt(database, physical.PhysicalName(namespace), physical.QueueUnmanagedObjects()[:4])
	if err != nil {
		tb.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		tb.Fatal(err)
	}
	return store.(*queueStore), database
}

func seedPostgreSQLClaimBacklog(tb testing.TB, database *sqlx.DB, table string, jobs int) {
	tb.Helper()
	statement := `INSERT INTO ` + table + ` ("id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","enqueued_at","finished_at","updated_at")
SELECT lpad(i::text,36,'0'),'type'||(i%3),'\x00'::bytea,
	CASE WHEN i%4=0 THEN 'succeeded' WHEN i%50=1 THEN 'leased' ELSE 'pending' END,
	CASE WHEN i%4<>0 AND i%50=1 THEN 1 ELSE 0 END,5,
	clock_timestamp()-interval '1 day'+i*interval '1 microsecond',
	CASE WHEN i%4<>0 AND i%50=1 THEN 'token-'||i END,
	CASE WHEN i%4<>0 AND i%50=1 THEN clock_timestamp()-interval '1 day'+i*interval '1 microsecond' END,
	clock_timestamp()-interval '1 day',
	CASE WHEN i%4=0 THEN clock_timestamp()-interval '1 day' END,
	clock_timestamp()-interval '1 day'
FROM generate_series(0,$1-1) AS i`
	if _, err := database.Exec(statement, jobs); err != nil {
		tb.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE ` + table); err != nil {
		tb.Fatal(err)
	}
}

func postgresqlClaimProbe(store *queueStore, limit int) (string, []any) {
	return postgresqlClaimDiscovery(store.table(), `job."type" IN ($1,$2)`, []any{"type0", "type1"}, limit)
}

func TestPostgreSQLClaimDiscoveryStopsAtItsLimit(t *testing.T) {
	store, database := openClaimNamespace(t, "queue_claim_plan")
	seedPostgreSQLClaimBacklog(t, database, store.table(), 4000)
	statement, arguments := postgresqlClaimProbe(store, 10)
	plan, raw := analyzedPostgreSQLPlan(t, database, statement, arguments)
	scanned := false
	plan.walk(func(node postgresqlPlanNode) {
		if node.RelationName != "golem_queue" || node.Alias == "holder" {
			return
		}
		scanned = true
		if node.ActualRows*node.ActualLoops > 200 {
			t.Fatalf("claim discovery read %v backlog rows through %s %s to return 10:\n%s", node.ActualRows*node.ActualLoops, node.NodeType, node.Alias, raw)
		}
	})
	if !scanned {
		t.Fatalf("plan never read golem_queue:\n%s", raw)
	}
}

func TestPostgreSQLClaimDiscoveryMergesStatusesByAvailability(t *testing.T) {
	store, database := openClaimNamespace(t, "queue_claim_order")
	seedPostgreSQLClaimBacklog(t, database, store.table(), 400)
	statement, arguments := postgresqlClaimProbe(store, 400)
	type row struct {
		ID     string `db:"id"`
		Status string `db:"status"`
	}
	transaction, err := database.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	var got []row
	if err := transaction.Select(&got, `SELECT "id","status" FROM (`+statement+`) AS discovered`, arguments...); err != nil {
		t.Fatal(err)
	}
	var want []row
	if err := transaction.Select(&want, `SELECT "id","status" FROM `+store.table()+` WHERE "status" IN ('pending','leased') AND "available_at"<=clock_timestamp() AND "type" IN ('type0','type1') ORDER BY "available_at","id" LIMIT 400`); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || len(want) == 0 {
		t.Fatalf("discovery returned %d rows, want %d", len(got), len(want))
	}
	leased := 0
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("row %d: got %+v want %+v", index, got[index], want[index])
		}
		if want[index].Status == "leased" {
			leased++
		}
	}
	if leased == 0 {
		t.Fatal("the backlog exercised no expired lease")
	}
}

func BenchmarkPostgreSQLClaimDiscovery(b *testing.B) {
	for _, jobs := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("jobs=%d", jobs), func(b *testing.B) {
			store, database := openClaimNamespace(b, "queue_claim_bench")
			seedPostgreSQLClaimBacklog(b, database, store.table(), jobs)
			statement, arguments := postgresqlClaimProbe(store, 10)
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				transaction, err := database.Beginx()
				if err != nil {
					b.Fatal(err)
				}
				var ids []string
				if err := transaction.Select(&ids, `SELECT "id" FROM (`+statement+`) AS discovered`, arguments...); err != nil {
					b.Fatal(err)
				}
				_ = transaction.Rollback()
				if len(ids) != 10 {
					b.Fatalf("claimed %d candidates", len(ids))
				}
			}
		})
	}
}

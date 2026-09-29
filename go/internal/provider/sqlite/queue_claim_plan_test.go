package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

type sqlitePlanStep struct {
	id     int
	parent int
	detail string
}

func sqlitePlanSteps(t testing.TB, database *sqlx.DB, statement string, arguments []any) []sqlitePlanStep {
	t.Helper()
	rows, err := database.Queryx("EXPLAIN QUERY PLAN "+statement, arguments...)
	if err != nil {
		t.Fatalf("explain %q: %v", statement, err)
	}
	defer rows.Close()
	var steps []sqlitePlanStep
	for rows.Next() {
		var step sqlitePlanStep
		var notUsed int
		if err := rows.Scan(&step.id, &step.parent, &notUsed, &step.detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return steps
}

func sqliteSortsTableRows(steps []sqlitePlanStep, alias string) bool {
	for _, sort := range steps {
		if sort.detail != "USE TEMP B-TREE FOR ORDER BY" {
			continue
		}
		for _, sibling := range steps {
			if sibling.parent != sort.parent {
				continue
			}
			if strings.HasPrefix(sibling.detail, "SEARCH "+alias+" ") || strings.HasPrefix(sibling.detail, "SCAN "+alias+" ") || sibling.detail == "SCAN "+alias {
				return true
			}
		}
	}
	return false
}

func renderSQLitePlan(steps []sqlitePlanStep) string {
	lines := make([]string, len(steps))
	for index, step := range steps {
		lines[index] = fmt.Sprintf("%d<-%d %s", step.id, step.parent, step.detail)
	}
	return strings.Join(lines, "\n")
}

func seedClaimBacklog(t testing.TB, database *sqlx.DB, jobs int, now int64) {
	t.Helper()
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare(`INSERT INTO "golem_queue" ("id","type","payload","status","attempt_count","max_attempts","available_at","lease_token","lease_until","enqueued_at","finished_at","updated_at") VALUES (?,?,X'00',?,?,5,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < jobs; index++ {
		status := "pending"
		attempts := 0
		var token, leaseUntil, finished any
		available := now - int64(jobs-index)
		switch {
		case index%4 == 0:
			status = "succeeded"
			finished = available
		case index%50 == 1:
			status = "leased"
			attempts = 1
			token = fmt.Sprintf("token-%d", index)
			leaseUntil = available
		}
		if _, err := statement.Exec(fmt.Sprintf("%036d", index), fmt.Sprintf("type%d", index%3), status, attempts, available, token, leaseUntil, available, finished, available); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func openClaimStore(tb testing.TB, admitted bool) (*queueStore, *sqlx.DB) {
	tb.Helper()
	database := sqlx.MustOpen("sqlite", "file:"+tb.TempDir()+"/queue.db?_pragma=foreign_keys(1)&_txlock=immediate")
	tb.Cleanup(func() { database.Close() })
	unmanaged := physical.QueueUnmanagedObjects()
	if !admitted {
		unmanaged = unmanaged[:4]
	}
	built, err := New().QueueStore(database, unmanaged)
	if err != nil {
		tb.Fatal(err)
	}
	if err := built.EnsureSchema(context.Background()); err != nil {
		tb.Fatal(err)
	}
	return built.(*queueStore), database
}

func sqliteClaimProbe(now int64, limit int) (string, []any) {
	return sqliteClaimDiscovery(`job."type" IN (?,?)`, []any{"type0", "type1"}, now, limit)
}

func TestSQLiteClaimDiscoveryWalksTheClaimIndexInOrder(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		_, database := openClaimStore(t, admitted)
		const now = int64(1_000_000_000)
		seedClaimBacklog(t, database, 4000, now)
		for _, analyzed := range []bool{false, true} {
			if analyzed {
				if _, err := database.Exec(`ANALYZE`); err != nil {
					t.Fatal(err)
				}
			}
			statement, arguments := sqliteClaimProbe(now, 10)
			steps := sqlitePlanSteps(t, database, statement, arguments)
			plan := renderSQLitePlan(steps)
			if sqliteSortsTableRows(steps, "job") {
				t.Fatalf("admitted=%v analyzed=%v: claim discovery sorts every claimable row before its LIMIT:\n%s", admitted, analyzed, plan)
			}
			if !strings.Contains(plan, "SEARCH job USING INDEX golem_queue_claim (status=? AND available_at<?)") {
				t.Fatalf("admitted=%v analyzed=%v: claim discovery does not seek golem_queue_claim by status and time:\n%s", admitted, analyzed, plan)
			}
		}
	}
}

func TestSQLiteClaimDiscoveryMergesStatusesByAvailability(t *testing.T) {
	_, database := openClaimStore(t, false)
	const now = int64(1_000_000_000)
	seedClaimBacklog(t, database, 400, now)
	statement, arguments := sqliteClaimProbe(now, 400)
	var got []struct {
		ID          string `db:"id"`
		Status      string `db:"status"`
		AvailableAt int64  `db:"available_at"`
	}
	if err := database.Select(&got, `SELECT "id","status","available_at" FROM (`+statement+`)`, arguments...); err != nil {
		t.Fatal(err)
	}
	var want []struct {
		ID          string `db:"id"`
		Status      string `db:"status"`
		AvailableAt int64  `db:"available_at"`
	}
	if err := database.Select(&want, `SELECT "id","status","available_at" FROM "golem_queue" WHERE "status" IN ('pending','leased') AND "available_at"<=? AND "type" IN ('type0','type1') ORDER BY "available_at","id" LIMIT 400`, now); err != nil {
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
		if want[index].Status == string(queueprovider.StateLeased) {
			leased++
		}
	}
	if leased == 0 {
		t.Fatal("the backlog exercised no expired lease")
	}
}

func BenchmarkSQLiteClaimDiscovery(b *testing.B) {
	for _, jobs := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("jobs=%d", jobs), func(b *testing.B) {
			_, database := openClaimStore(b, false)
			const now = int64(10_000_000_000)
			seedClaimBacklog(b, database, jobs, now)
			statement, arguments := sqliteClaimProbe(now, 10)
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				var ids []string
				if err := database.Select(&ids, `SELECT "id" FROM (`+statement+`)`, arguments...); err != nil {
					b.Fatal(err)
				}
				if len(ids) != 10 {
					b.Fatalf("claimed %d candidates", len(ids))
				}
			}
		})
	}
}

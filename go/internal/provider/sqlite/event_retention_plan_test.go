package sqlite

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func openOutboxSystemTables(tb testing.TB) *sqlx.DB {
	tb.Helper()
	database := sqlx.MustOpen("sqlite", "file:"+tb.TempDir()+"/outbox.db?_pragma=foreign_keys(1)&_txlock=immediate")
	tb.Cleanup(func() { database.Close() })
	for _, object := range []physical.SystemObject{physical.OutboxSystemObjectV1(), physical.OutboxDeliverySystemObjectV1()} {
		statement, err := renderSystemObject(object)
		if err != nil {
			tb.Fatal(err)
		}
		indexes, err := renderSystemIndexes(object)
		if err != nil {
			tb.Fatal(err)
		}
		for _, ddl := range append([]string{statement}, indexes...) {
			if _, err := database.Exec(ddl); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return database
}

func seedDeliveredCausations(tb testing.TB, database *sqlx.DB, groups int, base int64) {
	tb.Helper()
	transaction, err := database.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	delivery, err := transaction.Prepare(`INSERT INTO "_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","lease_token","lease_until","delivered_at","updated_at") VALUES (?,?,?,1,?,?,?,?,?)`)
	if err != nil {
		tb.Fatal(err)
	}
	fact, err := transaction.Prepare(`INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,1,'c','g','m','created',x'01',?,?,x'00',?)`)
	if err != nil {
		tb.Fatal(err)
	}
	for group := 0; group < groups; group++ {
		causation := fmt.Sprintf("%08x-0000-4000-8000-%012x", group, group)
		at := base + int64(group)
		status := "delivered"
		var token, leaseUntil, delivered any = nil, nil, at
		if group%2 == 1 {
			status = "leased"
			token = fmt.Sprintf("%08x-0000-4000-8000-%012x", group, group+1)
			leaseUntil = at + 1_000_000_000
			delivered = nil
		}
		if _, err := delivery.Exec(causation, status, at, at, token, leaseUntil, delivered, at); err != nil {
			tb.Fatal(err)
		}
		for ordinal := 0; ordinal < 2; ordinal++ {
			if _, err := fact.Exec(fmt.Sprintf("%08x-%04x-4000-8000-%012x", group, ordinal, group), causation, ordinal, at); err != nil {
				tb.Fatal(err)
			}
		}
	}
	for _, statement := range []interface{ Close() error }{delivery, fact} {
		if err := statement.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func TestSQLiteEventRetentionWalksTheDeliveryIndexToItsLimit(t *testing.T) {
	database := openOutboxSystemTables(t)
	const base = int64(1_000_000)
	seedDeliveredCausations(t, database, 2000, base)
	for _, analyzed := range []bool{false, true} {
		if analyzed {
			if _, err := database.Exec(`ANALYZE`); err != nil {
				t.Fatal(err)
			}
		}
		statement, arguments := sqliteRetentionStatement(base+10_000, 100)
		steps := sqlitePlanSteps(t, database, statement, arguments)
		plan := renderSQLitePlan(steps)
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("analyzed=%v: retention sorts or groups every delivered causation before its LIMIT:\n%s", analyzed, plan)
		}
		if !strings.Contains(plan, "USING INDEX _golem_outbox_delivery_pending (status=? AND available_at<?)") && !strings.Contains(plan, "USING COVERING INDEX _golem_outbox_delivery_pending (status=? AND available_at<?)") {
			t.Fatalf("analyzed=%v: retention does not walk _golem_outbox_delivery_pending:\n%s", analyzed, plan)
		}
		for _, step := range steps {
			if strings.HasPrefix(step.detail, "SCAN ") {
				t.Fatalf("analyzed=%v: retention scans a table:\n%s", analyzed, plan)
			}
		}
	}
}

func TestSQLiteEventRetentionSelectsOnlyFullyAgedDeliveredGroupsInOrder(t *testing.T) {
	database := openOutboxSystemTables(t)
	const base = int64(1_000_000)
	seedDeliveredCausations(t, database, 40, base)
	if _, err := database.Exec(`UPDATE "_golem_outbox" SET "recorded_at"=? WHERE "causation_id"=? AND "transaction_ordinal"=1`, base+1_000_000, fmt.Sprintf("%08x-0000-4000-8000-%012x", 4, 4)); err != nil {
		t.Fatal(err)
	}
	statement, arguments := sqliteRetentionStatement(base+30, 100)
	var got []string
	if err := database.Select(&got, statement, arguments...); err != nil {
		t.Fatal(err)
	}
	var want []string
	for group := 0; group <= 30; group += 2 {
		if group == 4 {
			continue
		}
		want = append(want, fmt.Sprintf("%08x-0000-4000-8000-%012x", group, group))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("retention selected\n%v\nwant\n%v", got, want)
	}
}

func BenchmarkSQLiteEventRetentionSelection(b *testing.B) {
	for _, groups := range []int{2000, 200000} {
		b.Run(fmt.Sprintf("groups=%d", groups), func(b *testing.B) {
			database := openOutboxSystemTables(b)
			const base = int64(1_000_000)
			seedDeliveredCausations(b, database, groups, base)
			statement, arguments := sqliteRetentionStatement(base+int64(groups), 100)
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				var causations []string
				if err := database.Select(&causations, statement, arguments...); err != nil {
					b.Fatal(err)
				}
				if len(causations) != 100 {
					b.Fatalf("selected %d causations", len(causations))
				}
			}
		})
	}
}

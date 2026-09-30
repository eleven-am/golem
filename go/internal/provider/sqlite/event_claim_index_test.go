package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func seedClaimableCausations(tb testing.TB, database *sqlx.DB, groups int, now int64) {
	tb.Helper()
	transaction, err := database.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	delivery, err := transaction.Prepare(`INSERT INTO "_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","lease_token","lease_until","delivered_at","updated_at") VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tb.Fatal(err)
	}
	fact, err := transaction.Prepare(`INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,1,'c','g','m','created',x'01',?,0,x'00',?)`)
	if err != nil {
		tb.Fatal(err)
	}
	for group := 0; group < groups; group++ {
		causation := fmt.Sprintf("%08x-0000-4000-8000-%012x", group, group)
		first := now - int64(groups) + int64(group)
		status, attempts, available := "pending", 0, first
		var token, leaseUntil, delivered any
		switch group % 10 {
		case 0, 1, 2, 3:
			status, attempts, delivered = "delivered", 1, first
		case 4:
			attempts, available = 1, now+60_000_000
		case 5:
			status, attempts = "leased", 1
			token = fmt.Sprintf("%08x-0000-4000-8000-%012x", group, group+1)
			available, leaseUntil = now+60_000_000, now+60_000_000
		}
		if _, err := delivery.Exec(causation, status, first, attempts, available, token, leaseUntil, delivered, first); err != nil {
			tb.Fatal(err)
		}
		if _, err := fact.Exec(fmt.Sprintf("%08x-0001-4000-8000-%012x", group, group), causation, first); err != nil {
			tb.Fatal(err)
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

func admittedSQLiteCoordinator(tb testing.TB, database *sqlx.DB) *eventCoordinator {
	tb.Helper()
	built, err := New().EventCoordinatorAdmitting(database, physical.OutboxDeliveryUnmanagedObjects())
	if err != nil {
		tb.Fatal(err)
	}
	coordinator := built.(*eventCoordinator)
	if err := coordinator.ensureClaimIndex(context.Background()); err != nil {
		tb.Fatal(err)
	}
	return coordinator
}

func TestSQLiteEventClaimWalksTheAdmittedClaimIndexToItsLimit(t *testing.T) {
	database := openOutboxSystemTables(t)
	const now = int64(10_000_000_000)
	seedClaimableCausations(t, database, 4000, now)
	coordinator := admittedSQLiteCoordinator(t, database)
	for _, analyzed := range []bool{false, true} {
		if analyzed {
			if _, err := database.Exec(`ANALYZE`); err != nil {
				t.Fatal(err)
			}
		}
		steps := sqlitePlanSteps(t, database, sqliteClaimableGroups(coordinator.claimIndexReady.Load()), []any{now, now, 16})
		plan := renderSQLitePlan(steps)
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("analyzed=%v: the claim sorts every claimable group before its LIMIT:\n%s", analyzed, plan)
		}
		if !strings.Contains(plan, "INDEX golem_outbox_delivery_claim") {
			t.Fatalf("analyzed=%v: the claim does not walk golem_outbox_delivery_claim:\n%s", analyzed, plan)
		}
	}
}

func TestSQLiteEventClaimWithoutTheIndexKeepsItsOrder(t *testing.T) {
	plain := openOutboxSystemTables(t)
	indexed := openOutboxSystemTables(t)
	const now = int64(10_000_000_000)
	seedClaimableCausations(t, plain, 400, now)
	seedClaimableCausations(t, indexed, 400, now)
	coordinator := admittedSQLiteCoordinator(t, indexed)
	var want, got []string
	if err := plain.Select(&want, sqliteClaimableGroups(false), now, now, 400); err != nil {
		t.Fatal(err)
	}
	if err := indexed.Select(&got, sqliteClaimableGroups(coordinator.claimIndexReady.Load()), now, now, 400); err != nil {
		t.Fatal(err)
	}
	if len(want) != 160 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("indexed claim order differs from the unindexed claim: %d/%d rows", len(got), len(want))
	}
}

func TestSQLiteUnadmittedCoordinatorCreatesNoClaimIndex(t *testing.T) {
	database := openOutboxSystemTables(t)
	built, err := New().EventCoordinatorAdmitting(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 1_000_000_000}); err != nil {
		t.Fatal(err)
	}
	var present int
	if err := database.Get(&present, `SELECT count(*) FROM sqlite_master WHERE name='golem_outbox_delivery_claim'`); err != nil {
		t.Fatal(err)
	}
	if present != 0 {
		t.Fatal("a coordinator whose schema does not admit the claim index created it")
	}
}

func TestSQLiteClaimIndexRefusesAForeignObjectOfItsName(t *testing.T) {
	database := openOutboxSystemTables(t)
	if _, err := database.Exec(`CREATE INDEX "golem_outbox_delivery_claim" ON "_golem_outbox_delivery" ("first_recorded_at")`); err != nil {
		t.Fatal(err)
	}
	built, err := New().EventCoordinatorAdmitting(database, physical.OutboxDeliveryUnmanagedObjects())
	if err != nil {
		t.Fatal(err)
	}
	_, err = built.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 1_000_000_000})
	if err == nil || !strings.Contains(err.Error(), "drop it") {
		t.Fatalf("a wrong-shaped golem_outbox_delivery_claim was used: %v", err)
	}
}

func BenchmarkSQLiteEventClaimDiscovery(b *testing.B) {
	for _, groups := range []int{1000, 100000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("groups=%d/indexed=%v", groups, indexed), func(b *testing.B) {
				database := openOutboxSystemTables(b)
				const now = int64(10_000_000_000)
				seedClaimableCausations(b, database, groups, now)
				ready := false
				if indexed {
					ready = admittedSQLiteCoordinator(b, database).claimIndexReady.Load()
				}
				query := sqliteClaimableGroups(ready)
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					var causations []string
					if err := database.Select(&causations, query, now, now, 16); err != nil {
						b.Fatal(err)
					}
					if len(causations) != 16 {
						b.Fatalf("claimed %d groups", len(causations))
					}
				}
			})
		}
	}
}

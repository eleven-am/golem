package postgresql

import (
	"context"
	"fmt"
	"strings"
	"testing"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func openDeliveryNamespace(tb testing.TB, namespace physical.PhysicalName) *sqlx.DB {
	tb.Helper()
	database, err := sqlx.Open("pgx", testenv.DisposablePostgreSQL(tb, testenv.PostgreSQLDSNVariable))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { database.Close() })
	statements := append([]string{`CREATE SCHEMA ` + quote(namespace)}, renderOutbox(namespace, "_golem_outbox")...)
	statements = append(statements, renderOutboxDelivery(namespace, "_golem_outbox_delivery")...)
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			tb.Fatalf("%s: %v", statement, err)
		}
	}
	return database
}

func seedPostgreSQLClaimableCausations(tb testing.TB, database *sqlx.DB, namespace physical.PhysicalName, groups int) {
	tb.Helper()
	statement := `INSERT INTO ` + qualified(namespace, "_golem_outbox_delivery") + ` ("causation_id","status","first_recorded_at","attempt_count","available_at","lease_token","lease_until","delivered_at","updated_at")
SELECT lpad(to_hex(g), 8, '0') || '-0000-4000-8000-' || lpad(to_hex(g), 12, '0'),
	CASE WHEN g % 10 < 4 THEN 'delivered' WHEN g % 10 = 5 THEN 'leased' ELSE 'pending' END,
	clock_timestamp() - interval '1 day' + g * interval '1 microsecond',
	CASE WHEN g % 10 < 6 AND g % 10 <> 4 THEN 1 WHEN g % 10 = 4 THEN 1 ELSE 0 END,
	CASE WHEN g % 10 IN (4, 5) THEN clock_timestamp() + interval '1 hour' ELSE clock_timestamp() - interval '1 day' + g * interval '1 microsecond' END,
	CASE WHEN g % 10 = 5 THEN lpad(to_hex(g), 8, '0') || '-0000-4000-8000-' || lpad(to_hex(g + 1), 12, '0') END,
	CASE WHEN g % 10 = 5 THEN clock_timestamp() + interval '1 hour' END,
	CASE WHEN g % 10 < 4 THEN clock_timestamp() - interval '1 day' END,
	clock_timestamp()
FROM generate_series(0, $1 - 1) AS g`
	if _, err := database.Exec(statement, groups); err != nil {
		tb.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE ` + qualified(namespace, "_golem_outbox_delivery")); err != nil {
		tb.Fatal(err)
	}
}

func admittedPostgreSQLCoordinator(tb testing.TB, database *sqlx.DB, namespace physical.PhysicalName) *eventCoordinator {
	tb.Helper()
	built, err := New().EventCoordinatorAtAdmitting(database, namespace, physical.OutboxDeliveryUnmanagedObjects())
	if err != nil {
		tb.Fatal(err)
	}
	coordinator := built.(*eventCoordinator)
	if err := coordinator.ensureClaimIndex(context.Background()); err != nil {
		tb.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE ` + qualified(namespace, "_golem_outbox_delivery")); err != nil {
		tb.Fatal(err)
	}
	return coordinator
}

func TestPostgreSQLEventClaimReadsOnlyToItsLimitWithTheAdmittedIndex(t *testing.T) {
	const namespace = physical.PhysicalName("event_claim_plan")
	database := openDeliveryNamespace(t, namespace)
	seedPostgreSQLClaimableCausations(t, database, namespace, 4000)
	coordinator := admittedPostgreSQLCoordinator(t, database, namespace)
	plan, raw := analyzedPostgreSQLPlan(t, database, postgresqlClaimableGroups(coordinator.deliveryTable()), []any{16})
	read := 0.0
	plan.walk(func(node postgresqlPlanNode) {
		if node.RelationName == "_golem_outbox_delivery" {
			read += node.ActualRows * node.ActualLoops
		}
	})
	if read == 0 || read > 64 {
		t.Fatalf("the claim read %v delivery rows to lease 16 groups:\n%s", read, raw)
	}
	if !strings.Contains(raw, "golem_outbox_delivery_claim") {
		t.Fatalf("the claim does not walk golem_outbox_delivery_claim:\n%s", raw)
	}
}

func TestPostgreSQLEventClaimOrderIsUnchangedByTheIndex(t *testing.T) {
	plainNamespace, indexedNamespace := physical.PhysicalName("event_claim_plain"), physical.PhysicalName("event_claim_indexed")
	plain := openDeliveryNamespace(t, plainNamespace)
	indexed := openDeliveryNamespace(t, indexedNamespace)
	seedPostgreSQLClaimableCausations(t, plain, plainNamespace, 400)
	seedPostgreSQLClaimableCausations(t, indexed, indexedNamespace, 400)
	coordinator := admittedPostgreSQLCoordinator(t, indexed, indexedNamespace)
	var want, got []string
	if err := plain.Select(&want, postgresqlClaimableGroups(qualified(plainNamespace, "_golem_outbox_delivery")), 400); err != nil {
		t.Fatal(err)
	}
	if err := indexed.Select(&got, postgresqlClaimableGroups(coordinator.deliveryTable()), 400); err != nil {
		t.Fatal(err)
	}
	if len(want) != 160 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("indexed claim order differs from the unindexed claim: %d/%d rows", len(got), len(want))
	}
}

func TestPostgreSQLUnadmittedCoordinatorCreatesNoClaimIndex(t *testing.T) {
	const namespace = physical.PhysicalName("event_claim_unadmitted")
	database := openDeliveryNamespace(t, namespace)
	built, err := New().EventCoordinatorAt(database, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 1_000_000_000}); err != nil {
		t.Fatal(err)
	}
	present, err := postgresqlOutboxDeliveryClaimPresent(context.Background(), database, namespace)
	if err != nil || present {
		t.Fatalf("a coordinator whose schema does not admit the claim index created it: present=%v err=%v", present, err)
	}
}

func TestPostgreSQLClaimIndexRefusesAForeignObjectOfItsName(t *testing.T) {
	const namespace = physical.PhysicalName("event_claim_foreign")
	database := openDeliveryNamespace(t, namespace)
	if _, err := database.Exec(`CREATE INDEX "golem_outbox_delivery_claim" ON ` + qualified(namespace, "_golem_outbox_delivery") + ` ("first_recorded_at")`); err != nil {
		t.Fatal(err)
	}
	built, err := New().EventCoordinatorAtAdmitting(database, namespace, physical.OutboxDeliveryUnmanagedObjects())
	if err != nil {
		t.Fatal(err)
	}
	_, err = built.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 1_000_000_000})
	if err == nil || !strings.Contains(err.Error(), "drop it") {
		t.Fatalf("a wrong-shaped golem_outbox_delivery_claim was used: %v", err)
	}
}

func BenchmarkPostgreSQLEventClaimDiscovery(b *testing.B) {
	for _, groups := range []int{1000, 100000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("groups=%d/indexed=%v", groups, indexed), func(b *testing.B) {
				const namespace = physical.PhysicalName("event_claim_bench")
				database := openDeliveryNamespace(b, namespace)
				seedPostgreSQLClaimableCausations(b, database, namespace, groups)
				if indexed {
					admittedPostgreSQLCoordinator(b, database, namespace)
				}
				query := postgresqlClaimableGroups(qualified(namespace, "_golem_outbox_delivery"))
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					transaction, err := database.Beginx()
					if err != nil {
						b.Fatal(err)
					}
					var causations []string
					if err := transaction.Select(&causations, query, 16); err != nil {
						b.Fatal(err)
					}
					_ = transaction.Rollback()
					if len(causations) != 16 {
						b.Fatalf("claimed %d groups", len(causations))
					}
				}
			})
		}
	}
}

package postgresql

import (
	"context"
	"strings"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func newAdmittedPostgreSQLCoordinator(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) *eventCoordinator {
	t.Helper()
	built, err := New().EventCoordinatorAtAdmitting(database, namespace, physical.OutboxDeliveryUnmanagedObjects())
	if err != nil {
		t.Fatal(err)
	}
	return built.(*eventCoordinator)
}

func holdPostgreSQLDeliveryWrite(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName, causation string) *sqlx.Tx {
	t.Helper()
	holder, err := database.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback() })
	insertPostgreSQLDelivery(t, holder, namespace, causation)
	return holder
}

func insertPostgreSQLDelivery(t *testing.T, executor sqlx.Execer, namespace physical.PhysicalName, causation string) {
	t.Helper()
	if _, err := executor.Exec(`INSERT INTO `+qualified(namespace, "_golem_outbox_delivery")+` ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") VALUES ($1,'pending',clock_timestamp(),0,clock_timestamp()+interval '1 hour',clock_timestamp())`, causation); err != nil {
		t.Fatal(err)
	}
}

func awaitPostgreSQLLockWaiters(t *testing.T, database *sqlx.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := database.Get(&waiting, `SELECT count(*) FROM pg_catalog.pg_locks WHERE NOT granted`); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lock waiters after 10s, want %d", waiting, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func postgresqlDeliveryProfiles(t *testing.T, run func(*testing.T, string)) {
	t.Helper()
	postgresqlClaimProfiles(t, run)
}

func TestPostgreSQLConcurrentNodesAllCreateTheClaimIndex(t *testing.T) {
	postgresqlDeliveryProfiles(t, func(t *testing.T, variable string) {
		const namespace = physical.PhysicalName("event_claim_race")
		database := openDeliveryNamespaceAt(t, variable, namespace)
		holder := holdPostgreSQLDeliveryWrite(t, database, namespace, "00000000-0000-4000-8000-000000000001")
		const nodes = 4
		results := make(chan error, nodes)
		for node := 0; node < nodes; node++ {
			coordinator := newAdmittedPostgreSQLCoordinator(t, database, namespace)
			go func() {
				_, err := coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
				results <- err
			}()
		}
		var failures []error
		finished := 0
		deadline := time.Now().Add(10 * time.Second)
		for {
			select {
			case err := <-results:
				finished++
				if err != nil {
					failures = append(failures, err)
				}
				continue
			default:
			}
			var waiting int
			if err := database.Get(&waiting, `SELECT count(*) FROM pg_catalog.pg_locks WHERE NOT granted`); err != nil {
				t.Fatal(err)
			}
			if waiting+finished >= nodes {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d lock waiters and %d finished nodes after 10s", waiting, finished)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		for ; finished < nodes; finished++ {
			if err := <-results; err != nil {
				failures = append(failures, err)
			}
		}
		for _, err := range failures {
			t.Errorf("a node racing another to create the claim index failed its claim: %v", err)
		}
		if _, err := newAdmittedPostgreSQLCoordinator(t, database, namespace).Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute}); err != nil {
			t.Fatal(err)
		}
		present, err := postgresqlOutboxDeliveryClaimPresent(context.Background(), database, namespace)
		if err != nil || !present {
			t.Fatalf("claim index present=%v err=%v", present, err)
		}
	})
}

func TestPostgreSQLClaimIndexBuildDoesNotBlockOutboxWrites(t *testing.T) {
	postgresqlDeliveryProfiles(t, func(t *testing.T, variable string) {
		const namespace = physical.PhysicalName("event_claim_build")
		database := openDeliveryNamespaceAt(t, variable, namespace)
		holder := holdPostgreSQLDeliveryWrite(t, database, namespace, "00000000-0000-4000-8000-000000000001")
		coordinator := newAdmittedPostgreSQLCoordinator(t, database, namespace)
		result := make(chan error, 1)
		go func() {
			_, err := coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			result <- err
		}()
		awaitPostgreSQLLockWaiters(t, database, 1)
		writer, err := database.Beginx()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(`SET LOCAL statement_timeout='3s'`); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(`INSERT INTO `+qualified(namespace, "_golem_outbox_delivery")+` ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") VALUES ($1,'pending',clock_timestamp(),0,clock_timestamp()+interval '1 hour',clock_timestamp())`, "00000000-0000-4000-8000-000000000002"); err != nil {
			_ = writer.Rollback()
			_ = holder.Commit()
			<-result
			t.Fatalf("an outbox write queued behind the claim index build: %v", err)
		}
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		present, err := postgresqlOutboxDeliveryClaimPresent(context.Background(), database, namespace)
		if err != nil || !present {
			t.Fatalf("claim index present=%v err=%v", present, err)
		}
	})
}

func TestPostgreSQLClaimRebuildsADroppedOrAbandonedClaimIndex(t *testing.T) {
	postgresqlDeliveryProfiles(t, func(t *testing.T, variable string) {
		ctx := context.Background()
		const namespace = physical.PhysicalName("event_claim_rebuild")
		database := openDeliveryNamespaceAt(t, variable, namespace)
		coordinator := newAdmittedPostgreSQLCoordinator(t, database, namespace)
		claim := func() {
			t.Helper()
			if _, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute}); err != nil {
				t.Fatal(err)
			}
		}
		claim()
		if _, err := database.Exec(`DROP INDEX ` + qualified(namespace, "golem_outbox_delivery_claim")); err != nil {
			t.Fatal(err)
		}
		claim()
		claim()
		if present, err := postgresqlOutboxDeliveryClaimPresent(ctx, database, namespace); err != nil || !present {
			t.Fatalf("a dropped claim index was not rebuilt: present=%v err=%v", present, err)
		}
		if _, err := database.Exec(`UPDATE pg_catalog.pg_index SET indisvalid=false WHERE indexrelid=to_regclass($1)`, string(namespace)+".golem_outbox_delivery_claim"); err != nil {
			t.Fatal(err)
		}
		claim()
		claim()
		if present, err := postgresqlOutboxDeliveryClaimPresent(ctx, database, namespace); err != nil || !present {
			t.Fatalf("an abandoned invalid claim index was not rebuilt: present=%v err=%v", present, err)
		}
		if _, err := database.Exec(`DROP INDEX ` + qualified(namespace, "golem_outbox_delivery_claim")); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE INDEX "golem_outbox_delivery_claim" ON ` + qualified(namespace, "_golem_outbox_delivery") + ` ("first_recorded_at")`); err != nil {
			t.Fatal(err)
		}
		_, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "drop it") {
			t.Fatalf("a foreign object that replaced the claim index was accepted: %v", err)
		}
	})
}

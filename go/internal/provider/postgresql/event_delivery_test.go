package postgresql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/event/provider/providertest"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func TestP7PostgreSQLDeliveryCoordinatorLiveProfiles(t *testing.T) {
	profiles := []struct {
		name string
		env  string
	}{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dsn := os.Getenv(profile.env)
			if dsn == "" {
				t.Skip(profile.env + " is not configured; P7 final evidence requires this live profile")
			}
			testP7PostgreSQLDeliveryCoordinatorLive(t, dsn)
		})
	}
}

func TestP7PostgreSQLSystemCheckConstraintKeysCanonicalizeAsSetsOnly(t *testing.T) {
	if got := canonicalSystemConstraintKeys("c", "{2,6,7,8,10,11,9,6}"); got != "{2,6,7,8,9,10,11}" {
		t.Fatalf("CHECK conkey canonicalization=%q", got)
	}
	for _, kind := range []string{"p", "u", "f"} {
		const ordered = "{2,1,2}"
		if got := canonicalSystemConstraintKeys(kind, ordered); got != ordered {
			t.Fatalf("%s conkey order changed to %q", kind, got)
		}
	}
}

func testP7PostgreSQLDeliveryCoordinatorLive(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cleanup := func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "golem_p7_delivery_live" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}
	cleanup()
	defer cleanup()
	schema, err := provider.Lower(ctx, fixtureModel(), physical.LowerOptions{Namespace: "golem_p7_delivery_live"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	harnessCause := postgresqlDeliveryUUID(9)
	insertPostgreSQLDeliveryFact(t, database, harnessCause, postgresqlDeliveryUUID(91), 1)
	insertPostgreSQLDeliveryFact(t, database, harnessCause, postgresqlDeliveryUUID(92), 2)
	harnessCoordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	providertest.RunCoordinatorContract(t, harnessCoordinator, harnessCause, 2)
	expiryCause := postgresqlDeliveryUUID(8)
	insertPostgreSQLDeliveryFact(t, database, expiryCause, postgresqlDeliveryUUID(80), 1)
	expired, err := harnessCoordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 5 * time.Millisecond})
	if err != nil || len(expired) != 1 || expired[0].Delivery.CausationID != expiryCause {
		t.Fatalf("expiry claim=%#v error=%v", expired, err)
	}
	staleToken := expired[0].Delivery.LeaseToken
	time.Sleep(15 * time.Millisecond)
	if changed, renewErr := harnessCoordinator.Renew(ctx, expiryCause, staleToken, 5*time.Millisecond); renewErr != nil || !changed {
		t.Fatalf("expired unreowned renew changed=%t error=%v", changed, renewErr)
	}
	time.Sleep(15 * time.Millisecond)
	reowned, err := harnessCoordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(reowned) != 1 || reowned[0].Delivery.LeaseToken == staleToken {
		t.Fatalf("expiry reownership=%#v error=%v", reowned, err)
	}
	if changed, ackErr := harnessCoordinator.Acknowledge(ctx, expiryCause, staleToken); ackErr != nil || changed {
		t.Fatalf("expired stale ack changed=%t error=%v", changed, ackErr)
	}
	if changed, blockErr := harnessCoordinator.Block(ctx, expiryCause, reowned[0].Delivery.LeaseToken, "expiry-test-complete"); blockErr != nil || !changed {
		t.Fatalf("expiry owner block changed=%t error=%v", changed, blockErr)
	}
	if changed, retireErr := harnessCoordinator.Retire(ctx, expiryCause); retireErr != nil || !changed {
		t.Fatalf("expiry retire changed=%t error=%v", changed, retireErr)
	}
	for group := 1; group <= 4; group++ {
		cause := postgresqlDeliveryUUID(group)
		for ordinal := 1; ordinal <= 3; ordinal++ {
			insertPostgreSQLDeliveryFact(t, database, cause, postgresqlDeliveryUUID(group*100+ordinal), ordinal)
		}
	}
	// Isolate SKIP LOCKED ownership from legacy missing-state materialization.
	// Concurrent backfill transactions may legitimately contend on the same
	// absent candidates and return an uneven first claim; once the durable state
	// exists, every worker competes only through the intended row locks.
	materialized, err := harnessCoordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 4, LeaseDuration: time.Second})
	if err != nil || len(materialized) != 4 {
		t.Fatalf("pre-materialize claims=%#v error=%v", materialized, err)
	}
	for _, lease := range materialized {
		if changed, releaseErr := harnessCoordinator.Release(ctx, lease.Delivery.CausationID, lease.Delivery.LeaseToken); releaseErr != nil || !changed {
			t.Fatalf("pre-materialize release %s changed=%t error=%v", lease.Delivery.CausationID, changed, releaseErr)
		}
	}
	start := make(chan struct{})
	results := make(chan []eventprovider.Lease, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			coordinator, coordinatorErr := provider.EventCoordinator(database)
			if coordinatorErr != nil {
				errors <- coordinatorErr
				return
			}
			<-start
			leases, claimErr := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Second})
			if claimErr != nil {
				errors <- claimErr
				return
			}
			results <- leases
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for claimErr := range errors {
		t.Fatal(claimErr)
	}
	seen := map[string]eventprovider.Lease{}
	for leases := range results {
		if len(leases) != 2 {
			t.Fatalf("worker claimed %d groups; want 2", len(leases))
		}
		for _, lease := range leases {
			if _, duplicate := seen[lease.Delivery.CausationID]; duplicate {
				t.Fatalf("causation %s was concurrently owned", lease.Delivery.CausationID)
			}
			if len(lease.Facts) != 3 || lease.Facts[0].TransactionOrdinal != 1 || lease.Facts[2].TransactionOrdinal != 3 {
				t.Fatalf("split or unordered causation=%#v", lease)
			}
			seen[lease.Delivery.CausationID] = lease
		}
	}
	if len(seen) != 4 {
		t.Fatalf("claimed groups=%d", len(seen))
	}
	coordinator, _ := provider.EventCoordinator(database)
	for cause, lease := range seen {
		if changed, ackErr := coordinator.Acknowledge(ctx, cause, lease.Delivery.LeaseToken); ackErr != nil || !changed {
			t.Fatalf("ack %s changed=%t error=%v", cause, changed, ackErr)
		}
	}
	retained, err := coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Minute), MaxRows: 2})
	if err != nil || retained != (eventprovider.RetentionResult{}) {
		t.Fatalf("oversized causal retention=%#v error=%v", retained, err)
	}
	retained, err = coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Minute), MaxRows: 3})
	if err != nil || retained.Causations != 1 || retained.Facts != 3 {
		t.Fatalf("bounded causal retention=%#v error=%v", retained, err)
	}
	assertPostgreSQLMissingStateMaterializationBounded(t, database, coordinator)
}

func assertPostgreSQLMissingStateMaterializationBounded(t *testing.T, database *sqlx.DB, coordinator eventprovider.Coordinator) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Exec(`DELETE FROM "_golem"."_golem_outbox_delivery"`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM "_golem"."_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	const backlogGroups = 257
	for group := 1; group <= backlogGroups; group++ {
		insertPostgreSQLDeliveryFact(t, database, postgresqlDeliveryUUID(1_000+group), postgresqlDeliveryUUID(2_000+group), 1)
	}
	for claim := 1; claim <= 4; claim++ {
		leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: 10 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(leases) != 2 {
			t.Fatalf("bounded claim %d leases=%d want=2", claim, len(leases))
		}
		var states int
		if err := database.Get(&states, `SELECT COUNT(*) FROM "_golem"."_golem_outbox_delivery"`); err != nil {
			t.Fatal(err)
		}
		wantStates := claim * 2
		if states != wantStates {
			t.Fatalf("bounded claim %d materialized states=%d want=%d", claim, states, wantStates)
		}
	}
}

func insertPostgreSQLDeliveryFact(t *testing.T, database *sqlx.DB, causation, event string, ordinal int) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO "_golem"."_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,clock_timestamp())`, event, 1, "golem.fact.v1", strings.Repeat("1", 64), strings.Repeat("2", 32), "created", []byte{1}, causation, ordinal, []byte{byte(ordinal)})
	if err != nil {
		t.Fatal(err)
	}
}

func postgresqlDeliveryUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", value)
}

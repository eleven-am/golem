package postgresql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/event/provider/providertest"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func TestPostgreSQLDeliveryCoordinatorLiveProfiles(t *testing.T) {
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

func TestPostgreSQLClaimDepthSnapshotLiveProfiles(t *testing.T) {
	if dsn := os.Getenv("GOLEM_P8_POSTGRES_DEPTH_WORKER_DSN"); dsn != "" {
		p8PostgreSQLDepthSubprocessWorker(t, dsn, os.Getenv("GOLEM_P8_DEPTH_WORKER_OUTPUT"), os.Getenv("GOLEM_P8_DEPTH_WORKER_MODE"), os.Getenv("GOLEM_P8_DEPTH_WORKER_CAUSATION"))
		return
	}
	for _, profile := range []struct{ name, env string }{{"c", "GOLEM_TEST_POSTGRES_DSN"}, {"linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		t.Run(profile.name, func(t *testing.T) {
			dsn := os.Getenv(profile.env)
			if dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertPostgreSQLClaimDepthSnapshot(t, dsn)
		})
	}
}

func assertPostgreSQLClaimDepthSnapshot(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cleanup := func() {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "golem_p8_depth_live" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}
	cleanup()
	defer cleanup()
	schema, err := provider.Lower(ctx, fixtureModel(), physical.LowerOptions{Namespace: "golem_p8_depth_live"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	depthCoordinator, ok := coordinator.(eventprovider.ClaimDepthCoordinator)
	if !ok {
		t.Fatal("released PostgreSQL coordinator has no transaction-coupled depth snapshot")
	}
	for index := 1; index <= 3; index++ {
		insertPostgreSQLDeliveryFact(t, database, postgresqlDeliveryUUID(700+index), postgresqlDeliveryUUID(800+index), 1)
	}
	claimed, err := depthCoordinator.ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 3, LeaseDuration: time.Minute})
	if err != nil || len(claimed.Leases) != 3 || claimed.Depth != (eventprovider.DepthSnapshot{}) {
		t.Fatalf("initial claim=%#v error=%v", claimed, err)
	}
	if changed, err := coordinator.Retry(ctx, claimed.Leases[0].Delivery.CausationID, claimed.Leases[0].Delivery.LeaseToken, time.Hour, "depth-pending"); err != nil || !changed {
		t.Fatalf("pending transition changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Block(ctx, claimed.Leases[1].Delivery.CausationID, claimed.Leases[1].Delivery.LeaseToken, "depth-blocked"); err != nil || !changed {
		t.Fatalf("blocked transition changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Block(ctx, claimed.Leases[2].Delivery.CausationID, claimed.Leases[2].Delivery.LeaseToken, "depth-retired"); err != nil || !changed {
		t.Fatalf("retire pre-block changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Retire(ctx, claimed.Leases[2].Delivery.CausationID); err != nil || !changed {
		t.Fatalf("retire changed=%t error=%v", changed, err)
	}
	snapshot, err := depthCoordinator.ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(snapshot.Leases) != 0 || snapshot.Depth != (eventprovider.DepthSnapshot{Pending: 1, Blocked: 1, Retired: 1}) {
		t.Fatalf("status snapshot=%#v error=%v", snapshot, err)
	}

	start := make(chan struct{})
	results := make(chan eventprovider.ClaimSnapshot, 2)
	for range 2 {
		go func() {
			other, err := provider.EventCoordinator(database)
			if err != nil {
				results <- eventprovider.ClaimSnapshot{Depth: eventprovider.DepthSnapshot{Pending: -1}}
				return
			}
			<-start
			value, err := other.(eventprovider.ClaimDepthCoordinator).ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil {
				results <- eventprovider.ClaimSnapshot{Depth: eventprovider.DepthSnapshot{Pending: -1}}
				return
			}
			results <- value
		}()
	}
	close(start)
	for range 2 {
		select {
		case value := <-results:
			if len(value.Leases) != 0 || value.Depth != snapshot.Depth {
				t.Fatalf("concurrent snapshot=%#v want depth=%#v", value, snapshot.Depth)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent depth snapshot deadlocked")
		}
	}

	contentionCause := postgresqlDeliveryUUID(704)
	insertPostgreSQLDeliveryFact(t, database, contentionCause, postgresqlDeliveryUUID(804), 1)
	p8PostgreSQLMaterializeDelivery(t, coordinator.(*eventCoordinator), database)
	p8RunPostgreSQLDepthSubprocesses(t, dsn, contentionCause, snapshot.Depth)
	p8AssertPostgreSQLDeliveryConservation(t, database, map[string]int{"pending": 1, "blocked": 1, "retired": 1, "leased": 1})

	crashCause := postgresqlDeliveryUUID(705)
	insertPostgreSQLDeliveryFact(t, database, crashCause, postgresqlDeliveryUUID(805), 1)
	p8PostgreSQLMaterializeDelivery(t, coordinator.(*eventCoordinator), database)
	p8RunPostgreSQLDepthCrash(t, dsn, crashCause)
	var rolledBack struct {
		Status string         `db:"status"`
		Token  sql.NullString `db:"lease_token"`
	}
	if err := database.GetContext(ctx, &rolledBack, `SELECT "status","lease_token" FROM "_golem"."_golem_outbox_delivery" WHERE "causation_id"=$1`, crashCause); err != nil || rolledBack.Status != "pending" || rolledBack.Token.Valid {
		t.Fatalf("crashed PostgreSQL claim persisted=%#v err=%v", rolledBack, err)
	}
	afterCrash, err := depthCoordinator.ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(afterCrash.Leases) != 1 || afterCrash.Leases[0].Delivery.CausationID != crashCause || afterCrash.Depth != snapshot.Depth {
		t.Fatalf("post-crash PostgreSQL claim=%#v err=%v", afterCrash, err)
	}
	p8AssertPostgreSQLDeliveryConservation(t, database, map[string]int{"pending": 1, "blocked": 1, "retired": 1, "leased": 2})
}

type p8PostgreSQLDepthWorkerResult struct {
	Depth      eventprovider.DepthSnapshot `json:"depth"`
	Causations []string                    `json:"causations"`
}

func p8RunPostgreSQLDepthSubprocesses(t *testing.T, dsn, wantCausation string, baseline eventprovider.DepthSnapshot) {
	t.Helper()
	type process struct {
		command *exec.Cmd
		output  string
		log     *bytes.Buffer
	}
	processes := make([]process, 2)
	outputDirectory := t.TempDir()
	for index := range processes {
		output := filepath.Join(outputDirectory, fmt.Sprintf("depth-%d.json", index))
		command := exec.Command(os.Args[0], "-test.run=^TestPostgreSQLClaimDepthSnapshotLiveProfiles$", "-test.count=1")
		command.Env = append(os.Environ(), "GOLEM_P8_POSTGRES_DEPTH_WORKER_DSN="+dsn, "GOLEM_P8_DEPTH_WORKER_OUTPUT="+output, "GOLEM_P8_DEPTH_WORKER_MODE=claim")
		log := &bytes.Buffer{}
		command.Stdout = log
		command.Stderr = log
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		processes[index] = process{command: command, output: output, log: log}
	}
	claimed := make([]string, 0, 1)
	for _, process := range processes {
		if err := process.command.Wait(); err != nil {
			t.Fatalf("PostgreSQL depth subprocess failed: %v\n%s", err, process.log.String())
		}
		contents, err := os.ReadFile(process.output)
		if err != nil {
			t.Fatal(err)
		}
		var result p8PostgreSQLDepthWorkerResult
		if err := json.Unmarshal(contents, &result); err != nil {
			t.Fatalf("decode PostgreSQL subprocess result: %v", err)
		}
		if result.Depth.Blocked != baseline.Blocked || result.Depth.Retired != baseline.Retired {
			t.Fatalf("PostgreSQL subprocess depth=%#v baseline=%#v", result.Depth, baseline)
		}
		if len(result.Causations) == 1 {
			if result.Depth != baseline {
				t.Fatalf("PostgreSQL owning transaction depth=%#v want=%#v", result.Depth, baseline)
			}
		} else if len(result.Causations) != 0 || (result.Depth.Pending != baseline.Pending && result.Depth.Pending != baseline.Pending+1) {
			t.Fatalf("PostgreSQL losing transaction result=%#v baseline=%#v", result, baseline)
		}
		claimed = append(claimed, result.Causations...)
	}
	if len(claimed) != 1 || claimed[0] != wantCausation {
		t.Fatalf("PostgreSQL cross-process claims=%v want exactly %s", claimed, wantCausation)
	}
}

func p8PostgreSQLDepthSubprocessWorker(t *testing.T, dsn, output, mode, causation string) {
	t.Helper()
	provider := New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if mode == "crash" {
		transaction, err := database.BeginTxx(context.Background(), &sql.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := transaction.ExecContext(context.Background(), `UPDATE "_golem"."_golem_outbox_delivery" SET "status"='leased',"lease_token"='00000000-0000-4000-8000-000000009999',"lease_until"=clock_timestamp()+interval '1 minute' WHERE "causation_id"=$1 AND "status"='pending'`, causation)
		if err != nil {
			t.Fatal(err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			t.Fatalf("PostgreSQL crash worker changed=%d", changed)
		}
		os.Exit(17)
	}
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.(eventprovider.ClaimDepthCoordinator).ClaimWithDepth(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("PostgreSQL subprocess claim=%#v err=%v", snapshot, err)
	}
	result := p8PostgreSQLDepthWorkerResult{Depth: snapshot.Depth, Causations: make([]string, len(snapshot.Leases))}
	for index := range snapshot.Leases {
		result.Causations[index] = snapshot.Leases[index].Delivery.CausationID
	}
	contents, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func p8RunPostgreSQLDepthCrash(t *testing.T, dsn, causation string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestPostgreSQLClaimDepthSnapshotLiveProfiles$", "-test.count=1")
	command.Env = append(os.Environ(), "GOLEM_P8_POSTGRES_DEPTH_WORKER_DSN="+dsn, "GOLEM_P8_DEPTH_WORKER_MODE=crash", "GOLEM_P8_DEPTH_WORKER_CAUSATION="+causation)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 17 {
		t.Fatalf("PostgreSQL crash worker exit=%v output=%s", err, output)
	}
}

func p8PostgreSQLMaterializeDelivery(t *testing.T, coordinator *eventCoordinator, database *sqlx.DB) {
	t.Helper()
	transaction, err := database.BeginTxx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if err := coordinator.materializeMissing(context.Background(), transaction, 1); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func p8AssertPostgreSQLDeliveryConservation(t *testing.T, database *sqlx.DB, want map[string]int) {
	t.Helper()
	var rows []struct {
		Status string `db:"status"`
		Count  int    `db:"count"`
	}
	if err := database.Select(&rows, `SELECT "status",COUNT(*) AS "count" FROM "_golem"."_golem_outbox_delivery" GROUP BY "status"`); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int, len(rows))
	for _, row := range rows {
		got[row.Status] = row.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PostgreSQL delivery conservation=%v want=%v", got, want)
	}
}

func TestPostgreSQLSystemCheckConstraintKeysCanonicalizeAsSetsOnly(t *testing.T) {
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

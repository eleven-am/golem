package sqlite

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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/event/provider/providertest"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/jmoiron/sqlx"
)

func TestSQLiteDeliveryClaimFenceOperatorAndRetention(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(1)
	insertDeliveryFact(t, database, cause, deliveryUUID(11), 2, 20)
	insertDeliveryFact(t, database, cause, deliveryUUID(10), 1, 10)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].Delivery.CausationID != cause || leases[0].Delivery.Status != eventprovider.StatusLeased || leases[0].Delivery.AttemptCount != 1 || len(leases[0].Facts) != 2 || leases[0].Facts[0].TransactionOrdinal != 1 || leases[0].Facts[1].TransactionOrdinal != 2 {
		t.Fatalf("lease=%#v", leases)
	}
	token := leases[0].Delivery.LeaseToken
	if changed, err := coordinator.Acknowledge(ctx, cause, deliveryUUID(99)); err != nil || changed {
		t.Fatalf("stale ack changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Renew(ctx, cause, token, 2*time.Second); err != nil || !changed {
		t.Fatalf("renew changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Retry(ctx, cause, token, 0, "transport-timeout"); err != nil || !changed {
		t.Fatalf("retry changed=%t error=%v", changed, err)
	}
	leases, err = coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 || leases[0].Delivery.AttemptCount != 2 || leases[0].Delivery.LeaseToken == token {
		t.Fatalf("reclaim=%#v error=%v", leases, err)
	}
	secondToken := leases[0].Delivery.LeaseToken
	if changed, err := coordinator.Block(ctx, cause, secondToken, "fact-corrupt"); err != nil || !changed {
		t.Fatalf("block changed=%t error=%v", changed, err)
	}
	state, err := coordinator.Inspect(ctx, cause)
	if err != nil || state.Status != eventprovider.StatusBlocked || state.LastFailureCode != "fact-corrupt" || state.BlockedAt == nil || state.ImmutableFactRows != 2 {
		t.Fatalf("blocked state=%#v error=%v", state, err)
	}
	if changed, err := coordinator.Resume(ctx, cause); err != nil || !changed {
		t.Fatalf("resume changed=%t error=%v", changed, err)
	}
	leases, err = coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 {
		t.Fatalf("post-resume claim=%#v error=%v", leases, err)
	}
	if changed, err := coordinator.Acknowledge(ctx, cause, leases[0].Delivery.LeaseToken); err != nil || !changed {
		t.Fatalf("ack changed=%t error=%v", changed, err)
	}
	retained, err := coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Minute), MaxRows: 1})
	if err != nil || retained != (eventprovider.RetentionResult{}) {
		t.Fatalf("oversized-group retention=%#v error=%v", retained, err)
	}
	var factsBefore int
	if err := database.Get(&factsBefore, `SELECT count(*) FROM "_golem_outbox"`); err != nil || factsBefore != 2 {
		t.Fatalf("oversized group was partially deleted: facts=%d error=%v", factsBefore, err)
	}
	retained, err = coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Minute), MaxRows: 2})
	if err != nil || retained.Causations != 1 || retained.Facts != 2 {
		t.Fatalf("retention=%#v error=%v", retained, err)
	}
	var facts, states int
	if err := database.Get(&facts, `SELECT count(*) FROM "_golem_outbox"`); err != nil || facts != 0 {
		t.Fatalf("facts=%d error=%v", facts, err)
	}
	if err := database.Get(&states, `SELECT count(*) FROM "_golem_outbox_delivery"`); err != nil || states != 0 {
		t.Fatalf("states=%d error=%v", states, err)
	}
}

func TestSQLiteDeliveryClaimsFirstCausationAboveEstimatedByteBudget(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(700)
	insertDeliveryFact(t, database, cause, deliveryUUID(701), 1, 1)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second, MaxBytes: 1})
	if err != nil || len(leases) != 1 || leases[0].Delivery.CausationID != cause {
		t.Fatalf("oversized claim=%#v error=%v", leases, err)
	}
	state, err := coordinator.Inspect(ctx, cause)
	if err != nil || state.Status != eventprovider.StatusLeased || state.LastFailureCode != "" {
		t.Fatalf("oversized state=%#v error=%v", state, err)
	}
}

func TestSQLiteConcurrentWorkersClaimWholeCausationsExclusively(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	for group := 1; group <= 4; group++ {
		cause := deliveryUUID(group)
		for ordinal := 1; ordinal <= 3; ordinal++ {
			insertDeliveryFact(t, database, cause, deliveryUUID(group*100+ordinal), ordinal, int64(group*100+ordinal))
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
			coordinator, err := provider.EventCoordinator(database)
			if err != nil {
				errors <- err
				return
			}
			<-start
			leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
			if err != nil {
				errors <- err
				return
			}
			results <- leases
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for leases := range results {
		if len(leases) != 2 {
			t.Fatalf("worker claimed %d groups", len(leases))
		}
		for _, lease := range leases {
			if seen[lease.Delivery.CausationID] {
				t.Fatalf("causation %s was concurrently claimed", lease.Delivery.CausationID)
			}
			seen[lease.Delivery.CausationID] = true
			if len(lease.Facts) != 3 {
				t.Fatalf("causation %s split to %d facts", lease.Delivery.CausationID, len(lease.Facts))
			}
		}
	}
	if len(seen) != 4 {
		t.Fatalf("claimed causations=%v", seen)
	}
}

func TestSQLiteClaimDepthSnapshotIsExactAndSerialized(t *testing.T) {
	if databasePath := os.Getenv("GOLEM_P8_SQLITE_DEPTH_WORKER_DATABASE"); databasePath != "" {
		p8SQLiteDepthSubprocessWorker(t, databasePath, os.Getenv("GOLEM_P8_DEPTH_WORKER_OUTPUT"), os.Getenv("GOLEM_P8_DEPTH_WORKER_MODE"), os.Getenv("GOLEM_P8_DEPTH_WORKER_CAUSATION"))
		return
	}
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	depthCoordinator, ok := coordinator.(eventprovider.ClaimDepthCoordinator)
	if !ok {
		t.Fatal("released SQLite coordinator has no transaction-coupled depth snapshot")
	}
	for index := 1; index <= 3; index++ {
		insertDeliveryFact(t, database, deliveryUUID(700+index), deliveryUUID(800+index), 1, int64(index))
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
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			other, err := provider.EventCoordinator(database)
			if err != nil {
				errors <- err
				return
			}
			<-start
			value, err := other.(eventprovider.ClaimDepthCoordinator).ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil {
				errors <- err
				return
			}
			results <- value
		}()
	}
	close(start)
	for range 2 {
		select {
		case err := <-errors:
			t.Fatal(err)
		case value := <-results:
			if len(value.Leases) != 0 || value.Depth != snapshot.Depth {
				t.Fatalf("concurrent serialized snapshot=%#v want depth=%#v", value, snapshot.Depth)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent depth snapshot deadlocked")
		}
	}
	var databasePath string
	if err := database.GetContext(ctx, &databasePath, `SELECT file FROM pragma_database_list WHERE name='main'`); err != nil || databasePath == "" {
		t.Fatalf("resolve SQLite fixture path=%q err=%v", databasePath, err)
	}
	contentionCause := deliveryUUID(704)
	insertDeliveryFact(t, database, contentionCause, deliveryUUID(804), 1, 4)
	p8SQLiteMaterializeDelivery(t, database)
	p8RunSQLiteDepthSubprocesses(t, databasePath, contentionCause, snapshot.Depth)
	p8AssertSQLiteDeliveryConservation(t, database, map[string]int{"pending": 1, "blocked": 1, "retired": 1, "leased": 1})

	crashCause := deliveryUUID(705)
	insertDeliveryFact(t, database, crashCause, deliveryUUID(805), 1, 5)
	p8SQLiteMaterializeDelivery(t, database)
	p8RunSQLiteDepthCrash(t, databasePath, crashCause)
	var rolledBack struct {
		Status string         `db:"status"`
		Token  sql.NullString `db:"lease_token"`
	}
	if err := database.GetContext(ctx, &rolledBack, `SELECT "status","lease_token" FROM "_golem_outbox_delivery" WHERE "causation_id"=?`, crashCause); err != nil || rolledBack.Status != "pending" || rolledBack.Token.Valid {
		t.Fatalf("crashed SQLite claim persisted=%#v err=%v", rolledBack, err)
	}
	afterCrash, err := depthCoordinator.ClaimWithDepth(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(afterCrash.Leases) != 1 || afterCrash.Leases[0].Delivery.CausationID != crashCause || afterCrash.Depth != snapshot.Depth {
		t.Fatalf("post-crash SQLite claim=%#v err=%v", afterCrash, err)
	}
	p8AssertSQLiteDeliveryConservation(t, database, map[string]int{"pending": 1, "blocked": 1, "retired": 1, "leased": 2})
}

type p8SQLiteDepthWorkerResult struct {
	Depth      eventprovider.DepthSnapshot `json:"depth"`
	Causations []string                    `json:"causations"`
}

func p8RunSQLiteDepthSubprocesses(t *testing.T, databasePath, wantCausation string, wantDepth eventprovider.DepthSnapshot) {
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
		command := exec.Command(os.Args[0], "-test.run=^TestSQLiteClaimDepthSnapshotIsExactAndSerialized$", "-test.count=1")
		command.Env = append(os.Environ(), "GOLEM_P8_SQLITE_DEPTH_WORKER_DATABASE="+databasePath, "GOLEM_P8_DEPTH_WORKER_OUTPUT="+output, "GOLEM_P8_DEPTH_WORKER_MODE=claim")
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
			t.Fatalf("SQLite depth subprocess failed: %v\n%s", err, process.log.String())
		}
		contents, err := os.ReadFile(process.output)
		if err != nil {
			t.Fatal(err)
		}
		var result p8SQLiteDepthWorkerResult
		if err := json.Unmarshal(contents, &result); err != nil || result.Depth != wantDepth {
			t.Fatalf("SQLite subprocess result=%#v want depth=%#v decode=%v", result, wantDepth, err)
		}
		claimed = append(claimed, result.Causations...)
	}
	if len(claimed) != 1 || claimed[0] != wantCausation {
		t.Fatalf("SQLite cross-process claims=%v want exactly %s", claimed, wantCausation)
	}
}

func p8SQLiteDepthSubprocessWorker(t *testing.T, databasePath, output, mode, causation string) {
	t.Helper()
	provider := New()
	database, _, err := provider.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if mode == "crash" {
		connection, err := database.Connx(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		result, err := connection.ExecContext(context.Background(), `UPDATE "_golem_outbox_delivery" SET "status"='leased',"lease_token"='00000000-0000-4000-8000-000000009999',"lease_until"=`+sqliteDatabaseMicros+`+60000000 WHERE "causation_id"=? AND "status"='pending'`, causation)
		if err != nil {
			t.Fatal(err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			t.Fatalf("SQLite crash worker changed=%d", changed)
		}
		os.Exit(17)
	}
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.(eventprovider.ClaimDepthCoordinator).ClaimWithDepth(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("SQLite subprocess claim=%#v err=%v", snapshot, err)
	}
	result := p8SQLiteDepthWorkerResult{Depth: snapshot.Depth, Causations: make([]string, len(snapshot.Leases))}
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

func p8RunSQLiteDepthCrash(t *testing.T, databasePath, causation string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteClaimDepthSnapshotIsExactAndSerialized$", "-test.count=1")
	command.Env = append(os.Environ(), "GOLEM_P8_SQLITE_DEPTH_WORKER_DATABASE="+databasePath, "GOLEM_P8_DEPTH_WORKER_MODE=crash", "GOLEM_P8_DEPTH_WORKER_CAUSATION="+causation)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 17 {
		t.Fatalf("SQLite crash worker exit=%v output=%s", err, output)
	}
}

func p8SQLiteMaterializeDelivery(t *testing.T, database *sqlx.DB) {
	t.Helper()
	if _, err := sqliteMaterializeMissing(context.Background(), database, 1); err != nil {
		t.Fatal(err)
	}
}

func p8AssertSQLiteDeliveryConservation(t *testing.T, database *sqlx.DB, want map[string]int) {
	t.Helper()
	var rows []struct {
		Status string `db:"status"`
		Count  int    `db:"count"`
	}
	if err := database.Select(&rows, `SELECT "status",COUNT(*) AS "count" FROM "_golem_outbox_delivery" GROUP BY "status"`); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int, len(rows))
	for _, row := range rows {
		got[row.Status] = row.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SQLite delivery conservation=%v want=%v", got, want)
	}
}

func TestSQLiteMissingStateMaterializationIsBoundedPerClaim(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	const backlogGroups = 257
	for group := 1; group <= backlogGroups; group++ {
		insertDeliveryFact(t, database, deliveryUUID(1_000+group), deliveryUUID(2_000+group), 1, int64(group))
	}
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	for claim := 1; claim <= 4; claim++ {
		leases, claimErr := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: 10 * time.Minute})
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if len(leases) != 2 {
			t.Fatalf("claim %d leases=%d want=2", claim, len(leases))
		}
		var states int
		if err := database.Get(&states, `SELECT COUNT(*) FROM "_golem_outbox_delivery"`); err != nil {
			t.Fatal(err)
		}
		wantStates := claim * 2
		if states != wantStates {
			t.Fatalf("claim %d materialized states=%d want bounded total=%d", claim, states, wantStates)
		}
	}
}

func TestSQLiteNewCausalDeliveryInsertIsIdempotentAndConflictSpecific(t *testing.T) {
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	recordedAt := time.Unix(1_700_000_000, 123_456_789).UTC()
	row := mutationfact.OutboxRow{
		EventID: deliveryUUID(3_001), FactVersion: int64(mutationfact.FormatVersionV1), CodecIdentity: mutationfact.CodecIdentityV1,
		GenerationFingerprint: strings.Repeat("1", 64), ModelID: strings.Repeat("2", 32), Action: "created",
		AfterIdentity: []byte{1}, CausationID: deliveryUUID(3_000), TransactionOrdinal: 1, Metadata: []byte{1}, RecordedAt: recordedAt,
	}
	statement, err := mutationfact.RenderDeliveryInsertAt(policyir.ProviderSQLite, "main", []mutationfact.OutboxRow{row})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := database.Exec(statement.SQL(), statement.Args()...); err != nil {
			t.Fatalf("idempotent insert %d: %v", attempt, err)
		}
	}
	var state struct {
		Count int   `db:"count"`
		First int64 `db:"first_recorded_at"`
	}
	if err := database.Get(&state, `SELECT COUNT(*) AS "count",MIN("first_recorded_at") AS "first_recorded_at" FROM "_golem_outbox_delivery"`); err != nil || state.Count != 1 || state.First != recordedAt.Truncate(time.Microsecond).UnixMicro() {
		t.Fatalf("idempotent state=%#v err=%v", state, err)
	}
	row.CausationID = "not-a-canonical-causation"
	invalid, err := mutationfact.RenderDeliveryInsertAt(policyir.ProviderSQLite, "main", []mutationfact.OutboxRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(invalid.SQL(), invalid.Args()...); err == nil {
		t.Fatal("non-conflict delivery constraint failure was silently ignored")
	}
}

func TestSQLiteMissingStateInspectionAndRetireAreClosed(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(7)
	insertDeliveryFact(t, database, cause, deliveryUUID(70), 1, 70)
	coordinator, _ := provider.EventCoordinator(database)
	state, err := coordinator.Inspect(ctx, cause)
	if err != nil || state.Status != eventprovider.StatusPending || state.ImmutableFactRows != 1 {
		t.Fatalf("virtual pending=%#v error=%v", state, err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	if changed, err := coordinator.Block(ctx, cause, leases[0].Delivery.LeaseToken, "schema-unavailable"); err != nil || !changed {
		t.Fatalf("block changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Retire(ctx, cause); err != nil || !changed {
		t.Fatalf("retire changed=%t error=%v", changed, err)
	}
	state, err = coordinator.Inspect(ctx, cause)
	if err != nil || state.Status != eventprovider.StatusRetired || state.RetiredAt == nil {
		t.Fatalf("retired state=%#v error=%v", state, err)
	}
	retained, err := coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().Add(24 * time.Hour), MaxRows: 10})
	if err != nil || retained != (eventprovider.RetentionResult{}) {
		t.Fatalf("retired retention=%#v error=%v", retained, err)
	}
}

func TestSQLiteExpiredTokenCanRenewUntilDatabaseReownershipThenIsFenced(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(8)
	insertDeliveryFact(t, database, cause, deliveryUUID(80), 1, 80)
	coordinator, _ := provider.EventCoordinator(database)
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: 5 * time.Millisecond})
	if err != nil || len(leases) != 1 {
		t.Fatalf("initial claim=%#v error=%v", leases, err)
	}
	staleToken := leases[0].Delivery.LeaseToken
	time.Sleep(15 * time.Millisecond)
	// Expiry makes the group claimable; it does not itself transfer ownership.
	// The token fence remains valid until a database claim installs a new token.
	if changed, err := coordinator.Renew(ctx, cause, staleToken, 5*time.Millisecond); err != nil || !changed {
		t.Fatalf("expired but unreowned renew changed=%t error=%v", changed, err)
	}
	time.Sleep(15 * time.Millisecond)
	reowned, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(reowned) != 1 || reowned[0].Delivery.LeaseToken == staleToken {
		t.Fatalf("reowned claim=%#v error=%v", reowned, err)
	}
	if changed, err := coordinator.Renew(ctx, cause, staleToken, time.Second); err != nil || changed {
		t.Fatalf("stale renew changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Acknowledge(ctx, cause, staleToken); err != nil || changed {
		t.Fatalf("stale ack changed=%t error=%v", changed, err)
	}
	if changed, err := coordinator.Acknowledge(ctx, cause, reowned[0].Delivery.LeaseToken); err != nil || !changed {
		t.Fatalf("owner ack changed=%t error=%v", changed, err)
	}
}

func TestSQLiteDeliveryProviderCommonHarness(t *testing.T) {
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(9)
	insertDeliveryFact(t, database, cause, deliveryUUID(91), 1, 91)
	insertDeliveryFact(t, database, cause, deliveryUUID(92), 2, 92)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	providertest.RunCoordinatorContract(t, coordinator, cause, 2)
}

func openEventDeliveryFixture(t *testing.T, provider *Provider) *sqlx.DB {
	t.Helper()
	schema, err := provider.Lower(context.Background(), socialModelIR(), physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "event-delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
	return database
}

func insertDeliveryFact(t *testing.T, database *sqlx.DB, causation, event string, ordinal int, recorded int64) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?)`, event, 1, "golem.fact.v1", fmt.Sprintf("%064x", 1), fmt.Sprintf("%032x", 2), "created", []byte{1}, causation, ordinal, []byte{byte(ordinal)}, recorded)
	if err != nil {
		t.Fatal(err)
	}
}

func deliveryUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", value)
}

func sortedLeaseCausations(leases []eventprovider.Lease) []string {
	result := make([]string, len(leases))
	for index := range leases {
		result[index] = leases[index].Delivery.CausationID
	}
	sort.Strings(result)
	return result
}

func TestSQLiteClaimRefusesLiveLeaseWrittenWithoutAvailableAtAlignment(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	warm := deliveryUUID(800)
	insertDeliveryFact(t, database, warm, deliveryUUID(801), 1, 1)
	if leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second}); err != nil || len(leases) != 1 {
		t.Fatalf("warming claim=%#v error=%v", leases, err)
	}
	if changed, err := coordinator.Acknowledge(ctx, warm, warmLeaseToken(t, database, warm)); err != nil || !changed {
		t.Fatalf("warming ack changed=%t error=%v", changed, err)
	}
	legacy := deliveryUUID(810)
	insertDeliveryFact(t, database, legacy, deliveryUUID(811), 1, 2)
	now := time.Now().UTC().UnixMicro()
	insertLegacyDelivery(t, database, legacy, "leased", now-int64(time.Hour/time.Microsecond), sql.NullInt64{Int64: now + int64(time.Hour/time.Microsecond), Valid: true}, sql.NullInt64{}, sql.NullString{String: deliveryUUID(812), Valid: true})
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 0 {
		t.Fatalf("legacy live lease was stolen: leases=%#v error=%v", leases, err)
	}
	state, err := coordinator.Inspect(ctx, legacy)
	if err != nil || state.Status != eventprovider.StatusLeased || state.LeaseToken != deliveryUUID(812) {
		t.Fatalf("legacy lease ownership changed: %#v error=%v", state, err)
	}
}

func TestSQLiteRetentionHonoursLegacyDeliveryTimeWithoutRewritingRows(t *testing.T) {
	ctx := context.Background()
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	micros := func(offset time.Duration) int64 { return now.Add(offset).UnixMicro() }
	recent := deliveryUUID(820)
	insertDeliveryFact(t, database, recent, deliveryUUID(821), 1, micros(-3*time.Hour))
	insertLegacyDelivery(t, database, recent, "delivered", micros(-3*time.Hour), sql.NullInt64{}, sql.NullInt64{Int64: micros(-time.Minute), Valid: true}, sql.NullString{})
	expired := deliveryUUID(830)
	insertDeliveryFact(t, database, expired, deliveryUUID(831), 1, micros(-4*time.Hour))
	insertLegacyDelivery(t, database, expired, "delivered", micros(-4*time.Hour), sql.NullInt64{}, sql.NullInt64{Int64: micros(-2 * time.Hour), Valid: true}, sql.NullString{})
	retained, err := coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: now.Add(-time.Hour), MaxRows: 8})
	if err != nil || retained.Causations != 1 || retained.Facts != 1 {
		t.Fatalf("retention=%#v error=%v", retained, err)
	}
	var survivors int
	if err := database.Get(&survivors, `SELECT count(*) FROM "_golem_outbox_delivery" WHERE "causation_id"=?`, expired); err != nil || survivors != 0 {
		t.Fatalf("expired legacy group survived: %d error=%v", survivors, err)
	}
	var available int64
	if err := database.Get(&available, `SELECT "available_at" FROM "_golem_outbox_delivery" WHERE "causation_id"=?`, recent); err != nil {
		t.Fatal(err)
	}
	if available != micros(-3*time.Hour) {
		t.Fatalf("retention rewrote an undeletable delivered row: available_at=%d want=%d", available, micros(-3*time.Hour))
	}
}

func TestSQLiteFactByteBudgetMeasuresBytesNotCharacters(t *testing.T) {
	provider := New()
	database := openEventDeliveryFixture(t, provider)
	cause := deliveryUUID(840)
	codec := "golem.fact.v1.ünïcødé"
	event := deliveryUUID(841)
	fingerprint := fmt.Sprintf("%064x", 1)
	model := fmt.Sprintf("%032x", 2)
	_, err := database.Exec(`INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?)`, event, 1, codec, fingerprint, model, "created", []byte{1}, cause, 1, []byte{7}, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(event) + len(codec) + len(fingerprint) + len(model) + len("created") + 1 + len(cause) + 1 + 32)
	var measured int64
	if err := database.Get(&measured, `SELECT SUM(`+sqliteFactByteExpression+`) FROM "_golem_outbox" WHERE "causation_id"=?`, cause); err != nil {
		t.Fatal(err)
	}
	if measured != want {
		t.Fatalf("claim budget measured %d units for %d bytes", measured, want)
	}
}

func warmLeaseToken(t *testing.T, database *sqlx.DB, causation string) string {
	t.Helper()
	var token string
	if err := database.Get(&token, `SELECT "lease_token" FROM "_golem_outbox_delivery" WHERE "causation_id"=?`, causation); err != nil {
		t.Fatal(err)
	}
	return token
}

func insertLegacyDelivery(t *testing.T, database *sqlx.DB, causation, status string, availableAt int64, leaseUntil, deliveredAt sql.NullInt64, leaseToken sql.NullString) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO "_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","lease_token","lease_until","delivered_at","updated_at") VALUES (?,?,?,?,?,?,?,?,?)`, causation, status, availableAt, 1, availableAt, leaseToken, leaseUntil, deliveredAt, availableAt)
	if err != nil {
		t.Fatal(err)
	}
}

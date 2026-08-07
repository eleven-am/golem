package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
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

func TestP7SQLiteDeliveryClaimFenceOperatorAndRetention(t *testing.T) {
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

func TestP7SQLiteConcurrentWorkersClaimWholeCausationsExclusively(t *testing.T) {
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

func TestP7SQLiteMissingStateMaterializationIsBoundedPerClaim(t *testing.T) {
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

func TestP7SQLiteNewCausalDeliveryInsertIsIdempotentAndConflictSpecific(t *testing.T) {
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

func TestP7SQLiteMissingStateInspectionAndRetireAreClosed(t *testing.T) {
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

func TestP7SQLiteExpiredTokenCanRenewUntilDatabaseReownershipThenIsFenced(t *testing.T) {
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

func TestP7SQLiteDeliveryProviderCommonHarness(t *testing.T) {
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

package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

func restoreQueueValidationFixture(t *testing.T) (*sqlx.DB, queueprovider.Store) {
	t.Helper()
	provider := New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := provider.QueueStore(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{ID: "job", Type: "job.test", Payload: []byte(`{"value":1}`), MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	return database, store
}

func TestRestoreQueueRowsRejectMalformedStorage(t *testing.T) {
	for _, scenario := range []struct{ name, update, diagnostic string }{
		{"oversized payload", `payload=zeroblob(1048577)`, "SQL types or oversized"},
		{"text payload", `payload='{}'`, "SQL types or oversized"},
		{"empty payload", `payload=zeroblob(0)`, "SQL types or oversized"},
		{"oversized identity", `id=printf('%065d',1)`, "SQL types or oversized"},
		{"oversized multibyte key", `dedupe_key=replace(printf('%0130d',0),'0','é')`, "SQL types or oversized"},
		{"unknown state", `status='forgotten'`, "unknown state"},
		{"invalid type", `type='NOT CANONICAL'`, "envelope"},
		{"invalid code", `last_code='NOT CANONICAL'`, "code"},
		{"negative attempts", `attempt_count=-1`, "negative attempt"},
		{"zero max attempts", `max_attempts=0`, "envelope"},
		{"text date", `available_at='tomorrow'`, "SQL types or oversized"},
		{"real date", `updated_at=1.5`, "SQL types or oversized"},
		{"bad optional date", `cancel_requested_at=x'00'`, "SQL types or oversized"},
		{"missing lease deadline", `status='leased',lease_token='lease-token'`, "no deadline"},
		{"empty lease token", `status='leased',lease_token='',lease_until=1`, "lease"},
		{"unleased token", `lease_token='lease-token'`, "unleased"},
		{"terminal missing finish", `status='failed'`, "finished time"},
		{"pending finish", `finished_at=1`, "finished time"},
		{"partial resource", `status='leased',lease_token='lease-token',lease_until=1,resource_name='pool'`, "incomplete resource"},
		{"over capacity", `status='leased',lease_token='lease-token',lease_until=1,resource_name='pool',resource_cost=2,resource_capacity=1`, "resource"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			database, _ := restoreQueueValidationFixture(t)
			if _, err := database.Exec(`UPDATE golem_queue SET ` + scenario.update); err != nil {
				t.Fatal(err)
			}
			err := validateRestoreQueueRows(context.Background(), database)
			if err == nil || !strings.Contains(err.Error(), scenario.diagnostic) {
				t.Fatalf("validation=%v, want %s", err, scenario.diagnostic)
			}
		})
	}
}

func TestRestoreQueueRowsAcceptProducerAndReleasedFinalizerStates(t *testing.T) {
	database, store := restoreQueueValidationFixture(t)
	ctx := context.Background()
	check := func() {
		t.Helper()
		if err := validateRestoreQueueRows(ctx, database); err != nil {
			t.Fatal(err)
		}
	}
	check()
	records, err := store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"job.test"}, Limit: 1, LeaseDuration: time.Minute, Resource: &queueprovider.ClaimResource{Name: "pool", Concurrency: 2, Costs: map[string]int64{"job.test": 1}}})
	if err != nil || len(records) != 1 {
		t.Fatalf("claim=%v, %v", records, err)
	}
	check()
	// Provider identities are bounded strings, not UUIDs.
	if _, err := database.Exec(`UPDATE golem_queue SET lease_token='lease-token'`); err != nil {
		t.Fatal(err)
	}
	check()
	if ok, err := store.DeferFailure(ctx, "job", "lease-token", queueprovider.CodeTerminal); err != nil || !ok {
		t.Fatalf("defer=%v,%v", ok, err)
	}
	if ok, err := store.Release(ctx, "job", "lease-token"); err != nil || !ok {
		t.Fatalf("release=%v,%v", ok, err)
	}
	check()
	var token *string
	var state string
	if err := database.QueryRow(`SELECT status,lease_token FROM golem_queue`).Scan(&state, &token); err != nil {
		t.Fatal(err)
	}
	if state != "leased" || token != nil {
		t.Fatalf("released finalizer=%s,%v", state, token)
	}
	// Offline reconciliation uses this same tokenless expired finalizer shape.
	records, err = store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"job.test"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(records) != 1 {
		t.Fatalf("reclaim=%v,%v", records, err)
	}
	if ok, err := store.Fail(ctx, nil, "job", records[0].LeaseToken, "terminal"); err != nil || !ok {
		t.Fatalf("fail=%v,%v", ok, err)
	}
	check()
}

func TestRestoreQueueRowsAcceptProducerBounds(t *testing.T) {
	database, store := restoreQueueValidationFixture(t)
	_, err := store.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{
		ID: strings.Repeat("i", queueprovider.MaximumIdentityBytes), Type: strings.Repeat("t", 128),
		Payload: make([]byte, queueprovider.MaximumPayloadBytes), MaxAttempts: 1,
		DedupeKey: strings.Repeat("d", queueprovider.MaximumKeyBytes), ExclusiveKey: strings.Repeat("e", queueprovider.MaximumKeyBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreQueueRows(context.Background(), database); err != nil {
		t.Fatal(err)
	}
}

package outbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestP7CorruptFactBlocksAndRemainsInspectable(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewSubscribedIndexed(t)
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "corrupt-fact.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}

	seed := publisherValidLease(t, fixture)
	row := p7OutboxRowFromLease(seed.Facts[0])
	statements, err := mutationfact.RenderInserts(policyir.ProviderSQLite, []mutationfact.OutboxRow{row}, 999)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement.SQL(), statement.Args()...); err != nil {
			t.Fatal(err)
		}
	}
	delivery, err := mutationfact.RenderDeliveryInsertAt(policyir.ProviderSQLite, "main", []mutationfact.OutboxRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, delivery.SQL(), delivery.Args()...); err != nil {
		t.Fatal(err)
	}
	const corruptModel = "000000000000000000000000000000ff"
	if _, err := database.ExecContext(ctx, `UPDATE "_golem_outbox" SET "model_id"=? WHERE "event_id"=?`, corruptModel, row.EventID); err != nil {
		t.Fatal(err)
	}

	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 {
		t.Fatalf("claim leases=%d error=%v", len(leases), err)
	}
	transport := &captureTransport{}
	publisher := publisherForTest(t, coordinator, publisherTestResolver{fixture.Registry}, transport)
	if err := publisher.publishLease(ctx, leases[0]); err != nil {
		t.Fatal(err)
	}
	if len(transport.batches) != 0 {
		t.Fatal("corrupt fact reached transport")
	}
	state, err := coordinator.Inspect(ctx, row.CausationID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != eventprovider.StatusBlocked || state.LastFailureCode != "fact-invalid" || state.ImmutableFactRows != 1 || state.BlockedAt == nil {
		t.Fatalf("blocked state=%#v", state)
	}
	var factCount int
	if err := database.GetContext(ctx, &factCount, `SELECT count(*) FROM "_golem_outbox" WHERE "causation_id"=? AND "model_id"=?`, row.CausationID, corruptModel); err != nil || factCount != 1 {
		t.Fatalf("inspectable immutable corrupt facts=%d error=%v", factCount, err)
	}
}

func p7OutboxRowFromLease(row eventprovider.FactRow) mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
		GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
		BeforeIdentity: row.BeforeIdentity, AfterIdentity: row.AfterIdentity, CausationID: row.CausationID,
		TransactionOrdinal: row.TransactionOrdinal, Metadata: row.Metadata, DeleteSnapshot: row.DeleteSnapshot,
		RecordedAt: row.RecordedAt,
	}
}

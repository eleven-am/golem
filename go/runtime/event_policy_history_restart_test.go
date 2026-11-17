package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

// Open starts no workers and App has no Close method. This helper is used only
// before any publisher, queue worker, or subscription has been started. End the
// old context and verified database ownership before constructing fresh owners.
func eventHistoryRestart(t *testing.T, fixture policyFactRuntimeFixture) policyFactRuntimeFixture {
	t.Helper()
	old := fixture.app
	if old.EventCapabilities().PublisherRunning() || fixture.applicationConfig.Queue != nil {
		t.Fatal("restart helper requires no started publisher or configured queue")
	}
	fixture.stop()
	if err := fixture.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := old.database.PingContext(context.Background()); err == nil {
		t.Fatal("original database still accepts reads after shutdown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	database, err := publicsqlite.Open(ctx, fixture.databaseConfig)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	t.Cleanup(cancel)
	config := fixture.applicationConfig
	config.Database = database
	app, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("real startup refused the persisted event backlog: %v", err)
	}
	if app == old || app.database == old.database || app.registry == old.registry || app.eventSchemas == old.eventSchemas {
		t.Fatal("restart reused the original application or schema ownership")
	}
	if app.EventCapabilities().PublisherRunning() {
		t.Fatal("reopening the application unexpectedly started its publisher")
	}
	fixture.app, fixture.ctx, fixture.stop, fixture.applicationConfig = app, ctx, cancel, config
	return fixture
}

func eventHistoryRestartCausation(t *testing.T, value string) golem.CausationID {
	t.Helper()
	id, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return golem.CausationID(id)
}

func eventHistoryRestartWaitDelivery(t *testing.T, fixture policyFactRuntimeFixture, causal string, status events.DeliveryStatus) events.Delivery {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		delivery, err := fixture.app.EventOperator().Inspect(ctx, eventHistoryRestartCausation(t, causal))
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Status() == status {
			return delivery
		}
		if delivery.Status() == events.DeliveryBlocked || delivery.Status() == events.DeliveryDelivered {
			t.Fatalf("causation reached %s/%s, want %s", delivery.Status(), delivery.FailureCode(), status)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("causation did not reach %s: %v", status, ctx.Err())
		case <-ticker.C:
		}
	}
}

// An uncaptured ordinary model is a genuine V3 producer in both binary modes.
// Its normal transaction owns the fact version; no stored metadata is changed
// to manufacture a mixed-version backlog beside the original V4 causation.
func eventHistoryRestartOrdinaryNeighbor(t *testing.T, fixture policyFactRuntimeFixture, id byte, body string) string {
	t.Helper()
	if len(fixture.app.registry.PolicyCaptureFields(fixture.schema.Comment)) != 0 {
		t.Fatal("ordinary restart neighbor must remain outside the capture inventory")
	}
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxCreate(fixture.ctx, transaction, mutationHistoryGraphComments(fixture.schema), mutationHistoryGraphCommentInput(fixture.schema, id, 131, body)); err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		if len(state.facts) != 1 {
			t.Fatalf("ordinary neighbor emitted %d facts, want1", len(state.facts))
		}
		causal = state.facts[0].CausationID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.FormatVersion) || rows[0].CodecIdentity != mutationfact.CodecIdentity {
		t.Fatalf("uncaptured ordinary neighbor was not persisted as V3: %#v", rows)
	}
	fact, err := mutationfact.ValidateStoredRow(rows[0], fixture.app.eventSchemas)
	if err != nil || golem.ModelID(fact.ModelID()) != fixture.schema.Comment || fact.Action() != mutationir.FactCreated {
		t.Fatalf("ordinary restart neighbor has the wrong source model/action: %v", err)
	}
	return causal
}

func TestEventPolicyHistoryRestartReadsAndPublishesPendingV3AndV4(t *testing.T) {
	fixture, originalHistory := eventPolicyHistoryFixture(t)
	historicalCausal := originalHistory[0].CausationID
	legacyCausal := eventHistoryRestartOrdinaryNeighbor(t, fixture, 233, "legacy-before-restart")
	for _, causal := range []string{legacyCausal, historicalCausal} {
		eventHistoryRestartWaitDelivery(t, fixture, causal, events.DeliveryPending)
	}
	fixture = eventHistoryRestart(t, fixture)
	var expected []mutationfact.OutboxRow
	for _, group := range []struct {
		causal  string
		version int64
		count   int
	}{{legacyCausal, 3, 1}, {historicalCausal, 4, 2}} {
		rows := mutationHistorySealStoredRows(t, fixture, group.causal)
		if len(rows) != group.count {
			t.Fatalf("persisted group has %d rows, want %d", len(rows), group.count)
		}
		for _, row := range rows {
			decoded, err := mutationfact.ValidateStoredRow(row, fixture.app.eventSchemas)
			if err != nil || row.FactVersion != group.version || int64(decoded.FormatVersion()) != group.version {
				t.Fatalf("fresh reader refused stored V%d: %v", group.version, err)
			}
		}
		expected = append(expected, rows...)
	}
	historical := mutationHistorySealStoredRows(t, fixture, historicalCausal)
	wire := eventPolicyHistoryWire(t, fixture, historical[1])
	if wire.TransactionOrdinal() != 2 {
		t.Fatal("restart recovery must begin from the non-anchor notice")
	}
	evidence, err := fixture.app.policyHistoryForNotice(fixture.ctx, &eventNoticeShared{envelope: wire})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := mutationfact.DecodePolicyHistoryCausation(historical, fixture.app.eventSchemas)
	if err != nil || evidence.value.Digest() != stored.Digest() || evidence.registry != fixture.app.registry || len(evidence.value.Graph().Roots()) != 1 {
		t.Fatalf("fresh loader failed to recover ordinal-one history: %v", err)
	}
	// Restart preserves the actual binary's writer contract. This ordinary
	// uncaptured model remains V3 even when captured writes require V4.
	freshCausal := eventHistoryRestartOrdinaryNeighbor(t, fixture, 234, "legacy-after-restart")
	freshRows := mutationHistorySealStoredRows(t, fixture, freshCausal)
	if len(freshRows) != 1 || freshRows[0].FactVersion != 3 || freshRows[0].CodecIdentity != "golem.fact.v3" {
		t.Fatal("fresh application changed the uncaptured ordinary writer from V3")
	}
	expected = append(expected, freshRows...)
	fixture.startPublisher(t)
	for _, causal := range []string{legacyCausal, historicalCausal, freshCausal} {
		eventHistoryRestartWaitDelivery(t, fixture, causal, events.DeliveryDelivered)
	}
	wanted := map[golem.EventID]mutationfact.OutboxRow{}
	for _, row := range expected {
		decoded, err := mutationfact.ValidateStoredRow(row, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		wanted[golem.EventID(decoded.EventID())] = row
	}
	for range expected {
		select {
		case notice := <-fixture.transport.published:
			row, present := wanted[notice.EventID()]
			if !present {
				t.Fatal("fresh publisher duplicated or fabricated a notice")
			}
			delete(wanted, notice.EventID())
			decoded, err := eventcodec.Decode(notice.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
			if err != nil || decoded.EventID() != notice.EventID() || decoded.CausationID() != eventHistoryRestartCausation(t, row.CausationID) || int64(decoded.Fact().FormatVersion()) != row.FactVersion || int64(decoded.TransactionOrdinal()) != row.TransactionOrdinal {
				t.Fatalf("fresh publisher changed stored event identity/version/order: %v", err)
			}
			if _, embedded := decoded.Fact().PolicyHistoryCompanion(); embedded {
				t.Fatal("publisher leaked the private graph into transport")
			}
		default:
			t.Fatal("delivered group did not publish every stored fact")
		}
	}
	select {
	case <-fixture.transport.published:
		t.Fatal("fresh publisher emitted extra notices")
	default:
	}
}

func TestEventPolicyHistoryRestartBlocksMissingOrMalformedCompanion(t *testing.T) {
	for _, name := range []string{"missing", "malformed"} {
		t.Run(name, func(t *testing.T) {
			fixture, rows := eventPolicyHistoryFixture(t)
			legacyCausal := eventHistoryRestartOrdinaryNeighbor(t, fixture, 233, "valid-legacy-neighbor")
			var companion any
			if name == "malformed" {
				companion = []byte("private-companion-corruption")
			}
			// Corruption is confined to this disposable file, before all original
			// owners are closed. No valid metadata or ordinal is rewritten.
			eventPolicyHistoryExec(t, fixture, `UPDATE _golem_outbox SET delete_snapshot=? WHERE event_id=?`, companion, rows[0].EventID)
			fixture = eventHistoryRestart(t, fixture)
			freshRows := mutationHistorySealStoredRows(t, fixture, rows[0].CausationID)
			if len(freshRows) != 2 {
				t.Fatal("companion damage changed the group's fact count")
			}
			wire := eventPolicyHistoryWire(t, fixture, freshRows[1])
			eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: wire})
			fixture.startPublisher(t)
			blocked := eventHistoryRestartWaitDelivery(t, fixture, rows[0].CausationID, events.DeliveryBlocked)
			if blocked.FailureCode() != "policy-history-invalid" || blocked.FactRows() != 2 {
				t.Fatalf("damaged group lost its sanitized whole-causation refusal: %s rows=%d", blocked.FailureCode(), blocked.FactRows())
			}
			eventHistoryRestartWaitDelivery(t, fixture, legacyCausal, events.DeliveryDelivered)
			select {
			case notice := <-fixture.transport.published:
				if notice.CausationID() != eventHistoryRestartCausation(t, legacyCausal) {
					t.Fatal("fresh publisher published a fact from the damaged group")
				}
			default:
				t.Fatal("damaged group prevented the valid V3 neighbor from publishing")
			}
			select {
			case <-fixture.transport.published:
				t.Fatal("fresh publisher partially published the damaged V4 causation")
			default:
			}
		})
	}
}

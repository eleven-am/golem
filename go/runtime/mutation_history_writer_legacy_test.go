package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

type mutationHistoryLegacyCDCAdapter struct{ starts atomic.Int64 }

func (*mutationHistoryLegacyCDCAdapter) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "legacy-history-regression", Version: "1", Provider: golem.SQLite}
}
func (*mutationHistoryLegacyCDCAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}
func (adapter *mutationHistoryLegacyCDCAdapter) Run(context.Context, events.CDCEmitter) error {
	adapter.starts.Add(1)
	return nil
}

func TestMutationHistoryWriterRejectsCDCAtPublicOpen(t *testing.T) {
	requireMutationHistoryWriter(t)
	fixture := eventHistoryScopeFixture(t, false)
	if fixture.app.registry.PolicyCaptureFingerprint() == (golem.SchemaDigest{}) {
		t.Fatal("startup refusal requires registered capture inventory")
	}
	adapter := &mutationHistoryLegacyCDCAdapter{}
	if _, err := events.ValidateCDCAdapters(golem.SQLite, []events.CDCAdapter{adapter}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	config := fixture.applicationConfig
	config.CDCAdapters = []events.CDCAdapter{adapter}
	app, err := Open(ctx, config)
	if err == nil || app != nil {
		t.Fatalf("history writer accepted an adapter that emits only V3: app=%v err=%v", app != nil, err)
	}
	// This must be a configuration refusal, not an unrelated fixture startup
	// failure. Open must not try to discover unsupported behavior by running CDC.
	if !strings.Contains(err.Error(), string(events.CodeEventConfig)) {
		t.Fatalf("CDC history refusal is not an event configuration error: %v", err)
	}
	if adapter.starts.Load() != 0 {
		t.Fatal("rejected CDC adapter started")
	}
}

func TestMutationHistoryLegacyCDCStartupCompatibility(t *testing.T) {
	for _, captured := range []bool{false, true} {
		name := "no-template"
		if captured {
			name = "reader3-capture-template"
		}
		t.Run(name, func(t *testing.T) {
			if captured && mutationHistoryWriterVersion == mutationfact.HistoryFactVersion {
				t.Skip("writer4 capture startup refusal has its own acceptance")
			}
			options := policyFactRuntimeOptions{rootNoFacts: true}
			if captured {
				options.schemaTransform = func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }
			}
			fixture := newPolicyFactRuntimeFixture(t, false, options)
			adapter := &mutationHistoryLegacyCDCAdapter{}
			ctx, cancel := context.WithCancel(fixture.ctx)
			defer cancel()
			config := fixture.applicationConfig
			config.CDCAdapters = []events.CDCAdapter{adapter}
			app, err := Open(ctx, config)
			if err != nil {
				t.Fatalf("compatible ordinary CDC startup refused: %v", err)
			}
			if !app.EventCapabilities().ExternalWritesObserved() || len(app.EventCapabilities().CDCAdapterIdentities()) != 1 {
				t.Fatal("compatible CDC adapter disappeared from capabilities")
			}
			if adapter.starts.Load() != 0 || app.EventCapabilities().PublisherRunning() {
				t.Fatal("Open unexpectedly started event workers")
			}
		})
	}
}

// Produce a genuinely supported V3 CDC envelope, as retained by an older
// installation. Starting a CDC producer in history mode is deliberately
// forbidden; codec compatibility must still permit reading its old bytes.
func mutationHistoryLegacyCDCDelete(t *testing.T, fixture policyFactRuntimeFixture, row golem.RuntimeModelRow) eventcodec.Envelope {
	t.Helper()
	encoder, err := newCDCEventEncoder(fixture.app.registry, fixture.app.eventSchemas, 0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encoder.EncodeCDC(fixture.ctx, eventcdc.EncodeInput{
		Adapter:             (&mutationHistoryLegacyCDCAdapter{}).Identity(),
		SourceTransactionID: "retained-cdc-transaction", Cursor: []byte("retained-cdc-cursor"),
		RecordedAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC),
		EventID:    golem.EventID{15: 231}, CausationID: golem.CausationID{15: 232},
		Ordinal: 1, Version: 7, Model: row.ModelID(), Action: golem.EventDeleted, Before: &row,
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventcodec.Decode(encoded.Notice.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Fact().FormatVersion() != mutationfact.FormatVersion {
		t.Fatal("legacy acceptance accidentally acquired history evidence")
	}
	if _, present := wire.Fact().PolicyHistory(); present {
		t.Fatal("CDC envelope unexpectedly has a certified graph")
	}
	if _, present := wire.Fact().PrivateDeleteSnapshot(); !present {
		t.Fatal("CDC envelope lacks the before image needed to expose live routing")
	}
	return wire
}

func TestMutationHistoryWriterRefusesLegacyFactBeforeLiveReparentProbe(t *testing.T) {
	requireMutationHistoryWriter(t)
	extra := schematest.NewPolicyCaptureOrdinaryGuardGraph(t)
	fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		ordinaryGuardGraph: &extra, postMutationGrants: true, userNameScope: true,
	})
	fixture.mutate(t, golem.EventCreated, 233, 1, "video")
	comments := mutationHistoryGraphComments(fixture.schema)
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, 234, 233, "ready")); err != nil {
		t.Fatal(err)
	}
	// Read the actual physical row version instead of inventing an image
	// version after automatic writes have advanced the provider clock.
	versionField, present := fixture.app.registry.PhysicalField(golem.SQLite, fixture.schema.Comment, fixture.schema.CommentRowVersion)
	if !present {
		t.Fatal("Comment version field missing")
	}
	idField, present := fixture.app.registry.PhysicalField(golem.SQLite, fixture.schema.Comment, fixture.schema.CommentID)
	if !present {
		t.Fatal("Comment identity field missing")
	}
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	table, idColumn := quote(string(versionField.TableName())), quote(string(idField.ColumnName()))
	var version int64
	if err := fixture.app.database.GetContext(fixture.ctx, &version, `SELECT `+quote(string(versionField.ColumnName()))+` FROM `+table+` WHERE `+idColumn+`=?`, (golem.UUID{15: 234}).String()); err != nil {
		t.Fatal(err)
	}
	before, err := golem.RuntimeCDCModelRow(fixture.schema.Comment,
		golem.RuntimePresentReadCell(fixture.schema.CommentID, golem.UUID{15: 234}, nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentPostID, golem.UUID{15: 233}, nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentBody, "ready", nil),
		golem.RuntimePresentReadCell(fixture.schema.CommentRowVersion, version, nil))
	if err != nil {
		t.Fatal(err)
	}
	// A is an external delete represented by a production CDC envelope. B
	// reparents its surviving intermediate Post before that old notice arrives.
	wire := mutationHistoryLegacyCDCDelete(t, fixture, before)
	if _, err := fixture.app.database.ExecContext(fixture.ctx, `DELETE FROM `+table+` WHERE `+idColumn+`=?`, (golem.UUID{15: 234}).String()); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 233), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	metadata, present := fixture.app.eventRegistry.Lookup(fixture.schema.Comment)
	if !present {
		t.Fatal("Comment event metadata missing")
	}
	for _, actor := range []byte{1, 2} {
		caller := eventHistoryScopeCaller(t, fixture, actor)
		plan := eventHistoryRoutePlan(t, fixture, caller)
		ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
		changed, err := fixture.app.historicalFactDependencyChanged(ctx, caller, fixture.schema.User, &eventNoticeShared{envelope: wire}, plan)
		observeexec.Finish(span, observe.OutcomeRefused, observe.ReasonNone)
		var refusal *events.Error
		if changed || span.StatementCount() != 0 || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
			t.Errorf("actor%d legacy notice used a live fact path: changed=%v statements=%d err=%v", actor, changed, span.StatementCount(), err)
			continue
		}
		if refusal.Reason() != "" || refusal.Detail() != "" || errors.Unwrap(refusal) != nil {
			t.Errorf("actor%d refusal leaked source attribution: %v", actor, refusal)
		}
	}
	// Ordinary consumers retain their existing scalar before-image semantics.
	// This is an ordinary member control, not a physical fact subscriber.
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.schema.Comment), true)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := &eventDependencyMember{metadata: metadata, membership: true, judge: &eventJudgeSpec{row: truth}}
	if changed, err := fixture.app.dependencyChanged(ordinary, &eventNoticeShared{envelope: wire}); err != nil || !changed {
		t.Fatalf("ordinary V3 consumer was disabled with physical fact routing: changed=%v err=%v", changed, err)
	}
}

func TestMutationHistoryLegacyNoTemplateOrdinaryRoutingCompatibility(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true})
	if fixture.app.registry.PolicyCaptureFingerprint() != (golem.SchemaDigest{}) {
		t.Fatal("compatibility fixture unexpectedly requires captured history")
	}
	fixture.mutate(t, golem.EventCreated, 235, 1, "ready")
	fixture.mutate(t, golem.EventDeleted, 235, 1, "ready")
	var causal string
	if err := fixture.app.database.GetContext(fixture.ctx, &causal, `SELECT causation_id FROM _golem_outbox WHERE action=?`, string(golem.EventDeleted)); err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.FormatVersion) {
		t.Fatal("ordinary no-template writer must produce an actual V3 group")
	}
	wire := eventPolicyHistoryWire(t, fixture, rows[0])
	metadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.schema.Post), true)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := &eventDependencyMember{metadata: metadata, membership: true, judge: &eventJudgeSpec{row: truth}}
	if changed, err := fixture.app.dependencyChanged(ordinary, &eventNoticeShared{envelope: wire}); err != nil || !changed {
		t.Fatalf("ordinary V3 delete stopped invalidating: changed=%v err=%v", changed, err)
	}
	// Missing history is refused even without registered capture templates;
	// no binary/schema combination can restore the retired physical live join.
	ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
	changed, err := fixture.app.historicalFactDependencyChanged(ctx, caller, fixture.schema.User, &eventNoticeShared{envelope: wire}, observation.Plan{})
	observeexec.Finish(span, observe.OutcomeRefused, observe.ReasonNone)
	var refusal *events.Error
	if changed || span.StatementCount() != 0 || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation || refusal.Reason() != "" || refusal.Detail() != "" {
		t.Fatalf("no-template V3 acquired physical authority: changed=%v statements=%d err=%v", changed, span.StatementCount(), err)
	}
}

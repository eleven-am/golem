package runtime

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

func eventPolicyHistoryFixture(t *testing.T) (policyFactRuntimeFixture, []mutationfact.OutboxRow) {
	t.Helper()
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		for _, id := range []byte{131, 132} {
			if err := mutationHistorySealCreate(fixture, transaction, id); err != nil {
				return err
			}
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		state, err := transaction.system.executor.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		return state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas)
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 2 {
		t.Fatalf("stored causation facts=%d want 2", len(rows))
	}
	return fixture, rows
}

func eventPolicyHistoryWire(t *testing.T, fixture policyFactRuntimeFixture, row mutationfact.OutboxRow) eventcodec.Envelope {
	t.Helper()
	encoded, err := eventcodec.EncodeStoredRow(row, fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := eventcodec.Decode(encoded.Encoded(), fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := decoded.Fact().PolicyHistoryCompanion(); present {
		t.Fatal("wire fixture retained the storage anchor graph")
	}
	return decoded
}

func eventPolicyHistoryExec(t *testing.T, fixture policyFactRuntimeFixture, query string, args ...any) {
	t.Helper()
	if _, err := fixture.app.database.ExecContext(fixture.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func eventPolicyHistoryRefused(t *testing.T, fixture policyFactRuntimeFixture, shared *eventNoticeShared) {
	t.Helper()
	evidence, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared)
	var refusal *events.Error
	if evidence != nil || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation {
		t.Fatalf("history refusal: evidence present=%v error=%v", evidence != nil, err)
	}
	if err.Error() != events.Failure(events.CodeSubscriptionRevalidation).Error() || strings.Contains(err.Error(), "private-graph-value") {
		t.Fatal("history refusal disclosed stored private evidence or driver details")
	}
}

func TestEventPolicyHistoryLoadsAnchorForSecondNoticeAfterReparent(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	wire := eventPolicyHistoryWire(t, fixture, rows[1])
	// A committed under User 1; a later independent causation moves its Post.
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 132), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	var author string
	if err := fixture.app.database.GetContext(fixture.ctx, &author, `SELECT author_id FROM posts WHERE id=?`, golem.UUID{15: 132}.String()); err != nil || author != (golem.UUID{15: 2}).String() {
		t.Fatalf("later causation did not reparent the current Post: author=%q err=%v", author, err)
	}
	shared := &eventNoticeShared{envelope: wire}
	evidence, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	exact, known := fixture.app.eventSchemas.ResolvePolicyHistorySchema(want.Generation(), want.ProjectionFingerprint())
	if !known || evidence.registry != exact || evidence.value.Digest() != want.Digest() || !reflect.DeepEqual(evidence.value.Graph().Input(), want.Graph().Input()) {
		t.Fatal("loader did not recover the exact committed producer graph")
	}
	graph := evidence.value.Graph()
	if len(graph.Roots()) != 1 || mutationHistoryGraphRowID(t, graph.Roots()[0].Identity, fixture.schema.UserID) != 1 {
		t.Fatal("historical source root followed the later current relationship")
	}
	children, complete := graph.Children(policyhistory.After, graph.RootKeys()[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
	if !complete || len(children) != 2 {
		t.Fatal("historical User 1 extent lost its complete two-Post evidence")
	}
	for _, child := range children {
		if mutationHistoryGraphRowID(t, child, fixture.schema.AuthorID) != 1 {
			t.Fatal("historical child followed the current User 2 relationship")
		}
	}
	owned := evidence.value.Encoded()
	owned[0] ^= 1
	if bytes.Equal(owned, evidence.value.Encoded()) {
		t.Fatal("historical bytes expose mutable cache ownership")
	}
	// Retention after the first load cannot alter this notice's immutable cache.
	eventPolicyHistoryExec(t, fixture, `DELETE FROM _golem_outbox WHERE causation_id=?`, rows[0].CausationID)
	cached, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared)
	if err != nil || cached != evidence {
		t.Fatalf("per-notice evidence cache was reloaded: %v", err)
	}
	eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: wire})
}

func TestEventPolicyHistoryRejectsDamagedStoredCausation(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*testing.T, policyFactRuntimeFixture, []mutationfact.OutboxRow)
	}{
		{"missing anchor", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `DELETE FROM _golem_outbox WHERE causation_id=? AND transaction_ordinal=1`, rows[0].CausationID)
		}},
		{"malformed private graph", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET delete_snapshot=? WHERE event_id=?`, []byte("private-graph-value-corrupt"), rows[0].EventID)
		}},
		{"mixed fact versions", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET fact_version=3 WHERE event_id=?`, rows[0].EventID)
		}},
		{"invalid commitment", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET recorded_at=recorded_at+1 WHERE event_id=?`, rows[0].EventID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, rows := eventPolicyHistoryFixture(t)
			wire := eventPolicyHistoryWire(t, fixture, rows[1])
			test.damage(t, fixture, rows)
			eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: wire})
		})
	}
}

func TestEventPolicyHistoryRejectsValidWireWithoutExactStoredMembership(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	original := eventPolicyHistoryWire(t, fixture, rows[1]).Fact()
	for _, change := range []string{"event ID", "row version", "recorded time"} {
		t.Run(change, func(t *testing.T) {
			row := rows[1]
			if change == "recorded time" {
				row.RecordedAt = row.RecordedAt.Add(time.Microsecond)
			} else {
				after, err := mutationdecode.NewRow(fixture.app.registry, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{
					mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 132})),
				})
				if err != nil {
					t.Fatal(err)
				}
				requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}, nil)
				if err != nil {
					t.Fatal(err)
				}
				requirement, err = requirement.WithEventSchema([32]byte(original.EventSchemaDigest()))
				if err != nil {
					t.Fatal(err)
				}
				event, version := original.EventID(), original.RowVersion()
				if change == "event ID" {
					event[0] ^= 1
				} else {
					version++
				}
				fact, err := mutationfact.New(fixture.app.registry, original.EventSchemaDigest(), event, requirement, original.CausationID(), 2, version, nil, &after)
				if err != nil {
					t.Fatal(err)
				}
				reference, _ := original.PolicyHistory()
				fact, err = fact.WithPolicyHistory(reference)
				if err != nil {
					t.Fatal(err)
				}
				row, err = fact.OutboxRow(rows[1].RecordedAt)
				if err != nil {
					t.Fatal(err)
				}
			}
			wire := eventPolicyHistoryWire(t, fixture, row)
			eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: wire})
		})
	}
}

func eventPolicyHistoryProjectedRows(t *testing.T, fixture policyFactRuntimeFixture, causal string) int {
	t.Helper()
	query, args, err := fixture.app.policyHistoryStatement(causal)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.app.database.QueryxContext(fixture.ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestEventPolicyHistoryBoundsExcludePayloadProjection(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*testing.T, policyFactRuntimeFixture, []mutationfact.OutboxRow)
	}{
		{"binary evidence bytes", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET delete_snapshot=zeroblob(?) WHERE event_id=?`, MaxMutationOutboxBytes+1, rows[0].EventID)
		}},
		{"header bytes", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET generation_fingerprint=? WHERE event_id=?`, strings.Repeat("x", maxEventPolicyHistoryHeaderBytes+1), rows[0].EventID)
		}},
		{"causation fact count", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `WITH RECURSIVE ids(n) AS (SELECT 3 UNION ALL SELECT n+1 FROM ids WHERE n<?) INSERT INTO _golem_outbox (`+runtimeOutboxFactColumns+`) SELECT printf('00000000-0000-4000-8000-%012d',n),fact_version,codec_identity,generation_fingerprint,model_id,action,before_identity,after_identity,causation_id,n,X'00',NULL,recorded_at FROM _golem_outbox,ids WHERE event_id=?`, MaxMutationFacts+1, rows[1].EventID)
		}},
		{"all V3", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET fact_version=3 WHERE causation_id=?`, rows[0].CausationID)
		}},
		{"mixed V3 and V4", func(t *testing.T, f policyFactRuntimeFixture, rows []mutationfact.OutboxRow) {
			eventPolicyHistoryExec(t, f, `UPDATE _golem_outbox SET fact_version=3 WHERE event_id=?`, rows[0].EventID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, rows := eventPolicyHistoryFixture(t)
			wire := eventPolicyHistoryWire(t, fixture, rows[1])
			if got := eventPolicyHistoryProjectedRows(t, fixture, rows[0].CausationID); got != 2 {
				t.Fatalf("valid V4 projected rows=%d want 2", got)
			}
			test.damage(t, fixture, rows)
			if got := eventPolicyHistoryProjectedRows(t, fixture, rows[0].CausationID); got != 0 {
				t.Fatalf("refused evidence projected %d payload rows", got)
			}
			eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: wire})
		})
	}
}

func TestEventPolicyHistoryStatementUsesCausationIndexAndProviderDialect(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	query, args, err := fixture.app.policyHistoryStatement(rows[0].CausationID)
	if err != nil {
		t.Fatal(err)
	}
	var plan []struct {
		ID     int    `db:"id"`
		Parent int    `db:"parent"`
		Unused int    `db:"notused"`
		Detail string `db:"detail"`
	}
	if err := fixture.app.database.SelectContext(fixture.ctx, &plan, "EXPLAIN QUERY PLAN "+query, args...); err != nil {
		t.Fatal(err)
	}
	searches := 0
	for _, step := range plan {
		upper := strings.ToUpper(step.Detail)
		if !strings.Contains(upper, "_GOLEM_OUTBOX") {
			continue
		}
		if strings.Contains(upper, "SCAN ") {
			t.Fatalf("history lookup scans the full outbox: %s", step.Detail)
		}
		if strings.Contains(upper, "SEARCH ") && strings.Contains(upper, "CAUSATION_ID=?") {
			searches++
		}
	}
	if searches < 2 {
		t.Fatalf("history bounds and payload reads must use the causation index: %#v", plan)
	}
	postgres := &App[policyFactRuntimePrincipal, policyFactRuntimeActor]{registry: fixture.app.registry, database: fixture.app.database, provider: policyir.ProviderPostgreSQL, eventProvider: golem.PostgreSQL}
	pgQuery, pgArgs, err := postgres.policyHistoryStatement(rows[0].CausationID)
	if err != nil {
		t.Fatal(err)
	}
	namespace, known := fixture.app.registry.PhysicalSystemNamespace(golem.PostgreSQL)
	if !known || !strings.Contains(pgQuery, `"`+string(namespace)+`"."_golem_outbox"`) || !strings.Contains(pgQuery, `"causation_id"=$1`) || !strings.Contains(pgQuery, `"causation_id"=$2`) || !strings.Contains(pgQuery, "octet_length(") || strings.Contains(pgQuery, "typeof(") || strings.Contains(pgQuery, "?") || !reflect.DeepEqual(pgArgs, []any{rows[0].CausationID, rows[0].CausationID}) {
		t.Fatal("PostgreSQL history statement lost its namespace, length checks, or bound arguments")
	}
}

func TestEventPolicyHistoryCancelledContextDoesNotPopulateCache(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	shared := &eventNoticeShared{envelope: eventPolicyHistoryWire(t, fixture, rows[1])}
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	if value, err := fixture.app.policyHistoryForNotice(ctx, shared); value != nil || err != context.Canceled {
		t.Fatalf("cancelled load=%v err=%v", value, err)
	}
	if _, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared); err != nil {
		t.Fatalf("cancelled attempt poisoned unstarted cache: %v", err)
	}
}

func TestEventPolicyHistoryCancelledLoadDoesNotPoisonOtherSubscribers(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	shared := &eventNoticeShared{envelope: eventPolicyHistoryWire(t, fixture, rows[1])}
	// Hold the sole connection so cancellation occurs inside a started SQL
	// load, rather than at the entry check. This notice is shared by actors
	// whose subscription contexts have independent lifetimes.
	fixture.app.database.SetMaxOpenConns(1)
	connection, err := fixture.app.database.Connx(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	waits := fixture.app.database.Stats().WaitCount
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := fixture.app.policyHistoryForNotice(ctx, shared)
		finished <- err
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for fixture.app.database.Stats().WaitCount == waits {
		select {
		case err := <-finished:
			t.Fatalf("history load did not reach the held pool: %v", err)
		case <-deadline.C:
			t.Fatal("history load did not wait for the held connection")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled load returned %v", err)
		}
	case <-deadline.C:
		t.Fatal("history load ignored cancellation")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.app.policyHistoryForNotice(fixture.ctx, shared)
	if err != nil || evidence == nil {
		t.Fatalf("one subscriber's cancelled load poisoned another subscriber: evidence=%v err=%v", evidence != nil, err)
	}
}

func TestEventPolicyHistoryRejectsGraphFromAnotherProvider(t *testing.T) {
	fixture, rows := eventPolicyHistoryFixture(t)
	companion, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := policyhistory.New(fixture.app.registry, policyir.ProviderPostgreSQL, companion.Graph().Input())
	if err != nil {
		t.Fatal(err)
	}
	var base []mutationfact.OutboxRow
	for _, stored := range rows {
		original, err := mutationfact.DecodeWithResolver(stored.Metadata, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		identity, present := original.AfterIdentity()
		if !present {
			t.Fatal("fixture must contain creates")
		}
		var fields []policyir.FieldID
		var cells []mutationdecode.Cell
		for _, component := range identity.Components() {
			value, present := component.PolicyValue()
			if !present {
				t.Fatal("fixture identity is not a scalar")
			}
			fields = append(fields, component.FieldID())
			cells = append(cells, mutationdecode.Value(component.FieldID(), value))
		}
		after, err := mutationdecode.NewRow(fixture.app.registry, original.ModelID(), cells)
		if err != nil {
			t.Fatal(err)
		}
		requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, fields, nil)
		if err != nil {
			t.Fatal(err)
		}
		requirement, err = requirement.WithEventSchema([32]byte(original.EventSchemaDigest()))
		if err != nil {
			t.Fatal(err)
		}
		fact, err := mutationfact.New(fixture.app.registry, original.EventSchemaDigest(), original.EventID(), requirement, original.CausationID(), original.TransactionOrdinal(), original.RowVersion(), nil, &after)
		if err != nil {
			t.Fatal(err)
		}
		row, err := fact.OutboxRow(stored.RecordedAt)
		if err != nil {
			t.Fatal(err)
		}
		base = append(base, row)
	}
	sealed, _, err := mutationfact.SealPolicyHistoryCausation(base, foreign, fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical facts, graph and member all match. Only the recorded physical
	// provider differs from this SQLite application's authoritative outbox.
	for _, row := range sealed {
		eventPolicyHistoryExec(t, fixture, `UPDATE _golem_outbox SET metadata=?,delete_snapshot=? WHERE event_id=?`, row.Metadata, row.DeleteSnapshot, row.EventID)
	}
	if _, err := mutationfact.DecodePolicyHistoryCausation(sealed, fixture.app.eventSchemas); err != nil {
		t.Fatal(err)
	}
	eventPolicyHistoryRefused(t, fixture, &eventNoticeShared{envelope: eventPolicyHistoryWire(t, fixture, sealed[1])})
}

func TestEventPolicyHistoryNeverUsesLegacyLiveRouting(t *testing.T) {
	fixture := eventHistoryScopeFixture(t, false)
	evidence := eventHistoryRouteReadyPost(t, fixture)
	shared := eventHistoryRouteNotice(t, fixture, evidence)
	caller := eventHistoryScopeCaller(t, fixture, 1)
	plan := eventHistoryRoutePlan(t, fixture, caller)
	metadata, ok := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
	if !ok {
		t.Fatal("Post event metadata missing")
	}
	// An ordinary snapshot is deliberately available. Historical fact routing
	// must never consume it to replace missing committed evidence.
	truth, err := policyir.NewConstant(policyir.ModelID(fixture.schema.Post), true)
	if err != nil {
		t.Fatal(err)
	}
	judge := &eventJudgeSpec{row: truth, watch: []golem.FieldID{fixture.schema.AuthorID, fixture.schema.PostTitle}}
	identity, _ := shared.envelope.Fact().AfterIdentity()
	snapshot, err := fixture.app.readEventSnapshot(fixture.ctx, metadata, []*eventJudgeSpec{judge}, identity)
	if err != nil || snapshot == nil || !snapshot.found {
		t.Fatalf("live-after-image control missing: %v", err)
	}
	shared.snapshot = snapshot
	causal := golem.UUID(evidence.value.CausationID()).String()
	eventPolicyHistoryExec(t, fixture, `DELETE FROM _golem_outbox WHERE causation_id=? AND transaction_ordinal=1`, causal)
	ctx, span := observeexec.Begin(fixture.ctx, nil, fixture.app.eventProvider, fixture.schema.User, observe.KindRead, observe.OperationSubscriptionEvaluation, observe.PhaseVerify)
	matched, err := fixture.app.historicalFactDependencyChanged(ctx, caller, fixture.schema.User, shared, plan)
	observeexec.Finish(span, observe.OutcomeRefused, observe.ReasonNone)
	var refusal *events.Error
	// The sole statement loads the authoritative outbox group. There are no
	// live target/source probes after that group is found incomplete.
	if matched || span.StatementCount() != 1 || !errors.As(err, &refusal) || refusal.Code() != events.CodeSubscriptionRevalidation || refusal.Reason() != "" {
		t.Fatalf("missing committed evidence used live source routing: matched=%v statements=%d err=%v", matched, span.StatementCount(), err)
	}
}

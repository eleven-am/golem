package runtime

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// This value is shared only while processing one notice. It contains immutable
// physical evidence, never an actor-dependent authorization result.
type eventPolicyHistory struct {
	value    mutationfact.PolicyHistory
	registry *schema.Registry
}

func (app *App[P, A]) policyHistoryForNotice(ctx context.Context, shared *eventNoticeShared) (*eventPolicyHistory, error) {
	if app == nil || shared == nil || shared.failed || ctx == nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shared.historyMu.Lock()
	defer shared.historyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !shared.historyRead {
		value, err := app.readPolicyHistoryNotice(ctx, shared)
		// Loading uses one subscriber's cancellation context. An interrupted
		// attempt is not a conclusion about the notice's physical evidence and
		// must not close other subscribers that share this notice.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		shared.history, shared.historyErr = value, err
		shared.historyRead = true
	}
	return shared.history, shared.historyErr
}

func (app *App[P, A]) readPolicyHistoryNotice(ctx context.Context, shared *eventNoticeShared) (*eventPolicyHistory, error) {
	ref, present := shared.envelope.Fact().PolicyHistory()
	if !present || app.eventSchemas == nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	causation := golem.UUID(shared.envelope.CausationID()).String()
	query, args, err := app.policyHistoryStatement(causation)
	if err != nil {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	recordQueryerStatement(ctx, app.database)
	rows, err := app.database.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	defer rows.Close()
	var stored []mutationfact.OutboxRow
	expected := 0
	for rows.Next() {
		if len(stored) >= MaxMutationFacts {
			return nil, events.Failure(events.CodeSubscriptionRevalidation)
		}
		count, row, err := app.scanOutboxFact(rows)
		if err != nil || count < 1 || count > MaxMutationFacts || expected != 0 && count != expected {
			return nil, eventPolicyHistoryFailure(ctx)
		}
		expected = count
		stored = append(stored, row)
	}
	if err := rows.Err(); err != nil {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	if expected == 0 || len(stored) != expected {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	companion, err := mutationfact.DecodePolicyHistoryCausation(stored, app.eventSchemas)
	if err != nil {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	actual, err := companion.Reference()
	if err != nil || actual != ref || companion.CausationID() != shared.envelope.Fact().CausationID() || companion.Generation() != shared.envelope.GenerationDigest() || companion.Graph().Provider() != app.provider {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	registry, known := app.eventSchemas.ResolvePolicyHistorySchema(companion.Generation(), companion.ProjectionFingerprint())
	if !known {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	// A valid graph reference alone does not prove that an injected notice
	// belongs to that graph's committed fact set. Compare the exact member's
	// canonical transport projection, including its private before-image.
	wire, err := shared.envelope.Fact().TransportRow(shared.envelope.RecordedAt())
	if err != nil {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	member := false
	for _, row := range stored {
		if row.EventID != wire.EventID {
			continue
		}
		envelope, err := mutationfact.ValidateStoredRow(row, app.eventSchemas)
		if err != nil {
			return nil, eventPolicyHistoryFailure(ctx)
		}
		canonical, err := envelope.TransportRow(row.RecordedAt)
		if err != nil || !canonical.RecordedAt.Equal(wire.RecordedAt) || !bytes.Equal(canonical.Metadata, wire.Metadata) || !bytes.Equal(canonical.DeleteSnapshot, wire.DeleteSnapshot) {
			return nil, events.Failure(events.CodeSubscriptionRevalidation)
		}
		member = true
		break
	}
	if !member {
		return nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &eventPolicyHistory{value: companion, registry: registry}, nil
}

func eventPolicyHistoryFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Driver/decoder errors may contain stored values. Public event failures
	// carry only the stable revalidation code.
	return events.Failure(events.CodeSubscriptionRevalidation)
}

// Header sizes are independent of binary evidence and bounded by the released
// UUID/digest/codec/action representations. Numeric SQL values are fixed-size;
// SQLite's dynamic typing is checked before they reach the Go scanner.
const maxEventPolicyHistoryHeaderBytes = MaxMutationFacts * (36 + 36 + 64 + 32 + len(mutationfact.HistoryFactCodecIdentity) + len("created"))

func (app *App[P, A]) policyHistoryStatement(causation string) (string, []any, error) {
	if app == nil || app.registry == nil || app.database == nil || app.provider != policyir.ProviderSQLite && app.provider != policyir.ProviderPostgreSQL {
		return "", nil, fmt.Errorf("event policy history: provider is unavailable")
	}
	namespace, known := app.registry.PhysicalSystemNamespace(app.eventProvider)
	if !known || namespace == "" || strings.ContainsAny(string(namespace), "\x00\"") {
		return "", nil, fmt.Errorf("event policy history: system namespace is unavailable")
	}
	table := `"` + string(namespace) + `"."_golem_outbox"`
	var args []any
	mark := func() string {
		args = append(args, causation)
		if app.provider == policyir.ProviderPostgreSQL {
			return fmt.Sprintf("$%d", len(args))
		}
		return "?"
	}
	var binary, headers []string
	for _, column := range []string{"before_identity", "after_identity", "metadata", "delete_snapshot"} {
		name := `"` + column + `"`
		length := `COALESCE(octet_length(` + name + `),0)`
		if app.provider == policyir.ProviderSQLite {
			length = fmt.Sprintf(`CASE WHEN %s IS NULL THEN 0 WHEN typeof(%s)='blob' THEN length(%s) ELSE %d END`, name, name, name, MaxMutationOutboxBytes+1)
		}
		binary = append(binary, length)
	}
	for _, column := range []string{"event_id", "causation_id", "generation_fingerprint", "model_id", "codec_identity", "action"} {
		name := `"` + column + `"`
		length := `COALESCE(octet_length(` + name + `),0)`
		if app.provider == policyir.ProviderSQLite {
			length = fmt.Sprintf(`CASE WHEN typeof(%s)='text' THEN length(CAST(%s AS BLOB)) ELSE %d END`, name, name, maxEventPolicyHistoryHeaderBytes+1)
		}
		headers = append(headers, length)
	}
	version := `"fact_version"`
	if app.provider == policyir.ProviderSQLite {
		version = `CASE WHEN typeof("fact_version")='integer' THEN "fact_version" ELSE -1 END`
		for _, column := range []string{"transaction_ordinal", "recorded_at"} {
			headers = append(headers, fmt.Sprintf(`CASE WHEN typeof("%s")='integer' THEN 0 ELSE %d END`, column, maxEventPolicyHistoryHeaderBytes+1))
		}
	}
	// The bounded window selects lengths, not payloads. One SQL statement
	// certifies count/bytes and reads the complete immutable group from one
	// database snapshot, even if retention runs concurrently. A sentinel row
	// refuses an oversized group without scanning an unbounded causation.
	query := fmt.Sprintf(`WITH "golem_history_window" AS (SELECT %s AS "fact_version",(%s) AS "evidence_bytes",(%s) AS "header_bytes" FROM %s WHERE "causation_id"=%s ORDER BY "transaction_ordinal" LIMIT %d),"golem_history_bounds" AS (SELECT COUNT(*) AS "fact_count",COALESCE(SUM("evidence_bytes"),0) AS "evidence_bytes",COALESCE(SUM("header_bytes"),0) AS "header_bytes",MIN("fact_version") AS "minimum_version",MAX("fact_version") AS "maximum_version" FROM "golem_history_window") SELECT (SELECT "fact_count" FROM "golem_history_bounds") AS "golem_check",%s FROM %s WHERE "causation_id"=%s AND EXISTS(SELECT 1 FROM "golem_history_bounds" WHERE "fact_count" BETWEEN 1 AND %d AND "evidence_bytes"<=%d AND "header_bytes"<=%d AND "minimum_version"=%d AND "maximum_version"=%d) ORDER BY "transaction_ordinal" LIMIT %d`, version, strings.Join(binary, "+"), strings.Join(headers, "+"), table, mark(), MaxMutationFacts+1, runtimeOutboxFactColumns, table, mark(), MaxMutationFacts, MaxMutationOutboxBytes, maxEventPolicyHistoryHeaderBytes, mutationfact.HistoryFactVersion, mutationfact.HistoryFactVersion, MaxMutationFacts+1)
	return query, args, nil
}

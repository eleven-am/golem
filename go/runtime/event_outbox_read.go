package runtime

import (
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"time"
)

// Every runtime outbox reader shares this fixed column/scanner contract.
// Its leading check value routes legacy batched probes or carries a bounded
// history read's expected complete-causation count.
const runtimeOutboxFactColumns = `"event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at"`

type outboxFactScan struct {
	Check                 int64  `db:"golem_check"`
	EventID               string `db:"event_id"`
	FactVersion           int64  `db:"fact_version"`
	CodecIdentity         string `db:"codec_identity"`
	GenerationFingerprint string `db:"generation_fingerprint"`
	ModelID               string `db:"model_id"`
	Action                string `db:"action"`
	BeforeIdentity        []byte `db:"before_identity"`
	AfterIdentity         []byte `db:"after_identity"`
	CausationID           string `db:"causation_id"`
	TransactionOrdinal    int64  `db:"transaction_ordinal"`
	Metadata              []byte `db:"metadata"`
	DeleteSnapshot        []byte `db:"delete_snapshot"`
}

func (app *App[P, A]) scanOutboxFact(rows interface{ Scan(...any) error }) (int, mutationfact.OutboxRow, error) {
	var value outboxFactScan
	destinations := []any{&value.Check, &value.EventID, &value.FactVersion, &value.CodecIdentity, &value.GenerationFingerprint, &value.ModelID, &value.Action, &value.BeforeIdentity, &value.AfterIdentity, &value.CausationID, &value.TransactionOrdinal, &value.Metadata, &value.DeleteSnapshot}
	var recorded time.Time
	var micros int64
	if app.provider == policyir.ProviderPostgreSQL {
		destinations = append(destinations, &recorded)
	} else {
		destinations = append(destinations, &micros)
	}
	if err := rows.Scan(destinations...); err != nil {
		return 0, mutationfact.OutboxRow{}, err
	}
	if app.provider != policyir.ProviderPostgreSQL {
		recorded = time.UnixMicro(micros)
	}
	return int(value.Check), mutationfact.OutboxRow{
		EventID: value.EventID, FactVersion: value.FactVersion, CodecIdentity: value.CodecIdentity,
		GenerationFingerprint: value.GenerationFingerprint, ModelID: value.ModelID, Action: value.Action,
		BeforeIdentity: value.BeforeIdentity, AfterIdentity: value.AfterIdentity, CausationID: value.CausationID,
		TransactionOrdinal: value.TransactionOrdinal, Metadata: value.Metadata, DeleteSnapshot: value.DeleteSnapshot,
		RecordedAt: recorded.UTC(),
	}, nil
}

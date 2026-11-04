package fact

import (
	"encoding/hex"
	"fmt"
	"time"
)

// OutboxRow is one immutable-by-convention value set for the closed
// _golem_outbox V1 column contract. The runtime owns placeholder rendering and
// converts RecordedAt to provider storage (timestamptz(6) or Unix microseconds).
type OutboxRow struct {
	EventID               string
	FactVersion           int64
	CodecIdentity         string
	GenerationFingerprint string
	ModelID               string
	Action                string
	BeforeIdentity        []byte
	AfterIdentity         []byte
	CausationID           string
	TransactionOrdinal    int64
	Metadata              []byte
	DeleteSnapshot        []byte
	RecordedAt            time.Time
}

func (envelope Envelope) OutboxRow(recordedAt time.Time) (OutboxRow, error) {
	if recordedAt.IsZero() {
		return OutboxRow{}, fmt.Errorf("P4_FACT_OUTBOX: recorded time is zero")
	}
	metadata, err := Encode(envelope)
	if err != nil {
		return OutboxRow{}, err
	}
	var beforeIdentity, afterIdentity, deleteSnapshot []byte
	if before, ok := envelope.BeforeIdentity(); ok {
		beforeIdentity, err = encodeIdentity(before)
		if err != nil {
			return OutboxRow{}, err
		}
	}
	if after, ok := envelope.AfterIdentity(); ok {
		afterIdentity, err = encodeIdentity(after)
		if err != nil {
			return OutboxRow{}, err
		}
	}
	if len(envelope.snapshotFields) != 0 {
		if envelope.deleteSnapshot == nil {
			return OutboxRow{}, fmt.Errorf("P4_FACT_OUTBOX: configured private delete snapshot is absent")
		}
		deleteSnapshot, err = encodeRowBlob(*envelope.deleteSnapshot)
		if err != nil {
			return OutboxRow{}, err
		}
	}
	return OutboxRow{
		EventID: formatUUID([16]byte(envelope.event)), FactVersion: int64(FormatVersion),
		CodecIdentity: CodecIdentity, GenerationFingerprint: hex.EncodeToString(envelope.generation[:]),
		ModelID: hex.EncodeToString(envelope.model[:]), Action: actionText(envelope.action),
		BeforeIdentity: beforeIdentity, AfterIdentity: afterIdentity,
		CausationID: formatUUID([16]byte(envelope.causation)), TransactionOrdinal: int64(envelope.ordinal),
		Metadata: metadata, DeleteSnapshot: deleteSnapshot,
		RecordedAt: recordedAt.UTC().Truncate(time.Microsecond),
	}, nil
}

// EncodedBytes is the exact byte-limit contribution of binary outbox columns.
func (row OutboxRow) EncodedBytes() int {
	return len(row.BeforeIdentity) + len(row.AfterIdentity) + len(row.Metadata) + len(row.DeleteSnapshot)
}

func (envelope Envelope) EncodedSize() (int, error) {
	row, err := envelope.OutboxRow(time.Unix(1, 0))
	if err != nil {
		return 0, err
	}
	return row.EncodedBytes(), nil
}

func formatUUID(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

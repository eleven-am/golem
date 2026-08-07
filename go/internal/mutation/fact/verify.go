package fact

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

// ValidateStoredRow joins and validates the immutable outbox row, then
// cross-checks every duplicated column against canonical metadata. RecordedAt
// is intentionally excluded because the V1/V2 fact metadata never contained
// it; the provider column remains validated for non-zero/canonical storage by
// insertion and provider readers.
func ValidateStoredRow(row OutboxRow, resolver HistoricalSchemaResolver) (Envelope, error) {
	if err := validateOutboxRow(row); err != nil {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: %w", err)
	}
	envelope, err := DecodeOutboxWithResolver(row.Metadata, row.DeleteSnapshot, resolver)
	if err != nil {
		return Envelope{}, err
	}
	if row.FactVersion != int64(envelope.FormatVersion()) || row.CodecIdentity != envelope.CodecIdentity() {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: codec columns disagree with metadata")
	}
	if row.EventID != formatUUID([16]byte(envelope.EventID())) || row.CausationID != formatUUID([16]byte(envelope.CausationID())) {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: event or causation column disagrees with metadata")
	}
	generation := envelope.Generation()
	if row.GenerationFingerprint != hex.EncodeToString(generation[:]) {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: generation column disagrees with metadata")
	}
	model := envelope.ModelID()
	if row.ModelID != hex.EncodeToString(model[:]) || row.Action != actionText(envelope.Action()) || row.TransactionOrdinal != int64(envelope.TransactionOrdinal()) {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: model, action, or ordinal column disagrees with metadata")
	}
	var before, after []byte
	if identity, present := envelope.BeforeIdentity(); present {
		before, err = encodeIdentity(identity)
		if err != nil {
			return Envelope{}, err
		}
	}
	if identity, present := envelope.AfterIdentity(); present {
		after, err = encodeIdentity(identity)
		if err != nil {
			return Envelope{}, err
		}
	}
	if !bytes.Equal(row.BeforeIdentity, before) || !bytes.Equal(row.AfterIdentity, after) {
		return Envelope{}, fmt.Errorf("P7_FACT_STORED_ROW: identity columns disagree with metadata")
	}
	return envelope, nil
}

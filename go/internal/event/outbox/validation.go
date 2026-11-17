package outbox

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

// ValidationLimits uses the same encoded event and batch budgets as publishing.
// Zero values select the existing publisher defaults.
type ValidationLimits struct {
	MaxEncodedBytes int
	MaxBatchBytes   int
}

func (limits ValidationLimits) normalized() (ValidationLimits, error) {
	if limits.MaxEncodedBytes == 0 {
		limits.MaxEncodedBytes = eventcodec.DefaultMaxEncodedBytes
	}
	if limits.MaxBatchBytes == 0 {
		limits.MaxBatchBytes = events.DefaultLimits().MaxEncodedBatchBytes
	}
	if limits.MaxEncodedBytes < 1 || limits.MaxEncodedBytes > eventcodec.HardMaxEncodedBytes {
		return ValidationLimits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: encoded event bytes are invalid")
	}
	if limits.MaxBatchBytes < limits.MaxEncodedBytes || limits.MaxBatchBytes > events.MaximumLimits().MaxEncodedBatchBytes {
		return ValidationLimits{}, fmt.Errorf("P7_PUBLISHER_LIMIT: encoded batch bytes are invalid")
	}
	return limits, nil
}

// ValidationError retains the publisher's durable block reason without changing
// delivery state. Offline validation must never claim or publish archive rows.
type ValidationError struct{ Code string }

func (failure *ValidationError) Error() string { return "P7_CAUSATION_INVALID: " + failure.Code }

// ActiveEventSchemaCompatibility proves a historical event can enter the active
// delivery surface. Historical archive validity alone does not establish it.
type ActiveEventSchemaCompatibility interface {
	CanDeliverEventSchema(golem.ModelID, golem.EventSchemaDigest) bool
}

// ValidateStoredCausation validates a complete ordered group, including V4 policy
// history and every stored codec, without requiring current delivery compatibility.
// Use it for completed or explicitly abandoned archived groups.
func ValidateStoredCausation(causationID string, facts []eventprovider.FactRow, resolver mutationfact.HistoricalSchemaResolver, limits ValidationLimits) (eventvalue.EventBatch, error) {
	return validateCausation(causationID, facts, resolver, nil, limits)
}

// ValidateDeliveryCausation additionally requires active event-schema compatibility.
// Publishers and restored groups that will resume delivery use this same owner.
func ValidateDeliveryCausation(causationID string, facts []eventprovider.FactRow, resolver mutationfact.HistoricalSchemaResolver, compatibility ActiveEventSchemaCompatibility, limits ValidationLimits) (eventvalue.EventBatch, error) {
	if compatibility == nil {
		return eventvalue.EventBatch{}, fmt.Errorf("P7_PUBLISHER_CONFIG: active event-schema compatibility is required")
	}
	return validateCausation(causationID, facts, resolver, compatibility, limits)
}

func validateCausationIdentity(causationID string, facts []eventprovider.FactRow) (golem.CausationID, error) {
	causation, err := parseUUID[golem.CausationID](causationID)
	if err != nil || len(facts) == 0 {
		return golem.CausationID{}, &ValidationError{Code: "fact-invalid"}
	}
	return causation, nil
}

func validateCausation(causationID string, facts []eventprovider.FactRow, resolver mutationfact.HistoricalSchemaResolver, compatibility ActiveEventSchemaCompatibility, limits ValidationLimits) (eventvalue.EventBatch, error) {
	causation, err := validateCausationIdentity(causationID, facts)
	if err != nil {
		return eventvalue.EventBatch{}, err
	}
	if resolver == nil {
		return eventvalue.EventBatch{}, fmt.Errorf("P7_PUBLISHER_CONFIG: historical resolver is required")
	}
	limits, err = limits.normalized()
	if err != nil {
		return eventvalue.EventBatch{}, err
	}
	if len(facts) > eventprovider.MaximumCausationFacts {
		return eventvalue.EventBatch{}, &ValidationError{Code: "fact-invalid"}
	}
	// A historical graph commits to the complete ordered causation, including
	// private before-images and recorded times. Validate it before projecting
	// any individual event, so a valid member cannot hide a damaged sibling.
	for _, row := range facts {
		if row.FactVersion != int64(mutationfact.HistoryFactVersion) {
			continue
		}
		rows := make([]mutationfact.OutboxRow, len(facts))
		for index, member := range facts {
			rows[index] = codecFactRow(member)
		}
		if _, err := mutationfact.DecodePolicyHistoryCausation(rows, resolver); err != nil {
			return eventvalue.EventBatch{}, &ValidationError{Code: "policy-history-invalid"}
		}
		break
	}
	notices := make([]eventvalue.Notice, len(facts))
	for index, row := range facts {
		if row.CausationID != causationID || row.TransactionOrdinal != int64(index+1) {
			return eventvalue.EventBatch{}, &ValidationError{Code: "fact-order-invalid"}
		}
		envelope, encodeErr := eventcodec.EncodeStoredRow(codecFactRow(row), resolver, eventcodec.Limits{MaxEncodedBytes: limits.MaxEncodedBytes})
		if encodeErr != nil {
			return eventvalue.EventBatch{}, &ValidationError{Code: classifyFactFailure(encodeErr)}
		}
		if compatibility != nil && !compatibility.CanDeliverEventSchema(envelope.ModelID(), envelope.ResolvedEventSchemaDigest()) {
			return eventvalue.EventBatch{}, &ValidationError{Code: "event-schema-incompatible"}
		}
		notice, noticeErr := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
		if noticeErr != nil {
			return eventvalue.EventBatch{}, &ValidationError{Code: "event-invalid"}
		}
		notices[index] = notice
	}
	batch, err := eventvalue.NewEventBatchBounded(causation, notices, limits.MaxBatchBytes)
	if err != nil {
		if errors.Is(err, eventvalue.ErrBatchTooLarge) {
			return eventvalue.EventBatch{}, &ValidationError{Code: "batch-too-large"}
		}
		return eventvalue.EventBatch{}, &ValidationError{Code: "fact-order-invalid"}
	}
	return batch, nil
}

func codecFactRow(row eventprovider.FactRow) mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: row.EventID, FactVersion: row.FactVersion, CodecIdentity: row.CodecIdentity,
		GenerationFingerprint: row.GenerationFingerprint, ModelID: row.ModelID, Action: row.Action,
		BeforeIdentity: append([]byte(nil), row.BeforeIdentity...), AfterIdentity: append([]byte(nil), row.AfterIdentity...),
		CausationID: row.CausationID, TransactionOrdinal: row.TransactionOrdinal,
		Metadata: append([]byte(nil), row.Metadata...), DeleteSnapshot: append([]byte(nil), row.DeleteSnapshot...),
		RecordedAt: row.RecordedAt,
	}
}

func classifyFactFailure(err error) string {
	var unavailable *mutationfact.SchemaUnavailableError
	if errors.As(err, &unavailable) {
		return "schema-unavailable"
	}
	var retired *mutationfact.FormatRetiredError
	if errors.As(err, &retired) {
		return "format-retired"
	}
	return "fact-invalid"
}

func parseUUID[T ~[16]byte](value string) (T, error) {
	var result T
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return result, fmt.Errorf("invalid UUID")
	}
	compact := value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != compact {
		return result, fmt.Errorf("invalid UUID")
	}
	copy(result[:], decoded)
	return result, nil
}

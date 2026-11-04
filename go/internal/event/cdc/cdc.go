// Package cdc implements the adapter-neutral CDC validation and publication
// boundary. Canonical fact/event encoding is injected from the shared event
// codec integration; this package never defines a second wire format.
package cdc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

type Encoder interface {
	EncodeCDC(context.Context, EncodeInput) (Encoded, error)
}

type Encoded struct {
	Notice events.Notice
	Fact   mutationfact.OutboxRow
}

type Change struct {
	events.CDCChangeInput
	EventID golem.EventID
}

type Stamp struct {
	Version    int64
	Superseded bool
}

type Recorder interface {
	RecordCDC(ctx context.Context, causation golem.CausationID, changes []Change, build func(stamps []Stamp) ([]mutationfact.OutboxRow, error)) error
}

type Correlator interface {
	GolemTransaction(context.Context, CorrelationInput) (bool, error)
}

type EncodeInput struct {
	Adapter             events.CDCIdentity
	SourceTransactionID string
	RecordedAt          time.Time
	Cursor              []byte
	EventID             golem.EventID
	CausationID         golem.CausationID
	Ordinal             uint32
	Version             int64
	Model               golem.ModelID
	Action              golem.EventAction
	Before              *golem.RuntimeModelRow
	After               *golem.RuntimeModelRow
}

type CorrelationInput struct {
	adapter             events.CDCIdentity
	sourceTransactionID string
	cursor              []byte
}

func (input CorrelationInput) Adapter() events.CDCIdentity { return input.adapter }
func (input CorrelationInput) SourceTransactionID() string { return input.sourceTransactionID }
func (input CorrelationInput) Cursor() []byte              { return bytes.Clone(input.cursor) }

type Config struct {
	Adapter       events.CDCAdapter
	Transport     events.EventTransport
	Encoder       Encoder
	Recorder      Recorder
	Observer      events.Observer
	MaxBatchBytes int
}

type Emitter struct{ config Config }

func NewEmitter(config Config) (*Emitter, error) {
	if config.Adapter == nil || events.ValidateCDCIdentity(config.Adapter.Identity()) != nil || config.Transport == nil || config.Encoder == nil || config.Recorder == nil {
		return nil, events.Failure(events.CodeCDCInvalid)
	}
	if config.MaxBatchBytes == 0 {
		config.MaxBatchBytes = events.DefaultLimits().MaxEncodedBatchBytes
	}
	if config.MaxBatchBytes < 1 || config.MaxBatchBytes > events.MaximumLimits().MaxEncodedBatchBytes {
		return nil, events.Failure(events.CodeCDCInvalid)
	}
	return &Emitter{config: config}, nil
}

func (emitter *Emitter) Emit(ctx context.Context, input events.CDCBatchInput) error {
	if ctx == nil || ctx.Err() != nil {
		return events.Failure(events.CodeCDCUnavailable)
	}
	owned, err := cloneAndValidate(input)
	if err != nil {
		return err
	}
	identity := emitter.config.Adapter.Identity()
	correlated, err := emitter.config.Adapter.CorrelatesGolemTransaction(ctx, events.RuntimeCDCCorrelationInput(owned.SourceTransactionID, owned.Cursor))
	if err != nil {
		return events.Failure(events.CodeCDCUnavailable)
	}
	if correlated {
		for _, change := range owned.Changes {
			events.Observe(emitter.config.Observer, ctx, change.Model, change.Action, events.ObservationSuppression, events.OutcomeSuppressed, events.SuppressionCorrelatedGolemWrite, 0, 0, 0, 0, 1)
		}
		return nil
	}
	causation := deriveCausationID(identity, owned.SourceTransactionID)
	changes := make([]Change, len(owned.Changes))
	for index, change := range owned.Changes {
		changes[index] = Change{CDCChangeInput: change, EventID: deriveEventID(identity, owned.SourceTransactionID, change.Ordinal)}
	}
	var batch events.EventBatch
	var kept []events.CDCChangeInput
	build := func(stamps []Stamp) ([]mutationfact.OutboxRow, error) {
		if len(stamps) != len(changes) {
			return nil, events.Failure(events.CodeCDCInvalid)
		}
		notices := make([]events.Notice, 0, len(changes))
		facts := make([]mutationfact.OutboxRow, 0, len(changes))
		survivors := make([]events.CDCChangeInput, 0, len(changes))
		for index, change := range changes {
			if stamps[index].Superseded {
				events.Observe(emitter.config.Observer, ctx, change.Model, change.Action, events.ObservationSuppression, events.OutcomeSuppressed, events.SuppressionCDCSuperseded, 0, 0, 0, 0, 1)
				continue
			}
			ordinal := uint32(len(facts) + 1)
			encoded, encodeErr := emitter.config.Encoder.EncodeCDC(ctx, EncodeInput{
				Adapter: identity, SourceTransactionID: owned.SourceTransactionID,
				RecordedAt: owned.RecordedAt, Cursor: bytes.Clone(owned.Cursor), EventID: change.EventID, CausationID: causation,
				Ordinal: ordinal, Version: stamps[index].Version, Model: change.Model, Action: change.Action,
				Before: cloneRowPointer(change.Before), After: cloneRowPointer(change.After),
			})
			notice, fact := encoded.Notice, encoded.Fact
			if encodeErr != nil || !notice.Valid() || !factMatches(fact, change.EventID, causation, ordinal) || notice.EventID() != change.EventID || notice.CausationID() != causation || notice.TransactionOrdinal() != ordinal || notice.ModelID() != change.Model || notice.Action() != change.Action {
				return nil, events.Failure(events.CodeCDCInvalid)
			}
			notices = append(notices, notice)
			facts = append(facts, fact)
			survivors = append(survivors, change.CDCChangeInput)
			events.Observe(emitter.config.Observer, ctx, change.Model, change.Action, events.ObservationCDCReceive, events.OutcomeSuccess, "", 0, 0, 0, 0, 1)
		}
		kept, batch = survivors, events.EventBatch{}
		if len(notices) == 0 {
			return facts, nil
		}
		built, batchErr := eventvalue.NewEventBatchBounded(causation, notices, emitter.config.MaxBatchBytes)
		if batchErr != nil {
			return nil, events.Failure(events.CodeCDCInvalid)
		}
		batch = built
		return facts, nil
	}
	if err := emitter.config.Recorder.RecordCDC(ctx, causation, changes, build); err != nil {
		if code, ok := events.CodeOf(err); ok && code == events.CodeCDCInvalid {
			return events.Failure(events.CodeCDCInvalid)
		}
		return events.Failure(events.CodeCDCUnavailable)
	}
	if len(kept) == 0 {
		return nil
	}
	if err := emitter.config.Transport.Publish(ctx, batch); err != nil {
		return events.Failure(events.CodeCDCUnavailable)
	}
	for _, change := range kept {
		events.Observe(emitter.config.Observer, ctx, change.Model, change.Action, events.ObservationCDCAck, events.OutcomeSuccess, "", 0, 0, 0, 0, 1)
	}
	return nil
}

func factMatches(fact mutationfact.OutboxRow, eventID golem.EventID, causation golem.CausationID, ordinal uint32) bool {
	return fact.EventID == golem.UUID(eventID).String() && fact.CausationID == golem.UUID(causation).String() && fact.TransactionOrdinal == int64(ordinal)
}

func cloneAndValidate(input events.CDCBatchInput) (events.CDCBatchInput, error) {
	if len(input.SourceTransactionID) == 0 || len(input.SourceTransactionID) > events.MaximumCDCSourceTransactionBytes || !utf8.ValidString(input.SourceTransactionID) ||
		!canonicalRecordedAt(input.RecordedAt) ||
		len(input.Cursor) > events.MaximumCDCCursorBytes ||
		len(input.Changes) == 0 || len(input.Changes) > events.MaximumCDCChangesPerTransaction {
		return events.CDCBatchInput{}, events.Failure(events.CodeCDCInvalid)
	}
	owned := events.CDCBatchInput{SourceTransactionID: input.SourceTransactionID, RecordedAt: input.RecordedAt, Cursor: bytes.Clone(input.Cursor), Changes: make([]events.CDCChangeInput, len(input.Changes))}
	for index, change := range input.Changes {
		if change.Ordinal != uint32(index+1) || change.Model == (golem.ModelID{}) || !validChangeShape(change) {
			return events.CDCBatchInput{}, events.Failure(events.CodeCDCInvalid)
		}
		owned.Changes[index] = events.CDCChangeInput{Ordinal: change.Ordinal, Model: change.Model, Action: change.Action, Before: cloneRowPointer(change.Before), After: cloneRowPointer(change.After)}
	}
	return owned, nil
}

func canonicalRecordedAt(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Microsecond) == 0
}

func validChangeShape(change events.CDCChangeInput) bool {
	if change.Before != nil && change.Before.ModelID() != change.Model {
		return false
	}
	if change.After != nil && change.After.ModelID() != change.Model {
		return false
	}
	switch change.Action {
	case golem.EventCreated:
		return change.Before == nil && change.After != nil
	case golem.EventUpdated:
		return change.After != nil
	case golem.EventDeleted:
		return change.Before != nil && change.After == nil
	default:
		return false
	}
}

// RuntimeModelRow is representation-opaque and exposes only clone-on-read
// accessors. Copying the value detaches the caller's pointer without exposing
// mutable map storage; exact field completeness is validated by Encoder.
func cloneRowPointer(row *golem.RuntimeModelRow) *golem.RuntimeModelRow {
	if row == nil {
		return nil
	}
	owned := *row
	return &owned
}

func deriveCausationID(identity events.CDCIdentity, sourceTransactionID string) golem.CausationID {
	digest := derive("golem.cdc.causation.v1", identity, sourceTransactionID, 0, false)
	return golem.CausationID(digest)
}

func deriveEventID(identity events.CDCIdentity, sourceTransactionID string, ordinal uint32) golem.EventID {
	digest := derive("golem.cdc.event.v1", identity, sourceTransactionID, ordinal, true)
	return golem.EventID(digest)
}

func derive(domain string, identity events.CDCIdentity, sourceTransactionID string, ordinal uint32, includeOrdinal bool) [16]byte {
	hash := sha256.New()
	writePart(hash, []byte(domain))
	writePart(hash, []byte(identity.Provider))
	writePart(hash, []byte(identity.Name))
	writePart(hash, []byte(identity.Version))
	writePart(hash, []byte(sourceTransactionID))
	if includeOrdinal {
		var encoded [4]byte
		binary.BigEndian.PutUint32(encoded[:], ordinal)
		writePart(hash, encoded[:])
	}
	var result [16]byte
	copy(result[:], hash.Sum(nil))
	// RFC 9562 UUIDv8 and the RFC variant make diagnostics/interoperability
	// canonical without pretending this deterministic SHA-256 ID is UUIDv5.
	result[6] = result[6]&0x0f | 0x80
	result[8] = result[8]&0x3f | 0x80
	return result
}

type byteWriter interface{ Write([]byte) (int, error) }

func writePart(writer byteWriter, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

var _ events.CDCEmitter = (*Emitter)(nil)

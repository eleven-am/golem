// Package value owns the representation of P7's sealed transport values.
//
// It is internal so infrastructure adapters can construct values only after
// validating their wire representation, while applications importing the
// public events package can inspect but cannot forge them.
package value

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

var ErrBatchTooLarge = errors.New("sealed event batch exceeds its encoded byte limit")

type Notice struct {
	eventID     golem.EventID
	generation  golem.SchemaDigest
	eventSchema golem.EventSchemaDigest
	model       golem.ModelID
	action      golem.EventAction
	causation   golem.CausationID
	ordinal     uint32
	encoded     []byte
}

func NewNotice(eventID golem.EventID, generation golem.SchemaDigest, model golem.ModelID, action golem.EventAction, causation golem.CausationID, ordinal uint32, encoded []byte) (Notice, error) {
	return NewRoutedNotice(eventID, generation, golem.EventSchemaDigest(generation), model, action, causation, ordinal, encoded)
}

// NewRoutedNotice keeps the immutable fact generation while sealing the
// logical event-schema identity used for cross-generation transport routing.
func NewRoutedNotice(eventID golem.EventID, generation golem.SchemaDigest, eventSchema golem.EventSchemaDigest, model golem.ModelID, action golem.EventAction, causation golem.CausationID, ordinal uint32, encoded []byte) (Notice, error) {
	if eventID == (golem.EventID{}) || generation == (golem.SchemaDigest{}) || eventSchema == (golem.EventSchemaDigest{}) || model == (golem.ModelID{}) || causation == (golem.CausationID{}) {
		return Notice{}, fmt.Errorf("sealed event notice has absent identity metadata")
	}
	if action != golem.EventCreated && action != golem.EventUpdated && action != golem.EventDeleted {
		return Notice{}, fmt.Errorf("sealed event notice has unknown action")
	}
	if ordinal == 0 {
		return Notice{}, fmt.Errorf("sealed event notice has zero transaction ordinal")
	}
	if len(encoded) == 0 {
		return Notice{}, fmt.Errorf("sealed event notice has empty encoding")
	}
	return Notice{
		eventID: eventID, generation: generation, eventSchema: eventSchema, model: model, action: action,
		causation: causation, ordinal: ordinal, encoded: bytes.Clone(encoded),
	}, nil
}

func (notice Notice) EventID() golem.EventID                     { return notice.eventID }
func (notice Notice) GenerationDigest() golem.SchemaDigest       { return notice.generation }
func (notice Notice) EventSchemaDigest() golem.EventSchemaDigest { return notice.eventSchema }
func (notice Notice) ModelID() golem.ModelID                     { return notice.model }
func (notice Notice) Action() golem.EventAction                  { return notice.action }
func (notice Notice) CausationID() golem.CausationID             { return notice.causation }
func (notice Notice) TransactionOrdinal() uint32                 { return notice.ordinal }
func (notice Notice) Encoded() []byte                            { return bytes.Clone(notice.encoded) }

func (notice Notice) Valid() bool {
	validated, err := NewRoutedNotice(notice.eventID, notice.generation, notice.eventSchema, notice.model, notice.action, notice.causation, notice.ordinal, notice.encoded)
	return err == nil && validated.eventID == notice.eventID
}

type EventBatch struct {
	causation golem.CausationID
	events    []Notice
}

func NewEventBatch(causation golem.CausationID, notices []Notice) (EventBatch, error) {
	return NewEventBatchBounded(causation, notices, 0)
}

func NewEventBatchBounded(causation golem.CausationID, notices []Notice, maximumBytes int) (EventBatch, error) {
	if causation == (golem.CausationID{}) || len(notices) == 0 {
		return EventBatch{}, fmt.Errorf("sealed event batch has absent causation or events")
	}
	owned := append([]Notice(nil), notices...)
	seen := make(map[golem.EventID]struct{}, len(owned))
	total := 0
	for index, notice := range owned {
		if !notice.Valid() || notice.causation != causation || notice.ordinal != uint32(index+1) {
			return EventBatch{}, fmt.Errorf("sealed event batch is not one contiguous causation")
		}
		if _, exists := seen[notice.eventID]; exists {
			return EventBatch{}, fmt.Errorf("sealed event batch repeats an event identity")
		}
		seen[notice.eventID] = struct{}{}
		if maximumBytes > 0 && len(notice.encoded) > maximumBytes-total {
			return EventBatch{}, ErrBatchTooLarge
		}
		total += len(notice.encoded)
	}
	return EventBatch{causation: causation, events: owned}, nil
}

func (batch EventBatch) CausationID() golem.CausationID { return batch.causation }
func (batch EventBatch) Events() []Notice               { return append([]Notice(nil), batch.events...) }
func (batch EventBatch) Valid() bool {
	_, err := NewEventBatch(batch.causation, batch.events)
	return err == nil
}

type Subscription struct {
	generation  golem.SchemaDigest
	eventSchema golem.EventSchemaDigest
	model       golem.ModelID
}

func NewSubscription(generation golem.SchemaDigest, model golem.ModelID) (Subscription, error) {
	return NewRoutedSubscription(generation, golem.EventSchemaDigest(generation), model)
}

func NewRoutedSubscription(generation golem.SchemaDigest, eventSchema golem.EventSchemaDigest, model golem.ModelID) (Subscription, error) {
	if generation == (golem.SchemaDigest{}) || eventSchema == (golem.EventSchemaDigest{}) || model == (golem.ModelID{}) {
		return Subscription{}, fmt.Errorf("sealed event subscription has absent generation or model")
	}
	return Subscription{generation: generation, eventSchema: eventSchema, model: model}, nil
}

func (subscription Subscription) GenerationDigest() golem.SchemaDigest {
	return subscription.generation
}
func (subscription Subscription) EventSchemaDigest() golem.EventSchemaDigest {
	return subscription.eventSchema
}
func (subscription Subscription) ModelID() golem.ModelID { return subscription.model }
func (subscription Subscription) Valid() bool {
	return subscription.generation != (golem.SchemaDigest{}) && subscription.eventSchema != (golem.EventSchemaDigest{}) && subscription.model != (golem.ModelID{})
}

type Observation struct {
	model      golem.ModelID
	action     golem.EventAction
	kind       string
	outcome    string
	reason     string
	attempt    int
	queueDepth int
	queueLimit int
	duration   time.Duration
	count      int64
}

func NewObservation(model golem.ModelID, action golem.EventAction, kind, outcome, reason string, attempt, queueDepth, queueLimit int, duration time.Duration, count int64) Observation {
	return Observation{model: model, action: action, kind: kind, outcome: outcome, reason: reason, attempt: attempt, queueDepth: queueDepth, queueLimit: queueLimit, duration: duration, count: count}
}

func (observation Observation) ModelID() golem.ModelID    { return observation.model }
func (observation Observation) Action() golem.EventAction { return observation.action }
func (observation Observation) Kind() string              { return observation.kind }
func (observation Observation) Outcome() string           { return observation.outcome }
func (observation Observation) SuppressionReason() string { return observation.reason }
func (observation Observation) Attempt() int              { return observation.attempt }
func (observation Observation) QueueDepth() int           { return observation.queueDepth }
func (observation Observation) QueueLimit() int           { return observation.queueLimit }
func (observation Observation) Duration() time.Duration   { return observation.duration }
func (observation Observation) AggregateCount() int64     { return observation.count }

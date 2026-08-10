// Package typedvalue owns the unforgeable runtime-to-generated-event handoff.
// Its constructor is internal to Golem; external application modules can name
// the public runtime alias but cannot construct a non-zero validated value.
package typedvalue

import (
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/event/metadatavalue"
)

type Metadata struct {
	EventID        golem.EventID
	Action         golem.EventAction
	CausationID    golem.CausationID
	Ordinal        uint32
	RecordedAt     time.Time
	Generation     golem.SchemaDigest
	EventSchema    golem.EventSchemaDigest
	HasEventSchema bool
	// ResolvedEventSchema is the compiler-owned logical event schema selected
	// by the historical fact resolver. It is present for both V1 and V2 facts:
	// V1 does not carry the digest on wire, so generation resolution supplies
	// it; V2 must resolve to the digest carried on wire.
	ResolvedEventSchema golem.EventSchemaDigest
	ModelID             golem.ModelID
}

type ValidatedEvent struct {
	metadata            golem.EventMetadata
	resolvedEventSchema golem.EventSchemaDigest
	identity            []any
	entity              golem.RuntimeModelRow
	hasEntity           bool
}

func New(metadata Metadata, identity []any, entity *golem.RuntimeModelRow) (ValidatedEvent, error) {
	value := metadatavalue.New(metadatavalue.Input{
		EventID: [16]byte(metadata.EventID), Action: string(metadata.Action),
		CausationID: [16]byte(metadata.CausationID), Ordinal: metadata.Ordinal,
		RecordedAt: metadata.RecordedAt, Generation: [32]byte(metadata.Generation),
		EventSchema: [32]byte(metadata.EventSchema), HasEventSchema: metadata.HasEventSchema,
		ModelID: [16]byte(metadata.ModelID),
	})
	public, err := golem.RuntimeValidatedEventMetadata(value)
	if err != nil {
		return ValidatedEvent{}, err
	}
	if len(identity) == 0 {
		return ValidatedEvent{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated event identity is empty")
	}
	if metadata.ResolvedEventSchema == (golem.EventSchemaDigest{}) {
		return ValidatedEvent{}, fmt.Errorf("GOLEM_EVENT_CODEC: resolved event schema is absent")
	}
	if metadata.HasEventSchema && metadata.EventSchema != metadata.ResolvedEventSchema {
		return ValidatedEvent{}, fmt.Errorf("GOLEM_EVENT_CODEC: wire and resolved event schemas differ")
	}
	owned := make([]any, len(identity))
	for index, value := range identity {
		owned[index] = cloneIdentityValue(value)
		if owned[index] == nil {
			return ValidatedEvent{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated event identity component %d is absent", index)
		}
	}
	result := ValidatedEvent{metadata: public, resolvedEventSchema: metadata.ResolvedEventSchema, identity: owned}
	if entity != nil {
		if entity.ModelID() != metadata.ModelID || metadata.Action == golem.EventDeleted {
			return ValidatedEvent{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated event entity disagrees with metadata")
		}
		result.entity, result.hasEntity = *entity, true
	}
	return result, nil
}

func (event ValidatedEvent) Metadata() golem.EventMetadata { return event.metadata }
func (event ValidatedEvent) ResolvedEventSchemaDigest() golem.EventSchemaDigest {
	return event.resolvedEventSchema
}
func (event ValidatedEvent) IdentityValues() []any {
	result := make([]any, len(event.identity))
	for index, value := range event.identity {
		result[index] = cloneIdentityValue(value)
	}
	return result
}
func (event ValidatedEvent) Entity() (golem.RuntimeModelRow, bool) {
	return event.entity, event.hasEntity
}

func cloneIdentityValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

package golem

import (
	"context"
	"fmt"
	"time"

	"github.com/eleven-am/golem/go/internal/event/metadatavalue"
)

// EventAction is the closed action vocabulary shared by durable mutation
// facts, typed event streams, transports, and GraphQL subscriptions.
type EventAction string

const (
	EventCreated EventAction = "created"
	EventUpdated EventAction = "updated"
	EventDeleted EventAction = "deleted"
)

type EventID [16]byte
type CausationID [16]byte
type EventSchemaDigest [32]byte

// EventMetadata is the immutable public portion of a validated event. Its
// representation is private so application code cannot forge deliverable
// event metadata from model IDs or transport bytes.
type EventMetadata struct {
	eventID     EventID
	action      EventAction
	causationID CausationID
	ordinal     uint32
	recordedAt  time.Time
	generation  SchemaDigest
	eventSchema EventSchemaDigest
	hasSchema   bool
	model       ModelID
}

// RuntimeValidatedEventMetadata consumes a representation-closed internal
// capability. The argument type lives below Go's internal-package boundary, so
// an external application can inspect EventMetadata but cannot implement a
// lookalike view and materialize arbitrary metadata.
func RuntimeValidatedEventMetadata(value metadatavalue.Value) (EventMetadata, error) {
	eventID, action := EventID(value.EventID()), EventAction(value.Action())
	causation, ordinal := CausationID(value.CausationID()), value.Ordinal()
	recordedAt, generation, model := value.RecordedAt(), SchemaDigest(value.Generation()), ModelID(value.ModelID())
	eventSchemaValue, hasSchema := value.EventSchema()
	eventSchema := EventSchemaDigest(eventSchemaValue)
	if eventID == (EventID{}) || causation == (CausationID{}) || ordinal == 0 || recordedAt.IsZero() || generation == (SchemaDigest{}) || model == (ModelID{}) {
		return EventMetadata{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated metadata is incomplete")
	}
	if action != EventCreated && action != EventUpdated && action != EventDeleted {
		return EventMetadata{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated metadata action is unknown")
	}
	recordedAt = recordedAt.UTC().Truncate(time.Microsecond)
	if hasSchema && eventSchema == (EventSchemaDigest{}) || !hasSchema && eventSchema != (EventSchemaDigest{}) {
		return EventMetadata{}, fmt.Errorf("GOLEM_EVENT_CODEC: validated event-schema metadata is inconsistent")
	}
	return EventMetadata{eventID: eventID, action: action, causationID: causation, ordinal: ordinal, recordedAt: recordedAt, generation: generation, eventSchema: eventSchema, hasSchema: hasSchema, model: model}, nil
}

func (metadata EventMetadata) EventID() EventID               { return metadata.eventID }
func (metadata EventMetadata) Action() EventAction            { return metadata.action }
func (metadata EventMetadata) CausationID() CausationID       { return metadata.causationID }
func (metadata EventMetadata) TransactionOrdinal() uint32     { return metadata.ordinal }
func (metadata EventMetadata) RecordedAt() time.Time          { return metadata.recordedAt }
func (metadata EventMetadata) GenerationDigest() SchemaDigest { return metadata.generation }
func (metadata EventMetadata) ModelID() ModelID               { return metadata.model }
func (metadata EventMetadata) EventSchemaDigest() (EventSchemaDigest, bool) {
	return metadata.eventSchema, metadata.hasSchema
}

// EventOption is sealed to Golem's typed where and selection constructors.
// Values retain their model witness and own mutable input data at freeze time.
type EventOption[M any] interface{ eventOption(M) eventOptionNode[M] }

type eventOptionKind uint8

const (
	eventOptionWhere eventOptionKind = iota + 1
	eventOptionSelect
)

type eventOptionNode[M any] struct {
	kind      eventOptionKind
	predicate Predicate[M]
	fields    []FieldID
}

type eventOption[M any] struct{ node eventOptionNode[M] }

func (option eventOption[M]) eventOption(M) eventOptionNode[M] {
	result := option.node
	result.fields = append([]FieldID(nil), option.node.fields...)
	return result
}

func EventWhere[M any](predicate Predicate[M]) EventOption[M] {
	return eventOption[M]{node: eventOptionNode[M]{kind: eventOptionWhere, predicate: predicate}}
}

func EventSelect[M any](fields ...Field[M]) EventOption[M] {
	identities := make([]FieldID, len(fields))
	var witness M
	for index, field := range fields {
		if field != nil {
			field.fieldModel(witness)
			identities[index] = field.fieldIdentity()
		}
	}
	return eventOption[M]{node: eventOptionNode[M]{kind: eventOptionSelect, fields: identities}}
}

// FrozenEventRequest is the representation-opaque, owned event request used by
// later P7 runtime layers. It deliberately exposes no transport or publisher
// capability.
type FrozenEventRequest struct {
	model     ModelID
	where     *FrozenPredicate
	selection []FieldID
}

func RuntimeFreezeEventOptions[M any](descriptor ModelDescriptor[M], options ...EventOption[M]) (FrozenEventRequest, error) {
	metadata := descriptor.Metadata()
	model := metadata.ModelID()
	if model == (ModelID{}) {
		return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: model descriptor is absent")
	}
	storedFields := make(map[FieldID]struct{}, len(metadata.ScanFields()))
	for _, field := range metadata.ScanFields() {
		if field != (FieldID{}) {
			storedFields[field] = struct{}{}
		}
	}
	relations := make(map[FieldID]RelationID, len(metadata.Relations()))
	for _, relation := range metadata.Relations() {
		if relation.ModelID() == model && relation.FieldID() != (FieldID{}) && relation.RelationID() != (RelationID{}) {
			relations[relation.FieldID()] = relation.RelationID()
		}
	}
	result := FrozenEventRequest{model: model}
	var witness M
	for index, option := range options {
		if option == nil {
			return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: option %d is nil", index)
		}
		node := option.eventOption(witness)
		switch node.kind {
		case eventOptionWhere:
			if result.where != nil {
				return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: where is declared more than once")
			}
			frozen, err := node.predicate.Freeze(descriptor)
			if err != nil {
				return FrozenEventRequest{}, err
			}
			if err := validateEventPredicateRoot(frozen.View().Root(), storedFields, relations); err != nil {
				return FrozenEventRequest{}, err
			}
			result.where = &frozen
		case eventOptionSelect:
			if result.selection != nil {
				return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: selection is declared more than once")
			}
			if len(node.fields) == 0 {
				return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: selection is empty")
			}
			seen := make(map[FieldID]bool, len(node.fields))
			for fieldIndex, field := range node.fields {
				if field == (FieldID{}) || seen[field] {
					return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: selection field %d is absent or duplicated", fieldIndex)
				}
				if _, stored := storedFields[field]; !stored {
					if _, relation := relations[field]; !relation {
						return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: selection field %d is foreign to the model descriptor", fieldIndex)
					}
				}
				seen[field] = true
			}
			result.selection = append([]FieldID(nil), node.fields...)
		default:
			return FrozenEventRequest{}, fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: option %d has an unknown kind", index)
		}
	}
	return result, nil
}

func validateEventPredicateRoot(condition FrozenConditionView, storedFields map[FieldID]struct{}, relations map[FieldID]RelationID) error {
	switch condition.Kind() {
	case FrozenConditionConstant:
		return nil
	case FrozenConditionLogical:
		for _, child := range condition.Children() {
			if err := validateEventPredicateRoot(child, storedFields, relations); err != nil {
				return err
			}
		}
		return nil
	case FrozenConditionRelation:
		reference, ok := condition.Relation()
		if !ok || relations[reference.FieldID()] != reference.RelationID() {
			return fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: where contains a foreign relation")
		}
		// A ModelDescriptor deliberately contains only this model's shape. The
		// complete runtime registry validates fields below the relation edge.
		return nil
	case FrozenConditionScalar, FrozenConditionList, FrozenConditionJSON:
		field, ok := condition.FieldID()
		if !ok {
			return fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: where contains an absent field")
		}
		if _, stored := storedFields[field]; !stored {
			return fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: where contains a foreign field")
		}
		return nil
	default:
		return fmt.Errorf("GOLEM_SUBSCRIPTION_INVALID: where contains an unknown condition")
	}
}

func (request FrozenEventRequest) ModelID() ModelID { return request.model }
func (request FrozenEventRequest) Where() (FrozenPredicate, bool) {
	if request.where == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*request.where), true
}
func (request FrozenEventRequest) Selection() []FieldID {
	return append([]FieldID(nil), request.selection...)
}

type EventStream[E any] interface {
	Recv(context.Context) (E, error)
	Close() error
}

// EventModelMetadata is the immutable generated registry entry used to route a
// validated notice to its exact generated identity and event payload shape.
type EventModelMetadata struct {
	model        ModelID
	schema       EventSchemaDigest
	identity     []FieldID
	payloadType  string
	identityType string
}

func GeneratedEventModelMetadata(model ModelID, schema EventSchemaDigest, identity []FieldID, payloadType, identityType string) EventModelMetadata {
	return EventModelMetadata{model: model, schema: schema, identity: append([]FieldID(nil), identity...), payloadType: payloadType, identityType: identityType}
}

func (metadata EventModelMetadata) ModelID() ModelID                     { return metadata.model }
func (metadata EventModelMetadata) EventSchemaDigest() EventSchemaDigest { return metadata.schema }
func (metadata EventModelMetadata) IdentityFields() []FieldID {
	return append([]FieldID(nil), metadata.identity...)
}
func (metadata EventModelMetadata) PayloadTypeName() string  { return metadata.payloadType }
func (metadata EventModelMetadata) IdentityTypeName() string { return metadata.identityType }
func (metadata EventModelMetadata) clone() EventModelMetadata {
	return GeneratedEventModelMetadata(metadata.model, metadata.schema, metadata.identity, metadata.payloadType, metadata.identityType)
}

type PackageEventRegistry struct {
	generation SchemaDigest
	models     []EventModelMetadata
}

type EventRegistry struct {
	generation SchemaDigest
	packages   []PackageEventRegistry
}

func GeneratedPackageEventRegistry(generation SchemaDigest, models ...EventModelMetadata) PackageEventRegistry {
	result := PackageEventRegistry{generation: generation, models: make([]EventModelMetadata, len(models))}
	for index, model := range models {
		result.models[index] = model.clone()
	}
	return result
}

func GeneratedEventRegistry(expected SchemaDigest, packages ...PackageEventRegistry) (EventRegistry, error) {
	digests := make([]SchemaDigest, len(packages))
	for index, registry := range packages {
		digests[index] = registry.generation
	}
	if err := validateGenerationDigests("events", expected, digests); err != nil {
		return EventRegistry{}, err
	}
	seen := make(map[ModelID]bool)
	result := EventRegistry{generation: expected, packages: make([]PackageEventRegistry, len(packages))}
	for index, registry := range packages {
		models := registry.Models()
		for _, model := range models {
			if err := validateEventModelMetadata(model); err != nil {
				return EventRegistry{}, err
			}
			if seen[model.model] {
				return EventRegistry{}, fmt.Errorf("generated event registry repeats model %x", model.model)
			}
			seen[model.model] = true
		}
		result.packages[index] = GeneratedPackageEventRegistry(registry.generation, models...)
	}
	return result, nil
}

func validateEventModelMetadata(metadata EventModelMetadata) error {
	if metadata.model == (ModelID{}) || metadata.schema == (EventSchemaDigest{}) || metadata.payloadType == "" || len(metadata.identity) == 0 {
		return fmt.Errorf("generated event registry contains incomplete model metadata")
	}
	seen := make(map[FieldID]bool, len(metadata.identity))
	for _, field := range metadata.identity {
		if field == (FieldID{}) || seen[field] {
			return fmt.Errorf("generated event registry contains invalid identity fields")
		}
		seen[field] = true
	}
	return nil
}

func (registry PackageEventRegistry) GenerationDigest() SchemaDigest { return registry.generation }
func (registry PackageEventRegistry) Models() []EventModelMetadata {
	result := make([]EventModelMetadata, len(registry.models))
	for index, model := range registry.models {
		result[index] = model.clone()
	}
	return result
}
func (registry EventRegistry) GenerationDigest() SchemaDigest { return registry.generation }
func (registry EventRegistry) Models() []EventModelMetadata {
	var result []EventModelMetadata
	for _, pkg := range registry.packages {
		result = append(result, pkg.Models()...)
	}
	return result
}
func (registry EventRegistry) Lookup(model ModelID) (EventModelMetadata, bool) {
	for _, metadata := range registry.Models() {
		if metadata.model == model {
			return metadata, true
		}
	}
	return EventModelMetadata{}, false
}

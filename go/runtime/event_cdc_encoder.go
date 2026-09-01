package runtime

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// cdcEventEncoder is the production bridge from one trusted adapter's exact
// stored-scalar images into the same V2 mutation fact and golem.event.v1 codec
// path used by the transactional outbox publisher. It defines no CDC wire
// format and accepts no caller-supplied schema, generation, or encoded bytes.
type cdcEventEncoder struct {
	registry *schema.Registry
	history  *eventSchemaHistory
	limits   eventcodec.Limits
}

func newCDCEventEncoder(registry *schema.Registry, history *eventSchemaHistory, maximumEncodedBytes int) (*cdcEventEncoder, error) {
	if registry == nil || history == nil {
		return nil, fmt.Errorf("P7_CDC_ENCODER_CONFIG: active registry and event history are required")
	}
	// EncodeStoredRow performs the authoritative lower/upper-bound validation.
	// Reject a negative value here; zero deliberately selects the codec default.
	if maximumEncodedBytes < 0 || maximumEncodedBytes > eventcodec.HardMaxEncodedBytes {
		return nil, fmt.Errorf("P7_CDC_ENCODER_CONFIG: encoded event byte limit is invalid")
	}
	return &cdcEventEncoder{
		registry: registry,
		history:  history,
		limits:   eventcodec.Limits{MaxEncodedBytes: maximumEncodedBytes},
	}, nil
}

func (encoder *cdcEventEncoder) EncodeCDC(ctx context.Context, input eventcdc.EncodeInput) (events.Notice, error) {
	if encoder == nil || encoder.registry == nil || encoder.history == nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_ENCODER_CONFIG: encoder is incomplete")
	}
	if ctx == nil || ctx.Err() != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_ENCODER_INPUT: context is unavailable")
	}
	if err := validateCDCEncodeInput(encoder.registry, input); err != nil {
		return events.Notice{}, err
	}

	modelID := policyir.ModelID(input.Model)
	requirement, eventSchema, err := cdcFactRequirement(encoder.registry, modelID, input.Action)
	if err != nil {
		return events.Notice{}, err
	}
	before, err := decodeCDCExactImage(encoder.registry, modelID, input.Before)
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_IMAGE: before: %w", err)
	}
	after, err := decodeCDCExactImage(encoder.registry, modelID, input.After)
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_IMAGE: after: %w", err)
	}

	fact, err := mutationfact.NewV2(
		encoder.registry,
		golem.SchemaDigest(eventSchema),
		mutationfact.EventID(input.EventID),
		requirement,
		mutationfact.CausationID(input.CausationID),
		input.Ordinal,
		before,
		after,
	)
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_FACT: %w", err)
	}
	row, err := fact.OutboxRow(input.RecordedAt)
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_FACT: canonical row: %w", err)
	}
	envelope, err := eventcodec.EncodeStoredRow(row, encoder.history, encoder.limits)
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_EVENT: %w", err)
	}
	if envelope.EventID() != input.EventID || envelope.CausationID() != input.CausationID || envelope.TransactionOrdinal() != input.Ordinal || envelope.ModelID() != input.Model || envelope.Action() != input.Action || envelope.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(eventSchema) {
		return events.Notice{}, fmt.Errorf("P7_CDC_EVENT: canonical metadata differs from injected input")
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		return events.Notice{}, fmt.Errorf("P7_CDC_EVENT: sealed notice: %w", err)
	}
	return notice, nil
}

func validateCDCEncodeInput(registry *schema.Registry, input eventcdc.EncodeInput) error {
	if events.ValidateCDCIdentity(input.Adapter) != nil ||
		len(input.SourceTransactionID) == 0 || len(input.SourceTransactionID) > events.MaximumCDCSourceTransactionBytes || !utf8.ValidString(input.SourceTransactionID) ||
		input.RecordedAt.IsZero() || input.RecordedAt.Location() != time.UTC || input.RecordedAt.Nanosecond()%int(time.Microsecond) != 0 ||
		len(input.Cursor) > events.MaximumCDCCursorBytes ||
		input.EventID == (golem.EventID{}) || input.CausationID == (golem.CausationID{}) || input.Ordinal == 0 || input.Model == (golem.ModelID{}) {
		return fmt.Errorf("P7_CDC_ENCODER_INPUT: identity metadata is invalid")
	}
	providerEnabled := false
	for _, provider := range registry.Providers() {
		if provider == input.Adapter.Provider {
			providerEnabled = true
			break
		}
	}
	if !providerEnabled {
		return fmt.Errorf("P7_CDC_ENCODER_INPUT: adapter provider is outside the active schema")
	}
	if input.Before != nil && input.Before.ModelID() != input.Model || input.After != nil && input.After.ModelID() != input.Model {
		return fmt.Errorf("P7_CDC_ENCODER_INPUT: image model differs from the declared model")
	}
	switch input.Action {
	case golem.EventCreated:
		if input.Before != nil || input.After == nil {
			return fmt.Errorf("P7_CDC_ENCODER_INPUT: create requires only an after image")
		}
	case golem.EventUpdated:
		if input.Before == nil || input.After == nil {
			return fmt.Errorf("P7_CDC_ENCODER_INPUT: update requires before and after images")
		}
	case golem.EventDeleted:
		if input.Before == nil || input.After != nil {
			return fmt.Errorf("P7_CDC_ENCODER_INPUT: delete requires only a before image")
		}
	default:
		return fmt.Errorf("P7_CDC_ENCODER_INPUT: action is invalid")
	}
	return nil
}

func cdcFactRequirement(registry *schema.Registry, modelID policyir.ModelID, action golem.EventAction) (mutationir.FactRequirement, [32]byte, error) {
	_, eventSchema, deleteSnapshot, err := mutationplan.ModelEventSchema(registry, modelID)
	if err != nil {
		return mutationir.FactRequirement{}, [32]byte{}, err
	}
	model, ok := registry.Model(golem.ModelID(modelID))
	if !ok || len(model.PrimaryKey()) == 0 {
		return mutationir.FactRequirement{}, [32]byte{}, fmt.Errorf("P7_CDC_FACT: compiler-owned primary identity is absent")
	}
	identity := make([]policyir.FieldID, len(model.PrimaryKey()))
	for index, field := range model.PrimaryKey() {
		identity[index] = policyir.FieldID(field)
	}
	var requirement mutationir.FactRequirement
	switch action {
	case golem.EventCreated:
		requirement, err = mutationir.NewFactRequirement(mutationir.FactCreated, nil, identity, nil)
	case golem.EventUpdated:
		requirement, err = mutationir.NewFactRequirement(mutationir.FactUpdated, identity, identity, nil)
	case golem.EventDeleted:
		requirement, err = mutationir.NewDeleteFactRequirement(identity, mutationir.DeleteSnapshotStoredScalars, deleteSnapshot)
	default:
		err = fmt.Errorf("unknown event action")
	}
	if err != nil {
		return mutationir.FactRequirement{}, [32]byte{}, fmt.Errorf("P7_CDC_FACT: requirement: %w", err)
	}
	requirement, err = requirement.WithEventSchema(eventSchema)
	if err != nil {
		return mutationir.FactRequirement{}, [32]byte{}, fmt.Errorf("P7_CDC_FACT: event schema: %w", err)
	}
	return requirement, eventSchema, nil
}

func decodeCDCExactImage(registry *schema.Registry, modelID policyir.ModelID, source *golem.RuntimeModelRow) (*mutationdecode.Row, error) {
	if source == nil {
		return nil, nil
	}
	if source.ModelID() != golem.ModelID(modelID) {
		return nil, fmt.Errorf("model identity differs")
	}
	inventory, exact := golem.RuntimeCDCModelRowInventory(*source)
	if !exact {
		return nil, fmt.Errorf("row lacks trusted exact-image provenance or contains response relation data")
	}
	model, ok := registry.Model(golem.ModelID(modelID))
	if !ok {
		return nil, fmt.Errorf("model is absent from active schema")
	}
	wanted := make(map[golem.FieldID]schema.Field)
	for _, fieldID := range model.Fields() {
		field, present := registry.Field(golem.ModelID(modelID), fieldID)
		if !present {
			return nil, fmt.Errorf("compiler-owned field disappeared")
		}
		if field.Kind() != compilerir.FieldRelation {
			wanted[fieldID] = field
		}
	}
	if len(inventory) != len(wanted) {
		return nil, fmt.Errorf("stored-scalar inventory is incomplete or contains foreign/relation fields")
	}
	cells := make([]mutationdecode.Cell, len(inventory))
	seen := make(map[golem.FieldID]struct{}, len(inventory))
	for index, fieldID := range inventory {
		field, present := wanted[fieldID]
		if !present {
			return nil, fmt.Errorf("field %x is foreign, relational, or absent", fieldID)
		}
		if _, duplicate := seen[fieldID]; duplicate {
			return nil, fmt.Errorf("field %x is duplicated", fieldID)
		}
		seen[fieldID] = struct{}{}
		cell := golem.RuntimeTransportField(*source, fieldID)
		switch cell.State() {
		case golem.ReadNull:
			if !field.Nullable() {
				return nil, fmt.Errorf("non-null field %x contains NULL", fieldID)
			}
			cells[index] = mutationdecode.Null(policyir.FieldID(fieldID))
		case golem.ReadPresent:
			raw, present := cell.Get()
			if !present {
				return nil, fmt.Errorf("field %x has no exact value", fieldID)
			}
			value, err := mutationbind.ExactStoredScalarValue(raw, field, registry)
			if err != nil {
				return nil, fmt.Errorf("field %x: %w", fieldID, err)
			}
			cells[index] = mutationdecode.Value(policyir.FieldID(fieldID), value)
		default:
			return nil, fmt.Errorf("field %x is unselected or masked", fieldID)
		}
	}
	row, err := mutationdecode.NewCompleteRow(registry, modelID, cells)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

var _ eventcdc.Encoder = (*cdcEventEncoder)(nil)

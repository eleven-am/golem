package operation

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

// EncodeEventWithComputedPartial prepares one already authorized event for the
// active gqlgen executable. It uses the same scalar, relation, masking, and
// computed encoders as ordinary P5 reads; it does not serialize GraphQL JSON.
func (c *Compiler) EncodeEventWithComputedPartial(ctx context.Context, root EventRoot, metadata golem.EventMetadata, identity []any, entity *golem.RuntimeModelRow, resolve ComputedResolver) (map[string]any, []ComputedFieldFailure, error) {
	if c == nil || resolve == nil || root.Model == "" || len(root.IdentityFields) == 0 {
		return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: compiler, model, identity, and resolver are required")
	}
	publicModel, err := publicModelID(root.Model)
	if err != nil || metadata.ModelID() != publicModel || len(identity) != len(root.IdentityFields) {
		return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: event metadata or identity does not match the compiled model")
	}
	model, contract, ok := c.model(root.Model)
	if !ok || contract.Event == nil {
		return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: event contract is absent")
	}
	fields := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	encodedIdentity := make(map[compilerir.FieldID]any, len(identity))
	for index, fieldID := range root.IdentityFields {
		field, present := fields[fieldID]
		if !present || field.Scalar == nil {
			return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: identity field %s is absent", fieldID)
		}
		value, err := c.encodeLogical(field.Scalar.Type, identity[index])
		if err != nil {
			return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: identity field %s: %w", fieldID, err)
		}
		encodedIdentity[fieldID] = value
	}
	var failures []ComputedFieldFailure
	result := make(map[string]any, len(root.Slots))
	for _, slot := range root.Slots {
		switch slot.Kind {
		case EventSlotTypename:
			result[slot.ResponseName] = contract.Event.PayloadTypeName
		case EventSlotMetadata:
			value, err := encodeEventMetadata(slot.FieldName, metadata)
			if err != nil {
				return nil, nil, err
			}
			result[slot.ResponseName] = value
		case EventSlotIdentity:
			if len(root.IdentityFields) == 1 {
				result[slot.ResponseName] = encodedIdentity[root.IdentityFields[0]]
				continue
			}
			value := make(map[string]any, len(slot.Identity))
			for _, child := range slot.Identity {
				if child.Typename {
					value[child.ResponseName] = contract.Event.IdentityTypeName
					continue
				}
				encoded, present := encodedIdentity[child.FieldID]
				if !present {
					return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: selected identity field %s is absent", child.FieldID)
				}
				value[child.ResponseName] = encoded
			}
			result[slot.ResponseName] = value
		case EventSlotEntity:
			if entity == nil {
				result[slot.ResponseName] = nil
				continue
			}
			rows, computedFailures, err := c.encodeComputedRowsPartial(ctx, root.Model, []golem.RuntimeModelRow{*entity}, slot.EntitySlots, resolve, [][]any{{root.ResponseName, slot.ResponseName}})
			if err != nil {
				return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: entity %s: %w", slot.ResponseName, err)
			}
			result[slot.ResponseName] = rows[0]
			failures = append(failures, computedFailures...)
		default:
			return nil, nil, fmt.Errorf("P7_ENCODE_EVENT: event slot %q has invalid kind %d", slot.ResponseName, slot.Kind)
		}
	}
	return result, failures, nil
}

func encodeEventMetadata(name string, metadata golem.EventMetadata) (any, error) {
	switch name {
	case "eventID":
		return golem.UUID(metadata.EventID()).String(), nil
	case "causationID":
		return golem.UUID(metadata.CausationID()).String(), nil
	case "transactionOrdinal":
		if metadata.TransactionOrdinal() > math.MaxInt32 {
			return nil, fmt.Errorf("P7_ENCODE_EVENT: transaction ordinal exceeds GraphQL Int")
		}
		return int32(metadata.TransactionOrdinal()), nil
	case "recordedAt":
		return metadata.RecordedAt().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), nil
	case "type":
		value := strings.ToUpper(string(metadata.Action()))
		if value != "CREATED" && value != "UPDATED" && value != "DELETED" {
			return nil, fmt.Errorf("P7_ENCODE_EVENT: event action is unknown")
		}
		return value, nil
	default:
		return nil, fmt.Errorf("P7_ENCODE_EVENT: metadata field %q is unknown", name)
	}
}

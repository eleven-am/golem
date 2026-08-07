// Package fact owns the lossless, versioned P4 mutation-fact envelope. It
// prepares provider-neutral values for atomic outbox insertion; leasing,
// publication, retries, and subscriptions remain P7 concerns.
package fact

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ParseEventSchemaFingerprint converts the sole compiler-owned hexadecimal
// event-schema fingerprint into its fixed-width wire representation.
func ParseEventSchemaFingerprint(value compilerir.Fingerprint) (golem.SchemaDigest, error) {
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(golem.SchemaDigest{}) {
		return golem.SchemaDigest{}, fmt.Errorf("P7_FACT_EVENT_SCHEMA: fingerprint is not 32 canonical bytes")
	}
	if string(value) != hex.EncodeToString(decoded) {
		return golem.SchemaDigest{}, fmt.Errorf("P7_FACT_EVENT_SCHEMA: fingerprint is not lowercase canonical hex")
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	if result == (golem.SchemaDigest{}) {
		return golem.SchemaDigest{}, fmt.Errorf("P7_FACT_EVENT_SCHEMA: fingerprint is zero")
	}
	return result, nil
}

const (
	FormatVersionV1 uint16 = 1
	FormatVersionV2 uint16 = 2
	CodecIdentityV1        = "golem.fact.v1"
	CodecIdentityV2        = "golem.fact.v2"

	// FormatVersion and CodecIdentity remain aliases for the historical P4 API.
	// New P7 runtime paths must opt into NewV2 with a normalized event-schema
	// digest; silently relabelling old constructors would make compatibility
	// tests incapable of producing genuine V1 bytes.
	FormatVersion = FormatVersionV1
	CodecIdentity = CodecIdentityV1
)

type EventID [16]byte
type CausationID [16]byte

type Envelope struct {
	formatVersion  uint16
	codecIdentity  string
	event          EventID
	generation     golem.SchemaDigest
	eventSchema    golem.SchemaDigest
	model          policyir.ModelID
	action         mutationir.FactAction
	causation      CausationID
	ordinal        uint32
	beforeIdentity *mutationdecode.Identity
	afterIdentity  *mutationdecode.Identity
	snapshotFields []policyir.FieldID
	deleteSnapshot *mutationdecode.Row
	deleteState    mutationir.DeleteSnapshotState
}

func New(registry *schema.Registry, event EventID, requirement mutationir.FactRequirement, causation CausationID, ordinal uint32, before, after *mutationdecode.Row) (Envelope, error) {
	return newEnvelope(registry, event, requirement, causation, ordinal, before, after, false)
}

func newEnvelope(registry *schema.Registry, event EventID, requirement mutationir.FactRequirement, causation CausationID, ordinal uint32, before, after *mutationdecode.Row, allowEventSchema bool) (Envelope, error) {
	if registry == nil {
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: active schema registry is required")
	}
	if event == (EventID{}) || causation == (CausationID{}) {
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: event and causation identities must be non-zero")
	}
	if _, present := requirement.EventSchema(); present && !allowEventSchema {
		return Envelope{}, fmt.Errorf("P7_FACT_INPUT: event-schema-bound requirement must use V2 constructor")
	}
	action, enabled := requirement.Action()
	if !enabled {
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: fact requirement is disabled")
	}
	model := policyir.ModelID{}
	if before != nil {
		model = before.ModelID()
	}
	if after != nil {
		if model != (policyir.ModelID{}) && model != after.ModelID() {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: before and after models differ")
		}
		model = after.ModelID()
	}
	beforeFields, afterFields := requirement.BeforeIdentity(), requirement.AfterIdentity()
	snapshotFields := requirement.PrivateDeleteSnapshot()
	switch action {
	case mutationir.FactCreated:
		if len(beforeFields) != 0 || len(afterFields) == 0 || before != nil || after == nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: created fact requires only an after image")
		}
	case mutationir.FactUpdated:
		if len(beforeFields) == 0 || len(afterFields) == 0 || before == nil || after == nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: updated fact requires before and after images")
		}
	case mutationir.FactDeleted:
		if len(beforeFields) == 0 || len(afterFields) != 0 || before == nil || after != nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: deleted fact requires only a before image")
		}
	default:
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: unknown fact action %d", action)
	}
	generation := registry.GenerationDigest()
	if generation == (golem.SchemaDigest{}) {
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: generation fingerprint is zero")
	}
	result := Envelope{
		formatVersion: FormatVersionV1, codecIdentity: CodecIdentityV1,
		event: event, generation: generation, model: model, action: action,
		causation: causation, ordinal: ordinal,
		snapshotFields: append([]policyir.FieldID(nil), snapshotFields...),
		deleteState:    requirement.DeleteSnapshotState(),
	}
	if len(beforeFields) != 0 {
		identity, err := mutationdecode.ExtractOrderedIdentity(registry, *before, beforeFields)
		if err != nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: before identity: %w", err)
		}
		result.beforeIdentity = &identity
	}
	if len(afterFields) != 0 {
		identity, err := mutationdecode.ExtractOrderedIdentity(registry, *after, afterFields)
		if err != nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: after identity: %w", err)
		}
		result.afterIdentity = &identity
	}
	if len(snapshotFields) != 0 {
		snapshot, err := before.Select(registry, snapshotFields)
		if err != nil {
			return Envelope{}, fmt.Errorf("P4_FACT_INPUT: private delete snapshot: %w", err)
		}
		result.deleteSnapshot = &snapshot
	}
	return result, nil
}

// NewV2 constructs a fact bound to the compiler-owned event-schema digest.
// The digest must come from compiler/ir.EventSchemaFingerprint; this package
// deliberately does not maintain a second schema hash implementation.
func NewV2(registry *schema.Registry, eventSchema golem.SchemaDigest, event EventID, requirement mutationir.FactRequirement, causation CausationID, ordinal uint32, before, after *mutationdecode.Row) (Envelope, error) {
	if eventSchema == (golem.SchemaDigest{}) {
		return Envelope{}, fmt.Errorf("P7_FACT_INPUT: event-schema fingerprint is zero")
	}
	if required, present := requirement.EventSchema(); !present || golem.SchemaDigest(required) != eventSchema {
		return Envelope{}, fmt.Errorf("P7_FACT_INPUT: requirement and event-schema fingerprints differ")
	}
	envelope, err := newEnvelope(registry, event, requirement, causation, ordinal, before, after, true)
	if err != nil {
		return Envelope{}, err
	}
	envelope.formatVersion = FormatVersionV2
	envelope.codecIdentity = CodecIdentityV2
	envelope.eventSchema = eventSchema
	return envelope, nil
}

func (envelope Envelope) FormatVersion() uint16          { return envelope.formatVersion }
func (envelope Envelope) CodecIdentity() string          { return envelope.codecIdentity }
func (envelope Envelope) EventID() EventID               { return envelope.event }
func (envelope Envelope) Generation() golem.SchemaDigest { return envelope.generation }
func (envelope Envelope) EventSchemaDigest() (golem.SchemaDigest, bool) {
	return envelope.eventSchema, envelope.formatVersion == FormatVersionV2
}
func (envelope Envelope) ModelID() policyir.ModelID     { return envelope.model }
func (envelope Envelope) Action() mutationir.FactAction { return envelope.action }
func (envelope Envelope) CausationID() CausationID      { return envelope.causation }
func (envelope Envelope) TransactionOrdinal() uint32    { return envelope.ordinal }
func (envelope Envelope) WithTransactionOrdinal(ordinal uint32) (Envelope, error) {
	if ordinal == 0 {
		return Envelope{}, fmt.Errorf("P4_FACT_INPUT: transaction ordinal must be positive")
	}
	envelope.ordinal = ordinal
	return envelope, nil
}
func (envelope Envelope) BeforeIdentity() (mutationdecode.Identity, bool) {
	if envelope.beforeIdentity == nil {
		return mutationdecode.Identity{}, false
	}
	return *envelope.beforeIdentity, true
}
func (envelope Envelope) AfterIdentity() (mutationdecode.Identity, bool) {
	if envelope.afterIdentity == nil {
		return mutationdecode.Identity{}, false
	}
	return *envelope.afterIdentity, true
}
func (envelope Envelope) PrivateDeleteSnapshotFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), envelope.snapshotFields...)
}
func (envelope Envelope) PrivateDeleteSnapshot() (mutationdecode.Row, bool) {
	if envelope.deleteSnapshot == nil {
		return mutationdecode.Row{}, false
	}
	return *envelope.deleteSnapshot, true
}
func (envelope Envelope) DeleteSnapshotState() mutationir.DeleteSnapshotState {
	return envelope.deleteState
}

func actionText(action mutationir.FactAction) string {
	switch action {
	case mutationir.FactCreated:
		return "created"
	case mutationir.FactUpdated:
		return "updated"
	case mutationir.FactDeleted:
		return "deleted"
	default:
		return ""
	}
}

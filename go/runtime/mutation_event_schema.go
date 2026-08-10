package runtime

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// mutationEventSchema consumes only compiler-validated registry metadata. It
// never guesses a snapshot from whatever fields a mutation happened to read.
func mutationEventSchema(registry *schema.Registry, model policyir.ModelID) (mutationir.FactCodecRequirement, [32]byte, []policyir.FieldID, error) {
	if registry == nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, fmt.Errorf("P7_RUNTIME_EVENT_SCHEMA: registry is absent")
	}
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok || !metadata.SubscriptionsEnabled() {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, fmt.Errorf("P7_RUNTIME_EVENT_SCHEMA: model is not subscription-enabled")
	}
	fingerprint, snapshot, ok := metadata.EventSchema()
	if !ok {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, fmt.Errorf("P7_RUNTIME_EVENT_SCHEMA: normalized event schema is absent")
	}
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, err
	}
	codec, err := mutationir.NewFactCodecRequirement(mutationfact.FormatVersionV2, mutationfact.CodecIdentityV2, [32]byte(registry.GenerationDigest()))
	if err != nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, err
	}
	fields := make([]policyir.FieldID, len(snapshot))
	for index, field := range snapshot {
		fields[index] = policyir.FieldID(field)
	}
	return codec, [32]byte(digest), fields, nil
}

func decodeRuntimeMutationFact(registry *schema.Registry, row mutationfact.OutboxRow) (mutationfact.Envelope, error) {
	encodedModel, err := hex.DecodeString(row.ModelID)
	if err != nil || len(encodedModel) != 16 {
		return mutationfact.Envelope{}, fmt.Errorf("P7_RUNTIME_EVENT_SCHEMA: outbox model identity is invalid")
	}
	var model policyir.ModelID
	copy(model[:], encodedModel)
	return decodeCurrentMutationFact(registry, model, row.Metadata, row.DeleteSnapshot)
}

// decodeCurrentMutationFact is the only active-registry inspection seam for
// runtime-owned V2 facts. It resolves the compiler digest for the exact model;
// callers cannot make Decode/DecodeOutbox silently accept V2 as V1.
func decodeCurrentMutationFact(registry *schema.Registry, model policyir.ModelID, metadata, deleteSnapshot []byte) (mutationfact.Envelope, error) {
	_, digest, _, err := mutationEventSchema(registry, model)
	if err != nil {
		return mutationfact.Envelope{}, err
	}
	return mutationfact.DecodeOutboxV2(metadata, deleteSnapshot, registry, golem.SchemaDigest(digest))
}

func decodeCurrentMutationFactMetadata(registry *schema.Registry, model policyir.ModelID, metadata []byte) (mutationfact.Envelope, error) {
	_, digest, _, err := mutationEventSchema(registry, model)
	if err != nil {
		return mutationfact.Envelope{}, err
	}
	return mutationfact.DecodeV2(metadata, registry, golem.SchemaDigest(digest))
}

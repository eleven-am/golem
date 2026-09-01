package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// ModelEventSchema consumes only compiler-validated registry metadata. It never
// guesses a snapshot from whatever fields a mutation happened to read.
func ModelEventSchema(registry *schema.Registry, model policyir.ModelID) (mutationir.FactCodecRequirement, [32]byte, []policyir.FieldID, error) {
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

// factInventory is the complete durable-capture decision for one root plan.
// BuildRoot derives it once from the active registry so no prepare site can
// enable, disable, or re-spell capture on its own.
type factInventory struct {
	capture     bool
	semantic    bool
	codec       mutationir.FactCodecRequirement
	eventSchema [32]byte
	snapshot    []policyir.FieldID
}

func deriveFactInventory(registry *schema.Registry, model policyir.ModelID) (factInventory, error) {
	if registry == nil {
		return factInventory{}, fmt.Errorf("active model registry is absent")
	}
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok {
		return factInventory{}, fmt.Errorf("registry does not own model")
	}
	inventory := factInventory{semantic: metadata.SemanticIndexed()}
	if !metadata.SubscriptionsEnabled() {
		return inventory, nil
	}
	codec, eventSchema, snapshot, err := ModelEventSchema(registry, model)
	if err != nil {
		return factInventory{}, err
	}
	inventory.capture, inventory.codec, inventory.eventSchema, inventory.snapshot = true, codec, eventSchema, snapshot
	return inventory, nil
}

// deleteSnapshotFor is the operation-shaped half of the decision. Only the
// delete families carry a private pre-delete scalar snapshot, and that is
// mechanically derivable from the node's own operation.
func (inventory factInventory) deleteSnapshotFor(operation mutationir.Operation) []policyir.FieldID {
	if !inventory.capture || operation != mutationir.Delete && operation != mutationir.DeleteMany {
		return nil
	}
	return inventory.snapshot
}

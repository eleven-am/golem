package runtime

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

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
	_, digest, _, err := mutationplan.ModelEventSchema(registry, model)
	if err != nil {
		return mutationfact.Envelope{}, err
	}
	return mutationfact.DecodeOutboxV2(metadata, deleteSnapshot, registry, golem.SchemaDigest(digest))
}

func decodeCurrentMutationFactMetadata(registry *schema.Registry, model policyir.ModelID, metadata []byte) (mutationfact.Envelope, error) {
	_, digest, _, err := mutationplan.ModelEventSchema(registry, model)
	if err != nil {
		return mutationfact.Envelope{}, err
	}
	return mutationfact.DecodeV2(metadata, registry, golem.SchemaDigest(digest))
}

package runtime

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// eventSchemaHistory is the immutable startup-built resolver joining the
// active schema and explicitly supplied generated historical bundles. V1
// resolves by exact generation. V2 resolves by logical event-schema digest.
type eventSchemaHistory struct {
	byGeneration  map[golem.SchemaDigest]*schema.Registry
	byEventSchema map[golem.SchemaDigest]*schema.Registry
	activeSchemas map[golem.ModelID]golem.EventSchemaDigest
}

func newEventSchemaHistory(active *schema.Registry, historical []golem.SchemaBundle) (*eventSchemaHistory, error) {
	if active == nil {
		return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: active event schema registry is absent")
	}
	result := &eventSchemaHistory{
		byGeneration:  make(map[golem.SchemaDigest]*schema.Registry, len(historical)+1),
		byEventSchema: make(map[golem.SchemaDigest]*schema.Registry),
		activeSchemas: make(map[golem.ModelID]golem.EventSchemaDigest),
	}
	if err := result.add(active); err != nil {
		return nil, err
	}
	for _, model := range active.EventModels() {
		fingerprint, _, enabled := model.EventSchema()
		if !enabled {
			continue
		}
		digest, parseErr := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if parseErr != nil {
			return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: active model %x event schema: %w", model.ID(), parseErr)
		}
		result.activeSchemas[model.ID()] = golem.EventSchemaDigest(digest)
	}
	for index, bundle := range historical {
		registry, err := schema.NewHistorical(bundle)
		if err != nil {
			return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: historical event bundle %d: %w", index, err)
		}
		if err := result.add(registry); err != nil {
			return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: historical event bundle %d: %w", index, err)
		}
	}
	return result, nil
}

func (history *eventSchemaHistory) CanDeliverEventSchema(model golem.ModelID, digest golem.EventSchemaDigest) bool {
	if history == nil || model == (golem.ModelID{}) || digest == (golem.EventSchemaDigest{}) {
		return false
	}
	return history.activeSchemas[model] == digest
}

func (history *eventSchemaHistory) add(registry *schema.Registry) error {
	generation := registry.GenerationDigest()
	if _, duplicate := history.byGeneration[generation]; duplicate {
		return fmt.Errorf("duplicate generation digest %s", generation)
	}
	history.byGeneration[generation] = registry
	for _, model := range registry.EventModels() {
		fingerprint, _, enabled := model.EventSchema()
		if !enabled {
			continue
		}
		digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if err != nil {
			return fmt.Errorf("model %x event schema: %w", model.ID(), err)
		}
		// Identical event schemas may legitimately survive multiple contract-only
		// generations. Either exact registry validates the same event shape.
		if _, exists := history.byEventSchema[digest]; !exists {
			history.byEventSchema[digest] = registry
		}
	}
	return nil
}

func (history *eventSchemaHistory) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if history == nil {
		return nil, golem.SchemaDigest{}, false
	}
	if reference.FormatVersion == mutationfact.FormatVersionV1 {
		registry, ok := history.byGeneration[reference.Generation]
		return registry, golem.SchemaDigest{}, ok
	}
	if reference.FormatVersion == mutationfact.FormatVersionV2 {
		registry, ok := history.byEventSchema[reference.EventSchema]
		return registry, reference.EventSchema, ok
	}
	return nil, golem.SchemaDigest{}, false
}

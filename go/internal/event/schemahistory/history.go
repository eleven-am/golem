// Package schemahistory resolves stored event and policy evidence against its exact admitted schema history.
package schemahistory

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type History struct {
	byGeneration  map[golem.SchemaDigest]*schema.Registry
	byEventSchema map[golem.SchemaDigest]*schema.Registry
	activeSchemas map[golem.ModelID]golem.EventSchemaDigest
}

func New(active *schema.Registry, historical []golem.SchemaBundle) (*History, error) {
	if active == nil {
		return nil, fmt.Errorf("GOLEM_EVENT_CONFIG: active event schema registry is absent")
	}
	result := &History{
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

func (history *History) CanDeliverEventSchema(model golem.ModelID, digest golem.EventSchemaDigest) bool {
	if history == nil || model == (golem.ModelID{}) || digest == (golem.EventSchemaDigest{}) {
		return false
	}
	return history.activeSchemas[model] == digest
}

func (history *History) add(registry *schema.Registry) error {
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

func (history *History) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if history == nil {
		return nil, golem.SchemaDigest{}, false
	}
	if reference.FormatVersion == mutationfact.FormatVersion {
		registry, ok := history.byEventSchema[reference.EventSchema]
		return registry, reference.EventSchema, ok
	}
	if reference.FormatVersion == mutationfact.HistoryFactVersion {
		registry, ok := history.byGeneration[reference.Generation]
		return registry, reference.EventSchema, ok
	}
	return nil, golem.SchemaDigest{}, false
}

// ResolvePolicyHistorySchema resolves graph evidence only against its exact
// producer generation and capture projection. Event-row compatibility is not
// authority to reinterpret historical policy paths under another generation.
func (history *History) ResolvePolicyHistorySchema(generation, projectionDigest golem.SchemaDigest) (*schema.Registry, bool) {
	if history == nil || generation == (golem.SchemaDigest{}) || projectionDigest == (golem.SchemaDigest{}) {
		return nil, false
	}
	registry, found := history.byGeneration[generation]
	if !found || registry == nil || registry.PolicyCaptureFingerprint() != projectionDigest {
		return nil, false
	}
	return registry, true
}

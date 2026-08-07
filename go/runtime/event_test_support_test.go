package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type runtimeTestEventFactory struct {
	model  golem.ModelID
	schema golem.EventSchemaDigest
}

func (factory runtimeTestEventFactory) ModelID() golem.ModelID { return factory.model }
func (factory runtimeTestEventFactory) EventSchemaDigest() golem.EventSchemaDigest {
	return factory.schema
}
func (factory runtimeTestEventFactory) Build(value ValidatedEvent) (any, error) {
	return value.Metadata(), nil
}

// withRuntimeTestEvents supplies exact generated-equivalent event artifacts to
// legacy mutation/read fixtures whose ContractIR already enables subscription
// fact capture. Production Open remains strict; only tests that explicitly opt
// into this helper receive the inert memory transport/factories.
func withRuntimeTestEvents[P, A any](t testing.TB, config Config[P, A]) Config[P, A] {
	t.Helper()
	if len(config.EventRegistry.Models()) != 0 || config.Bundle.GenerationDigest() == (golem.SchemaDigest{}) {
		return config
	}
	registry, err := schema.New(config.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	models := registry.EventModels()
	if len(models) == 0 {
		return config
	}
	metadata := make([]golem.EventModelMetadata, len(models))
	factories := make([]EventFactory, len(models))
	for index, model := range models {
		fingerprint, _, enabled := model.EventSchema()
		digest, parseErr := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if !enabled || parseErr != nil {
			t.Fatalf("test event model %x schema: %v", model.ID(), parseErr)
		}
		eventSchema := golem.EventSchemaDigest(digest)
		metadata[index] = golem.GeneratedEventModelMetadata(model.ID(), eventSchema, model.PrimaryKey(), "runtimeTestEvent", "runtimeTestIdentity")
		factories[index] = runtimeTestEventFactory{model: model.ID(), schema: eventSchema}
	}
	config.EventRegistry, err = golem.GeneratedEventRegistry(config.Bundle.GenerationDigest(), golem.GeneratedPackageEventRegistry(config.Bundle.GenerationDigest(), metadata...))
	if err != nil {
		t.Fatal(err)
	}
	config.EventFactories, err = GeneratedEventFactoryRegistry(config.Bundle.GenerationDigest(), GeneratedPackageEventFactories(config.Bundle.GenerationDigest(), factories...))
	if err != nil {
		t.Fatal(err)
	}
	config.EventTransport, err = events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	config.ReportEventOperator = func(context.Context, events.OperatorAuditRecord) {}
	return config
}

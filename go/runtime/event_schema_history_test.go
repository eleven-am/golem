package runtime

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	typedvalue "github.com/eleven-am/golem/go/internal/event/typedvalue"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type historyCompatibilityFactory struct {
	model  golem.ModelID
	schema golem.EventSchemaDigest
}

func (factory historyCompatibilityFactory) ModelID() golem.ModelID { return factory.model }
func (factory historyCompatibilityFactory) EventSchemaDigest() golem.EventSchemaDigest {
	return factory.schema
}
func (factory historyCompatibilityFactory) Build(input ValidatedEvent) (any, error) {
	if input.ResolvedEventSchemaDigest() != factory.schema {
		return nil, errHistoryIncompatible
	}
	return input.Metadata(), nil
}

type historyError string

func (failure historyError) Error() string { return string(failure) }

const errHistoryIncompatible historyError = "incompatible history"

func TestHistoricalBundleResolvesV1IntoCompatibleCurrentFactory(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	historicalBundle := golem.GeneratedSchemaBundle(
		golem.SchemaDigest{99}, fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), fixture.Bundle.Contract(), fixture.Bundle.Providers()...,
	)
	history, err := newEventSchemaHistory(fixture.Registry, []golem.SchemaBundle{historicalBundle})
	if err != nil {
		t.Fatal(err)
	}
	historicalRegistry, _, ok := history.ResolveFactSchema(mutationfact.SchemaReference{FormatVersion: mutationfact.FormatVersionV1, Generation: golem.SchemaDigest{99}})
	if !ok {
		t.Fatal("historical V1 generation was not registered")
	}
	title, _ := policyir.StringValue("historical")
	row, err := mutationdecode.NewRow(historicalRegistry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{7})),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{8})),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), title),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	fact, err := mutationfact.New(historicalRegistry, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := fact.OutboxRow(time.Unix(10, 999))
	transport, err := eventcodec.EncodeStoredRow(stored, history, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := model.EventSchema()
	currentDigest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if transport.ResolvedEventSchemaDigest() != golem.EventSchemaDigest(currentDigest) {
		t.Fatal("historical bundle did not prove the current logical event schema")
	}
	validated, err := typedvalue.New(typedvalue.Metadata{
		EventID:             transport.EventID(),
		Action:              transport.Action(),
		CausationID:         transport.CausationID(),
		Ordinal:             transport.TransactionOrdinal(),
		RecordedAt:          transport.RecordedAt(),
		Generation:          transport.GenerationDigest(),
		ResolvedEventSchema: transport.ResolvedEventSchemaDigest(),
		ModelID:             transport.ModelID(),
	}, []any{golem.UUID{7}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := GeneratedEventFactoryRegistry(fixture.Registry.GenerationDigest(), GeneratedPackageEventFactories(
		fixture.Registry.GenerationDigest(), historyCompatibilityFactory{model: fixture.Post, schema: golem.EventSchemaDigest(currentDigest)},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeBuildValidatedEvent(registry, validated); err != nil {
		t.Fatalf("compatible historical V1 did not reach current factory: %v", err)
	}

	incompatible, err := typedvalue.New(typedvalue.Metadata{
		EventID: golem.EventID{3}, Action: golem.EventCreated, CausationID: golem.CausationID{4}, Ordinal: 1,
		RecordedAt: time.Unix(11, 0), Generation: golem.SchemaDigest{99}, ResolvedEventSchema: golem.EventSchemaDigest{5}, ModelID: fixture.Post,
	}, []any{golem.UUID{7}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeBuildValidatedEvent(registry, incompatible); err == nil {
		t.Fatal("incompatible historical event schema reached the typed payload")
	}
}

func TestHistoricalEventBundleRejectsDuplicateGeneration(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	if _, err := newEventSchemaHistory(fixture.Registry, []golem.SchemaBundle{fixture.Bundle}); err == nil {
		t.Fatal("duplicate historical generation was accepted")
	}
}

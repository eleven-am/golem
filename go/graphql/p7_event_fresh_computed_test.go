package graphql

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	typedvalue "github.com/eleven-am/golem/go/internal/event/typedvalue"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

type p7ComputedCaller struct{ stream *p7ComputedEventStream }

func (caller p7ComputedCaller) ExecuteFrozenRead(context.Context, golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	return nil, fmt.Errorf("ordinary read must not execute during event delivery")
}

func (caller p7ComputedCaller) SubscribeFrozenEvents(context.Context, golem.FrozenReadRequest, bool) (EventStream, error) {
	return caller.stream, nil
}

type p7ParityCaller struct {
	stream         *p7ComputedEventStream
	request        chan<- golem.FrozenReadRequest
	entitySelected chan<- bool
}

func (caller p7ParityCaller) ExecuteFrozenRead(context.Context, golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	return nil, fmt.Errorf("ordinary read must not execute during event delivery")
}

func (caller p7ParityCaller) SubscribeFrozenEvents(_ context.Context, request golem.FrozenReadRequest, entitySelected bool) (EventStream, error) {
	if caller.request != nil {
		caller.request <- request
	}
	if caller.entitySelected != nil {
		caller.entitySelected <- entitySelected
	}
	return caller.stream, nil
}

type p7ComputedEventStream struct {
	fixture computedTestFixture
	version *atomic.Int64
	schema  golem.EventSchemaDigest
	next    int64
	closed  bool
}

func (stream *p7ComputedEventStream) Recv(context.Context) (GeneratedEvent, error) {
	if stream.closed || stream.next >= 500 {
		return GeneratedEvent{}, io.EOF
	}
	stream.next++
	stream.version.Store(stream.next)
	row := stream.fixture.rowFromName(stream.fixture.uuid(7), "event-row")
	var eventID golem.EventID
	eventID[14], eventID[15] = byte(stream.next>>8), byte(stream.next)
	causation := golem.CausationID(eventID)
	validated, err := typedvalue.New(typedvalue.Metadata{
		EventID: eventID, Action: golem.EventUpdated, CausationID: causation, Ordinal: 1,
		RecordedAt: time.Unix(stream.next, 0).UTC(), Generation: stream.fixture.bundle.GenerationDigest(),
		EventSchema: stream.schema, HasEventSchema: true, ResolvedEventSchema: stream.schema, ModelID: stream.fixture.model,
	}, []any{stream.fixture.uuid(7)}, &row)
	if err != nil {
		return GeneratedEvent{}, err
	}
	return NewGeneratedEvent(validated.Metadata(), validated.IdentityValues(), &row)
}

func (stream *p7ComputedEventStream) Close() error {
	stream.closed = true
	return nil
}

func TestSecondAndFiveHundredthEventSeeFreshComputedValues(t *testing.T) {
	fixture, eventSchema := p7ComputedSubscriptionFixture(t)
	var version atomic.Int64
	batch := fixture.batchBinding(t, nil, func(_ context.Context, parents []ComputedBatchParent, _ []ComputedArgument) (map[string]ComputedBatchResult, error) {
		result := make(map[string]ComputedBatchResult, len(parents))
		for _, parent := range parents {
			result[parent.CacheKey()] = ComputedBatchResult{Value: fmt.Sprintf("event-%d", version.Load())}
		}
		return result, nil
	})
	source := &p7ComputedEventStream{fixture: fixture, version: &version, schema: eventSchema}
	executorValue, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: fixture.bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			return p7ComputedCaller{stream: source}, nil
		},
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), batch},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, ok := executorValue.(SubscriptionExecutor[int])
	if !ok {
		t.Fatal("generated executor omitted subscription capability")
	}
	document, err := parser.ParseQuery(&ast.Source{Name: "p7-fresh-computed.graphql", Input: `subscription { userEvents { entity { batchGreeting(prefix: "fresh") } } }`})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := executor.Subscribe(context.Background(), 1, Operation{Document: document, Definition: document.Operations[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for index := int64(1); index <= 500; index++ {
		response, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		root := response.Data.(map[string]any)["userEvents"].(map[string]any)
		entity := root["entity"].(map[string]any)
		want := fmt.Sprintf("event-%d", index)
		if got := entity["batchGreeting"]; got != want {
			t.Fatalf("event %d computed=%#v want=%q", index, got, want)
		}
	}
	if version.Load() != 500 {
		t.Fatalf("source version=%d", version.Load())
	}
}

func TestGraphQLAndCallerEventEvaluationPolicySQLAndPayloadOracle(t *testing.T) {
	fixture, eventSchema := p7ComputedSubscriptionFixture(t)
	requestChannel := make(chan golem.FrozenReadRequest, 1)
	selectedChannel := make(chan bool, 1)
	var graphVersion atomic.Int64
	graphSource := &p7ComputedEventStream{fixture: fixture, version: &graphVersion, schema: eventSchema}
	executorValue, err := NewGeneratedExecutor(GeneratedExecutorConfig[int]{
		Bundle: fixture.bundle,
		BeginCaller: func(context.Context, int) (CallerExecution, error) {
			return p7ParityCaller{stream: graphSource, request: requestChannel, entitySelected: selectedChannel}, nil
		},
		ComputedBindings:    []ComputedBinding{fixture.greetingBinding(t), fixture.batchBinding(t, nil, nil)},
		ReportInternalError: func(context.Context, error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := executorValue.(SubscriptionExecutor[int])
	document, err := parser.ParseQuery(&ast.Source{Name: "p7-caller-graphql-parity.graphql", Input: `subscription CallerParity { aliased: userEvents { eventID type id entity { name } } }`})
	if err != nil {
		t.Fatal(err)
	}
	graphStream, err := executor.Subscribe(context.Background(), 7, Operation{Document: document, Definition: document.Operations[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer graphStream.Close()
	frozen := <-requestChannel
	if selected := <-selectedChannel; !selected {
		t.Fatal("GraphQL entity selection was not carried into the caller capability")
	}
	if frozen.ModelID() != fixture.model || frozen.Operation() != golem.ReadFindMany {
		t.Fatalf("GraphQL frozen read model=%x operation=%q", frozen.ModelID(), frozen.Operation())
	}
	selection := frozen.Selection()
	if len(selection) != 1 || selection[0].FieldID() != fixture.name {
		t.Fatalf("GraphQL frozen read selection=%#v", selection)
	}

	var directVersion atomic.Int64
	directSource := &p7ComputedEventStream{fixture: fixture, version: &directVersion, schema: eventSchema}
	directStream, err := (p7ParityCaller{stream: directSource}).SubscribeFrozenEvents(context.Background(), frozen, true)
	if err != nil {
		t.Fatal(err)
	}
	defer directStream.Close()
	direct, err := directStream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response, err := graphStream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := response.Data.(map[string]any)["aliased"].(map[string]any)
	wantEventID := golem.UUID(direct.metadata.EventID()).String()
	if root["eventID"] != wantEventID || root["type"] != "UPDATED" {
		t.Fatalf("GraphQL metadata=%#v direct eventID=%q action=%q", root, wantEventID, direct.metadata.Action())
	}
	wantIdentity := direct.identity[0].(golem.UUID).String()
	if root["id"] != wantIdentity {
		t.Fatalf("GraphQL identity=%#v direct=%q", root["id"], wantIdentity)
	}
	directName, present := golem.RuntimeTransportField(direct.entity, fixture.name).Get()
	if !present {
		t.Fatal("direct caller event omitted the selected name")
	}
	entity := root["entity"].(map[string]any)
	if entity["name"] != directName || directName != "event-row" {
		t.Fatalf("GraphQL entity=%#v direct name=%#v", entity, directName)
	}
}

func p7ComputedSubscriptionFixture(t *testing.T) (computedTestFixture, golem.EventSchemaDigest) {
	t.Helper()
	fixture := computedFixture(t)
	fixture.compilation.Contract.CustomOperations = nil
	var logical compilerir.ModelDeclIR
	for _, model := range fixture.compilation.Model.Models {
		if generatedTestModelID(t, model.ID) == fixture.model {
			logical = model
			break
		}
	}
	if logical.ID == "" {
		t.Fatal("computed model is absent")
	}
	snapshot := make([]compilerir.FieldID, 0, len(logical.Fields))
	for _, field := range logical.Fields {
		if field.Kind != compilerir.FieldRelation && field.Scalar != nil {
			snapshot = append(snapshot, field.ID)
		}
	}
	shape, err := compilerir.BuildEventSchemaShape(logical, fixture.compilation.Model.Enums, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.EventSchemaFingerprint(shape)
	if err != nil {
		t.Fatal(err)
	}
	for index := range fixture.compilation.Contract.Models {
		contract := &fixture.compilation.Contract.Models[index]
		if generatedTestModelID(t, contract.ModelID) != fixture.model {
			continue
		}
		contract.Subscriptions = true
		contract.Roots.Events = "userEvents"
		contract.Event = &compilerir.EventContractIR{
			PayloadTypeName: "UserEvent", MetadataFields: []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"},
			DeleteSnapshotFull: true, Schema: shape, SchemaFingerprint: fingerprint,
		}
		fixture.contract = *contract
	}
	fixture.bundle = bundleForCompilation(t, fixture.compilation)
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("event schema fingerprint=%q err=%v", fingerprint, err)
	}
	var digest golem.EventSchemaDigest
	copy(digest[:], decoded)
	return fixture, digest
}

func (fixture computedTestFixture) rowFromName(id golem.UUID, name string) golem.RuntimeModelRow {
	row, _ := golem.RuntimeModelReadRow(fixture.model,
		golem.RuntimePresentReadCell(fixture.id, id, nil),
		golem.RuntimePresentReadCell(fixture.name, name, nil),
	)
	return row
}

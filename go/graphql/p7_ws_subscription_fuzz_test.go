package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/vektah/gqlparser/v2/ast"
)

const p7WSFuzzInputLimit = 8 << 10

// FuzzP7GraphQLWSMessageAndSubscriptionInput is the P7 evidence target for
// hostile graphql-transport-ws frames and the subscription request/freeze
// boundary. Work is explicitly capped before any parser is entered.
func FuzzP7GraphQLWSMessageAndSubscriptionInput(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		[]byte(`{}`),
		[]byte(`{"type":7}`),
		[]byte(`{"type":"connection_init"}`),
		[]byte(`{"id":"forbidden","type":"connection_init"}`),
		[]byte(`{"type":"ping","payload":{"probe":1}}`),
		[]byte(`{"id":"one","type":"subscribe","payload":{"query":"subscription { ticks }"}}`),
		[]byte(`{"id":"one","type":"subscribe","payload":{"query":"subscription Tick($input: JSON) { ticks(input: $input) }","operationName":"Tick","variables":{"input":{"nested":[1,2,3]}}}}`),
		[]byte(`{"id":"one","type":"subscribe","payload":{"query":"query { viewer }"}}`),
		[]byte(`{"id":"one","type":"complete"}`),
		[]byte(`{"type":"unknown","payload":{}}`),
		[]byte(`{"type":"ping","unknown":true}`),
		[]byte(`{"type":"ping"} {}`),
		[]byte(`{"query":"subscription { ticks }"}`),
		bytes.Repeat([]byte{'['}, 256),
		append([]byte{'{'}, []byte{0xff, '}'}...),
	} {
		f.Add(seed)
	}
	server := newP7WSFuzzServer(f)
	f.Fuzz(func(t *testing.T, source []byte) {
		bounded := append([]byte(nil), source...)
		if len(bounded) > p7WSFuzzInputLimit {
			bounded = bounded[:p7WSFuzzInputLimit]
		}
		entropy := append([]byte(nil), bounded...)

		message, messageErr := decodeWSMessage(bounded)
		requestPayload := json.RawMessage(bounded)
		if messageErr == nil {
			payloadSnapshot := cloneRaw(message.Payload)
			mutateP7FuzzBytes(bounded)
			if !bytes.Equal(message.Payload, payloadSnapshot) {
				t.Fatal("decoded WebSocket message aliases caller frame storage")
			}
			if message.Type == "" {
				t.Fatal("empty WebSocket message type passed production decoder")
			}
			ready := validWSReadyMessageShape(message)
			switch message.Type {
			case "ping", "pong", "connection_init", "subscribe", "complete":
			default:
				if ready {
					t.Fatalf("unknown protocol message %q bypassed ready-state classification", message.Type)
				}
			}
			if validWSInitialMessage(message, 512) && message.Type != "connection_init" {
				t.Fatal("non-initialisation message bypassed initial-state classification")
			}
			if message.Type == "subscribe" {
				requestPayload = cloneRaw(message.Payload)
			}
		}

		request, requestErr := decodeWSRequest(requestPayload, 2<<10)
		if requestErr == nil {
			querySnapshot := request.Query
			variablesSnapshot, err := json.Marshal(request.Variables)
			if err != nil {
				t.Fatal(err)
			}
			mutateP7FuzzBytes(requestPayload)
			variablesAfterPayloadMutation, err := json.Marshal(request.Variables)
			if err != nil {
				t.Fatal(err)
			}
			if request.Query != querySnapshot || !bytes.Equal(variablesSnapshot, variablesAfterPayloadMutation) {
				t.Fatal("decoded subscription request aliases caller payload storage")
			}

			prepared, failure := server.prepareRequest(request, false)
			if validWSSubscriptionRequest(prepared, failure) {
				if prepared.Operation.Definition.Operation != ast.Subscription {
					t.Fatal("non-subscription operation bypassed transport admission")
				}
				preparedSnapshot, err := json.Marshal(prepared.Operation.Variables)
				if err != nil {
					t.Fatal(err)
				}
				mutateP7FuzzValue(request.Variables)
				preparedAfterMutation, err := json.Marshal(prepared.Operation.Variables)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(preparedSnapshot, preparedAfterMutation) {
					t.Fatal("prepared subscription variables alias decoded input")
				}
			}
		}

		assertP7FuzzReadFreezeOwnership(t, entropy)
	})
}

func newP7WSFuzzServer(t testing.TB) *Server[int] {
	t.Helper()
	server, err := NewServer(`scalar JSON
type Query { viewer: Int! }
type Subscription { ticks(input: JSON): Int! }`, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits: Limits{
			MaxRequestBytes: 4 << 10, MaxVariableBytes: 2 << 10,
			MaxTokens: 512, MaxASTNodes: 256, MaxFragments: 8,
			MaxDepth: 8, MaxSelectedFields: 32, MaxAliases: 16,
			MaxInputDepth: 8, MaxInputNodes: 128, MaxListItems: 64,
			MaxComplexity: 256, MaxPageSize: 64, MaxResolverConcurrency: 4,
			MaxComputedBatchSize: 32, MaxGroups: 128,
		},
		ReportInternalError: func(context.Context, error) {},
	}, fuzzExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func assertP7FuzzReadFreezeOwnership(t testing.TB, entropy []byte) {
	t.Helper()
	model := golem.ModelID{0: 1}
	field := golem.FieldID{0: 2}
	for index := 1; index < len(field) && index-1 < len(entropy); index++ {
		model[index] = entropy[index-1]
		field[index] = entropy[len(entropy)-index]
	}
	take := 1
	if len(entropy) != 0 {
		take = int(entropy[0]%32) + 1
	}
	distinct := []golem.FieldID{field}
	selection := []golem.RuntimeReadSelectionInput{{Kind: golem.RuntimeReadScalar, Field: field}}
	input := golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: model, Take: &take,
		Distinct: distinct, Selection: selection, Projection: golem.ProjectionSelect,
	}
	frozen, err := golem.RuntimeFreezeReadRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	distinct[0] = golem.FieldID{}
	selection[0].Field = golem.FieldID{}
	take = -1
	if got, present := frozen.Take(); !present || got < 1 {
		t.Fatalf("frozen take aliases caller integer: value=%d present=%t", got, present)
	}
	if got := frozen.Distinct(); len(got) != 1 || got[0] != field {
		t.Fatalf("frozen distinct aliases caller slice: %x", got)
	}
	if got := frozen.Selection(); len(got) != 1 || got[0].FieldID() != field {
		t.Fatalf("frozen selection aliases caller slice: %#v", got)
	}
	returned := frozen.Distinct()
	returned[0] = golem.FieldID{}
	if got := frozen.Distinct(); len(got) != 1 || got[0] != field {
		t.Fatal("frozen distinct getter exposes mutable internal storage")
	}

	// The same entropy also drives hostile enum/identity combinations through
	// the production freezer. Rejection is acceptable; panic is not.
	var operation golem.ReadOperation
	var kind golem.RuntimeReadSelectionKind
	if len(entropy) != 0 {
		operation = golem.ReadOperation(entropy[0])
		kind = golem.RuntimeReadSelectionKind(entropy[len(entropy)-1])
	}
	_, _ = golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: operation, Model: golem.ModelID{},
		Selection:  []golem.RuntimeReadSelectionInput{{Kind: kind}},
		Projection: golem.ReadProjectionMode(255),
	})
}

func mutateP7FuzzBytes(value []byte) {
	for index := range value {
		value[index] ^= byte(index*31 + 1)
	}
}

func mutateP7FuzzValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			mutateP7FuzzValue(child)
			delete(typed, key)
		}
		typed["__mutated"] = true
	case []any:
		for index := range typed {
			mutateP7FuzzValue(typed[index])
			typed[index] = nil
		}
	case []byte:
		mutateP7FuzzBytes(typed)
	}
}

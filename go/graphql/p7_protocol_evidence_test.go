package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/gorilla/websocket"
)

func TestP7HTTPQueriesMutationsRemainP5Equivalent(t *testing.T) {
	executor := &boundaryExecutor{}
	server, err := NewServer(`type Query { viewer: Int! } type Mutation { setViewer(value: Int!): Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 41, true },
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	requests := []Request{
		{Query: `query Current { viewer }`, OperationName: "Current"},
		{Query: `mutation Change($value: Int!) { setViewer(value: $value) }`, OperationName: "Change", Variables: map[string]any{"value": 9}},
	}
	for _, request := range requests {
		direct := server.Execute(context.Background(), 41, request)
		body, err := json.Marshal(requestEnvelope{Query: request.Query, OperationName: request.OperationName, Variables: mustP7JSON(t, request.Variables)})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		httpRequest.Header.Set("Content-Type", "application/json")
		server.Handler().ServeHTTP(recorder, httpRequest)
		if recorder.Code != http.StatusOK {
			t.Fatalf("HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		directJSON, err := json.Marshal(direct)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace(recorder.Body.Bytes()), directJSON) {
			t.Fatalf("HTTP/direct wire drift for %q: http=%s direct=%s", request.OperationName, recorder.Body.Bytes(), directJSON)
		}
	}
}

func mustP7JSON(t testing.TB, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestP7UnsupportedLegacyProtocolAndSSEAreNotSilentlyAccepted(t *testing.T) {
	server, _ := newProtocolServer(t, events.Limits{})
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	legacy := websocket.Dialer{Subprotocols: []string{"graphql-ws"}}
	connection, response, err := legacy.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err == nil || connection != nil || response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy websocket was silently accepted: conn=%v response=%v err=%v", connection, response, err)
	}

	body, _ := json.Marshal(Request{Query: `subscription { ticks }`})
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotAcceptable || strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(recorder.Body.String(), "SUBSCRIPTION_TRANSPORT_UNSUPPORTED") {
		t.Fatalf("unsupported SSE was treated as a subscription stream: status=%d type=%q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}

func TestP7WebSocketAuthInitAndOperationLimits(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		server, _ := newProtocolServer(t, events.Limits{})
		host := httptest.NewServer(server.Handler())
		defer host.Close()
		dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
		connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"wrong"}`)})
		if code := readCloseCode(t, connection); code != 4403 {
			t.Fatalf("authentication close=%d", code)
		}
	})

	t.Run("initialization bytes", func(t *testing.T) {
		server, _ := newProtocolServer(t, events.Limits{ConnectionInitBytes: 8})
		host := httptest.NewServer(server.Handler())
		defer host.Close()
		dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
		connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
		if code := readCloseCode(t, connection); code != 4400 {
			t.Fatalf("oversized init close=%d", code)
		}
	})

	t.Run("active operations", func(t *testing.T) {
		server, _ := newProtocolServer(t, events.Limits{MaxSubscriptionsPerConnection: 1})
		host := httptest.NewServer(server.Handler())
		defer host.Close()
		dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
		connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
		if ack := readWS(t, connection); ack.Type != "connection_ack" {
			t.Fatalf("ack=%#v", ack)
		}
		payload := json.RawMessage(`{"query":"subscription { ticks }"}`)
		writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: payload})
		writeWS(t, connection, wsMessage{ID: "two", Type: "subscribe", Payload: payload})
		if code := readCloseCode(t, connection); code != 4400 {
			t.Fatalf("operation-limit close=%d", code)
		}
	})
}

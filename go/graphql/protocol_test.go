package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/gorilla/websocket"
)

var protocolFingerprint = golem.SchemaDigest{0xab, 0xcd, 0xef, 4, 5, 6, 7, 8}

type protocolFixture struct {
	server   *Server[int]
	executor *sseExecutor
	stream   *sseTestStream
	host     *httptest.Server
	init     chan json.RawMessage
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{stream: newSSETestStream(), init: make(chan json.RawMessage, 4)}
	fixture.executor = &sseExecutor{stream: fixture.stream}
	server, err := NewServer(`type Query { viewer: Int! } type Mutation { setViewer(value: Int!): Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(sseAuthKey{}).(int)
			return value, ok
		},
		WebSocketInit: func(ctx context.Context, payload json.RawMessage) (context.Context, error) {
			fixture.init <- payload
			return context.WithValue(ctx, sseAuthKey{}, 41), nil
		},
		ContractFingerprint: protocolFingerprint,
		ReportInternalError: func(context.Context, error) {},
	}, fixture.executor)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server = server
	fixture.host = httptest.NewServer(sseAuthenticated(server.Handler()))
	t.Cleanup(fixture.host.Close)
	return fixture
}

func (fixture *protocolFixture) goodBlock() string {
	return `"golem":{"protocol":1,"contract":"` + protocolFingerprint.String() + `"}`
}

func (fixture *protocolFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(fixture.host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func (fixture *protocolFixture) openLive(t *testing.T) *websocket.Conn {
	t.Helper()
	connection := fixture.dial(t)
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{` + fixture.goodBlock() + `}`)})
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("connection ack = %#v", ack)
	}
	return connection
}

func (fixture *protocolFixture) sseBody(query, extensions string) string {
	if extensions == "" {
		return `{"query":"` + query + `"}`
	}
	return `{"query":"` + query + `","extensions":` + extensions + `}`
}

func TestCheckHandshakeRefusalTable(t *testing.T) {
	fixture := newProtocolFixture(t)
	good := protocolFingerprint.String()
	cases := []struct {
		name  string
		block string
		want  handshakeRefusal
	}{
		{"absent", ``, refusedProtocol},
		{"null", `null`, refusedProtocol},
		{"not an object", `1`, refusedProtocol},
		{"array", `[]`, refusedProtocol},
		{"empty object", `{}`, refusedProtocol},
		{"contract only", `{"contract":"` + good + `"}`, refusedProtocol},
		{"protocol only", `{"protocol":1}`, refusedContract},
		{"protocol zero", `{"protocol":0,"contract":"` + good + `"}`, refusedProtocol},
		{"protocol two", `{"protocol":2,"contract":"` + good + `"}`, refusedProtocol},
		{"protocol string", `{"protocol":"1","contract":"` + good + `"}`, refusedProtocol},
		{"protocol fraction", `{"protocol":1.5,"contract":"` + good + `"}`, refusedProtocol},
		{"protocol null", `{"protocol":null,"contract":"` + good + `"}`, refusedProtocol},
		{"unknown field", `{"protocol":1,"contract":"` + good + `","noCredentialExpiry":true}`, refusedProtocol},
		{"trailing value", `{"protocol":1,"contract":"` + good + `"} 1`, refusedProtocol},
		{"wrong protocol and wrong contract", `{"protocol":2,"contract":"` + strings.Repeat("0", 64) + `"}`, refusedProtocol},
		{"other contract", `{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}`, refusedContract},
		{"upper case contract", `{"protocol":1,"contract":"` + strings.ToUpper(good) + `"}`, refusedContract},
		{"contract number", `{"protocol":1,"contract":7}`, refusedProtocol},
		{"contract null", `{"protocol":1,"contract":null}`, refusedContract},
		{"empty contract", `{"protocol":1,"contract":""}`, refusedContract},
		{"match", `{"protocol":1,"contract":"` + good + `"}`, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := fixture.server.checkHandshake(json.RawMessage(testCase.block)); got != testCase.want {
				t.Fatalf("checkHandshake(%s) = %q, want %q", testCase.block, got, testCase.want)
			}
		})
	}
}

func TestWebSocketInitHandshakeRefusals(t *testing.T) {
	fixture := newProtocolFixture(t)
	good := protocolFingerprint.String()
	cases := []struct {
		name    string
		payload string
		reason  string
	}{
		{"no payload", ``, "golem:protocol"},
		{"null payload", `null`, "golem:protocol"},
		{"payload without the block", `{"token":"valid"}`, "golem:protocol"},
		{"array payload", `[]`, "golem:protocol"},
		{"empty block", `{"golem":{}}`, "golem:protocol"},
		{"protocol two", `{"golem":{"protocol":2,"contract":"` + good + `"}}`, "golem:protocol"},
		{"credential expiry claim", `{"golem":{"protocol":1,"contract":"` + good + `","noCredentialExpiry":true}}`, "golem:protocol"},
		{"other contract", `{"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}}`, "golem:contract"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			connection := fixture.dial(t)
			message := wsMessage{Type: "connection_init"}
			if testCase.payload != "" {
				message.Payload = json.RawMessage(testCase.payload)
			}
			writeWS(t, connection, message)
			code, reason := readClose(t, connection)
			if code != 4426 || reason != testCase.reason {
				t.Fatalf("close = %d %q, want 4426 %q", code, reason, testCase.reason)
			}
		})
	}
	select {
	case payload := <-fixture.init:
		t.Fatalf("a refused handshake reached WebSocketInit with %s", payload)
	default:
	}
}

func TestWebSocketInitReceivesThePayloadWithoutTheGolemBlock(t *testing.T) {
	fixture := newProtocolFixture(t)
	connection := fixture.dial(t)
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid",` + fixture.goodBlock() + `}`)})
	if ack := readWS(t, connection); ack.Type != "connection_ack" || len(ack.Payload) != 0 {
		t.Fatalf("connection ack = %#v", ack)
	}
	if payload := <-fixture.init; string(payload) != `{"token":"valid"}` {
		t.Fatalf("WebSocketInit payload = %s", payload)
	}
}

func TestWebSocketSubscriptionAckIsSentOnceAndFirst(t *testing.T) {
	fixture := newProtocolFixture(t)
	connection := fixture.openLive(t)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	readAckWS(t, connection, "one")
	fixture.stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	fixture.stream.values <- Response{Data: map[string]any{"ticks": int32(2)}}
	for _, want := range []string{`"ticks":1`, `"ticks":2`} {
		frame := readWS(t, connection)
		if frame.ID != "one" || frame.Type != "next" || !strings.Contains(string(frame.Payload), want) || string(frame.Payload) == string(liveAckPayload) {
			t.Fatalf("event frame = %#v, want %s", frame, want)
		}
	}
	close(fixture.stream.values)
	if frame := readWS(t, connection); frame.ID != "one" || frame.Type != "complete" {
		t.Fatalf("terminal frame = %#v", frame)
	}
}

func TestWebSocketAckPrecedesAnEventAlreadyWaiting(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection := fixture.openLive(t)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	readAckWS(t, connection, "one")
	if frame := readWS(t, connection); frame.Type != "next" || !strings.Contains(string(frame.Payload), `"ticks":1`) {
		t.Fatalf("event after the ack = %#v", frame)
	}
}

func TestWebSocketRefusedSubscriptionsGetNoAck(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.executor.refusal = events.Failure(events.CodeSubscriptionForbidden)
	connection := fixture.openLive(t)
	writeWS(t, connection, wsMessage{ID: "denied", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	if frame := readWS(t, connection); frame.ID != "denied" || frame.Type != "error" {
		t.Fatalf("refused subscription frame = %#v", frame)
	}
	writeWS(t, connection, wsMessage{ID: "invalid", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { missing }"}`)})
	if frame := readWS(t, connection); frame.ID != "invalid" || frame.Type != "error" {
		t.Fatalf("invalid subscription frame = %#v", frame)
	}
	writeWS(t, connection, wsMessage{Type: "ping"})
	if frame := readWS(t, connection); frame.Type != "pong" {
		t.Fatalf("a refused subscription was followed by %#v", frame)
	}
}

func TestWebSocketSubscribeMessageRefusesExtensions(t *testing.T) {
	fixture := newProtocolFixture(t)
	connection := fixture.openLive(t)
	payload := json.RawMessage(`{"query":"subscription { ticks }","extensions":{"golem":{"protocol":1,"contract":"` + protocolFingerprint.String() + `"}}}`)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: payload})
	frame := readWS(t, connection)
	if frame.ID != "one" || frame.Type != "error" || !strings.Contains(string(frame.Payload), `"code":"BAD_REQUEST"`) {
		t.Fatalf("subscribe with extensions = %#v", frame)
	}
	if fixture.executor.subscribed.Load() != 0 {
		t.Fatal("a refused subscribe message reached the executor")
	}
}

func TestSSEHandshakeRefusals(t *testing.T) {
	fixture := newProtocolFixture(t)
	good := protocolFingerprint.String()
	cases := []struct {
		name       string
		extensions string
		code       string
	}{
		{"no extensions", ``, codeProtocolMismatch},
		{"extensions without the block", `{}`, codeProtocolMismatch},
		{"null block", `{"golem":null}`, codeProtocolMismatch},
		{"empty block", `{"golem":{}}`, codeProtocolMismatch},
		{"protocol two", `{"golem":{"protocol":2,"contract":"` + good + `"}}`, codeProtocolMismatch},
		{"credential expiry claim", `{"golem":{"protocol":1,"contract":"` + good + `","noCredentialExpiry":true}}`, codeProtocolMismatch},
		{"other contract", `{"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}}`, codeContractMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, accept := range []string{"text/event-stream", urqlDefaultAccept} {
				response, body := postGraphQL(t, fixture.host.URL, fixture.sseBody("subscription { ticks }", testCase.extensions), accept)
				wantStatus := http.StatusBadRequest
				if accept == urqlDefaultAccept {
					wantStatus = http.StatusOK
				}
				var decoded Response
				if err := json.Unmarshal([]byte(body), &decoded); err != nil {
					t.Fatalf("refusal is not JSON: %v; body=%q", err, body)
				}
				if response.StatusCode != wantStatus || response.Header.Get("Content-Type") != "application/json; charset=utf-8" || len(decoded.Errors) != 1 || decoded.Errors[0].Extensions["code"] != testCase.code {
					t.Fatalf("accept %q: status=%d type=%q body=%q", accept, response.StatusCode, response.Header.Get("Content-Type"), body)
				}
			}
		})
	}
	if fixture.executor.subscribed.Load() != 0 {
		t.Fatal("a refused handshake reached the executor")
	}
}

func TestSSESubscriptionAckIsSentOnceAndFirst(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection := openSSE(t, context.Background(), fixture.host.URL, fixture.sseBody("subscription { ticks }", `{"golem":{"protocol":1,"contract":"`+protocolFingerprint.String()+`"}}`), true)
	connection.expectAck(t)
	connection.expectNext(t, `{"data":{"ticks":1}}`)
	fixture.stream.values <- Response{Data: map[string]any{"ticks": int32(2)}}
	connection.expectNext(t, `{"data":{"ticks":2}}`)
	close(fixture.stream.values)
	connection.expectComplete(t)
}

func TestSSERefusedSubscriptionsGetNoAck(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.executor.refusal = events.Failure(events.CodeSubscriptionForbidden)
	extensions := `{"golem":{"protocol":1,"contract":"` + protocolFingerprint.String() + `"}}`
	for _, accept := range []string{"text/event-stream", urqlDefaultAccept} {
		_, body := postGraphQL(t, fixture.host.URL, fixture.sseBody("subscription { ticks }", extensions), accept)
		if strings.Contains(body, "live") || strings.Contains(body, "event:") || !strings.Contains(body, string(events.CodeSubscriptionForbidden)) {
			t.Fatalf("accept %q: refused subscription body = %q", accept, body)
		}
	}
}

func TestPlainHTTPGraphQLIsUnchangedWithoutTheGolemBlock(t *testing.T) {
	fixture := newProtocolFixture(t)
	for name, accept := range map[string]string{"json": "", "event stream": "text/event-stream", "acceptable": urqlDefaultAccept} {
		t.Run(name, func(t *testing.T) {
			_, body := postGraphQL(t, fixture.host.URL, fixture.sseBody("query { viewer }", ""), accept)
			if !strings.Contains(body, `"viewer":41`) || strings.Contains(body, "errors") {
				t.Fatalf("query without the block = %q", body)
			}
			_, body = postGraphQL(t, fixture.host.URL, fixture.sseBody("query { viewer }", `{}`), accept)
			if !strings.Contains(body, `"viewer":41`) || strings.Contains(body, "errors") {
				t.Fatalf("query with empty extensions = %q", body)
			}
		})
	}
}

func TestPlainHTTPGraphQLChecksAPresentGolemBlock(t *testing.T) {
	fixture := newProtocolFixture(t)
	good := `{"golem":{"protocol":1,"contract":"` + protocolFingerprint.String() + `"}}`
	other := `{"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}}`
	wrongProtocol := `{"golem":{"protocol":2,"contract":"` + protocolFingerprint.String() + `"}}`
	for name, accept := range map[string]string{"json": "", "event stream": "text/event-stream", "acceptable": urqlDefaultAccept} {
		t.Run(name, func(t *testing.T) {
			_, body := postGraphQL(t, fixture.host.URL, fixture.sseBody("query { viewer }", good), accept)
			if !strings.Contains(body, `"viewer":41`) {
				t.Fatalf("query with a matching block = %q", body)
			}
			for extensions, code := range map[string]string{other: codeContractMismatch, wrongProtocol: codeProtocolMismatch} {
				_, body = postGraphQL(t, fixture.host.URL, fixture.sseBody("query { viewer }", extensions), accept)
				if !strings.Contains(body, `"code":"`+code+`"`) || strings.Contains(body, "viewer") {
					t.Fatalf("query with %s = %q", extensions, body)
				}
			}
		})
	}
	if fixture.executor.executed.Load() != 3 {
		t.Fatalf("executed = %d, want only the three matching queries", fixture.executor.executed.Load())
	}
}

func TestEnvelopeRefusesUnknownExtensionKeys(t *testing.T) {
	fixture := newProtocolFixture(t)
	for name, accept := range map[string]string{"json": "", "event stream": "text/event-stream"} {
		t.Run(name, func(t *testing.T) {
			_, body := postGraphQL(t, fixture.host.URL, fixture.sseBody("query { viewer }", `{"persistedQuery":{}}`), accept)
			if !strings.Contains(body, `"code":"BAD_REQUEST"`) {
				t.Fatalf("unknown extension key = %q", body)
			}
		})
	}
}

func TestResponseExtensionsAreEncodedOnEveryPath(t *testing.T) {
	extensions := map[string]any{"golem": map[string]any{"commit": "unknown"}}
	cases := map[string]Response{
		"plain":      {Data: map[string]any{"a": 1}, Extensions: extensions},
		"executable": {Data: executableData(`{"a":1}`), Extensions: extensions},
		"errors":     {Data: executableData(`{"a":1}`), Errors: []Error{publicError("X", "m")}, Extensions: extensions},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			payload, err := encodeResponse(response)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Extensions map[string]map[string]string `json:"extensions"`
			}
			if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Extensions["golem"]["commit"] != "unknown" {
				t.Fatalf("payload = %s err=%v", payload, err)
			}
		})
	}
	payload, err := encodeResponse(Response{Data: executableData(`{"a":1}`)})
	if err != nil || strings.Contains(string(payload), "extensions") {
		t.Fatalf("a response without extensions encoded %s err=%v", payload, err)
	}
}

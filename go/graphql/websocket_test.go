package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/gorilla/websocket"
)

type protocolExecutor struct {
	stream ResponseStream
}

func (executor *protocolExecutor) Execute(context.Context, int, Operation) Response {
	return Response{Data: map[string]any{"viewer": int32(1)}}
}

func (executor *protocolExecutor) Subscribe(_ context.Context, principal int, operation Operation) (ResponseStream, error) {
	if principal != 41 || operation.Definition == nil {
		return nil, errors.New("unexpected subscription admission")
	}
	return executor.stream, nil
}

type protocolStream struct {
	values chan Response
	once   sync.Once
}

func (stream *protocolStream) Recv(ctx context.Context) (Response, error) {
	select {
	case value, open := <-stream.values:
		if !open {
			return Response{}, io.EOF
		}
		return value, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func (stream *protocolStream) Close() error { return nil }

type wsAuthKey struct{}

func newProtocolServer(t *testing.T, limits events.Limits) (*Server[int], *protocolStream) {
	t.Helper()
	stream := &protocolStream{values: make(chan Response, 2)}
	server, err := NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(wsAuthKey{}).(int)
			return value, ok
		},
		WebSocketInit: func(ctx context.Context, payload json.RawMessage) (context.Context, error) {
			if string(payload) != `{"token":"valid"}` {
				return nil, errors.New("invalid token")
			}
			// Deliberately discard the supplied parent: the server must still bind
			// every operation to its own connection/lifecycle cancellation.
			return context.WithValue(context.Background(), wsAuthKey{}, 41), nil
		},
		EventLimits: limits, ReportInternalError: func(context.Context, error) {},
	}, &protocolExecutor{stream: stream})
	if err != nil {
		t.Fatal(err)
	}
	return server, stream
}

func TestGraphQLTransportWSProtocolCorpus(t *testing.T) {
	server, stream := newProtocolServer(t, events.Limits{})
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
	if message := readWS(t, connection); message.Type != "connection_ack" {
		t.Fatalf("ack = %#v", message)
	}
	writeWS(t, connection, wsMessage{Type: "ping", Payload: json.RawMessage(`{"probe":1}`)})
	if message := readWS(t, connection); message.Type != "pong" || string(message.Payload) != `{"probe":1}` {
		t.Fatalf("pong = %#v", message)
	}
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	stream.values <- Response{Data: map[string]any{"ticks": int32(7)}}
	next := readWS(t, connection)
	if next.ID != "one" || next.Type != "next" || !strings.Contains(string(next.Payload), `"ticks":7`) {
		t.Fatalf("next = %#v", next)
	}
	close(stream.values)
	if complete := readWS(t, connection); complete.ID != "one" || complete.Type != "complete" {
		t.Fatalf("complete = %#v", complete)
	}
}

func TestWebSocketRejectsLegacyDuplicateMalformedAndTimesOutInit(t *testing.T) {
	server, _ := newProtocolServer(t, events.Limits{ConnectionInitTimeout: 25 * time.Millisecond})
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	url := "ws" + strings.TrimPrefix(host.URL, "http")
	legacy := websocket.Dialer{Subprotocols: []string{"graphql-ws"}}
	if connection, response, err := legacy.Dial(url, nil); err == nil || connection != nil || response == nil || response.StatusCode != 400 {
		t.Fatalf("legacy result = conn %v response %#v err %v", connection, response, err)
	}
	valid := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	timed, _, err := valid.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := readCloseCode(t, timed); code != 4408 {
		t.Fatalf("init timeout close = %d", code)
	}
	malformed, _, _ := valid.Dial(url, nil)
	if err := malformed.WriteMessage(websocket.TextMessage, []byte(`{"type":7}`)); err != nil {
		t.Fatal(err)
	}
	if code := readCloseCode(t, malformed); code != 4400 {
		t.Fatalf("malformed close = %d", code)
	}
	duplicate, _, _ := valid.Dial(url, nil)
	writeWS(t, duplicate, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
	_ = readWS(t, duplicate)
	payload := json.RawMessage(`{"query":"subscription { ticks }"}`)
	writeWS(t, duplicate, wsMessage{ID: "same", Type: "subscribe", Payload: payload})
	writeWS(t, duplicate, wsMessage{ID: "same", Type: "subscribe", Payload: payload})
	if code := readCloseCode(t, duplicate); code != 4409 {
		t.Fatalf("duplicate close = %d", code)
	}
}

func TestHTTPRefusesSubscriptionsAndShutdownClosesActiveWebSockets(t *testing.T) {
	server, _ := newProtocolServer(t, events.Limits{})
	response := server.Execute(context.Background(), 41, Request{Query: `subscription { ticks }`})
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "SUBSCRIPTION_TRANSPORT_REQUIRED" {
		t.Fatalf("HTTP subscription response = %#v", response)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
	_ = readWS(t, connection)
	writeWS(t, connection, wsMessage{ID: "active", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	writeWS(t, connection, wsMessage{Type: "ping"})
	if pong := readWS(t, connection); pong.Type != "pong" {
		t.Fatalf("active-subscription barrier = %#v", pong)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if code := readCloseCode(t, connection); code != websocket.CloseGoingAway && code != websocket.CloseAbnormalClosure {
		t.Fatalf("shutdown close = %d", code)
	}
}

type panicResponseStream struct{ closes atomic.Int32 }

func (*panicResponseStream) Recv(context.Context) (Response, error) { panic("private panic payload") }
func (stream *panicResponseStream) Close() error {
	stream.closes.Add(1)
	return nil
}

func TestWebSocketOperationPanicIsSanitizedAndConnectionSurvives(t *testing.T) {
	panicking := &panicResponseStream{}
	server, err := NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 41, true },
		ReportInternalError:  func(context.Context, error) {},
	}, &protocolExecutor{stream: panicking})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	writeWS(t, connection, wsMessage{Type: "connection_init"})
	_ = readWS(t, connection)
	writeWS(t, connection, wsMessage{ID: "panic", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	failure := readWS(t, connection)
	if failure.Type != "error" || strings.Contains(string(failure.Payload), "private") || !strings.Contains(string(failure.Payload), "INTERNAL_SERVER_ERROR") {
		t.Fatalf("panic failure = %#v", failure)
	}
	writeWS(t, connection, wsMessage{Type: "ping"})
	if pong := readWS(t, connection); pong.Type != "pong" {
		t.Fatalf("post-panic pong = %#v", pong)
	}
	deadline := time.Now().Add(time.Second)
	for panicking.closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if panicking.closes.Load() != 1 {
		t.Fatalf("panic stream closes = %d", panicking.closes.Load())
	}
}

func writeWS(t *testing.T, connection *websocket.Conn, message wsMessage) {
	t.Helper()
	if err := connection.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func readWS(t *testing.T, connection *websocket.Conn) wsMessage {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message wsMessage
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func readCloseCode(t *testing.T, connection *websocket.Conn) int {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := connection.ReadMessage(); err != nil {
			var closeError *websocket.CloseError
			if errors.As(err, &closeError) {
				return closeError.Code
			}
			t.Fatal(err)
		}
	}
}

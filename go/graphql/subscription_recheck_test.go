package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/gorilla/websocket"
)

type recheckExecutor struct {
	mu      sync.Mutex
	streams map[string]*sseTestStream
}

func (executor *recheckExecutor) Execute(context.Context, int, Operation) Response {
	return Response{Data: map[string]any{"viewer": int32(1)}}
}

func (executor *recheckExecutor) Subscribe(_ context.Context, _ int, operation Operation) (ResponseStream, error) {
	room := fmt.Sprint(operation.Variables["room"])
	stream := newSSETestStream()
	executor.mu.Lock()
	executor.streams[room] = stream
	executor.mu.Unlock()
	return stream, nil
}

func (executor *recheckExecutor) stream(t *testing.T, room string) *sseTestStream {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		stream := executor.streams[room]
		executor.mu.Unlock()
		if stream != nil {
			return stream
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("room %s was never subscribed", room)
	return nil
}

type recheckGrants struct {
	mu      sync.Mutex
	revoked map[string]error
}

func (grants *recheckGrants) revoke(room string, failure error) {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	grants.revoked[room] = failure
}

func (grants *recheckGrants) check(_ context.Context, principal int, operation Operation) error {
	if principal != 41 {
		return errors.New("unexpected principal")
	}
	grants.mu.Lock()
	defer grants.mu.Unlock()
	return grants.revoked[fmt.Sprint(operation.Variables["room"])]
}

const recheckSchema = `type Query { viewer: Int! } type Subscription { ticks(room: Int!): Int! }`

func newRecheckServer(t *testing.T) (*Server[int], *recheckExecutor, *recheckGrants) {
	t.Helper()
	grants := &recheckGrants{revoked: map[string]error{}}
	server, executor := newRecheckServerWith(t, grants.check)
	return server, executor, grants
}

func newRecheckServerWith(t *testing.T, check func(context.Context, int, Operation) error) (*Server[int], *recheckExecutor) {
	t.Helper()
	executor := &recheckExecutor{streams: map[string]*sseTestStream{}}
	server, err := NewServer(recheckSchema, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			if value, ok := ctx.Value(sseAuthKey{}).(int); ok {
				return value, true
			}
			value, ok := ctx.Value(wsAuthKey{}).(int)
			return value, ok
		},
		WebSocketInit: func(ctx context.Context, _ json.RawMessage) (context.Context, error) {
			return context.WithValue(ctx, wsAuthKey{}, 41), nil
		},
		ReportInternalError: func(context.Context, error) {},
		SubscriptionRecheck: SubscriptionRecheck[int]{Interval: events.MinimumRecheckInterval, Check: check},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return server, executor
}

func recheckHost(t *testing.T, server *Server[int]) *httptest.Server {
	t.Helper()
	host := httptest.NewServer(server.Handler())
	t.Cleanup(host.Close)
	return host
}

func dialRecheck(t *testing.T, server *Server[int]) *websocket.Conn {
	t.Helper()
	return dialRecheckHost(t, recheckHost(t, server))
}

func dialRecheckHost(t *testing.T, host *httptest.Server) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	writeWS(t, connection, wsMessage{Type: "connection_init"})
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("ack=%#v", ack)
	}
	return connection
}

func readWSWithin(t *testing.T, connection *websocket.Conn, within time.Duration) wsMessage {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(within))
	var message wsMessage
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func roomSubscription(room string) json.RawMessage {
	return json.RawMessage(`{"query":"subscription Room($room: Int!) { ticks(room: $room) }","variables":{"room":` + room + `}}`)
}

func operationErrorCode(t *testing.T, message wsMessage) string {
	t.Helper()
	var failures []Error
	if err := json.Unmarshal(message.Payload, &failures); err != nil || len(failures) != 1 {
		t.Fatalf("operation error payload=%s err=%v", message.Payload, err)
	}
	code, _ := failures[0].Extensions["code"].(string)
	return code
}

func TestWebSocketSubscriptionRecheckEndsOnlyTheRevokedOperation(t *testing.T) {
	server, executor, grants := newRecheckServer(t)
	connection := dialRecheck(t, server)
	writeWS(t, connection, wsMessage{ID: "revoked", Type: "subscribe", Payload: roomSubscription("1")})
	writeWS(t, connection, wsMessage{ID: "kept", Type: "subscribe", Payload: roomSubscription("2")})
	revokedStream, keptStream := executor.stream(t, "1"), executor.stream(t, "2")
	grants.revoke("1", errors.New("share revoked"))
	message := readWSWithin(t, connection, 5*time.Second)
	if message.ID != "revoked" || message.Type != "error" || operationErrorCode(t, message) != string(events.CodeSubscriptionForbidden) {
		t.Fatalf("revoked operation message=%#v payload=%s", message, message.Payload)
	}
	select {
	case <-revokedStream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the revoked operation's source stream stayed open")
	}
	keptStream.values <- Response{Data: map[string]any{"ticks": int32(9)}}
	next := readWS(t, connection)
	if next.ID != "kept" || next.Type != "next" || !strings.Contains(string(next.Payload), `"ticks":9`) {
		t.Fatalf("surviving operation message=%#v payload=%s", next, next.Payload)
	}
	writeWS(t, connection, wsMessage{Type: "ping"})
	if pong := readWS(t, connection); pong.Type != "pong" {
		t.Fatalf("connection did not survive the revoked operation: %#v", pong)
	}
}

func TestWebSocketSubscriptionRecheckKeepsAWhitelistedCode(t *testing.T) {
	server, executor, grants := newRecheckServer(t)
	connection := dialRecheck(t, server)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: roomSubscription("1")})
	executor.stream(t, "1")
	grants.revoke("1", events.Failure(events.CodeSubscriptionRevalidation))
	message := readWSWithin(t, connection, 5*time.Second)
	if message.ID != "one" || message.Type != "error" || operationErrorCode(t, message) != string(events.CodeSubscriptionRevalidation) {
		t.Fatalf("message=%#v payload=%s", message, message.Payload)
	}
}

func TestSSESubscriptionRecheckSendsAFinalErrorThenCompletes(t *testing.T) {
	server, executor, grants := newRecheckServer(t)
	host := httptest.NewServer(sseAuthenticated(server.Handler()))
	t.Cleanup(host.Close)
	connection := openSSE(t, context.Background(), host.URL, string(roomSubscription("3")), true)
	if connection.response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", connection.response.StatusCode)
	}
	stream := executor.stream(t, "3")
	stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection.expectNext(t, `{"data":{"ticks":1}}`)
	grants.revoke("3", errors.New("share revoked"))
	frame := connection.event(t)
	var decoded Response
	if err := json.Unmarshal([]byte(frame.data), &decoded); err != nil {
		t.Fatal(err)
	}
	if frame.event != "next" || decoded.Data != nil || len(decoded.Errors) != 1 || decoded.Errors[0].Extensions["code"] != string(events.CodeSubscriptionForbidden) {
		t.Fatalf("terminal frame=%#v", frame)
	}
	connection.expectComplete(t)
	<-stream.closed
}

func TestSubscriptionRecheckGoroutineEndsWithTheOperation(t *testing.T) {
	server, executor, _ := newRecheckServer(t)
	host := recheckHost(t, server)
	baseline := runtime.NumGoroutine()
	connection := dialRecheckHost(t, host)
	for _, room := range []string{"1", "2", "3"} {
		writeWS(t, connection, wsMessage{ID: "op" + room, Type: "subscribe", Payload: roomSubscription(room)})
		executor.stream(t, room)
	}
	for _, room := range []string{"1", "2", "3"} {
		writeWS(t, connection, wsMessage{ID: "op" + room, Type: "complete"})
		<-executor.stream(t, room).closed
	}
	_ = connection.Close()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count := runtime.NumGoroutine(); count > baseline {
		t.Fatalf("goroutines=%d after every operation ended; baseline=%d", count, baseline)
	}
}

func TestSubscriptionRecheckConfigIsComplete(t *testing.T) {
	for name, recheck := range map[string]SubscriptionRecheck[int]{
		"interval without check": {Interval: time.Second},
		"check without interval": {Check: func(context.Context, int, Operation) error { return nil }},
		"negative interval":      {Interval: -time.Second, Check: func(context.Context, int, Operation) error { return nil }},
		"nanosecond interval":    {Interval: time.Nanosecond, Check: func(context.Context, int, Operation) error { return nil }},
		"below the minimum":      {Interval: events.MinimumRecheckInterval - time.Millisecond, Check: func(context.Context, int, Operation) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewServer(recheckSchema, Config[int]{
				PrincipalFromContext: func(context.Context) (int, bool) { return 0, false },
				ReportInternalError:  func(context.Context, error) {},
				SubscriptionRecheck:  recheck,
			}, &recheckExecutor{streams: map[string]*sseTestStream{}})
			if err == nil {
				t.Fatal("an incomplete SubscriptionRecheck was accepted")
			}
		})
	}
}

func TestSSESubscriptionLimitIsTooManyRequests(t *testing.T) {
	_, executor, host := newSSEServer(t, events.Limits{}, newSSETestStream())
	executor.refusal = events.Failure(events.CodeSubscriptionLimit)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	body, err := io.ReadAll(connection.response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if connection.response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), `"code":"GOLEM_SUBSCRIPTION_LIMIT"`) {
		t.Fatalf("status=%d body=%s", connection.response.StatusCode, body)
	}
}

func TestSubscriptionRecheckPanicIsAnInternalFault(t *testing.T) {
	server, executor := newRecheckServerWith(t, func(context.Context, int, Operation) error { panic("check exploded") })
	connection := dialRecheck(t, server)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: roomSubscription("1")})
	executor.stream(t, "1")
	message := readWSWithin(t, connection, 5*time.Second)
	if message.ID != "one" || message.Type != "error" || operationErrorCode(t, message) != string(events.CodeSubscriptionSourceClosed) {
		t.Fatalf("message=%#v payload=%s", message, message.Payload)
	}
}

func TestShutdownWaitsForARunningSubscriptionRecheck(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	server, executor := newRecheckServerWith(t, func(ctx context.Context, _ int, _ Operation) error {
		first := false
		once.Do(func() { first = true })
		if !first {
			return nil
		}
		close(started)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)
		close(finished)
		return ctx.Err()
	})
	connection := dialRecheck(t, server)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: roomSubscription("1")})
	executor.stream(t, "1")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the recheck never ran")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Shutdown returned while a subscription recheck was still running")
	}
}

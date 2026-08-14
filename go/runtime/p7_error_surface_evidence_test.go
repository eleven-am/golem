package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	"github.com/gorilla/websocket"
)

type p7SecretPublisher struct{ secret string }

func (publisher p7SecretPublisher) Run(context.Context) error { return errors.New(publisher.secret) }

type p7SecretSubscriptionExecutor struct{ secret string }

func (p7SecretSubscriptionExecutor) Execute(context.Context, int, publicgraphql.Operation) publicgraphql.Response {
	return publicgraphql.Response{}
}

func (executor p7SecretSubscriptionExecutor) Subscribe(context.Context, int, publicgraphql.Operation) (publicgraphql.ResponseStream, error) {
	return nil, errors.New(executor.secret)
}

func TestPublisherSubscriptionAndWebSocketErrorsAreSanitized(t *testing.T) {
	const secret = "postgres://alice:private-password@internal-db/customer"

	publisherApp := &App[struct{}, struct{}]{
		eventPublisher: p7SecretPublisher{secret: secret},
		eventLimits:    events.DefaultLimits(),
	}
	publisherErr := publisherApp.RunEventPublisher(context.Background())
	assertP7StableSanitizedError(t, secret, publisherErr, events.CodeEventSourceClosed)

	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("transport subscription did not start")
	}
	fixture.app.resolvePrincipal = func(context.Context, p7EventPrincipal) (p7EventActor, error) {
		return p7EventActor{}, errors.New(secret)
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1930, "visible", false))
	receiveContext, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, subscriptionErr := stream.Recv(receiveContext)
	assertP7StableSanitizedError(t, secret, subscriptionErr, events.CodeSubscriptionRevalidation)

	server, err := publicgraphql.NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, publicgraphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  func(context.Context, error) {},
	}, p7SecretSubscriptionExecutor{secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	p7ReadSanitizedFrame(t, connection)
	if err := connection.WriteJSON(map[string]any{
		"id": "one", "type": "subscribe", "payload": map[string]any{"query": `subscription { ticks }`},
	}); err != nil {
		t.Fatal(err)
	}
	failure := p7ReadSanitizedFrame(t, connection)
	if failure.Type != "error" || failure.ID != "one" {
		t.Fatalf("websocket failure frame=%#v", failure)
	}
	if strings.Contains(string(failure.Payload), secret) || !strings.Contains(string(failure.Payload), `"code":"INTERNAL_SERVER_ERROR"`) {
		t.Fatalf("websocket failure was not closed and sanitized: %s", failure.Payload)
	}
}

func assertP7StableSanitizedError(t testing.TB, secret string, err error, expected events.ErrorCode) {
	t.Helper()
	code, ok := events.CodeOf(err)
	if !ok || code != expected {
		t.Fatalf("public error code=%q ok=%t error=%v", code, ok, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("public error leaked private cause: %v", err)
	}
}

type p7SanitizedFrame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func p7ReadSanitizedFrame(t testing.TB, connection *websocket.Conn) p7SanitizedFrame {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var frame p7SanitizedFrame
	if err := connection.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

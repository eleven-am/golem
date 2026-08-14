package graphql_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/graphql"
	p7gqlgen "github.com/eleven-am/golem/go/graphql/testdata/p7subscription/golemgqlgen"
	"github.com/gorilla/websocket"
)

type activeSubscriptionExecutor struct{}

func (activeSubscriptionExecutor) Execute(context.Context, int, graphql.Operation) graphql.Response {
	return graphql.Response{}
}
func (activeSubscriptionExecutor) Subscribe(context.Context, int, graphql.Operation) (graphql.ResponseStream, error) {
	return &activeSubscriptionStream{}, nil
}

type activeSubscriptionStream struct{ sent bool }

func (stream *activeSubscriptionStream) Recv(context.Context) (graphql.Response, error) {
	if stream.sent {
		return graphql.Response{}, io.EOF
	}
	stream.sent = true
	return graphql.Response{Data: map[string]any{"payload": graphql.PreparedObject{
		"kind": "UPDATED", "eventID": "unprojected-event-id",
		"entity": graphql.PreparedObject{"renamed": int32(9)},
	}}}, nil
}
func (*activeSubscriptionStream) Close() error { return nil }

func TestGQLGenExecutableProjectsSubscriptionAliasesDirectivesEnumsAndFragments(t *testing.T) {
	next := subscribeActiveFrame(t, p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}))
	var payload struct {
		Data map[string]map[string]any `json:"data"`
	}
	if err := json.Unmarshal(next.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	entity, _ := payload.Data["payload"]["entity"].(map[string]any)
	if next.Type != "next" || payload.Data["payload"]["kind"] != "UPDATED" || entity["renamed"] != float64(9) {
		t.Fatalf("active gqlgen frame = %s", next.Payload)
	}
	if strings.Contains(string(next.Payload), "unprojected-event-id") {
		t.Fatalf("skipped selection survived gqlgen projection: %s", next.Payload)
	}
}

func TestSubscriptionFramesAreEncodedOnlyByTheGQLGenExecutable(t *testing.T) {
	next := subscribeActiveFrame(t, nil)
	if next.Type != "next" {
		t.Fatalf("frame type = %q", next.Type)
	}
	if !strings.Contains(string(next.Payload), "unprojected-event-id") {
		t.Fatalf("a second event encoder projected the frame without a gqlgen executable: %s", next.Payload)
	}
}

func subscribeActiveFrame(t *testing.T, executable gqlgengraphql.ExecutableSchema) activeFrame {
	t.Helper()
	server, err := graphql.NewServer(p7gqlgenSchema, graphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  func(context.Context, error) {},
		ExecutableSchema:     executable,
	}, activeSubscriptionExecutor{})
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
	readActiveFrame(t, connection)
	query := `subscription Event($show: Boolean!) { payload: ticks { eventID @skip(if: true) kind: type entity @include(if: $show) { ...ThingPayload } } } fragment ThingPayload on Thing { renamed: value }`
	if err := connection.WriteJSON(map[string]any{"id": "one", "type": "subscribe", "payload": map[string]any{"query": query, "variables": map[string]any{"show": true}}}); err != nil {
		t.Fatal(err)
	}
	next := readActiveFrame(t, connection)
	if complete := readActiveFrame(t, connection); complete.Type != "complete" {
		t.Fatalf("complete frame = %#v", complete)
	}
	return next
}

type activeFrame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func readActiveFrame(t *testing.T, connection *websocket.Conn) activeFrame {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var frame activeFrame
	if err := connection.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

const p7gqlgenSchema = `scalar UUID
scalar DateTime
enum GolemEventType { CREATED UPDATED DELETED }
type Thing { value: Int! }
type TickEvent { eventID: UUID! type: GolemEventType! entity: Thing }
type Query { viewer: Int! }
type Subscription { ticks: TickEvent! }
`

package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/graphql"
	p7gqlgen "github.com/eleven-am/golem/go/graphql/testdata/p7subscription/golemgqlgen"
	"github.com/gorilla/websocket"
)

type inflightEventExecutor struct {
	cancelOperation func()
	closed          chan struct{}
}

type completedEventExecutor struct{ closed, entered chan struct{} }

func (completedEventExecutor) ListBounds() graphql.ListBounds {
	return graphql.NewListBounds(nil)
}

func (completedEventExecutor) Execute(context.Context, int, graphql.Operation) graphql.Response {
	return graphql.Response{}
}

func (executor completedEventExecutor) Subscribe(context.Context, int, graphql.Operation) (graphql.ResponseStream, error) {
	return &inflightEventStream{cancelOperation: func() {}, waitForCancel: true, closed: executor.closed, entered: executor.entered}, nil
}

func (inflightEventExecutor) ListBounds() graphql.ListBounds {
	return graphql.NewListBounds(nil)
}

func (inflightEventExecutor) Execute(context.Context, int, graphql.Operation) graphql.Response {
	return graphql.Response{}
}

func (executor inflightEventExecutor) Subscribe(context.Context, int, graphql.Operation) (graphql.ResponseStream, error) {
	return &inflightEventStream{cancelOperation: executor.cancelOperation, closed: executor.closed}, nil
}

type inflightEventStream struct {
	cancelOperation func()
	closed          chan struct{}
	waitForCancel   bool
	entered         chan struct{}
	calls           int
	closeOnce       sync.Once
}

func (stream *inflightEventStream) Recv(ctx context.Context) (graphql.Response, error) {
	stream.calls++
	if stream.calls > 1 {
		return graphql.Response{}, io.EOF
	}
	stream.cancelOperation()
	if stream.waitForCancel {
		stream.entered <- struct{}{}
		<-ctx.Done()
	}
	return graphql.Response{Data: map[string]any{"payload": graphql.PreparedObject{
		"kind": "UPDATED", "eventID": "e", "entity": graphql.PreparedObject{"renamed": int32(9)},
	}}}, nil
}

func (stream *inflightEventStream) Close() error {
	stream.closeOnce.Do(func() { stream.closed <- struct{}{} })
	return nil
}

type reportedErrors struct {
	mu       sync.Mutex
	messages []string
	count    atomic.Int64
}

func (reported *reportedErrors) report(_ context.Context, err error) {
	reported.count.Add(1)
	reported.mu.Lock()
	reported.messages = append(reported.messages, err.Error())
	reported.mu.Unlock()
}

func (reported *reportedErrors) snapshot() []string {
	reported.mu.Lock()
	defer reported.mu.Unlock()
	return append([]string(nil), reported.messages...)
}

const inflightEventQuery = `subscription { payload: ticks { kind: type } }`

func TestWebSocketEventInFlightWhenConnectionIsCancelledWritesNoFrameAndReportsNothing(t *testing.T) {
	reported := &reportedErrors{}
	var cancelConnection atomic.Pointer[context.CancelFunc]
	executor := inflightEventExecutor{closed: make(chan struct{}, 1)}
	executor.cancelOperation = func() { (*cancelConnection.Load())() }
	server, err := graphql.NewServer(p7gqlgenSchema, graphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  reported.report,
		ExecutableSchema:     p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}),
		WebSocketInit: func(ctx context.Context, _ json.RawMessage) (context.Context, error) {
			derived, cancel := context.WithCancel(ctx)
			cancelConnection.Store(&cancel)
			return derived, nil
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	connection, _, err := (&websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}).Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init", "payload": liveInit("")}); err != nil {
		t.Fatal(err)
	}
	readActiveFrame(t, connection)
	if err := connection.WriteJSON(map[string]any{"id": "one", "type": "subscribe", "payload": map[string]any{"query": inflightEventQuery}}); err != nil {
		t.Fatal(err)
	}
	readLiveAck(t, connection, "one")
	select {
	case <-executor.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("operation never finished after its connection was cancelled")
	}
	if err := connection.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	for {
		frame := readActiveFrame(t, connection)
		if frame.Type == "pong" {
			break
		}
		t.Fatalf("a cancelled operation wrote %q frame %s", frame.Type, frame.Payload)
	}
	if messages := reported.snapshot(); len(messages) != 0 {
		t.Fatalf("cancellation was reported as internal errors: %v", messages)
	}
}

func TestSSEEventInFlightWhenRequestIsCancelledWritesNoFrameAndReportsNothing(t *testing.T) {
	const repetitions = 64
	reported := &reportedErrors{}
	var cancelRequest atomic.Pointer[context.CancelFunc]
	executor := inflightEventExecutor{closed: make(chan struct{}, repetitions)}
	executor.cancelOperation = func() { (*cancelRequest.Load())() }
	server, err := graphql.NewServer(p7gqlgenSchema, graphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  reported.report,
		ExecutableSchema:     p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}),
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	body := liveBody(inflightEventQuery)
	for repetition := 0; repetition < repetitions; repetition++ {
		requestContext, cancel := context.WithCancel(context.Background())
		cancelRequest.Store(&cancel)
		request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body)).WithContext(requestContext)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		cancel()
		if recorder.Body.String() != "event: next\ndata: "+liveAckWire+"\n\n" {
			t.Fatalf("repetition %d: a cancelled stream wrote more than its ack: %q", repetition, recorder.Body.String())
		}
	}
	if messages := reported.snapshot(); len(messages) != 0 {
		t.Fatalf("cancellation was reported as internal errors %d times: %v", len(messages), messages)
	}
}

func TestWebSocketEventInFlightWhenClientCompletesReportsNothing(t *testing.T) {
	const repetitions = 64
	reported := &reportedErrors{}
	executor := completedEventExecutor{closed: make(chan struct{}, repetitions), entered: make(chan struct{}, repetitions)}
	server, err := graphql.NewServer(p7gqlgenSchema, graphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  reported.report,
		ExecutableSchema:     p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}),
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	connection, _, err := (&websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}).Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init", "payload": liveInit("")}); err != nil {
		t.Fatal(err)
	}
	readActiveFrame(t, connection)
	for repetition := 0; repetition < repetitions; repetition++ {
		id := strconv.Itoa(repetition)
		if err := connection.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": map[string]any{"query": inflightEventQuery}}); err != nil {
			t.Fatal(err)
		}
		readLiveAck(t, connection, id)
		select {
		case <-executor.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("repetition %d: operation never started receiving", repetition)
		}
		if err := connection.WriteJSON(map[string]any{"id": id, "type": "complete"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-executor.closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("repetition %d: operation never finished after the client completed it", repetition)
		}
	}
	if messages := reported.snapshot(); len(messages) != 0 {
		t.Fatalf("client completion was reported as internal errors %d/%d times: %v", len(messages), repetitions, messages)
	}
}

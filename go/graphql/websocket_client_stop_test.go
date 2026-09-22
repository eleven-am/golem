package graphql

import (
	"bytes"
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
	"github.com/eleven-am/golem/go/observe"
	"github.com/gorilla/websocket"
)

type clientStopStream struct {
	values  chan Response
	failure chan error
	closed  chan struct{}
	once    sync.Once
}

func newClientStopStream() *clientStopStream {
	return &clientStopStream{values: make(chan Response, 1), failure: make(chan error, 1), closed: make(chan struct{})}
}

func (stream *clientStopStream) Recv(ctx context.Context) (Response, error) {
	select {
	case value, open := <-stream.values:
		if !open {
			return Response{}, io.EOF
		}
		return value, nil
	case failure := <-stream.failure:
		return Response{}, failure
	case <-ctx.Done():
		return Response{}, events.Failure(events.CodeSubscriptionCancelled)
	}
}

func (stream *clientStopStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}

type clientStopExecutor struct{ created chan *clientStopStream }

func (executor *clientStopExecutor) Execute(context.Context, int, Operation) Response {
	return Response{Data: map[string]any{"viewer": int32(1)}}
}

func (executor *clientStopExecutor) Subscribe(_ context.Context, principal int, operation Operation) (ResponseStream, error) {
	if principal != 41 || operation.Definition == nil {
		return nil, errors.New("unexpected subscription admission")
	}
	stream := newClientStopStream()
	executor.created <- stream
	return stream, nil
}

type clientStopObserver struct{ finished chan observe.Observation }

func (observer *clientStopObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Operation() != observe.OperationGraphQLSubscription {
		return
	}
	select {
	case observer.finished <- value:
	default:
	}
}

func newClientStopServer(t *testing.T) (*websocket.Conn, *clientStopExecutor, *clientStopObserver) {
	t.Helper()
	executor := &clientStopExecutor{created: make(chan *clientStopStream, 4)}
	observer := &clientStopObserver{finished: make(chan observe.Observation, 16)}
	server, err := NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(wsAuthKey{}).(int)
			return value, ok
		},
		WebSocketInit: func(_ context.Context, payload json.RawMessage) (context.Context, error) {
			if string(payload) != `{"token":"valid"}` {
				return nil, errors.New("invalid token")
			}
			return context.WithValue(context.Background(), wsAuthKey{}, 41), nil
		},
		Observer:            observer,
		ReportInternalError: func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	t.Cleanup(host.Close)
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("ack = %#v", ack)
	}
	return connection, executor, observer
}

func awaitFinishedSubscription(t *testing.T, observer *clientStopObserver) observe.Observation {
	t.Helper()
	select {
	case finished := <-observer.finished:
		return finished
	case <-time.After(5 * time.Second):
		t.Fatal("subscription operation never finished")
		return observe.Observation{}
	}
}

func TestWebSocketClientCompletedOperationSendsNoFurtherFrame(t *testing.T) {
	connection, executor, observer := newClientStopServer(t)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	stopped := <-executor.created

	writeWS(t, connection, wsMessage{ID: "one", Type: "complete"})
	finished := awaitFinishedSubscription(t, observer)
	if finished.Outcome() == observe.OutcomeFailure {
		t.Fatalf("client-completed subscription recorded as failed: outcome=%s reason=%s", finished.Outcome(), finished.Reason())
	}
	select {
	case <-stopped.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("client-completed operation did not close its stream")
	}

	writeWS(t, connection, wsMessage{ID: "canary", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	canary := <-executor.created
	canary.values <- Response{Data: map[string]any{"ticks": int32(7)}}
	if frame := readWS(t, connection); frame.ID != "canary" || frame.Type != "next" {
		t.Fatalf("frame after a client-completed operation = %#v", frame)
	}
}

func TestWebSocketGenuineSubscriptionFailuresStillTerminateTheOperation(t *testing.T) {
	for _, code := range []events.ErrorCode{
		events.CodeSubscriptionOverflow,
		events.CodeSubscriptionRevalidation,
		events.CodeSubscriptionSourceClosed,
	} {
		t.Run(string(code), func(t *testing.T) {
			connection, executor, observer := newClientStopServer(t)
			writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
			stream := <-executor.created
			stream.failure <- events.Failure(code)
			frame := readWS(t, connection)
			if frame.ID != "one" || frame.Type != "error" || !bytes.Contains(frame.Payload, []byte(`"code":"`+string(code)+`"`)) {
				t.Fatalf("terminal frame for %s = %#v", code, frame)
			}
			if finished := awaitFinishedSubscription(t, observer); finished.Outcome() != observe.OutcomeFailure {
				t.Fatalf("%s recorded outcome=%s reason=%s", code, finished.Outcome(), finished.Reason())
			}
		})
	}
}

func TestWebSocketServerCompletedOperationStillSendsComplete(t *testing.T) {
	connection, executor, observer := newClientStopServer(t)
	writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	stream := <-executor.created
	close(stream.values)
	if frame := readWS(t, connection); frame.ID != "one" || frame.Type != "complete" {
		t.Fatalf("server-completed terminal frame = %#v", frame)
	}
	if finished := awaitFinishedSubscription(t, observer); finished.Outcome() != observe.OutcomeSuccess {
		t.Fatalf("server-completed subscription outcome=%s reason=%s", finished.Outcome(), finished.Reason())
	}
}

type blockingMarshal struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (value *blockingMarshal) MarshalJSON() ([]byte, error) {
	value.once.Do(func() { close(value.entered) })
	<-value.release
	return []byte("7"), nil
}

type stopRaceStream struct {
	value  *blockingMarshal
	handed atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (stream *stopRaceStream) Recv(ctx context.Context) (Response, error) {
	if stream.handed.CompareAndSwap(false, true) {
		return Response{Data: map[string]any{"ticks": stream.value}}, nil
	}
	<-ctx.Done()
	return Response{}, events.Failure(events.CodeSubscriptionCancelled)
}

func (stream *stopRaceStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}

type stopRaceExecutor struct {
	created    chan ResponseStream
	subscribes atomic.Int64
}

func (executor *stopRaceExecutor) Execute(context.Context, int, Operation) Response {
	return Response{Data: map[string]any{"viewer": int32(1)}}
}

func (executor *stopRaceExecutor) Subscribe(_ context.Context, _ int, operation Operation) (ResponseStream, error) {
	if operation.Definition == nil {
		return nil, errors.New("unexpected subscription admission")
	}
	if executor.subscribes.Add(1) > 1 {
		stream := newClientStopStream()
		executor.created <- stream
		return stream, nil
	}
	stream := &stopRaceStream{
		value:  &blockingMarshal{entered: make(chan struct{}), release: make(chan struct{})},
		closed: make(chan struct{}),
	}
	executor.created <- stream
	return stream, nil
}

func TestWebSocketStoppedOperationWritesNoFrameIssuedAfterTheStopWasProcessed(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		executor := &stopRaceExecutor{created: make(chan ResponseStream, 4)}
		server, err := NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, Config[int]{
			PrincipalFromContext: func(ctx context.Context) (int, bool) {
				value, ok := ctx.Value(wsAuthKey{}).(int)
				return value, ok
			},
			WebSocketInit: func(context.Context, json.RawMessage) (context.Context, error) {
				return context.WithValue(context.Background(), wsAuthKey{}, 41), nil
			},
			ReportInternalError: func(context.Context, error) {},
		}, executor)
		if err != nil {
			t.Fatal(err)
		}
		host := httptest.NewServer(server.Handler())
		dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
		connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
		if err != nil {
			host.Close()
			t.Fatal(err)
		}
		writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"token":"valid"}`)})
		if ack := readWS(t, connection); ack.Type != "connection_ack" {
			t.Fatalf("ack = %#v", ack)
		}
		writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
		stopped := (<-executor.created).(*stopRaceStream)
		<-stopped.value.entered

		writeWS(t, connection, wsMessage{ID: "one", Type: "complete"})
		<-stopped.closed

		writeWS(t, connection, wsMessage{ID: "canary", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
		canary := (<-executor.created).(*clientStopStream)
		canary.values <- Response{Data: map[string]any{"ticks": int32(99)}}
		if frame := readWS(t, connection); frame.ID != "canary" || frame.Type != "next" {
			t.Fatalf("attempt %d: canary frame = %#v", attempt, frame)
		}
		close(stopped.value.release)

		for {
			_ = connection.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			var frame wsMessage
			if err := connection.ReadJSON(&frame); err != nil {
				break
			}
			if frame.ID == "one" {
				t.Fatalf("attempt %d: %s frame for a completed operation was issued after the stop", attempt, frame.Type)
			}
		}
		_ = connection.Close()
		host.Close()
	}
}

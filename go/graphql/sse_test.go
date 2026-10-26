package graphql

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
)

type sseAuthKey struct{}

type sseTestStream struct {
	values    chan Response
	failure   error
	cancelled chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	cancel    sync.Once
}

func newSSETestStream() *sseTestStream {
	return &sseTestStream{values: make(chan Response, 4), cancelled: make(chan struct{}), closed: make(chan struct{})}
}

func (stream *sseTestStream) Recv(ctx context.Context) (Response, error) {
	select {
	case value, open := <-stream.values:
		if !open {
			if stream.failure != nil {
				return Response{}, stream.failure
			}
			return Response{}, io.EOF
		}
		return value, nil
	case <-ctx.Done():
		stream.cancel.Do(func() { close(stream.cancelled) })
		return Response{}, ctx.Err()
	}
}

func (stream *sseTestStream) Close() error {
	stream.closeOnce.Do(func() { close(stream.closed) })
	return nil
}

type sseExecutor struct {
	stream     ResponseStream
	refusal    error
	subscribed atomic.Int32
	executed   atomic.Int32
}

func (executor *sseExecutor) Execute(_ context.Context, principal int, operation Operation) Response {
	executor.executed.Add(1)
	if operation.Definition.Operation == "mutation" {
		value, _ := operation.Variables["value"].(int64)
		if plain, ok := operation.Variables["value"].(int); ok {
			value = int64(plain)
		}
		return Response{Data: map[string]any{"setViewer": int32(value) + int32(principal)}}
	}
	return Response{Data: map[string]any{"viewer": int32(principal)}}
}

func (executor *sseExecutor) Subscribe(_ context.Context, principal int, operation Operation) (ResponseStream, error) {
	executor.subscribed.Add(1)
	if executor.refusal != nil {
		return nil, executor.refusal
	}
	if principal != 41 || operation.Definition == nil {
		return nil, errors.New("unexpected subscription admission")
	}
	return executor.stream, nil
}

func newSSEServer(t *testing.T, limits events.Limits, stream ResponseStream) (*Server[int], *sseExecutor, *httptest.Server) {
	t.Helper()
	executor := &sseExecutor{stream: stream}
	server, err := NewServer(`type Query { viewer: Int! } type Mutation { setViewer(value: Int!): Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(sseAuthKey{}).(int)
			return value, ok
		},
		EventLimits: limits, ReportInternalError: func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(sseAuthenticated(server.Handler()))
	t.Cleanup(host.Close)
	return server, executor, host
}

func sseAuthenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer valid" {
			request = request.WithContext(context.WithValue(request.Context(), sseAuthKey{}, 41))
		}
		next.ServeHTTP(writer, request)
	})
}

type sseFrame struct {
	event   string
	data    string
	comment bool
}

type sseConnection struct {
	response *http.Response
	reader   *bufio.Reader
	cancel   context.CancelFunc
}

func openSSE(t *testing.T, ctx context.Context, url string, body string, authenticated bool) *sseConnection {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if authenticated {
		request.Header.Set("Authorization", "Bearer valid")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	connection := &sseConnection{response: response, reader: bufio.NewReader(response.Body), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = response.Body.Close()
	})
	return connection
}

func (connection *sseConnection) next() (sseFrame, error) {
	var frame sseFrame
	var data []string
	started := false
	for {
		line, err := connection.reader.ReadString('\n')
		if err != nil {
			return sseFrame{}, err
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if !started {
				continue
			}
			frame.data = strings.Join(data, "\n")
			return frame, nil
		case strings.HasPrefix(line, ":"):
			frame.comment = true
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		default:
			return sseFrame{}, errors.New("unexpected SSE line " + line)
		}
		started = true
	}
}

func (connection *sseConnection) event(t *testing.T) sseFrame {
	t.Helper()
	for {
		frame, err := connection.next()
		if err != nil {
			t.Fatalf("SSE stream ended before the next event: %v", err)
		}
		if !frame.comment {
			return frame
		}
	}
}

func (connection *sseConnection) expectNext(t *testing.T, want string) {
	t.Helper()
	frame := connection.event(t)
	if frame.event != "next" || frame.data != want {
		t.Fatalf("frame = %#v, want next %s", frame, want)
	}
}

func (connection *sseConnection) expectComplete(t *testing.T) {
	t.Helper()
	frame := connection.event(t)
	if frame.event != "complete" || frame.data != "" {
		t.Fatalf("frame = %#v, want complete", frame)
	}
	if _, err := connection.next(); err != io.EOF {
		t.Fatalf("SSE stream continued after complete: %v", err)
	}
}

func TestSSESubscriptionStreamsEachEventThenCompletes(t *testing.T) {
	stream := newSSETestStream()
	_, executor, host := newSSEServer(t, events.Limits{}, stream)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	if connection.response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", connection.response.StatusCode)
	}
	for header, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no", "Connection": "keep-alive"} {
		if got := connection.response.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	for tick := range 3 {
		stream.values <- Response{Data: map[string]any{"ticks": int32(tick)}}
		connection.expectNext(t, `{"data":{"ticks":`+string(rune('0'+tick))+`}}`)
	}
	close(stream.values)
	connection.expectComplete(t)
	<-stream.closed
	if executor.subscribed.Load() != 1 {
		t.Fatalf("subscriptions = %d", executor.subscribed.Load())
	}
}

func TestSSEQueryAndMutationSendOneNextThenComplete(t *testing.T) {
	_, executor, host := newSSEServer(t, events.Limits{}, newSSETestStream())
	query := openSSE(t, context.Background(), host.URL, `{"query":"query Current { viewer }","operationName":"Current"}`, true)
	if query.response.StatusCode != http.StatusOK || query.response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("query status=%d type=%q", query.response.StatusCode, query.response.Header.Get("Content-Type"))
	}
	query.expectNext(t, `{"data":{"viewer":41}}`)
	query.expectComplete(t)
	mutation := openSSE(t, context.Background(), host.URL, `{"query":"mutation Change($value: Int!) { setViewer(value: $value) }","operationName":"Change","variables":{"value":1}}`, true)
	mutation.expectNext(t, `{"data":{"setViewer":42}}`)
	mutation.expectComplete(t)
	if executor.executed.Load() != 2 || executor.subscribed.Load() != 0 {
		t.Fatalf("executed=%d subscribed=%d", executor.executed.Load(), executor.subscribed.Load())
	}
}

func TestSSETerminalErrorIsANextFrameThenComplete(t *testing.T) {
	stream := newSSETestStream()
	stream.failure = events.Failure(events.CodeSubscriptionOverflow)
	_, _, host := newSSEServer(t, events.Limits{}, stream)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection.expectNext(t, `{"data":{"ticks":1}}`)
	close(stream.values)
	frame := connection.event(t)
	var decoded Response
	if err := json.Unmarshal([]byte(frame.data), &decoded); err != nil {
		t.Fatal(err)
	}
	if frame.event != "next" || decoded.Data != nil || len(decoded.Errors) != 1 || decoded.Errors[0].Extensions["code"] != string(events.CodeSubscriptionOverflow) {
		t.Fatalf("terminal frame = %#v", frame)
	}
	connection.expectComplete(t)
	<-stream.closed
}

func TestSSEPanickingStreamIsSanitizedThenCompletes(t *testing.T) {
	panicking := &panicResponseStream{}
	_, _, host := newSSEServer(t, events.Limits{}, panicking)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	frame := connection.event(t)
	if frame.event != "next" || strings.Contains(frame.data, "private") || !strings.Contains(frame.data, `"code":"INTERNAL_SERVER_ERROR"`) {
		t.Fatalf("panic frame = %#v", frame)
	}
	connection.expectComplete(t)
	deadline := time.Now().Add(time.Second)
	for panicking.closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if panicking.closes.Load() != 1 {
		t.Fatalf("panic stream closes = %d", panicking.closes.Load())
	}
}

func TestSSEClientDisconnectCancelsTheSubscription(t *testing.T) {
	stream := newSSETestStream()
	server, _, host := newSSEServer(t, events.Limits{}, stream)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection.expectNext(t, `{"data":{"ticks":1}}`)
	connection.cancel()
	_ = connection.response.Body.Close()
	for name, signal := range map[string]chan struct{}{"cancelled": stream.cancelled, "closed": stream.closed} {
		select {
		case <-signal:
		case <-time.After(2 * time.Second):
			t.Fatalf("client disconnect left the subscription %s=false", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("a disconnected stream still held the server: %v", err)
	}
}

func TestSSEShutdownEndsActiveStreams(t *testing.T) {
	stream := newSSETestStream()
	server, _, host := newSSEServer(t, events.Limits{}, stream)
	connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
	stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
	connection.expectNext(t, `{"data":{"ticks":1}}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := connection.next()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("shutdown ended the stream with %v", err)
			}
			break
		}
		if !frame.comment {
			t.Fatalf("shutdown wrote %#v", frame)
		}
	}
	<-stream.closed
	for _, accept := range []string{"text/event-stream", "application/json, text/event-stream"} {
		response, body := postGraphQL(t, host.URL, `{"query":"subscription { ticks }"}`, accept)
		if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Content-Type") != "application/json; charset=utf-8" || body != "{\"errors\":[{\"message\":\"GraphQL server is shutting down\",\"extensions\":{\"code\":\"SERVICE_UNAVAILABLE\"}}]}\n" {
			t.Fatalf("post-shutdown Accept %q: status=%d type=%q body=%q", accept, response.StatusCode, response.Header.Get("Content-Type"), body)
		}
	}
}

func TestSSEKeepAliveComments(t *testing.T) {
	for name, limits := range map[string]events.Limits{
		"KeepAlive":          {KeepAlive: 20 * time.Millisecond},
		"WebSocketKeepAlive": {WebSocketKeepAlive: 20 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			stream := newSSETestStream()
			_, _, host := newSSEServer(t, limits, stream)
			connection := openSSE(t, context.Background(), host.URL, `{"query":"subscription { ticks }"}`, true)
			for comments := 0; comments < 2; {
				frame, err := connection.next()
				if err != nil {
					t.Fatal(err)
				}
				if !frame.comment || frame.event != "" || frame.data != "" {
					t.Fatalf("idle stream wrote %#v", frame)
				}
				comments++
			}
			stream.values <- Response{Data: map[string]any{"ticks": int32(1)}}
			connection.expectNext(t, `{"data":{"ticks":1}}`)
		})
	}
}

func TestSSERefusesBeforeTheStreamOpens(t *testing.T) {
	stream := newSSETestStream()
	_, executor, host := newSSEServer(t, events.Limits{}, stream)
	for name, testCase := range map[string]struct {
		body          string
		authenticated bool
		status        int
		code          string
	}{
		"unauthenticated":   {`{"query":"subscription { ticks }"}`, false, http.StatusUnauthorized, "UNAUTHENTICATED"},
		"malformed":         {`{"query":"subscription { ticks }","unknown":1}`, true, http.StatusBadRequest, "BAD_REQUEST"},
		"invalid operation": {`{"query":"subscription { missing }"}`, true, http.StatusBadRequest, "GRAPHQL_VALIDATION_FAILED"},
	} {
		t.Run(name, func(t *testing.T) {
			connection := openSSE(t, context.Background(), host.URL, testCase.body, testCase.authenticated)
			body, err := io.ReadAll(connection.response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if connection.response.StatusCode != testCase.status || !strings.HasPrefix(connection.response.Header.Get("Content-Type"), "application/json") || !bytes.Contains(body, []byte(`"code":"`+testCase.code+`"`)) {
				t.Fatalf("status=%d type=%q body=%s", connection.response.StatusCode, connection.response.Header.Get("Content-Type"), body)
			}
		})
	}
	if executor.subscribed.Load() != 0 || executor.executed.Load() != 0 {
		t.Fatalf("a refused request reached the executor: subscribed=%d executed=%d", executor.subscribed.Load(), executor.executed.Load())
	}
}

func TestSSESubscriptionsNeedASubscriptionExecutor(t *testing.T) {
	server, err := NewServer(`type Query { viewer: Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 41, true },
		ReportInternalError:  func(context.Context, error) {},
	}, fuzzExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"subscription { ticks }"}`))
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "SUBSCRIPTIONS_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/graphql", nil)
	request.Header.Set("Accept", "text/event-stream")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || !strings.Contains(recorder.Body.String(), "METHOD_NOT_ALLOWED") {
		t.Fatalf("GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

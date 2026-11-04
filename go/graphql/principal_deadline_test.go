package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/gorilla/websocket"
)

type principalShape struct {
	marked   bool
	deadline time.Duration
}

func (shape principalShape) apply(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = context.WithValue(ctx, sseAuthKey{}, 41)
	if shape.marked {
		ctx = golem.WithoutCredentialExpiry(ctx)
	}
	if shape.deadline > 0 {
		return context.WithTimeout(ctx, shape.deadline)
	}
	return ctx, func() {}
}

type deadlineFixture struct {
	server   *Server[int]
	executor *sseExecutor
	stream   *sseTestStream
	host     *httptest.Server
}

func newDeadlineFixture(t *testing.T, limits events.Limits, shape principalShape) *deadlineFixture {
	t.Helper()
	fixture := &deadlineFixture{stream: newSSETestStream()}
	fixture.executor = &sseExecutor{stream: fixture.stream}
	server, err := NewServer(`type Query { viewer: Int! } type Mutation { setViewer(value: Int!): Int! } type Subscription { ticks: Int! }`, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(sseAuthKey{}).(int)
			return value, ok
		},
		EventLimits: limits, ReportInternalError: func(context.Context, error) {},
	}, fixture.executor)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server = server
	handler := server.Handler()
	fixture.host = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := shape.apply(request.Context())
		defer cancel()
		handler.ServeHTTP(writer, request.WithContext(ctx))
	}))
	t.Cleanup(fixture.host.Close)
	return fixture
}

func (fixture *deadlineFixture) dial(t *testing.T, header http.Header) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
	connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(fixture.host.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func (fixture *deadlineFixture) init(t *testing.T, header http.Header, fields string) *websocket.Conn {
	t.Helper()
	connection := fixture.dial(t, header)
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: liveInit(fields)})
	return connection
}

func TestWebSocketClosesWith4401AtTheContextDeadline(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{}, principalShape{deadline: 500 * time.Millisecond})
	started := time.Now()
	connection := fixture.init(t, nil, "")
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("connection ack = %#v", ack)
	}
	writeWS(t, connection, wsMessage{ID: "1", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	readAckWS(t, connection, "1")
	code, reason := readClose(t, connection)
	elapsed := time.Since(started)
	if code != 4401 || reason != "credential expired" {
		t.Fatalf("close = %d %q, want 4401", code, reason)
	}
	if elapsed < 400*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("closed after %s, want about 500ms", elapsed)
	}
}

func TestWebSocketClosesWith4401AtOpenPlusRevalidateEveryForAMarkedPrincipal(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: events.MinimumRecheckInterval}, principalShape{marked: true})
	started := time.Now()
	connection := fixture.init(t, nil, "")
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("connection ack = %#v", ack)
	}
	code, _ := readClose(t, connection)
	elapsed := time.Since(started)
	if code != 4401 {
		t.Fatalf("close code = %d, want 4401", code)
	}
	if elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatalf("closed after %s, want about %s", elapsed, events.MinimumRecheckInterval)
	}
}

func TestWebSocketMarkedPrincipalWithADeadlineClosesAtTheDeadline(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: time.Hour}, principalShape{marked: true, deadline: 500 * time.Millisecond})
	started := time.Now()
	connection := fixture.init(t, nil, "")
	if ack := readWS(t, connection); ack.Type != "connection_ack" {
		t.Fatalf("connection ack = %#v", ack)
	}
	code, _ := readClose(t, connection)
	if code != 4401 || time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("close = %d after %s, want 4401 at the deadline", code, time.Since(started))
	}
}

func TestPrincipalDeadlineRule(t *testing.T) {
	openedAt := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	credentialExpiry := openedAt.Add(37 * time.Minute)
	withDeadline := func(ctx context.Context) context.Context {
		derived, cancel := context.WithDeadline(ctx, credentialExpiry)
		t.Cleanup(cancel)
		return derived
	}
	cases := []struct {
		name         string
		ctx          func() context.Context
		revalidate   time.Duration
		wantDeadline time.Time
		wantMessage  string
	}{
		{"deadline", func() context.Context { return withDeadline(context.Background()) }, 0, credentialExpiry, ""},
		{"deadline with revalidation", func() context.Context { return withDeadline(context.Background()) }, 2 * time.Second, credentialExpiry, ""},
		{"marked with a deadline uses the deadline", func() context.Context { return withDeadline(golem.WithoutCredentialExpiry(context.Background())) }, 2 * time.Second, credentialExpiry, ""},
		{"marked uses open plus RevalidateEvery", func() context.Context { return golem.WithoutCredentialExpiry(context.Background()) }, 2 * time.Second, openedAt.Add(2 * time.Second), ""},
		{"unmarked without a deadline", func() context.Context { return context.Background() }, 2 * time.Second, time.Time{}, "golem.WithoutCredentialExpiry"},
		{"unmarked without a deadline or RevalidateEvery", func() context.Context { return context.Background() }, 0, time.Time{}, "context deadline"},
		{"marked without RevalidateEvery", func() context.Context { return golem.WithoutCredentialExpiry(context.Background()) }, 0, time.Time{}, "RevalidateEvery"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := &Server[int]{eventLimits: events.Limits{RevalidateEvery: testCase.revalidate}}
			deadline, failure := server.principalDeadline(testCase.ctx(), openedAt)
			if testCase.wantMessage == "" {
				if failure != nil || !deadline.Equal(testCase.wantDeadline) {
					t.Fatalf("deadline = %s, failure = %v, want %s", deadline, failure, testCase.wantDeadline)
				}
				return
			}
			if failure == nil || failure.Extensions["code"] != codeDeadlineRequired || !strings.Contains(failure.Message, testCase.wantMessage) {
				t.Fatalf("failure = %#v, want %s naming %q", failure, codeDeadlineRequired, testCase.wantMessage)
			}
		})
	}
}

func TestWebSocketRefusesAPrincipalWithoutADerivableDeadline(t *testing.T) {
	cases := []struct {
		name   string
		limits events.Limits
		shape  principalShape
	}{
		{"unmarked without RevalidateEvery", events.Limits{}, principalShape{}},
		{"unmarked even with RevalidateEvery", events.Limits{RevalidateEvery: time.Hour}, principalShape{}},
		{"marked without RevalidateEvery", events.Limits{}, principalShape{marked: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeadlineFixture(t, testCase.limits, testCase.shape)
			connection := fixture.init(t, nil, "")
			code, reason := readClose(t, connection)
			if code != 4426 || reason != "golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED" {
				t.Fatalf("close = %d %q, want 4426 golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED", code, reason)
			}
		})
	}
}

func TestAClientCannotClaimNoCredentialExpiry(t *testing.T) {
	good := `"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"`
	claims := map[string]string{
		"golem block field": `{` + good + `,"noCredentialExpiry":true}}`,
		"golem block alias": `{` + good + `,"credentialExpiry":"none"}}`,
		"init payload":      `{` + good + `},"noCredentialExpiry":true,"withoutCredentialExpiry":true}`,
	}
	t.Run("websocket payload", func(t *testing.T) {
		fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: time.Hour}, principalShape{})
		for name, payload := range claims {
			connection := fixture.dial(t, nil)
			writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(payload)})
			code, reason := readClose(t, connection)
			want := "golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED"
			if strings.HasPrefix(name, "golem block") {
				want = "golem:protocol"
			}
			if code != 4426 || reason != want {
				t.Fatalf("%s: close = %d %q, want 4426 %q", name, code, reason, want)
			}
		}
	})
	t.Run("websocket upgrade header", func(t *testing.T) {
		fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: time.Hour}, principalShape{})
		connection := fixture.init(t, http.Header{"X-Golem-No-Credential-Expiry": {"true"}}, "")
		if code, reason := readClose(t, connection); code != 4426 || reason != "golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED" {
			t.Fatalf("close = %d %q", code, reason)
		}
	})
	t.Run("sse subscription", func(t *testing.T) {
		fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: time.Hour}, principalShape{})
		for name, extensions := range map[string]string{
			"golem block field": `{"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `","noCredentialExpiry":true}}`,
			"no claim":          `{"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}}`,
		} {
			_, body := postGraphQL(t, fixture.host.URL, `{"query":"subscription { ticks }","extensions":`+extensions+`}`, "text/event-stream")
			want := codeDeadlineRequired
			if strings.Contains(name, "field") {
				want = codeProtocolMismatch
			}
			if !strings.Contains(body, `"code":"`+want+`"`) {
				t.Fatalf("%s: body = %q, want %s", name, body, want)
			}
		}
		request, err := http.NewRequest(http.MethodPost, fixture.host.URL, strings.NewReader(string(liveBody("subscription { ticks }"))))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("X-Golem-No-Credential-Expiry", "true")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status with the header claim = %d, want a refusal", response.StatusCode)
		}
	})
}

func TestSSERefusesAPrincipalWithoutADerivableDeadline(t *testing.T) {
	cases := []struct {
		name    string
		limits  events.Limits
		shape   principalShape
		message string
	}{
		{"unmarked", events.Limits{RevalidateEvery: time.Hour}, principalShape{}, "golem.WithoutCredentialExpiry"},
		{"marked without RevalidateEvery", events.Limits{}, principalShape{marked: true}, "RevalidateEvery"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeadlineFixture(t, testCase.limits, testCase.shape)
			response, body := postGraphQL(t, fixture.host.URL, string(liveBody("subscription { ticks }")), "text/event-stream")
			var decoded Response
			if err := json.Unmarshal([]byte(body), &decoded); err != nil || len(decoded.Errors) != 1 {
				t.Fatalf("refusal = %q (%v)", body, err)
			}
			failure := decoded.Errors[0]
			if response.StatusCode != http.StatusBadRequest || failure.Extensions["code"] != codeDeadlineRequired || !strings.Contains(failure.Message, testCase.message) {
				t.Fatalf("status=%d failure=%#v", response.StatusCode, failure)
			}
			if fixture.executor.subscribed.Load() != 0 {
				t.Fatal("a refused principal reached the executor")
			}
		})
	}
}

func TestSSEStreamEndsAtThePrincipalDeadline(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{}, principalShape{deadline: 500 * time.Millisecond})
	started := time.Now()
	connection := openSSE(t, context.Background(), fixture.host.URL, string(liveBody("subscription { ticks }")), true)
	connection.expectAck(t)
	for {
		if _, err := connection.next(); err != nil {
			break
		}
	}
	if elapsed := time.Since(started); elapsed < 400*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("stream ended after %s, want about 500ms", elapsed)
	}
}

func TestHTTPQueriesAndMutationsAreNeverRefusedForWantOfADeadline(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{}, principalShape{})
	for _, accept := range []string{"", "text/event-stream"} {
		for _, body := range []string{
			`{"query":"query { viewer }"}`,
			`{"query":"mutation Change($value: Int!) { setViewer(value: $value) }","operationName":"Change","variables":{"value":1}}`,
		} {
			_, response := postGraphQL(t, fixture.host.URL, body, accept)
			if strings.Contains(response, "errors") || !strings.Contains(response, `"data"`) {
				t.Fatalf("accept %q %s = %q", accept, body, response)
			}
		}
	}
}

func TestSSEStreamOfAMarkedPrincipalEndsAtOpenPlusRevalidateEvery(t *testing.T) {
	fixture := newDeadlineFixture(t, events.Limits{RevalidateEvery: events.MinimumRecheckInterval}, principalShape{marked: true})
	started := time.Now()
	connection := openSSE(t, context.Background(), fixture.host.URL, string(liveBody("subscription { ticks }")), true)
	connection.expectAck(t)
	for {
		if _, err := connection.next(); err != nil {
			break
		}
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatalf("stream ended after %s, want about %s", elapsed, events.MinimumRecheckInterval)
	}
}

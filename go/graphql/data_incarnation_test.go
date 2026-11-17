package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/commitreport"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/gorilla/websocket"
)

func incarnationCompilation(t *testing.T) (compilerir.CompilationIR, golem.SchemaBundle) {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "testdata/dataincarnation", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("real opted compilation: %#v", compiled.Diagnostics)
	}
	return *compiled.Compilation, generatedTestBundle(t, *compiled.Compilation)
}

type incarnationProtocolExecutor struct {
	*sseExecutor
	identity        golem.DataIncarnation
	authentications atomic.Int32
	commit          string
	ownedExtensions map[string]any
}

func (e *incarnationProtocolExecutor) DataIncarnation() golem.DataIncarnation { return e.identity }
func (e *incarnationProtocolExecutor) AuthenticatePrincipal(context.Context, int) error {
	e.authentications.Add(1)
	return nil
}
func (e *incarnationProtocolExecutor) Execute(ctx context.Context, principal int, operation Operation) Response {
	if e.commit != "" {
		e.executed.Add(1)
		if e.commit == "PARTIAL" {
			commitreport.From(ctx).Committed()
		} else {
			commitreport.From(ctx).Unknown()
		}
		return Response{Errors: []Error{publicError("TEST_COMMIT_FAILURE", "test")}, Extensions: e.ownedExtensions}
	}
	return e.sseExecutor.Execute(ctx, principal, operation)
}

func incarnationTestBlock(bundle golem.SchemaBundle, tail string) string {
	return `{"protocol":1,"contract":"` + bundle.Contract().Fingerprint().String() + `","manifestVersion":3,"capabilities":["data-incarnation-v1"]` + tail + `}`
}

func TestDataIncarnationHTTPAndWebSocketAdmission(t *testing.T) {
	_, bundle := incarnationCompilation(t)
	identity, err := dataincarnation.Parse("11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	executor := &incarnationProtocolExecutor{sseExecutor: &sseExecutor{stream: newSSETestStream()}, identity: identity}
	config := Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(),
		PrincipalFromContext: func(ctx context.Context) (int, bool) { value, ok := ctx.Value(sseAuthKey{}).(int); return value, ok },
		ReportInternalError:  func(context.Context, error) {},
		WebSocketInit: func(ctx context.Context, _ json.RawMessage) (context.Context, error) {
			return withTestDeadline(context.WithValue(ctx, sseAuthKey{}, 41)), nil
		},
	}
	const sdl = `type Query { viewer: Int! } type Mutation { setViewer(value: Int!): Int! } type Subscription { ticks: Int! }`
	if _, err := NewServer(sdl, config, executor.sseExecutor); err == nil {
		t.Fatal("opted bundle accepted a wrapper dropping admitted identity")
	}
	withoutBundle := config
	withoutBundle.Bundle = golem.SchemaBundle{}
	if _, err := NewServer(sdl, withoutBundle, executor); err == nil {
		t.Fatal("identity without authoritative bundle was accepted")
	}
	server, err := NewServer(sdl, config, executor)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body, accept string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if authenticated {
			request = request.WithContext(withTestDeadline(context.WithValue(request.Context(), sseAuthKey{}, 41)))
		}
		if accept != "" {
			request.Header.Set("Accept", accept)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	stamp := incarnationTestBlock(bundle, `,"dataIncarnation":"`+identity.String()+`"`)
	for _, tail := range []string{"", `,"dataIncarnation":null`, `,"dataIncarnation":""`, `,"dataIncarnation":"00000000000000000000000000000000"`, `,"dataIncarnation":"22222222222222222222222222222222"`} {
		response := post(`{"query":"mutation { setViewer(value:1) }","extensions":{"golem":`+incarnationTestBlock(bundle, tail)+`}}`, "", true)
		if !strings.Contains(response.Body.String(), codeDataIncarnationMismatch) || strings.Contains(response.Body.String(), identity.String()) {
			t.Fatalf("missing/stale stamp response: %s", response.Body)
		}
	}
	if executor.executed.Load() != 0 {
		t.Fatal("rejected mutation reached business execution")
	}
	response := post(`{"query":"{ viewer }","extensions":{"golem":`+stamp+`}}`, "", true)
	if !strings.Contains(response.Body.String(), `"viewer":41`) || !strings.Contains(response.Body.String(), `"dataIncarnation":"`+identity.String()+`"`) {
		t.Fatalf("admitted response: %s", response.Body)
	}

	nonce := "33333333333333333333333333333333"
	discover := incarnationTestBlock(bundle, `,"discover":{"nonce":"`+nonce+`"}`)
	response = post(`{"extensions":{"golem":`+discover+`}}`, "", true)
	if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), `"data":null`) || !strings.Contains(response.Body.String(), `"nonce":"`+nonce+`"`) || !strings.Contains(response.Body.String(), identity.String()) || executor.authentications.Load() != 1 || executor.executed.Load() != 1 {
		t.Fatalf("discovery response=%s auth=%d business=%d", response.Body, executor.authentications.Load(), executor.executed.Load())
	}
	for _, extra := range []string{`"query":null,`, `"query":"",`, `"query":"mutation { setViewer(value:1) }",`, `"variables":null,`, `"operationName":"",`} {
		response = post(`{`+extra+`"extensions":{"golem":`+discover+`}}`, "", true)
		if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "BAD_REQUEST") {
			t.Fatalf("mixed discovery accepted: %s", response.Body)
		}
	}
	for _, tail := range []string{`,"discover":{"nonce":""}`, `,"discover":{"nonce":"00000000000000000000000000000000"}`, `,"discover":{"nonce":"ABCDEFABCDEFABCDEFABCDEFABCDEFAB"}`, `,"discover":{"nonce":"` + nonce + `","extra":true}`, `,"discover":{"nonce":"` + nonce + `"},"dataIncarnation":null`} {
		response = post(`{"extensions":{"golem":`+incarnationTestBlock(bundle, tail)+`}}`, "", true)
		if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "BAD_REQUEST") {
			t.Fatalf("invalid discovery accepted: %s", response.Body)
		}
	}
	response = post(`{"extensions":{"golem":`+discover+`}}`, "text/event-stream", true)
	if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "BAD_REQUEST") || executor.subscribed.Load() != 0 {
		t.Fatal("SSE discovery opened business work")
	}
	for _, accept := range []string{"", "text/event-stream"} {
		response = post(`{"extensions":{"golem":`+discover+`}}`, accept, false)
		if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "UNAUTHENTICATED") || strings.Contains(response.Body.String(), identity.String()) {
			t.Fatalf("unauthenticated discovery: %s", response.Body)
		}
	}
	if executor.authentications.Load() != 1 || executor.executed.Load() != 1 {
		t.Fatal("invalid discovery invoked auth/business callbacks")
	}

	for _, state := range []string{"PARTIAL", "UNKNOWN"} {
		executor.commit = state
		executor.ownedExtensions = map[string]any{"unrelated": "retained", "golem": map[string]any{"existing": true}}
		response = post(`{"query":"mutation { setViewer(value:1) }","extensions":{"golem":`+stamp+`}}`, "", true)
		if !strings.Contains(response.Body.String(), `"commit":"`+state+`"`) || !strings.Contains(response.Body.String(), `"existing":true`) || !strings.Contains(response.Body.String(), identity.String()) {
			t.Fatalf("commit metadata lost: %s", response.Body)
		}
		if len(executor.ownedExtensions["golem"].(map[string]any)) != 1 {
			t.Fatal("response merge mutated executor-owned extensions")
		}
	}
	executor.commit = ""
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	dial := func() *websocket.Conn {
		t.Helper()
		c, _, err := (&websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}).Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	bad := dial()
	writeWS(t, bad, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"golem":` + incarnationTestBlock(bundle, `,"dataIncarnation":"22222222222222222222222222222222"`) + `}`)})
	_, _, err = bad.ReadMessage()
	if failure, ok := err.(*websocket.CloseError); !ok || failure.Code != closeHandshakeRefused || failure.Text != "golem:incarnation" {
		t.Fatalf("stale WS init: %v", err)
	}
	connection := dial()
	writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{"golem":` + stamp + `}`)})
	if ack := readWS(t, connection); ack.Type != "connection_ack" || !strings.Contains(string(ack.Payload), identity.String()) {
		t.Fatalf("connection ack: %+v", ack)
	}
	writeWS(t, connection, wsMessage{ID: "missing", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }"}`)})
	if failure := readWS(t, connection); failure.Type != "error" || executor.subscribed.Load() != 0 {
		t.Fatalf("unstamped subscribe executed: %+v", failure)
	}
	writeWS(t, connection, wsMessage{ID: "good", Type: "subscribe", Payload: json.RawMessage(`{"query":"subscription { ticks }","extensions":{"golem":` + stamp + `}}`)})
	if ack := readWS(t, connection); ack.Type != "next" || !strings.Contains(string(ack.Payload), `"live":true`) || !strings.Contains(string(ack.Payload), identity.String()) {
		t.Fatalf("operation ack: %+v", ack)
	}
	writeWS(t, connection, wsMessage{ID: "good", Type: "complete"})
}

func TestDataIncarnationDiscoveryUsesGeneratedAuthenticationOnly(t *testing.T) {
	compilation, bundle := incarnationCompilation(t)
	identity, _ := dataincarnation.Parse("11111111111111111111111111111111")
	source := &incarnationProtocolExecutor{identity: identity}
	begins, authentications := 0, 0
	config := GeneratedExecutorConfig[int]{Bundle: bundle, DataIncarnationSource: source, ReportInternalError: func(context.Context, error) {},
		BeginCaller:           func(context.Context, int) (CallerExecution, error) { begins++; return &generatedTestCaller{}, nil },
		AuthenticatePrincipal: func(context.Context, int) error { authentications++; return nil },
	}
	missing := config
	missing.DataIncarnationSource = nil
	if _, err := NewGeneratedExecutor(missing); err == nil {
		t.Fatal("opted generated executor accepted missing App source")
	}
	executor, err := NewGeneratedExecutor(config)
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(document.SDL, Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(), PrincipalFromContext: func(context.Context) (int, bool) { return 41, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"extensions":{"golem":` + incarnationTestBlock(bundle, `,"discover":{"nonce":"33333333333333333333333333333333"}`) + `}}`
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if begins != 0 || authentications != 1 || !strings.Contains(response.Body.String(), identity.String()) {
		t.Fatalf("generated discovery begins=%d auth=%d response=%s", begins, authentications, response.Body)
	}
	config.AuthenticatePrincipal = func(context.Context, int) error { panic("private-auth-canary") }
	panicking, err := NewGeneratedExecutor(config)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	panicServer, err := NewServer(document.SDL, Config[int]{Bundle: bundle, ContractFingerprint: bundle.Contract().Fingerprint(), PrincipalFromContext: func(context.Context) (int, bool) { return 41, true }, ReportInternalError: func(_ context.Context, err error) {
		reports++
		if !strings.Contains(err.Error(), "discovery authentication panicked") {
			t.Errorf("unexpected trusted report: %v", err)
		}
	}}, panicking)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	panicServer.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if reports != 1 || begins != 0 || !strings.Contains(response.Body.String(), "INTERNAL_SERVER_ERROR") || strings.Contains(response.Body.String(), "private-auth-canary") || strings.Contains(response.Body.String(), identity.String()) {
		t.Fatalf("discovery panic reports=%d begins=%d response=%s", reports, begins, response.Body)
	}
}

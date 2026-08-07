package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

type testExecutor struct{ called bool }

func (executor *testExecutor) Execute(_ context.Context, principal int, _ Operation) Response {
	executor.called = true
	return Response{Data: map[string]any{"viewer": principal}}
}

type boundaryExecutor struct {
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func (executor *boundaryExecutor) Execute(ctx context.Context, principal int, _ Operation) Response {
	executor.calls.Add(1)
	if executor.entered != nil {
		select {
		case executor.entered <- struct{}{}:
		default:
		}
	}
	if executor.release != nil {
		select {
		case <-executor.release:
		case <-ctx.Done():
			return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
	}
	return Response{Data: map[string]any{"viewer": principal, "echo": "ok", "nested": map[string]any{"value": 1}, "items": []any{map[string]any{"value": 1}}}}
}

func TestServerAuthenticatesValidatesBoundsAndExecutes(t *testing.T) {
	executor := &testExecutor{}
	server, err := NewServer("type Query { viewer: Int! }", Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 7, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"query { viewer }"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 200 || !executor.called || !strings.Contains(response.Body.String(), `"viewer":7`) {
		t.Fatalf("response=%d %s called=%v", response.Code, response.Body.String(), executor.called)
	}
}

func TestServerRejectsMissingPrincipalBeforeExecution(t *testing.T) {
	executor := &testExecutor{}
	server, err := NewServer("type Query { viewer: Int! }", Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 0, false }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewBufferString(`{"query":"{ viewer }"}`)))
	if executor.called || !strings.Contains(response.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("response=%s called=%v", response.Body.String(), executor.called)
	}
}

func TestServerSeparatesParseValidationAndVariableCoercion(t *testing.T) {
	executor := &testExecutor{}
	server, err := NewServer("type Query { viewer(id: Int!): Int! }", Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(context.Context, error) {}}, executor)
	if err != nil {
		t.Fatal(err)
	}
	parse := server.Execute(context.Background(), 1, Request{Query: "query {"})
	if parse.Errors[0].Extensions["code"] != "GRAPHQL_PARSE_FAILED" {
		t.Fatalf("parse response = %#v", parse)
	}
	validation := server.Execute(context.Background(), 1, Request{Query: "query { missing }"})
	if validation.Errors[0].Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
		t.Fatalf("validation response = %#v", validation)
	}
	variables := server.Execute(context.Background(), 1, Request{Query: "query Read($id: Int!) { viewer(id: $id) }", OperationName: "Read", Variables: map[string]any{"id": "no"}})
	if variables.Errors[0].Extensions["code"] != "BAD_USER_INPUT" || executor.called {
		t.Fatalf("variable response = %#v called=%v", variables, executor.called)
	}
}

type panicExecutor struct{}

func (panicExecutor) Execute(context.Context, int, Operation) Response { panic("private panic") }

func TestServerContainsPanicAndPresenterSanitizesUnknownCause(t *testing.T) {
	reported := 0
	server, err := NewServer("type Query { viewer: Int! }", Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(_ context.Context, err error) {
		if err != nil {
			reported++
		}
	}}, panicExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 1, Request{Query: "query { viewer }"})
	if response.Errors[0].Extensions["code"] != "INTERNAL_SERVER_ERROR" || reported != 1 || strings.Contains(response.Errors[0].Message, "private") {
		t.Fatalf("response=%#v reported=%d", response, reported)
	}
	presented := PresentError(context.Background(), errors.New("driver secret"), []any{"viewer"}, func(context.Context, error) { reported++ })
	if presented.Extensions["code"] != "INTERNAL_SERVER_ERROR" || presented.Message != "internal server error" || reported != 2 {
		t.Fatalf("presented=%#v reported=%d", presented, reported)
	}
}

func TestGraphQLErrorPresenterMapsEveryCodeAndNeverLeaksTrustedCause(t *testing.T) {
	codes := []struct {
		code golem.ErrorCode
		want string
	}{
		{golem.CodeBadUserInput, "BAD_USER_INPUT"},
		{golem.CodeNotFound, "NOT_FOUND"},
		{golem.CodeConflict, "CONFLICT"},
		{golem.CodeUnauthenticated, "UNAUTHENTICATED"},
		{golem.CodeForbidden, "FORBIDDEN"},
	}
	for _, item := range codes {
		t.Run(item.want, func(t *testing.T) {
			trusted := errors.New("private sql driver detail")
			failure := golem.RuntimeReadError(item.code, "findMany", golem.ModelID{1}, golem.FieldID{2}, "stable public message", trusted)
			reported := 0
			presented := PresentError(context.Background(), failure, []any{"alias", 0}, func(context.Context, error) { reported++ })
			if presented.Extensions["code"] != item.want || presented.Message != "stable public message" || reported != 0 {
				t.Fatalf("presented=%#v reported=%d", presented, reported)
			}
			encoded := presented.Message
			if strings.Contains(encoded, "driver") || strings.Contains(encoded, "private") {
				t.Fatalf("trusted cause leaked: %#v", presented)
			}
		})
	}
	reported := 0
	unknown := PresentError(context.Background(), errors.New("postgres password=secret"), []any{"viewer"}, func(context.Context, error) { reported++ })
	if unknown.Extensions["code"] != "INTERNAL_SERVER_ERROR" || unknown.Message != "internal server error" || reported != 1 || strings.Contains(unknown.Message, "secret") {
		t.Fatalf("unknown=%#v reported=%d", unknown, reported)
	}
}

func TestGraphQLHTTPAndDirectExecutionLimitsRefuseAtExactBoundariesBeforeUnboundedWork(t *testing.T) {
	const scalarSDL = `scalar JSON
type Query { viewer: Int! echo(input: JSON): String! nested: Node! items(take: Int = 2): [Node!]! }
type Node { value: Int! nested: Node }`
	simple := Request{Query: `{ viewer }`}

	t.Run("MaxRequestBytes-direct-and-http", func(t *testing.T) {
		encodedVariables, _ := json.Marshal(simple.Variables)
		encodedEnvelope, _ := json.Marshal(requestEnvelope{Query: simple.Query, Variables: encodedVariables})
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxRequestBytes: len(encodedEnvelope)}, simple, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxRequestBytes: len(encodedEnvelope)}, Request{Query: simple.Query + " "}, false, "QUERY_LIMIT_EXCEEDED")

		body := []byte(`{"query":"{ viewer }"}`)
		executor := &boundaryExecutor{}
		server := newBoundaryServer(t, scalarSDL, Limits{MaxRequestBytes: len(body)}, executor)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body)))
		if executor.calls.Load() != 1 || response.Code != http.StatusOK {
			t.Fatalf("exact HTTP body boundary calls=%d response=%s", executor.calls.Load(), response.Body.String())
		}
		executor.calls.Store(0)
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(append(append([]byte(nil), body...), ' '))))
		if executor.calls.Load() != 0 || !strings.Contains(response.Body.String(), "BAD_REQUEST") {
			t.Fatalf("overflow HTTP body calls=%d response=%s", executor.calls.Load(), response.Body.String())
		}
	})

	t.Run("MaxVariableBytes", func(t *testing.T) {
		allowed := Request{Query: `query($input: JSON) { echo(input: $input) }`, Variables: map[string]any{"input": "x"}}
		refused := Request{Query: allowed.Query, Variables: map[string]any{"input": "xx"}}
		encoded, _ := json.Marshal(allowed.Variables)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxVariableBytes: len(encoded)}, allowed, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxVariableBytes: len(encoded)}, refused, false, "INPUT_LIMIT_EXCEEDED")

		allowedBody := []byte(`{"query":"query($input: JSON) { echo(input: $input) }","variables":{"input":"x"}}`)
		refusedBody := []byte(`{"query":"query($input: JSON) { echo(input: $input) }","variables":{"input":"xx"}}`)
		executor := &boundaryExecutor{}
		server := newBoundaryServer(t, scalarSDL, Limits{MaxVariableBytes: len(encoded)}, executor)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(allowedBody)))
		if executor.calls.Load() != 1 || response.Code != http.StatusOK {
			t.Fatalf("exact HTTP variables boundary calls=%d response=%s", executor.calls.Load(), response.Body.String())
		}
		executor.calls.Store(0)
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(refusedBody)))
		if executor.calls.Load() != 0 || !strings.Contains(response.Body.String(), "INPUT_LIMIT_EXCEEDED") {
			t.Fatalf("overflow HTTP variables crossed execution boundary calls=%d response=%s", executor.calls.Load(), response.Body.String())
		}
	})

	t.Run("MaxTokens", func(t *testing.T) {
		limit := exactTokenCount(simple.Query)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxTokens: limit}, simple, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxTokens: limit - 1}, simple, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxASTNodes", func(t *testing.T) {
		limit := exactDocumentASTNodes(t, simple.Query)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxASTNodes: limit}, simple, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxASTNodes: limit - 1}, simple, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxFragments", func(t *testing.T) {
		allowed := Request{Query: `query { ...One } fragment One on Query { viewer }`}
		refused := Request{Query: `query { ...One ...Two } fragment One on Query { viewer } fragment Two on Query { viewer }`}
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxFragments: 1}, allowed, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxFragments: 1}, refused, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxDepth", func(t *testing.T) {
		request := Request{Query: `{ nested { nested { value } } }`}
		shape := exactOperationShape(t, request.Query)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxDepth: shape.depth}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxDepth: shape.depth - 1}, request, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxSelectedFields", func(t *testing.T) {
		request := Request{Query: `{ first: viewer second: viewer }`}
		shape := exactOperationShape(t, request.Query)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxSelectedFields: shape.fields}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxSelectedFields: shape.fields - 1}, request, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxAliases", func(t *testing.T) {
		request := Request{Query: `{ first: viewer second: viewer }`}
		shape := exactOperationShape(t, request.Query)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxAliases: shape.aliases}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxAliases: shape.aliases - 1}, request, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("MaxInputDepth", func(t *testing.T) {
		request := Request{Query: `query($input: JSON) { echo(input: $input) }`, Variables: map[string]any{"input": map[string]any{"nested": map[string]any{"value": "x"}}}}
		_, depth, _, _ := inputShape(request.Variables, 1)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxInputDepth: depth}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxInputDepth: depth - 1}, request, false, "INPUT_LIMIT_EXCEEDED")
	})

	t.Run("MaxInputNodes", func(t *testing.T) {
		request := Request{Query: `query($input: JSON) { echo(input: $input) }`, Variables: map[string]any{"input": map[string]any{"left": 1, "right": 2}}}
		nodes, _, _, _ := inputShape(request.Variables, 1)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxInputNodes: nodes}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxInputNodes: nodes - 1}, request, false, "INPUT_LIMIT_EXCEEDED")
	})

	t.Run("MaxListItems", func(t *testing.T) {
		request := Request{Query: `query($input: JSON) { echo(input: $input) }`, Variables: map[string]any{"input": []any{1, 2, 3}}}
		_, _, items, _ := inputShape(request.Variables, 1)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxListItems: items}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxListItems: items - 1}, request, false, "INPUT_LIMIT_EXCEEDED")
	})

	t.Run("MaxComplexity", func(t *testing.T) {
		request := Request{Query: `{ items(take: 2) { value } }`}
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxComplexity: 3, MaxPageSize: 2}, request, true)
		assertDirectLimitBoundary(t, scalarSDL, Limits{MaxComplexity: 2, MaxPageSize: 2}, request, false, "QUERY_LIMIT_EXCEEDED")
	})

	t.Run("exactly-one-selected-operation", func(t *testing.T) {
		request := Request{Query: `query One { viewer } query Two { viewer }`}
		assertDirectLimitBoundary(t, scalarSDL, Limits{}, request, false, "GRAPHQL_VALIDATION_FAILED")
	})

	t.Run("MaxResolverConcurrency", func(t *testing.T) {
		executor := &boundaryExecutor{entered: make(chan struct{}, 2), release: make(chan struct{})}
		server := newBoundaryServer(t, scalarSDL, Limits{MaxResolverConcurrency: 1}, executor)
		firstDone := make(chan Response, 1)
		go func() { firstDone <- server.Execute(context.Background(), 1, simple) }()
		select {
		case <-executor.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("first resolver never entered")
		}
		secondCtx, cancel := context.WithCancel(context.Background())
		secondDone := make(chan Response, 1)
		go func() { secondDone <- server.Execute(secondCtx, 1, simple) }()
		select {
		case <-executor.entered:
			t.Fatal("second resolver crossed MaxResolverConcurrency boundary")
		case <-time.After(50 * time.Millisecond):
		}
		cancel()
		second := <-secondDone
		if executor.calls.Load() != 1 || len(second.Errors) != 1 || second.Errors[0].Extensions["code"] != "INTERNAL_SERVER_ERROR" {
			t.Fatalf("cancelled queued resolver calls=%d response=%#v", executor.calls.Load(), second)
		}
		close(executor.release)
		if first := <-firstDone; len(first.Errors) != 0 {
			t.Fatalf("admitted resolver failed: %#v", first)
		}
	})

	for index := 0; index < 16; index++ {
		limit := Limits{}
		setFuzzLimit(&limit, index, fuzzLimitMaximum(index)+1)
		if _, err := NormalizeLimits(limit); err == nil {
			t.Fatalf("public limit %d accepted a value above its portable hard maximum", index)
		}
	}
}

func newBoundaryServer(t testing.TB, sdl string, limits Limits, executor Executor[int]) *Server[int] {
	t.Helper()
	server, err := NewServer(sdl, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits:               limits,
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func assertDirectLimitBoundary(t testing.TB, sdl string, limits Limits, candidate Request, candidateAllowed bool, wantCode ...string) {
	t.Helper()
	executor := &boundaryExecutor{}
	server := newBoundaryServer(t, sdl, limits, executor)
	if candidateAllowed {
		response := server.Execute(context.Background(), 1, candidate)
		if len(response.Errors) != 0 || executor.calls.Load() != 1 {
			t.Fatalf("exact boundary refused: response=%#v calls=%d", response, executor.calls.Load())
		}
		return
	}
	response := server.Execute(context.Background(), 1, candidate)
	if len(response.Errors) != 1 || executor.calls.Load() != 0 {
		t.Fatalf("overflow crossed execution boundary: response=%#v calls=%d", response, executor.calls.Load())
	}
	if len(wantCode) != 1 || response.Errors[0].Extensions["code"] != wantCode[0] {
		t.Fatalf("overflow returned unstable code: response=%#v want=%v", response, wantCode)
	}
}

func exactTokenCount(query string) int {
	for limit := 1; limit <= hardLimits.MaxTokens; limit++ {
		if !exceedsLexicalTokenLimit(query, limit) {
			return limit
		}
	}
	return 0
}

func exactDocumentASTNodes(t testing.TB, query string) int {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "boundary.graphql", Input: query})
	if err != nil {
		t.Fatal(err)
	}
	for limit := 1; limit <= hardLimits.MaxASTNodes; limit++ {
		if !documentASTNodesExceed(document, limit) {
			return limit
		}
	}
	t.Fatal("could not measure GraphQL AST nodes")
	return 0
}

func exactOperationShape(t testing.TB, query string) operationShapeStats {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "boundary.graphql", Input: query})
	if err != nil {
		t.Fatal(err)
	}
	definition := document.Operations.ForName("")
	if definition == nil {
		t.Fatal("boundary operation is absent")
	}
	return boundedOperationShape(definition.SelectionSet, document.Fragments, hardLimits)
}

func TestGraphQLFragmentExpansionIsBoundedBeforeExecutorWork(t *testing.T) {
	executor := &testExecutor{}
	server, err := NewServer("type Query { viewer: Int! }", Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits:               Limits{MaxASTNodes: 9, MaxFragments: 2},
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	response := server.Execute(context.Background(), 1, Request{Query: `
		query { ...A ...A }
		fragment A on Query { ...B ...B }
		fragment B on Query { viewer }
	`})
	if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "QUERY_LIMIT_EXCEEDED" || executor.called {
		t.Fatalf("response=%#v called=%v", response, executor.called)
	}
}

func TestGraphQLUnexpectedPanicAndProviderErrorsReportTrustedCauseButReturnSanitizedShape(t *testing.T) {
	reported := make([]string, 0, 2)
	reporter := func(_ context.Context, err error) { reported = append(reported, err.Error()) }
	server, err := NewServer("type Query { viewer: Int! }", Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: reporter}, panicExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	panicResponse := server.Execute(context.Background(), 1, Request{Query: "{ viewer }"})
	if len(panicResponse.Errors) != 1 || panicResponse.Errors[0].Message != "internal server error" || len(reported) != 1 || !strings.Contains(reported[0], "private panic") {
		t.Fatalf("panic response=%#v reports=%#v", panicResponse, reported)
	}
	provider := PresentError(context.Background(), errors.New("pq: relation users_secret does not exist"), []any{"viewer"}, reporter)
	if provider.Message != "internal server error" || strings.Contains(provider.Message, "users_secret") || len(reported) != 2 || !strings.Contains(reported[1], "users_secret") {
		t.Fatalf("provider=%#v reports=%#v", provider, reported)
	}
}

func TestServerComplexityUsesListFanoutDefaultsVariablesAndDirectives(t *testing.T) {
	sdl := `type Query { items(take: Int = 2): [Item!]! } type Item { value: Int! children(take: Int = 2): [Item!]! }`
	executor := &testExecutor{}
	server, err := NewServer(sdl, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits:               Limits{MaxComplexity: 8, MaxPageSize: 4},
		ReportInternalError:  func(context.Context, error) {},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	overflow := server.Execute(context.Background(), 1, Request{Query: `query($take: Int!) { items(take: $take) { value children { value } } }`, Variables: map[string]any{"take": 2}})
	if len(overflow.Errors) != 1 || overflow.Errors[0].Extensions["code"] != "QUERY_LIMIT_EXCEEDED" || executor.called {
		t.Fatalf("overflow=%#v called=%v", overflow, executor.called)
	}
	allowed := server.Execute(context.Background(), 1, Request{Query: `query($skip: Boolean!) { items { value children @skip(if: $skip) { value } } }`, Variables: map[string]any{"skip": true}})
	if len(allowed.Errors) != 0 || !executor.called {
		t.Fatalf("allowed=%#v called=%v", allowed, executor.called)
	}
}

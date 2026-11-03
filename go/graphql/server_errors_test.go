package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	goast "go/ast"
	"go/build"
	goparser "go/parser"
	"go/token"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

const errorsSDL = `scalar JSON
type Query {
	viewer: Int!
	echo(input: JSON): String!
	echoString(value: String!): String!
	nested: Node!
	items(take: Int = 2): [Node!]!
}
type Node { value: Int! nested: Node children(take: Int = 2): [Node!]! }
type Subscription { ticks(input: JSON): Int! }`

func newErrorsServer(t testing.TB, limits Limits, introspection bool) *Server[int] {
	t.Helper()
	server, err := NewServer(errorsSDL, Config[int]{
		PrincipalFromContext: func(ctx context.Context) (int, bool) {
			value, ok := ctx.Value(wsAuthKey{}).(int)
			return value, ok
		},
		WebSocketInit: func(context.Context, json.RawMessage) (context.Context, error) {
			return context.WithValue(context.Background(), wsAuthKey{}, 41), nil
		},
		Limits:              limits,
		Introspection:       introspection,
		ReportInternalError: func(context.Context, error) {},
	}, &protocolExecutor{stream: &protocolStream{values: make(chan Response, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func executeErrors(t testing.TB, limits Limits, introspection bool, request Request) []Error {
	t.Helper()
	response := newErrorsServer(t, limits, introspection).Execute(context.Background(), 41, request)
	if response.Data != nil {
		t.Fatalf("refused request returned data: %#v", response)
	}
	return response.Errors
}

func TestLimitErrorsNameTheLimit(t *testing.T) {
	const inputQuery = `query($input: JSON) { echo(input: $input) }`
	cases := []struct {
		name          string
		limits        Limits
		introspection bool
		request       Request
		code          string
		message       string
		limit         string
		max           int
		cost          int
	}{
		{name: "MaxRequestBytes", limits: Limits{MaxRequestBytes: 20}, request: Request{Query: `{ viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL request exceeds MaxRequestBytes (20)", limit: "MaxRequestBytes", max: 20},
		{name: "MaxVariableBytes", limits: Limits{MaxVariableBytes: 5}, request: Request{Query: inputQuery, Variables: map[string]any{"input": "xxxxxxxx"}}, code: "INPUT_LIMIT_EXCEEDED", message: "GraphQL variables exceed MaxVariableBytes (5)", limit: "MaxVariableBytes", max: 5},
		{name: "MaxTokens", limits: Limits{MaxTokens: 2}, request: Request{Query: `{ viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxTokens (2)", limit: "MaxTokens", max: 2},
		{name: "MaxASTNodes", limits: Limits{MaxASTNodes: 1}, request: Request{Query: `{ viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxASTNodes (1)", limit: "MaxASTNodes", max: 1},
		{name: "MaxFragments", limits: Limits{MaxFragments: 1}, request: Request{Query: `query { ...One ...Two } fragment One on Query { viewer } fragment Two on Query { viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxFragments (1)", limit: "MaxFragments", max: 1},
		{name: "MaxDepth", limits: Limits{MaxDepth: 2}, request: Request{Query: `{ nested { nested { value } } }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxDepth (2)", limit: "MaxDepth", max: 2},
		{name: "MaxSelectedFields", limits: Limits{MaxSelectedFields: 1}, request: Request{Query: `{ first: viewer second: viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxSelectedFields (1)", limit: "MaxSelectedFields", max: 1},
		{name: "MaxAliases", limits: Limits{MaxAliases: 1}, request: Request{Query: `{ first: viewer second: viewer }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxAliases (1)", limit: "MaxAliases", max: 1},
		{name: "MaxInputDepth", limits: Limits{MaxInputDepth: 1}, request: Request{Query: inputQuery, Variables: map[string]any{"input": map[string]any{"nested": map[string]any{"value": "x"}}}}, code: "INPUT_LIMIT_EXCEEDED", message: "GraphQL input exceeds MaxInputDepth (1)", limit: "MaxInputDepth", max: 1},
		{name: "MaxInputNodes", limits: Limits{MaxInputNodes: 1}, request: Request{Query: inputQuery, Variables: map[string]any{"input": map[string]any{"left": 1, "right": 2}}}, code: "INPUT_LIMIT_EXCEEDED", message: "GraphQL input exceeds MaxInputNodes (1)", limit: "MaxInputNodes", max: 1},
		{name: "MaxListItems", limits: Limits{MaxListItems: 2}, request: Request{Query: inputQuery, Variables: map[string]any{"input": []any{1, 2, 3}}}, code: "INPUT_LIMIT_EXCEEDED", message: "GraphQL input exceeds MaxListItems (2)", limit: "MaxListItems", max: 2},
		{name: "MaxComplexity", limits: Limits{MaxComplexity: 2}, request: Request{Query: `{ items(take: 2) { value } }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query complexity 3 exceeds MaxComplexity (2)", limit: "MaxComplexity", max: 2, cost: 3},
		{name: "MaxComplexity-saturates", limits: Limits{MaxComplexity: 100_000}, request: Request{Query: `{ items(take: 2000000000) { children(take: 2000000000) { children(take: 2000000000) { children(take: 2000000000) { value } } } } }`}, code: "QUERY_LIMIT_EXCEEDED", message: fmt.Sprintf("GraphQL query complexity %d exceeds MaxComplexity (100000)", math.MaxInt), limit: "MaxComplexity", max: 100_000, cost: math.MaxInt},
		{name: "MaxSchemaRoots", introspection: true, request: Request{Query: `{ first: __schema { queryType { name } } second: __schema { queryType { name } } }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxSchemaRoots (1)", limit: "MaxSchemaRoots", max: 1},
		{name: "MaxTypeRoots", introspection: true, request: Request{Query: `{ a: __type(name: "Query") { name } b: __type(name: "Query") { name } c: __type(name: "Query") { name } d: __type(name: "Query") { name } e: __type(name: "Query") { name } f: __type(name: "Query") { name } g: __type(name: "Query") { name } h: __type(name: "Query") { name } i: __type(name: "Query") { name } }`}, code: "QUERY_LIMIT_EXCEEDED", message: "GraphQL query exceeds MaxTypeRoots (8)", limit: "MaxTypeRoots", max: 8},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failures := executeErrors(t, testCase.limits, testCase.introspection, testCase.request)
			if len(failures) != 1 {
				t.Fatalf("errors = %#v", failures)
			}
			want := map[string]any{"code": testCase.code, "limit": testCase.limit, "max": testCase.max}
			if testCase.cost != 0 {
				want["cost"] = testCase.cost
			}
			if failures[0].Message != testCase.message || !reflect.DeepEqual(failures[0].Extensions, want) {
				t.Fatalf("error = %#v\nwant message %q extensions %#v", failures[0], testCase.message, want)
			}
		})
	}
}

func TestRefusalsThatAreNotLimitsCarryNoLimit(t *testing.T) {
	cases := []struct {
		name          string
		introspection bool
		request       Request
		code          string
		message       string
	}{
		{"empty query", false, Request{}, "BAD_REQUEST", "GraphQL request requires a query"},
		{"introspection disabled", false, Request{Query: `{ __schema { queryType { name } } }`}, "GRAPHQL_VALIDATION_FAILED", "GraphQL introspection is disabled"},
		{"operation not found", false, Request{Query: `query One { viewer }`, OperationName: "Two"}, "GRAPHQL_VALIDATION_FAILED", "GraphQL operation was not found"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failures := executeErrors(t, Limits{}, testCase.introspection, testCase.request)
			if len(failures) != 1 || failures[0].Message != testCase.message || !reflect.DeepEqual(failures[0].Extensions, map[string]any{"code": testCase.code}) {
				t.Fatalf("errors = %#v", failures)
			}
		})
	}
}

func TestVariableBytesCheckedAfterDecodingNamesTheLimit(t *testing.T) {
	body := []byte("{\"query\":\"query($input: JSON) { echo(input: $input) }\",\"variables\":{\"a\":\"\xff\xff\xff\xff\"}}")
	server := newErrorsServer(t, Limits{MaxVariableBytes: 12}, false)
	for name, accept := range map[string]string{"http": "", "sse": "text/event-stream"} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
			request = request.WithContext(context.WithValue(request.Context(), wsAuthKey{}, 41))
			if accept != "" {
				request.Header.Set("Accept", accept)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			want := `{"errors":[{"message":"GraphQL variables exceed MaxVariableBytes (12)","extensions":{"code":"INPUT_LIMIT_EXCEEDED","limit":"MaxVariableBytes","max":12}}]}`
			assertSameJSON(t, recorder.Body.Bytes(), []byte(want))
		})
	}
}

func TestOperationShapeAndComplexityReportMalformedDocumentsInsteadOfLimits(t *testing.T) {
	document := mustParseDocument(t, `query { ...Missing }`)
	definition := document.Operations.ForName("")
	if shape := boundedOperationShape(definition.SelectionSet, document.Fragments, hardLimits); !shape.malformed || shape.limit != nil {
		t.Fatalf("missing fragment shape = %#v", shape)
	}
	if _, err := operationComplexity(definition.SelectionSet, document.Fragments, nil, NewListBounds(nil), map[string]bool{}, false); err == nil {
		t.Fatal("missing fragment complexity was accepted")
	}
	meta := rootMetaSelection{schemas: map[string]bool{}, types: map[string]bool{}}
	if err := meta.collect(definition.SelectionSet, document.Fragments, nil, map[string]bool{}); err == nil {
		t.Fatal("missing fragment introspection scan was accepted")
	}
	cyclic := mustParseDocument(t, `query { ...Loop } fragment Loop on Query { ...Loop }`)
	cyclicDefinition := cyclic.Operations.ForName("")
	if shape := boundedOperationShape(cyclicDefinition.SelectionSet, cyclic.Fragments, hardLimits); !shape.malformed || shape.limit != nil {
		t.Fatalf("cyclic fragment shape = %#v", shape)
	}
}

type errorsTransport struct {
	name string
	send func(t *testing.T, server *Server[int], body string) []map[string]any
}

func errorsTransports() []errorsTransport {
	return []errorsTransport{
		{"http", func(t *testing.T, server *Server[int], body string) []map[string]any {
			return decodeWireErrors(t, postErrors(t, server, body, ""))
		}},
		{"sse", func(t *testing.T, server *Server[int], body string) []map[string]any {
			return decodeWireErrors(t, postErrors(t, server, body, "text/event-stream"))
		}},
		{"websocket", func(t *testing.T, server *Server[int], body string) []map[string]any {
			host := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				server.Handler().ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), wsAuthKey{}, 41)))
			}))
			defer host.Close()
			dialer := websocket.Dialer{Subprotocols: []string{graphqlTransportWS}}
			connection, _, err := dialer.Dial("ws"+strings.TrimPrefix(host.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			writeWS(t, connection, wsMessage{Type: "connection_init", Payload: json.RawMessage(`{}`)})
			if ack := readWS(t, connection); ack.Type != "connection_ack" {
				t.Fatalf("ack = %#v", ack)
			}
			writeWS(t, connection, wsMessage{ID: "one", Type: "subscribe", Payload: json.RawMessage(body)})
			message := readWS(t, connection)
			if message.ID != "one" || message.Type != "error" {
				t.Fatalf("message = %#v payload=%s", message, message.Payload)
			}
			var failures []map[string]any
			if err := json.Unmarshal(message.Payload, &failures); err != nil {
				t.Fatal(err)
			}
			return failures
		}},
	}
}

func postErrors(t *testing.T, server *Server[int], body, accept string) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), wsAuthKey{}, 41))
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK && accept == "" {
		return recorder.Body.Bytes()
	}
	if accept != "" && recorder.Code != http.StatusBadRequest {
		t.Fatalf("SSE refusal status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func decodeWireErrors(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var envelope struct {
		Data   any              `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, body)
	}
	if envelope.Data != nil {
		t.Fatalf("refused request returned data: %s", body)
	}
	return envelope.Errors
}

func assertSameJSON(t testing.TB, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("response\n got=%s\nwant=%s", bytes.TrimSpace(got), want)
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestLimitAndValidationErrorsAreIdenticalOnEveryTransport(t *testing.T) {
	limits := Limits{MaxTokens: 64, MaxDepth: 4, MaxInputNodes: 4, MaxVariableBytes: 40, MaxComplexity: 20}
	manyFields := "{ " + strings.Repeat("missing ", 12) + "}"
	var manyLocations strings.Builder
	for index := 0; index < 8; index++ {
		fmt.Fprintf(&manyLocations, `{"message":"GraphQL field does not exist on the selected type","locations":[{"line":1,"column":%d}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED","rule":"FieldsOnCorrectType"}},`, 3+index*8)
	}
	cases := []struct {
		name  string
		query string
		vars  string
		want  string
	}{
		{"empty query", ``, ``, `[{"message":"GraphQL request requires a query","extensions":{"code":"BAD_REQUEST"}}]`},
		{"depth", `{ nested { nested { nested { value } } } }`, ``, `[{"message":"GraphQL query exceeds MaxDepth (4)","extensions":{"code":"QUERY_LIMIT_EXCEEDED","limit":"MaxDepth","max":4}}]`},
		{"tokens", "{ " + strings.Repeat("viewer ", 70) + "}", ``, `[{"message":"GraphQL query exceeds MaxTokens (64)","extensions":{"code":"QUERY_LIMIT_EXCEEDED","limit":"MaxTokens","max":64}}]`},
		{"input nodes", `query($input: JSON) { echo(input: $input) }`, `{"input":{"a":1,"b":2,"c":3}}`, `[{"message":"GraphQL input exceeds MaxInputNodes (4)","extensions":{"code":"INPUT_LIMIT_EXCEEDED","limit":"MaxInputNodes","max":4}}]`},
		{"variable bytes", `query($input: JSON) { echo(input: $input) }`, `{"input":"` + strings.Repeat("x", 40) + `"}`, `[{"message":"GraphQL variables exceed MaxVariableBytes (40)","extensions":{"code":"INPUT_LIMIT_EXCEEDED","limit":"MaxVariableBytes","max":40}}]`},
		{"complexity", `{ items(take: 5) { value nested { value } } other: items(take: 5) { value } }`, ``, `[{"message":"GraphQL query complexity 22 exceeds MaxComplexity (20)","extensions":{"code":"QUERY_LIMIT_EXCEEDED","limit":"MaxComplexity","max":20,"cost":22}}]`},
		{"introspection disabled", `{ __schema { queryType { name } } }`, ``, `[{"message":"GraphQL introspection is disabled","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]`},
		{"unknown field", `{ privateDriverToken }`, ``, `[{"message":"GraphQL field does not exist on the selected type","locations":[{"line":1,"column":3}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED","rule":"FieldsOnCorrectType"}}]`},
		{"wrong literal", "{\n  echoString(value: PRIVATE_DRIVER_TOKEN)\n}", ``, `[{"message":"GraphQL value has the wrong type","locations":[{"line":2,"column":21}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED","rule":"ValuesOfCorrectType"}}]`},
		{"missing required argument", `{ echoString }`, ``, `[{"message":"GraphQL required argument is missing","locations":[{"line":1,"column":3}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED","rule":"ProvidedRequiredArguments"}}]`},
		{"unknown fields are capped", manyFields, ``, `[` + manyLocations.String() + `{"message":"and 4 more validation errors","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]`},
	}
	for _, transport := range errorsTransports() {
		t.Run(transport.name, func(t *testing.T) {
			server := newErrorsServer(t, limits, false)
			for _, testCase := range cases {
				t.Run(testCase.name, func(t *testing.T) {
					body := `{"query":` + jsonString(testCase.query)
					if testCase.vars != "" {
						body += `,"variables":` + testCase.vars
					}
					body += `}`
					failures := transport.send(t, server, body)
					encoded, err := json.Marshal(failures)
					if err != nil {
						t.Fatal(err)
					}
					assertSameJSON(t, encoded, []byte(testCase.want))
					for _, echoed := range []string{"PRIVATE_DRIVER_TOKEN", "privateDriverToken"} {
						if bytes.Contains(encoded, []byte(echoed)) {
							t.Fatalf("response echoed request text %q: %s", echoed, encoded)
						}
					}
				})
			}
		})
	}
}

func TestValidationErrorsAreTheSameWithIntrospectionOnAndOff(t *testing.T) {
	queries := []string{`{ privateDriverToken }`, `{ echoString }`, `{ echoString(value: PRIVATE_DRIVER_TOKEN) }`, `query($unused: Int) { viewer }`}
	for _, query := range queries {
		off := newErrorsServer(t, Limits{}, false).Execute(context.Background(), 41, Request{Query: query})
		on := newErrorsServer(t, Limits{}, true).Execute(context.Background(), 41, Request{Query: query})
		if len(off.Errors) == 0 || !reflect.DeepEqual(off, on) {
			t.Fatalf("query %q: introspection off=%#v on=%#v", query, off, on)
		}
	}
}

func TestValidationErrorsAreCappedAtEight(t *testing.T) {
	for _, testCase := range []struct {
		fields  int
		summary string
	}{{8, ""}, {9, "and 1 more validation error"}, {12, "and 4 more validation errors"}} {
		query := "{ " + strings.Repeat("missing ", testCase.fields) + "}"
		failures := executeErrors(t, Limits{}, false, Request{Query: query})
		wantCount := min(testCase.fields, 8)
		if testCase.summary != "" {
			wantCount++
		}
		if len(failures) != wantCount {
			t.Fatalf("%d fields gave %d errors: %#v", testCase.fields, len(failures), failures)
		}
		if testCase.summary != "" {
			last := failures[len(failures)-1]
			if last.Message != testCase.summary || !reflect.DeepEqual(last.Extensions, map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"}) || last.Locations != nil {
				t.Fatalf("summary = %#v", last)
			}
		}
		encoded, err := json.Marshal(Response{Errors: failures})
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 4096 {
			t.Fatalf("capped response is %d bytes", len(encoded))
		}
	}
}

func TestValidationPresenterBoundsLocationsAndNamesUnknownRulesGenerically(t *testing.T) {
	locations := []gqlerror.Location{{Line: 1, Column: 1}, {Line: 2, Column: 2}, {Line: 3, Column: 3}, {Line: 4, Column: 4}, {Line: 5, Column: 5}, {Line: 6, Column: 6}}
	presented := presentValidationErrors(gqlerror.List{
		{Rule: "FromAFutureGqlparser", Message: `Cannot query field "privateDriverToken" on type "Query".`, Locations: locations},
		{Rule: "", Message: "private-driver-token"},
	})
	if len(presented) != 2 {
		t.Fatalf("presented = %#v", presented)
	}
	first, second := presented[0], presented[1]
	if first.Message != "GraphQL document is invalid" || !reflect.DeepEqual(first.Extensions, map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"}) || !reflect.DeepEqual(first.Locations, []Location{{1, 1}, {2, 2}, {3, 3}, {4, 4}}) {
		t.Fatalf("unknown rule = %#v", first)
	}
	if second.Message != "GraphQL document is invalid" || second.Locations != nil || !reflect.DeepEqual(second.Extensions, map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"}) {
		t.Fatalf("ruleless error = %#v", second)
	}
}

func TestValidationRuleTableCoversPinnedGqlparser(t *testing.T) {
	registered := registeredGqlparserRules(t)
	var missing, stale []string
	for _, name := range registered {
		if _, ok := validationTemplates[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range validationTemplates {
		found := false
		for _, candidate := range registered {
			found = found || candidate == name
		}
		if !found {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("validation templates drifted from the pinned gqlparser: missing=%v stale=%v", missing, stale)
	}
	seen := map[string]string{}
	for name, message := range validationTemplates {
		if previous, duplicate := seen[message]; duplicate {
			t.Fatalf("rules %s and %s share one message %q", previous, name, message)
		}
		seen[message] = name
	}
}

func registeredGqlparserRules(t *testing.T) []string {
	t.Helper()
	pkg, err := build.Import("github.com/vektah/gqlparser/v2/validator/rules", "", build.FindOnly)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(pkg.Dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("gqlparser rules not found: %v", err)
	}
	fileSet := token.NewFileSet()
	ruleNames := map[string]string{}
	var registrations []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := goparser.ParseFile(fileSet, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		goast.Inspect(parsed, func(node goast.Node) bool {
			switch value := node.(type) {
			case *goast.ValueSpec:
				for index, name := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[index].(*goast.CompositeLit)
					if !ok {
						continue
					}
					for _, element := range literal.Elts {
						pair, ok := element.(*goast.KeyValueExpr)
						if !ok {
							continue
						}
						if key, ok := pair.Key.(*goast.Ident); ok && key.Name == "Name" {
							if text, ok := pair.Value.(*goast.BasicLit); ok {
								ruleNames[name.Name], _ = strconv.Unquote(text.Value)
							}
						}
					}
				}
			case *goast.CallExpr:
				if function, ok := value.Fun.(*goast.Ident); ok && function.Name == "AddRule" && len(value.Args) == 2 {
					if selector, ok := value.Args[0].(*goast.SelectorExpr); ok {
						if owner, ok := selector.X.(*goast.Ident); ok {
							registrations = append(registrations, owner.Name)
						}
					}
				}
			}
			return true
		})
	}
	names := make([]string, 0, len(registrations))
	for _, owner := range registrations {
		name, ok := ruleNames[owner]
		if !ok {
			t.Fatalf("registered rule %s has no literal name", owner)
		}
		names = append(names, name)
	}
	if len(names) < 20 {
		t.Fatalf("found only %d registered rules: %v", len(names), names)
	}
	sort.Strings(names)
	return names
}

func mustParseDocument(t testing.TB, query string) *ast.QueryDocument {
	t.Helper()
	document, err := parser.ParseQuery(&ast.Source{Name: "errors.graphql", Input: query})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

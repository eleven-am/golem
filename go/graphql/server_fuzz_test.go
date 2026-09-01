package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

type fuzzExecutor struct{}

func (fuzzExecutor) Execute(_ context.Context, _ int, _ Operation) Response {
	return Response{Data: map[string]any{"viewer": 1, "echo": "bounded"}}
}

func FuzzGraphQLDirectParseValidationAndInputLimits(f *testing.F) {
	seeds := []struct {
		query     string
		variables []byte
	}{
		{`{ viewer }`, []byte(`{}`)},
		{`query Read($value: String!) { echo(value: $value) }`, []byte(`{"value":"ok"}`)},
		{`query Read($value: String!) { echo(value: $value) }`, []byte(`{"value":9007199254740993}`)},
		{`query {`, []byte(`{}`)},
		{`{ missing }`, []byte(`{}`)},
		{`query { ...Loop } fragment Loop on Query { ...Loop }`, []byte(`{}`)},
		{`query { a: viewer b: viewer c: viewer d: viewer e: viewer f: viewer g: viewer h: viewer }`, []byte(`{}`)},
		{`query { ...A ...A ...A } fragment A on Query { ...B ...B } fragment B on Query { viewer }`, []byte(`{}`)},
		{`query Inspect($input: JSON) { inspect(input: $input) }`, []byte(`{"input":{"a":{"b":{"c":[1,2,3,4,5]}}}}`)},
		{`query { items(take: 1000000) { value nested { value } } }`, []byte(`{}`)},
		{`query One { viewer } query Two { viewer }`, []byte(`{}`)},
		{strings.Repeat("{", 128), []byte(`[[[[[[[[0]]]]]]]]`)},
		{`query Read($value: String!) { echo(value: $value) }`, []byte(`{"value":"private-driver-token"}`)},
	}
	for _, seed := range seeds {
		f.Add(seed.query, seed.variables)
	}

	f.Fuzz(func(t *testing.T, query string, encodedVariables []byte) {
		if len(query) > 4096 || len(encodedVariables) > 4096 {
			t.Skip()
		}
		server := newFuzzServer(t)
		variables := decodeFuzzVariables(encodedVariables)
		response := server.Execute(context.Background(), 1, Request{Query: query, OperationName: operationNameForFuzz(query), Variables: variables})
		assertBoundedPublicResponse(t, response)
	})
}

func FuzzGraphQLHTTPEnvelopeLimits(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"query":"{ viewer }"}`),
		[]byte(`{"query":"query Read($value: String!) { echo(value: $value) }","operationName":"Read","variables":{"value":"ok"}}`),
		[]byte(`{"query":"query {"}`),
		[]byte(`{"query":"{ viewer }","unknown":"private-driver-token"}`),
		[]byte(`{"query":"query Inspect($input: JSON) { inspect(input: $input) }","variables":{"input":{"a":{"b":{"c":[1,2,3,4,5]}}}}}`),
		[]byte(`{"query":"query { a: viewer b: viewer c: viewer d: viewer e: viewer f: viewer g: viewer h: viewer }"}`),
		[]byte(`{"query":"query One { viewer } query Two { viewer }"}`),
		[]byte(`{"query":"{ viewer }"} {}`),
		bytes.Repeat([]byte{'x'}, 1025),
		append([]byte{'{'}, []byte{0xff, '}'}...),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 4096 {
			t.Skip()
		}
		server := newFuzzServer(t)
		request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Body.Len() > 4096 {
			t.Fatalf("GraphQL HTTP response exceeded fixed bound: %d bytes", response.Body.Len())
		}
		var decoded Response
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("GraphQL HTTP returned invalid JSON: %v; body=%q", err, response.Body.Bytes())
		}
		assertBoundedPublicResponse(t, decoded)
	})
}

func FuzzGraphQLLimitNormalizationBoundaries(f *testing.F) {
	for index := range limitBounds {
		maximum := fuzzLimitMaximum(index)
		for _, value := range []int{-1, 0, 1, maximum - 1, maximum, maximum + 1} {
			f.Add(uint8(index), int64(value))
		}
	}
	f.Fuzz(func(t *testing.T, rawIndex uint8, rawValue int64) {
		index := int(rawIndex) % len(limitBounds)
		if rawValue < -1_000_000_000 || rawValue > 1_000_000_000 {
			t.Skip()
		}
		value := int(rawValue)
		limits := Limits{}
		setFuzzLimit(&limits, index, value)
		_, err := NormalizeLimits(limits)
		maximum := fuzzLimitMaximum(index)
		wantOK := value == 0 || value >= 1 && value <= maximum
		if (err == nil) != wantOK {
			t.Fatalf("limit %d value %d (maximum %d): err=%v wantOK=%v", index, value, maximum, err, wantOK)
		}
	})
}

func FuzzGraphQLErrorPresentationNeverLeaksCause(f *testing.F) {
	for _, seed := range []string{"", "pq: password=secret", "sqlite: users_private", "panic\nprivate stack", strings.Repeat("x", 1024)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			t.Skip()
		}
		cause := "private-cause:" + fmt.Sprintf("%x", []byte(input))
		trusted := errors.New(cause)
		reported := ""
		presented := PresentError(context.Background(), trusted, []any{"viewer", 0}, func(_ context.Context, err error) {
			reported = err.Error()
		})
		assertSanitizedInternalError(t, presented, cause)
		if reported != cause {
			t.Fatalf("trusted reporter received %q, want exact cause %q", reported, cause)
		}

		reported = ""
		failure := golem.RuntimeReadError(golem.CodeForbidden, "findMany", golem.ModelID{1}, golem.FieldID{2}, "access denied", trusted)
		presented = PresentError(context.Background(), failure, []any{"viewer"}, func(_ context.Context, err error) {
			reported = err.Error()
		})
		encoded, err := json.Marshal(presented)
		if err != nil {
			t.Fatal(err)
		}
		if presented.Message != "access denied" || presented.Extensions["code"] != "FORBIDDEN" || reported != "" || strings.Contains(string(encoded), cause) {
			t.Fatalf("trusted Golem failure leaked cause: error=%#v report=%q encoded=%s", presented, reported, encoded)
		}
	})
}

func newFuzzServer(t testing.TB) *Server[int] {
	t.Helper()
	server, err := NewServer(`
		scalar JSON
		type Query {
			viewer: Int!
			echo(value: String!): String!
			inspect(input: JSON): String!
			items(take: Int = 2): [Node!]!
		}
		type Node { value: Int! nested: Node }
	`, Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		Limits: Limits{
			MaxRequestBytes: 1024, MaxVariableBytes: 512, MaxTokens: 128, MaxASTNodes: 128, MaxFragments: 8,
			MaxDepth: 8, MaxSelectedFields: 32, MaxAliases: 16, MaxInputDepth: 8, MaxInputNodes: 128,
			MaxListItems: 32, MaxComplexity: 128, MaxPageSize: 16, MaxResolverConcurrency: 4, MaxComputedBatchSize: 16,
		},
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("bounded GraphQL fuzz surface reported an internal failure: %v", err)
		},
	}, fuzzExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func decodeFuzzVariables(encoded []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var variables map[string]any
	if err := decoder.Decode(&variables); err == nil && variables != nil {
		return variables
	}
	return map[string]any{"value": string(encoded)}
}

func operationNameForFuzz(query string) string {
	if strings.Contains(query, "query Read") {
		return "Read"
	}
	return ""
}

func assertBoundedPublicResponse(t testing.TB, response Response) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("GraphQL response is not serializable: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("GraphQL response exceeded fixed bound: %d bytes", len(encoded))
	}
	allowed := map[string]map[string]bool{
		"BAD_REQUEST": {"invalid GraphQL request": true},
		"QUERY_LIMIT_EXCEEDED": {
			"GraphQL query exceeds a configured limit":   true,
			"GraphQL request exceeds a configured limit": true,
		},
		"INPUT_LIMIT_EXCEEDED": {
			"GraphQL input exceeds a configured limit":    true,
			"GraphQL variables exceed a configured limit": true,
		},
		"GRAPHQL_PARSE_FAILED": {"GraphQL parsing failed": true},
		"GRAPHQL_VALIDATION_FAILED": {
			"GraphQL validation failed":       true,
			"GraphQL operation was not found": true,
		},
		"BAD_USER_INPUT": {
			"GraphQL variables are invalid": true,
			"invalid GraphQL variables":     true,
		},
		"UNAUTHENTICATED":       {"authentication is required": true},
		"INTERNAL_SERVER_ERROR": {"internal server error": true},
	}
	for _, item := range response.Errors {
		code, ok := item.Extensions["code"].(string)
		if !ok {
			t.Fatalf("GraphQL error has no stable string code: %#v", item)
		}
		messages, ok := allowed[code]
		if !ok || !messages[item.Message] {
			t.Fatalf("GraphQL error is not a stable public shape: %#v", item)
		}
		for _, forbidden := range []string{"private-driver-token", "password=", "users_private", "private stack"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("GraphQL response leaked private material %q: %s", forbidden, encoded)
			}
		}
	}
}

func assertSanitizedInternalError(t testing.TB, presented Error, cause string) {
	t.Helper()
	encoded, err := json.Marshal(presented)
	if err != nil {
		t.Fatal(err)
	}
	if presented.Message != "internal server error" || presented.Extensions["code"] != "INTERNAL_SERVER_ERROR" || strings.Contains(string(encoded), cause) {
		t.Fatalf("internal GraphQL error was not sanitized: %#v encoded=%s", presented, encoded)
	}
}

func fuzzLimitMaximum(index int) int {
	maximums := hardLimits
	return *limitBounds[index].field(&maximums)
}

func setFuzzLimit(limits *Limits, index, value int) {
	*limitBounds[index].field(limits) = value
}

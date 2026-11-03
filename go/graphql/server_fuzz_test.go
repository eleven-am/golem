package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		{"{ " + strings.Repeat("missing ", 12) + "}", []byte(`{}`)},
		{`{ privateDriverToken }`, []byte(`{}`)},
		{`{ echo(value: private_driver_token) }`, []byte(`{}`)},
		{`{ echo }`, []byte(`{}`)},
		{`{ __schema { queryType { name } } }`, []byte(`{}`)},
		{`query($unused: Int) { viewer }`, []byte(`{}`)},
		{`query Inspect($input: JSON) { inspect(input: $input) }`, []byte(`{"input":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33]}`)},
		{`query { items(take: 16) { nested { nested { nested { nested { nested { nested { value } } } } } } } }`, []byte(`{}`)},
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
		[]byte(`{"query":""}`),
		[]byte(`{"query":"{ privateDriverToken }"}`),
		[]byte(`{"query":"{ echo(value: private_driver_token) }"}`),
		[]byte(`{"query":"query Inspect($input: JSON) { inspect(input: $input) }","variables":{"input":"` + strings.Repeat("x", 600) + `"}}`),
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

var fuzzValidationRules = map[string]string{
	"GraphQL field does not exist on the selected type":              "FieldsOnCorrectType",
	"GraphQL fragment must be on an object, interface or union type": "FragmentsOnCompositeTypes",
	"GraphQL argument is not defined for this field or directive":    "KnownArgumentNames",
	"GraphQL directive is unknown or not allowed here":               "KnownDirectives",
	"GraphQL fragment is not defined":                                "KnownFragmentNames",
	"GraphQL operation type is not supported by this schema":         "KnownRootType",
	"GraphQL type is unknown":                                        "KnownTypeNames",
	"GraphQL anonymous operation must be the only operation":         "LoneAnonymousOperation",
	"GraphQL introspection selection is nested too deeply":           "MaxIntrospectionDepth",
	"GraphQL fragments must not form a cycle":                        "NoFragmentCycles",
	"GraphQL variable is used but not defined":                       "NoUndefinedVariables",
	"GraphQL fragment is defined but not used":                       "NoUnusedFragments",
	"GraphQL variable is defined but not used":                       "NoUnusedVariables",
	"GraphQL fields conflict and cannot be merged":                   "OverlappingFieldsCanBeMerged",
	"GraphQL fragment cannot be spread in this position":             "PossibleFragmentSpreads",
	"GraphQL required argument is missing":                           "ProvidedRequiredArguments",
	"GraphQL field selection does not match its type":                "ScalarLeafs",
	"GraphQL subscription must select exactly one root field":        "SingleFieldSubscriptions",
	"GraphQL argument is provided more than once":                    "UniqueArgumentNames",
	"GraphQL directive is repeated at one location":                  "UniqueDirectivesPerLocation",
	"GraphQL fragment name is defined more than once":                "UniqueFragmentNames",
	"GraphQL input object field is provided more than once":          "UniqueInputFieldNames",
	"GraphQL operation name is defined more than once":               "UniqueOperationNames",
	"GraphQL variable name is defined more than once":                "UniqueVariableNames",
	"GraphQL value has the wrong type":                               "ValuesOfCorrectType",
	"GraphQL variable must have an input type":                       "VariablesAreInputTypes",
	"GraphQL variable type does not fit its position":                "VariablesInAllowedPosition",
	"GraphQL document is invalid":                                    "",
	"GraphQL operation was not found":                                "",
	"GraphQL introspection is disabled":                              "",
}

var fuzzComplexityMessage = regexp.MustCompile(`^GraphQL query complexity [0-9]+ exceeds MaxComplexity \(128\)$`)
var fuzzOmittedMessage = regexp.MustCompile(`^and [0-9]+ more validation errors?$`)

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
		"BAD_REQUEST": {"invalid GraphQL request": true, "GraphQL request requires a query": true},
		"QUERY_LIMIT_EXCEEDED": {
			"GraphQL request exceeds MaxRequestBytes (1024)": true,
			"GraphQL query exceeds MaxTokens (128)":          true,
			"GraphQL query exceeds MaxASTNodes (128)":        true,
			"GraphQL query exceeds MaxFragments (8)":         true,
			"GraphQL query exceeds MaxDepth (8)":             true,
			"GraphQL query exceeds MaxSelectedFields (32)":   true,
			"GraphQL query exceeds MaxAliases (16)":          true,
			"GraphQL query exceeds MaxSchemaRoots (1)":       true,
			"GraphQL query exceeds MaxTypeRoots (8)":         true,
		},
		"INPUT_LIMIT_EXCEEDED": {
			"GraphQL variables exceed MaxVariableBytes (512)": true,
			"GraphQL input exceeds MaxInputNodes (128)":       true,
			"GraphQL input exceeds MaxInputDepth (8)":         true,
			"GraphQL input exceeds MaxListItems (32)":         true,
		},
		"GRAPHQL_PARSE_FAILED": {"GraphQL parsing failed": true},
		"BAD_USER_INPUT": {
			"GraphQL variables are invalid": true,
			"invalid GraphQL variables":     true,
		},
		"UNAUTHENTICATED":       {"authentication is required": true},
		"INTERNAL_SERVER_ERROR": {"internal server error": true},
	}
	extensionKinds := map[string]string{"code": "string", "rule": "string", "limit": "string", "max": "number", "cost": "number"}
	for _, item := range response.Errors {
		code, ok := item.Extensions["code"].(string)
		if !ok {
			t.Fatalf("GraphQL error has no stable string code: %#v", item)
		}
		switch {
		case code == "GRAPHQL_VALIDATION_FAILED":
			rule, known := fuzzValidationRules[item.Message]
			if !known && !fuzzOmittedMessage.MatchString(item.Message) {
				t.Fatalf("GraphQL validation error is not a stable public shape: %#v", item)
			}
			if got, _ := item.Extensions["rule"].(string); got != rule {
				t.Fatalf("GraphQL validation error rule = %q, want %q: %#v", got, rule, item)
			}
		case code == "QUERY_LIMIT_EXCEEDED" && fuzzComplexityMessage.MatchString(item.Message):
			if item.Extensions["limit"] != "MaxComplexity" || fmt.Sprint(item.Extensions["max"]) != "128" {
				t.Fatalf("GraphQL complexity error has the wrong limit extensions: %#v", item)
			}
		default:
			messages, ok := allowed[code]
			if !ok || !messages[item.Message] {
				t.Fatalf("GraphQL error is not a stable public shape: %#v", item)
			}
		}
		for key, value := range item.Extensions {
			switch extensionKinds[key] {
			case "string":
				if _, ok := value.(string); !ok {
					t.Fatalf("GraphQL error extension %q is not a string: %#v", key, item)
				}
			case "number":
				switch value.(type) {
				case int, float64:
				default:
					t.Fatalf("GraphQL error extension %q is not a number: %#v", key, item)
				}
			default:
				t.Fatalf("GraphQL error carries an extension outside the public set: %q in %#v", key, item)
			}
		}
		if len(item.Locations) > maxErrorLocations {
			t.Fatalf("GraphQL error carries %d locations: %#v", len(item.Locations), item)
		}
		for _, location := range item.Locations {
			if location.Line < 1 || location.Column < 1 {
				t.Fatalf("GraphQL error location is not a position: %#v", item)
			}
		}
		for _, forbidden := range []string{"private-driver-token", "private_driver_token", "privateDriverToken", "password=", "users_private", "private stack"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("GraphQL response leaked private material %q: %s", forbidden, encoded)
			}
		}
	}
	if len(response.Errors) > maxValidationErrors+1 {
		t.Fatalf("GraphQL response carries %d errors", len(response.Errors))
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

func (fuzzExecutor) ListBounds() ListBounds { return NewListBounds(nil) }

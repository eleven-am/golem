package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/eleven-am/golem/go/graphql"
	p7gqlgen "github.com/eleven-am/golem/go/graphql/testdata/p7subscription/golemgqlgen"
	"github.com/vektah/gqlparser/v2/ast"
)

type introspectionExecutor struct{}

func (introspectionExecutor) Execute(_ context.Context, _ int, operation graphql.Operation) graphql.Response {
	data := map[string]any{}
	for _, selection := range operation.Definition.SelectionSet {
		if field, ok := selection.(*ast.Field); ok && field.Name == "viewer" {
			name := field.Alias
			if name == "" {
				name = field.Name
			}
			data[name] = 7
		}
	}
	return graphql.Response{Data: data}
}

const introspectionLimitSchema = `type Node { id: Int! child: Node children(take: Int): [Node!]! }
type Query { node: Node nodes(take: Int): [Node!]! }
`

func introspectionServer(t *testing.T, sdl string, introspection bool, limits graphql.Limits, executable gqlgengraphql.ExecutableSchema) *graphql.Server[int] {
	t.Helper()
	server, err := graphql.NewServer(sdl, graphql.Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
		Introspection:        introspection,
		Limits:               limits,
		ExecutableSchema:     executable,
	}, introspectionExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func p7IntrospectionServer(t *testing.T, introspection bool) *graphql.Server[int] {
	t.Helper()
	return introspectionServer(t, p7gqlgenSchema, introspection, graphql.Limits{}, p7gqlgen.NewExecutableSchema(p7gqlgen.Config{Resolvers: &p7gqlgen.Resolver{}}))
}

func introspectionData(t *testing.T, response graphql.Response) map[string]any {
	t.Helper()
	if len(response.Errors) != 0 {
		t.Fatalf("errors=%#v data=%#v", response.Errors, response.Data)
	}
	encoded, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func introspectionCode(response graphql.Response) string {
	if len(response.Errors) == 0 {
		return ""
	}
	code, _ := response.Errors[0].Extensions["code"].(string)
	return code
}

const missingExecutableRefusal = "__schema, __type and a root __typename need an executable schema, which this GraphQL server does not have"

func introspectionLimitOutcome(response graphql.Response) string {
	switch {
	case len(response.Errors) == 0:
		return "within limits"
	case len(response.Errors) == 1 && introspectionCode(response) == "QUERY_LIMIT_EXCEEDED":
		return "limited"
	case len(response.Errors) == 1 && introspectionCode(response) == "GRAPHQL_VALIDATION_FAILED" && response.Errors[0].Message == missingExecutableRefusal:
		return "within limits"
	default:
		return fmt.Sprintf("unexpected %#v", response.Errors)
	}
}

func graphqlJSIntrospectionQueries(t *testing.T) map[string]string {
	t.Helper()
	queries := map[string]string{}
	for _, name := range []string{"graphql-js-16.14.2-default.graphql", "graphql-js-16.14.2-all-options.graphql"} {
		text, err := os.ReadFile(filepath.Join("testdata", "introspection", name))
		if err != nil {
			t.Fatal(err)
		}
		queries[name] = string(text)
	}
	return queries
}

func TestIntrospectionEnabledAnswersMetaRootsThroughTheExecutable(t *testing.T) {
	server := p7IntrospectionServer(t, true)
	schema := introspectionData(t, server.Execute(context.Background(), 1, graphql.Request{Query: `{ __schema { queryType { name } subscriptionType { name } } }`}))
	if got, _ := json.Marshal(schema); string(got) != `{"__schema":{"queryType":{"name":"Query"},"subscriptionType":{"name":"Subscription"}}}` {
		t.Fatalf("__schema=%s", got)
	}
	thing := introspectionData(t, server.Execute(context.Background(), 1, graphql.Request{Query: `{ __type(name: "Thing") { name fields { name } } }`}))
	if got, _ := json.Marshal(thing); string(got) != `{"__type":{"fields":[{"name":"value"}],"name":"Thing"}}` {
		t.Fatalf("__type=%s", got)
	}
	mixed := introspectionData(t, server.Execute(context.Background(), 1, graphql.Request{Query: `{ kind: __typename viewer __schema { queryType { name } } }`}))
	if got, _ := json.Marshal(mixed); string(got) != `{"__schema":{"queryType":{"name":"Query"}},"kind":"Query","viewer":7}` {
		t.Fatalf("mixed=%s", got)
	}
}

func TestIntrospectionDisabledRefusesSchemaAndTypeButAnswersTypename(t *testing.T) {
	server := p7IntrospectionServer(t, false)
	for _, query := range []string{
		`{ __schema { queryType { name } } }`,
		`{ __type(name: "Thing") { name } }`,
		`{ viewer ...Meta } fragment Meta on Query { __schema { queryType { name } } }`,
		`{ viewer ... on Query { __type(name: "Thing") { name } } }`,
	} {
		if code := introspectionCode(server.Execute(context.Background(), 1, graphql.Request{Query: query})); code != "GRAPHQL_VALIDATION_FAILED" {
			t.Fatalf("%s code=%q", query, code)
		}
	}
	data := introspectionData(t, server.Execute(context.Background(), 1, graphql.Request{Query: `{ __typename viewer }`}))
	if got, _ := json.Marshal(data); string(got) != `{"__typename":"Query","viewer":7}` {
		t.Fatalf("__typename=%s", got)
	}
}

func TestGraphQLJSIntrospectionQueryPassesAtDefaultLimits(t *testing.T) {
	server := p7IntrospectionServer(t, true)
	for name, query := range graphqlJSIntrospectionQueries(t) {
		data := introspectionData(t, server.Execute(context.Background(), 1, graphql.Request{Query: query, OperationName: "IntrospectionQuery"}))
		schema, _ := data["__schema"].(map[string]any)
		queryType, _ := schema["queryType"].(map[string]any)
		types, _ := schema["types"].([]any)
		if queryType["name"] != "Query" || len(types) == 0 {
			t.Fatalf("%s returned %v", name, data)
		}
	}
	limited := introspectionServer(t, introspectionLimitSchema, true, graphql.Limits{}, nil)
	for name, query := range graphqlJSIntrospectionQueries(t) {
		if outcome := introspectionLimitOutcome(limited.Execute(context.Background(), 1, graphql.Request{Query: query, OperationName: "IntrospectionQuery"})); outcome != "within limits" {
			t.Fatalf("%s against the list schema: %s", name, outcome)
		}
	}
}

func TestIntrospectionIsChargedOnePerFieldAndNeverAsAPaginatedRead(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		query         string
		maxComplexity int
		refused       bool
	}{
		{"introspection lists at their exact cost", `{ __schema { types { name } } }`, 3, false},
		{"introspection lists one over their exact cost", `{ __schema { types { name } } }`, 2, true},
		{"nested introspection lists", `{ __schema { types { name fields { name args { name } } } } }`, 7, false},
		{"nested introspection lists one over", `{ __schema { types { name fields { name args { name } } } } }`, 6, true},
		{"introspection lists at default complexity", `{ __schema { types { name fields { name args { name type { name } } } inputFields { name } enumValues { name } interfaces { name } possibleTypes { name } } directives { name locations args { name } } } }`, 0, false},
		{"data at its exact cost", `{ nodes(take: 3) { id } }`, 4, false},
		{"data one over its exact cost", `{ nodes(take: 3) { id } }`, 3, true},
		{"data beside introspection at the exact sum", `{ __schema { queryType { name } } nodes(take: 3) { id } }`, 7, false},
		{"data beside introspection one over the exact sum", `{ __schema { queryType { name } } nodes(take: 3) { id } }`, 6, true},
		{"data typename beside a data root", `{ __typename nodes(take: 3) { id } }`, 4, true},
		{"unbounded data lists are still charged the page size", `{ nodes { children { id } } }`, 0, true},
		{"unbounded data lists beside introspection", `{ __schema { types { name } } nodes { children { id } } }`, 0, true},
		{"unbounded data lists hidden in a fragment beside introspection", `{ ...Hide } fragment Hide on Query { __schema { types { name } } nodes { children { id } } }`, 0, true},
		{"unbounded data lists in an inline fragment beside introspection", `{ __type(name: "Node") { fields { name } } ... on Query { nodes { children { id } } } }`, 0, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := introspectionServer(t, introspectionLimitSchema, true, graphql.Limits{MaxComplexity: testCase.maxComplexity}, nil)
			outcome := introspectionLimitOutcome(server.Execute(context.Background(), 1, graphql.Request{Query: testCase.query}))
			if testCase.refused && outcome != "limited" || !testCase.refused && outcome != "within limits" {
				t.Fatalf("outcome=%q refused=%t", outcome, testCase.refused)
			}
		})
	}
}

func introspectionChain(children int) string {
	return "node {" + strings.Repeat(" child {", children) + " id" + strings.Repeat(" }", children) + " }"
}

func introspectionTypeRef(levels int) string {
	return strings.Repeat(" ofType {", levels) + " name" + strings.Repeat(" }", levels)
}

func TestIntrospectionSelectionsDoNotCountTowardMaxDepthButDataStillDoes(t *testing.T) {
	deepSchema := `__schema { types { fields { args { type {` + introspectionTypeRef(12) + ` } } } } }`
	deepType := `__type(name: "Node") {` + introspectionTypeRef(20) + ` }`
	for _, testCase := range []struct {
		name    string
		query   string
		refused bool
	}{
		{"data at the default depth", `{ ` + introspectionChain(9) + ` }`, false},
		{"data one past the default depth", `{ ` + introspectionChain(10) + ` }`, true},
		{"deep schema introspection", `{ ` + deepSchema + ` }`, false},
		{"deep type introspection", `{ ` + deepType + ` }`, false},
		{"deep introspection beside data at the default depth", `{ ` + deepSchema + ` ` + introspectionChain(9) + ` }`, false},
		{"deep introspection beside data one past the default depth", `{ ` + deepSchema + ` ` + introspectionChain(10) + ` }`, true},
		{"deep introspection after data one past the default depth", `{ ` + introspectionChain(10) + ` ` + deepType + ` }`, true},
		{"deep data hidden in a fragment beside introspection", `{ ...Hide } fragment Hide on Query { ` + deepType + ` ` + introspectionChain(10) + ` }`, true},
		{"deep data hidden in an inline fragment beside introspection", `{ ` + deepSchema + ` ... on Query { ` + introspectionChain(10) + ` } }`, true},
		{"deep data under a typename root", `{ __typename ` + introspectionChain(10) + ` }`, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := introspectionServer(t, introspectionLimitSchema, true, graphql.Limits{MaxComplexity: 100_000}, nil)
			outcome := introspectionLimitOutcome(server.Execute(context.Background(), 1, graphql.Request{Query: testCase.query}))
			if testCase.refused && outcome != "limited" || !testCase.refused && outcome != "within limits" {
				t.Fatalf("outcome=%q refused=%t", outcome, testCase.refused)
			}
		})
	}
}

func TestRecursiveIntrospectionIsRefusedBeforeExecution(t *testing.T) {
	server := p7IntrospectionServer(t, true)
	for _, query := range []string{
		`{ __schema { types { fields { type { fields { type { fields { name } } } } } } } }`,
		`{ __type(name: "Query") { fields { type { interfaces { fields { type { possibleTypes { name } } } } } } } }`,
	} {
		if code := introspectionCode(server.Execute(context.Background(), 1, graphql.Request{Query: query})); code != "GRAPHQL_VALIDATION_FAILED" {
			t.Fatalf("%s code=%q", query, code)
		}
	}
}

func introspectionAliases(count int, field string) string {
	var builder strings.Builder
	for index := 0; index < count; index++ {
		builder.WriteString(" a")
		builder.WriteString(strings.Repeat("x", index+1))
		builder.WriteString(": ")
		builder.WriteString(field)
	}
	return builder.String()
}

func TestIntrospectionRootsAreBoundedPerOperation(t *testing.T) {
	server := p7IntrospectionServer(t, true)
	schema := `__schema { types { name fields { name } } }`
	thing := `__type(name: "Thing") { name fields { name } }`
	for _, testCase := range []struct {
		name    string
		query   string
		refused bool
	}{
		{"one schema root", `{ ` + schema + ` }`, false},
		{"one schema root merged from two selections", `{ __schema { queryType { name } } __schema { types { name } } }`, false},
		{"one schema root merged across a fragment", `{ s: __schema { queryType { name } } ...More } fragment More on Query { s: __schema { types { name } } }`, false},
		{"two aliased schema roots", `{ one: __schema { queryType { name } } two: __schema { queryType { name } } }`, true},
		{"a hundred aliased schema roots", `{` + introspectionAliases(100, schema) + ` }`, true},
		{"a second schema root hidden in a fragment", `{ ` + schema + ` ...More } fragment More on Query { again: __schema { queryType { name } } }`, true},
		{"a second schema root hidden in an inline fragment", `{ ` + schema + ` ... on Query { again: __schema { queryType { name } } } }`, true},
		{"a skipped second schema root", `{ ` + schema + ` again: __schema @skip(if: true) { queryType { name } } }`, false},
		{"eight type roots", `{` + introspectionAliases(8, thing) + ` }`, false},
		{"nine type roots", `{` + introspectionAliases(9, thing) + ` }`, true},
		{"one schema root beside eight type roots", `{ ` + schema + introspectionAliases(8, thing) + ` }`, false},
		{"a hundred type roots", `{` + introspectionAliases(100, thing) + ` }`, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := server.Execute(context.Background(), 1, graphql.Request{Query: testCase.query})
			code := introspectionCode(response)
			if testCase.refused && code != "QUERY_LIMIT_EXCEEDED" || !testCase.refused && code != "" {
				t.Fatalf("code=%q refused=%t errors=%#v", code, testCase.refused, response.Errors)
			}
		})
	}
}

func TestServerWithoutExecutableRefusesMetaRootsInsteadOfOmittingThem(t *testing.T) {
	server := introspectionServer(t, introspectionLimitSchema, true, graphql.Limits{}, nil)
	for _, query := range []string{
		`{ __typename }`,
		`{ __typename node { id } }`,
		`{ __schema { queryType { name } } }`,
		`{ __type(name: "Node") { name } node { id } }`,
		`{ node { id } ...Meta } fragment Meta on Query { kind: __typename }`,
		`{ node { id } ... on Query { __typename } }`,
	} {
		response := server.Execute(context.Background(), 1, graphql.Request{Query: query})
		if introspectionCode(response) != "GRAPHQL_VALIDATION_FAILED" || response.Data != nil || response.Errors[0].Message != missingExecutableRefusal {
			t.Fatalf("%s response=%#v", query, response)
		}
	}
	for _, query := range []string{`{ node { __typename id } }`, `{ __typename @skip(if: true) node { id } }`} {
		if response := server.Execute(context.Background(), 1, graphql.Request{Query: query}); len(response.Errors) != 0 {
			t.Fatalf("%s errors=%#v", query, response.Errors)
		}
	}
}

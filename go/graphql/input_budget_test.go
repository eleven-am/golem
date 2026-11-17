package graphql

import "testing"

func TestResolvedInputBudgetsCoverLiteralsDefaultsAndRepeatedVariables(t *testing.T) {
	const schema = `scalar JSON
type Query { echo(input: JSON): String! }`
	for _, test := range []struct {
		name    string
		request Request
		allowed Limits
		refused Limits
	}{
		{"literal depth", Request{Query: `{echo(input:{nested:{value:"x"}})}`}, Limits{MaxInputDepth: 4}, Limits{MaxInputDepth: 3}},
		{"literal nodes", Request{Query: `{echo(input:{left:1,right:2})}`}, Limits{MaxInputNodes: 4}, Limits{MaxInputNodes: 3}},
		{"repeated variable", Request{Query: `query($value:JSON){first:echo(input:$value) second:echo(input:$value)}`, Variables: map[string]any{"value": map[string]any{"left": 1, "right": 2}}}, Limits{MaxInputNodes: 7}, Limits{MaxInputNodes: 6}},
		{"variable default", Request{Query: `query($value:JSON={left:1,right:2}){echo(input:$value)}`}, Limits{MaxInputNodes: 4}, Limits{MaxInputNodes: 3}},
		{"literal list", Request{Query: `{echo(input:[1,2,3])}`}, Limits{MaxListItems: 3}, Limits{MaxListItems: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertDirectLimitBoundary(t, schema, test.allowed, test.request, true)
			assertDirectLimitBoundary(t, schema, test.refused, test.request, false, "INPUT_LIMIT_EXCEEDED")
		})
	}
}

func TestResolvedInputBudgetsCountSchemaArgumentAndMemberDefaults(t *testing.T) {
	const schema = `scalar JSON
input Payload { document: JSON = {left: 1, right: 2} }
type Query { echo(input: JSON = {left: 1, right: 2}): String! payload(input: Payload): String! }`
	for _, query := range []string{`{echo}`, `query($value:JSON){echo(input:$value)}`} {
		request := Request{Query: query}
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 4}, request, true)
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 3}, request, false, "INPUT_LIMIT_EXCEEDED")
	}
	request := Request{Query: `{payload(input:{})}`}
	assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 5}, request, true)
	assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 4}, request, false, "INPUT_LIMIT_EXCEEDED")
}

func TestResolvedInputBudgetsCountImplicitListContainers(t *testing.T) {
	const schema = `type Query { echo(input: [[String]]): String! }`
	for _, request := range []Request{
		{Query: `{echo(input:"x")}`},
		{Query: `{echo(input:["x"])}`},
		{Query: `query($input:[[String]]){echo(input:$input)}`, Variables: map[string]any{"input": "x"}},
	} {
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 4, MaxInputDepth: 4}, request, true)
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 3}, request, false, "INPUT_LIMIT_EXCEEDED")
		assertDirectLimitBoundary(t, schema, Limits{MaxInputDepth: 3}, request, false, "INPUT_LIMIT_EXCEEDED")
	}
	assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 2, MaxInputDepth: 2}, Request{Query: `{echo(input:null)}`}, true)
}

func TestResolvedInputBudgetsCountNestedSchemaDefaults(t *testing.T) {
	const schema = `input Payload { values: [[String]] = "x" }
type Query { echo(input: Payload = {}): String! }`
	for _, request := range []Request{
		{Query: `{echo}`},
		{Query: `{echo(input:{})}`},
		{Query: `query($input:Payload){echo(input:$input)}`, Variables: map[string]any{"input": map[string]any{}}},
	} {
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 5, MaxInputDepth: 5}, request, true)
		assertDirectLimitBoundary(t, schema, Limits{MaxInputNodes: 4}, request, false, "INPUT_LIMIT_EXCEEDED")
		assertDirectLimitBoundary(t, schema, Limits{MaxInputDepth: 4}, request, false, "INPUT_LIMIT_EXCEEDED")
	}
}

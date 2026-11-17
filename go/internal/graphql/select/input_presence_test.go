package selectset

import (
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"testing"
)

func TestComputedInputPresenceSeparatesOmittedVariablesAndNullCacheKeys(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { check(note: String): String }`})
	if err != nil {
		t.Fatal(err)
	}
	field := compilerir.ComputedFieldContractIR{Name: "check", Arguments: []compilerir.GraphQLArgumentContractIR{{Name: "note", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}, PresenceAware: true}}}
	keys := map[string]string{}
	for _, test := range []struct {
		name, query string
		variables   map[string]any
		set, null   bool
		text        string
	}{
		{"omitted", `{ check }`, nil, false, false, ""},
		{"undefined variable", `query($note: String) { check(note: $note) }`, map[string]any{}, false, false, ""},
		{"null", `{ check(note: null) }`, nil, true, true, ""},
		{"empty", `{ check(note: "") }`, nil, true, false, ""},
		{"default", `query($note: String = "default") { check(note: $note) }`, map[string]any{}, true, false, "default"},
		{"null overrides default", `query($note: String = "default") { check(note: $note) }`, map[string]any{"note": nil}, true, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, failures := gqlparser.LoadQuery(schema, test.query)
			if len(failures) != 0 {
				t.Fatal(failures)
			}
			arguments, key, err := bindComputedArguments(compilerir.CompilationIR{}, field, query.Operations[0].SelectionSet[0].(*ast.Field).Arguments, test.variables, 10)
			if err != nil {
				t.Fatal(err)
			}
			var value golem.InputValue[string]
			recognized, err := golem.RuntimeDecodeInputValue(&value, arguments[0].Value, func(raw, into any) error { *into.(*string) = raw.(string); return nil })
			if !recognized || err != nil {
				t.Fatal(err)
			}
			text, present := value.Get()
			if value.IsSet() != test.set || value.IsNull() != test.null || present != (test.set && !test.null) || text != test.text {
				t.Fatalf("presence=%#v", value)
			}
			keys[test.name] = key
		})
	}
	if keys["omitted"] != keys["undefined variable"] || keys["omitted"] == keys["null"] || keys["null"] != keys["null overrides default"] || keys["null"] == keys["empty"] {
		t.Fatalf("presence cache keys=%v", keys)
	}
}

func TestComputedInputObjectOmitsUndefinedVariableMembersButKeepsListPositions(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: `input Payload { note: String, items: [String] } type Query { check(input: Payload!): String }`})
	if err != nil {
		t.Fatal(err)
	}
	query, failures := gqlparser.LoadQuery(schema, `query($note: String) { check(input: {note: $note, items: [$note]}) }`)
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	str := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "String", Nullable: true}
	payload := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Payload"}
	compilation := compilerir.CompilationIR{Contract: compilerir.ContractIR{GraphQLTypes: []compilerir.GraphQLTypeDefinitionIR{{Name: "Payload", Kind: payload.Kind, Fields: []compilerir.GraphQLObjectFieldIR{{Name: "note", Type: str, PresenceAware: true}, {Name: "items", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &str, Nullable: true}}}}}}}
	field := compilerir.ComputedFieldContractIR{Name: "check", Arguments: []compilerir.GraphQLArgumentContractIR{{Name: "input", Type: payload}}}
	arguments, _, err := bindComputedArguments(compilation, field, query.Operations[0].SelectionSet[0].(*ast.Field).Arguments, map[string]any{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	value := arguments[0].Value.(map[string]any)
	if _, present := value["note"]; present {
		t.Fatalf("undefined variable became explicit null: %#v", value)
	}
	if items := value["items"].([]any); len(items) != 1 || items[0] != nil {
		t.Fatalf("undefined list position was removed: %#v", value)
	}
}

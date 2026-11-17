package schema

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func homeFeedObjectCompilation() ir.CompilationIR {
	return ir.CompilationIR{Contract: ir.ContractIR{
		FormatVersion: ir.ContractFormatVersion, GraphQLABIVersion: 1,
		GraphQLTypes: []ir.GraphQLTypeDefinitionIR{
			{Kind: ir.GraphQLTypeObject, Name: "HomeFeedPage", Go: ir.GoNamedTypeIR{PackagePath: "example.test/feed", Name: "HomeFeedPage"}, Fields: []ir.GraphQLObjectFieldIR{
				{Name: "results", GoName: "Results", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, MaxItems: 25, Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: "HomeSegment"}}},
			}},
			{Kind: ir.GraphQLTypeObject, Name: "HomeSegment", Go: ir.GoNamedTypeIR{PackagePath: "example.test/feed", Name: "HomeSegment"}, Fields: []ir.GraphQLObjectFieldIR{
				{Name: "kind", GoName: "Kind", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}},
				{Name: "index", GoName: "Index", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int"}},
			}},
			{Kind: ir.GraphQLTypeInputObject, Name: "HomeInput", Go: ir.GoNamedTypeIR{PackagePath: "example.test/feed", Name: "HomeInput"}, Fields: []ir.GraphQLObjectFieldIR{
				{Name: "range", GoName: "Range", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeInputObject, Name: "PageRange", Nullable: true}},
			}},
			{Kind: ir.GraphQLTypeInputObject, Name: "PageRange", Go: ir.GoNamedTypeIR{PackagePath: "example.test/feed", Name: "PageRange"}, Fields: []ir.GraphQLObjectFieldIR{
				{Name: "seed", GoName: "Seed", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int"}},
			}},
		},
		CustomOperations: []ir.CustomOperationContractIR{{Operation: ir.CustomOperationQuery, Name: "homeFeed", Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: "HomeFeedPage"}, Arguments: []ir.GraphQLArgumentContractIR{{Name: "input", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeInputObject, Name: "HomeInput"}}}}},
	}}
}

func TestNamedGraphQLObjectsAndNestedInputsLowerToDeterministicSDL(t *testing.T) {
	compilation := homeFeedObjectCompilation()
	first, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	definitions := compilation.Contract.GraphQLTypes
	for left, right := 0, len(definitions)-1; left < right; left, right = left+1, right-1 {
		definitions[left], definitions[right] = definitions[right], definitions[left]
	}
	second, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	if first.SDL != second.SDL {
		t.Fatal("registration order changed SDL")
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "home-feed", Input: first.SDL})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]struct {
		kind   ast.DefinitionKind
		fields map[string]string
	}{
		"HomeFeedPage": {ast.Object, map[string]string{"results": "[HomeSegment!]!"}},
		"HomeSegment":  {ast.Object, map[string]string{"kind": "String!", "index": "Int!"}},
		"HomeInput":    {ast.InputObject, map[string]string{"range": "PageRange"}},
		"PageRange":    {ast.InputObject, map[string]string{"seed": "Int!"}},
	}
	for name, check := range checks {
		definition := parsed.Types[name]
		if definition == nil || definition.Kind != check.kind {
			t.Fatalf("type %s = %#v", name, definition)
		}
		for name, want := range check.fields {
			field := definition.Fields.ForName(name)
			if field == nil || field.Type.String() != want {
				t.Fatalf("field %s = %#v, want %s", name, field, want)
			}
		}
	}
	query := parsed.Query.Fields.ForName("homeFeed")
	if query == nil || query.Type.String() != "HomeFeedPage!" || query.Arguments.ForName("input").Type.String() != "HomeInput!" {
		t.Fatalf("query = %#v", query)
	}
}

func TestNamedGraphQLDefinitionErrorsFailBeforeSchemaEmission(t *testing.T) {
	compilation := homeFeedObjectCompilation()
	compilation.Contract.GraphQLTypes[0].Fields[0].Type.Element.Name = "Unknown"
	document, err := Build(compilation)
	if err == nil || !strings.Contains(err.Error(), "unknown") || document.SDL != "" {
		t.Fatalf("SDL=%q error=%v", document.SDL, err)
	}
}

package schema

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"testing"
)

func TestPageQueryLowersToBoundedSyntheticObjectAndReservedArguments(t *testing.T) {
	compilation := homeFeedObjectCompilation()
	compilation.Contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: 1}
	page := &ir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "home-v1", Item: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: "HomeSegment"}}
	compilation.Contract.GraphQLTypes[0] = ir.GraphQLPageDefinition("HomeFeedPage", *page)
	operation := &compilation.Contract.CustomOperations[0]
	operation.Pagination = page
	operation.Arguments = append(operation.Arguments,
		ir.GraphQLArgumentContractIR{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
		ir.GraphQLArgumentContractIR{Name: "after", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: true}},
	)
	document, err := Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	root := schema.Query.Fields.ForName("homeFeed")
	if root.Type.String() != "HomeFeedPage!" || root.Arguments.ForName("take").DefaultValue.Raw != "25" || root.Arguments.ForName("after").Type.String() != "String" {
		t.Fatalf("root=%#v", root)
	}
	definition := schema.Types["HomeFeedPage"]
	if len(definition.Fields) != 2 || definition.Fields.ForName("items").Type.String() != "[HomeSegment!]!" || definition.Fields.ForName("next").Type.String() != "String" {
		t.Fatalf("page=%#v", definition)
	}
}

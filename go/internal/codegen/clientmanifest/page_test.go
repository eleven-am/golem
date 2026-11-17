package clientmanifest

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"testing"
)

func TestPageManifestRequiresVersionThreeAndNamesProtocolFields(t *testing.T) {
	page := &ir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "v1", Item: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}
	compilation := ir.CompilationIR{Contract: ir.ContractIR{GraphQLABIVersion: 1,
		DataIncarnation: &ir.DataIncarnationContractIR{Kind: "provider", Version: 1},
		GraphQLTypes:    []ir.GraphQLTypeDefinitionIR{ir.GraphQLPageDefinition("FeedPage", *page)},
		CustomOperations: []ir.CustomOperationContractIR{{Name: "feed", Operation: ir.CustomOperationQuery, Pagination: page, Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: "FeedPage"}, Arguments: []ir.GraphQLArgumentContractIR{
			{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "after", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: true}},
		}}},
	}}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(compilation, ir.Fingerprint("contract"), document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	root := manifest.Roots["feed"]
	want := Pagination{Kind: "continuation", TakeArgument: "take", AfterArgument: "after", ItemsField: "items", NextField: "next"}
	if manifest.FormatVersion != 3 || len(manifest.RequiredCapabilities) != 1 || manifest.RequiredCapabilities[0] != "data-incarnation-v1" || root.Pagination == nil || *root.Pagination != want || root.Shape != ShapeObject || root.Result == nil || root.Result.Name != "FeedPage" {
		t.Fatalf("manifest=%#v root=%#v", manifest, root)
	}
	if len(root.Models) != 0 || manifest.Types["FeedPage"].Fields["items"].MaxItems != 100 {
		t.Fatalf("page metadata=%#v", manifest)
	}
}

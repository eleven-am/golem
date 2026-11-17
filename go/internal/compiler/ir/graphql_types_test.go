package ir

import (
	"bytes"
	"strings"
	"testing"
)

func objectContractFixture() ContractIR {
	return ContractIR{
		FormatVersion: ContractFormatVersion,
		GraphQLTypes: []GraphQLTypeDefinitionIR{
			{Kind: GraphQLTypeObject, Name: "HomeFeedPage", Go: GoNamedTypeIR{PackagePath: "example.test/feed", Name: "HomeFeedPage"}, Fields: []GraphQLObjectFieldIR{
				{Name: "results", GoName: "Results", Type: GraphQLTypeIR{Kind: GraphQLTypeList, MaxItems: 25, Element: &GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "HomeSegment"}}},
				{Name: "seed", GoName: "Seed", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int"}},
			}},
			{Kind: GraphQLTypeObject, Name: "HomeSegment", Go: GoNamedTypeIR{PackagePath: "example.test/feed", Name: "HomeSegment"}, Fields: []GraphQLObjectFieldIR{
				{Name: "kind", GoName: "Kind", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String"}},
				{Name: "index", GoName: "Index", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int"}},
			}},
			{Kind: GraphQLTypeInputObject, Name: "FeedInput", Go: GoNamedTypeIR{PackagePath: "example.test/feed", Name: "FeedInput"}, Fields: []GraphQLObjectFieldIR{
				{Name: "seed", GoName: "Seed", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int"}},
			}},
		},
		CustomOperations: []CustomOperationContractIR{{Name: "homeFeed", Result: GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "HomeFeedPage"}, Arguments: []GraphQLArgumentContractIR{{Name: "input", Type: GraphQLTypeIR{Kind: GraphQLTypeInputObject, Name: "FeedInput"}}}}},
	}
}

func TestCanonicalGraphQLDefinitionsAreOrderedAndDoNotMutateInput(t *testing.T) {
	left, right := objectContractFixture(), objectContractFixture()
	right.GraphQLTypes[0], right.GraphQLTypes[2] = right.GraphQLTypes[2], right.GraphQLTypes[0]
	right.GraphQLTypes[2].Fields[0], right.GraphQLTypes[2].Fields[1] = right.GraphQLTypes[2].Fields[1], right.GraphQLTypes[2].Fields[0]
	first, err := CanonicalContract(left)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalContract(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("definition order changed contract:\n%s\n%s", first, second)
	}
	if left.GraphQLTypes[0].Name != "HomeFeedPage" || left.GraphQLTypes[1].Fields[0].Name != "kind" {
		t.Fatal("canonicalization mutated source definitions")
	}
	decoded, err := decodeCurrentContractCanonicalFraming(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.GraphQLTypes) != 3 || decoded.GraphQLTypes[0].Name != "FeedInput" {
		t.Fatalf("named types did not survive contract framing: %#v", decoded.GraphQLTypes)
	}
	for _, mutate := range []func(*ContractIR){
		func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.MaxItems++ },
		func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].GoName = "OtherResults" },
		func(c *ContractIR) { c.GraphQLTypes[1].Fields[0].Type.Nullable = true },
	} {
		changed := objectContractFixture()
		mutate(&changed)
		encoded, err := CanonicalContract(changed)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(first, encoded) {
			t.Fatal("changed field contract did not change canonical bytes")
		}
	}
}

func TestGraphQLDefinitionGraphRejectsInvalidContracts(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*ContractIR)
	}{
		{"duplicate type", "duplicate", func(c *ContractIR) { c.GraphQLTypes = append(c.GraphQLTypes, c.GraphQLTypes[0]) }},
		{"missing Go identity", "Go", func(c *ContractIR) { c.GraphQLTypes[0].Go = GoNamedTypeIR{} }},
		{"duplicate field", "duplicate", func(c *ContractIR) {
			c.GraphQLTypes[0].Fields = append(c.GraphQLTypes[0].Fields, c.GraphQLTypes[0].Fields[0])
		}},
		{"duplicate Go field", "Go field", func(c *ContractIR) { c.GraphQLTypes[0].Fields[1].GoName = "Results" }},
		{"reserved name", "name", func(c *ContractIR) { c.GraphQLTypes[0].Name = "__Private" }},
		{"scalar collision", "reserved", func(c *ContractIR) { c.GraphQLTypes[0].Name = "String" }},
		{"builtin ID collision", "reserved", func(c *ContractIR) { c.GraphQLTypes[0].Name = "ID" }},
		{"empty object", "field", func(c *ContractIR) { c.GraphQLTypes[0].Fields = nil }},
		{"dangling reference", "unknown", func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.Element.Name = "Missing" }},
		{"direct recursion", "recursive", func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.Element.Name = "HomeFeedPage" }},
		{"mutual recursion", "recursive", func(c *ContractIR) {
			c.GraphQLTypes[1].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "HomeFeedPage"}
		}},
		{"unbounded list", "maximum", func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.MaxItems = 0 }},
		{"stray maximum", "maximum", func(c *ContractIR) { c.GraphQLTypes[1].Fields[0].Type.MaxItems = 2 }},
		{"output in input", "input", func(c *ContractIR) {
			c.GraphQLTypes[2].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "HomeFeedPage"}
		}},
		{"input in output", "output", func(c *ContractIR) {
			c.GraphQLTypes[1].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeInputObject, Name: "FeedInput"}
		}},
		{"unknown root reference", "unknown", func(c *ContractIR) { c.CustomOperations[0].Result.Name = "Missing" }},
		{"input root result", "output", func(c *ContractIR) {
			c.CustomOperations[0].Result = GraphQLTypeIR{Kind: GraphQLTypeInputObject, Name: "FeedInput"}
		}},
		{"output argument", "input", func(c *ContractIR) {
			c.CustomOperations[0].Arguments[0].Type = GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "HomeFeedPage"}
		}},
		{"kind mismatch", "kind", func(c *ContractIR) { c.CustomOperations[0].Result.Kind = GraphQLTypeInputObject }},
		{"unknown enum", "unknown", func(c *ContractIR) {
			c.GraphQLTypes[1].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeEnum, Name: "Missing"}
		}},
		{"unknown model", "unknown", func(c *ContractIR) {
			c.GraphQLTypes[1].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeModel, Name: "Missing"}
		}},
		{"hidden model", "hidden", func(c *ContractIR) {
			c.Models = []ModelContractIR{{GraphQLName: "Media", Exposed: false}}
			c.GraphQLTypes[1].Fields[0].Type = GraphQLTypeIR{Kind: GraphQLTypeModel, Name: "Media"}
		}},
		{"malformed list", "element", func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.Element = nil }},
		{"malformed leaf", "element", func(c *ContractIR) {
			c.GraphQLTypes[1].Fields[0].Type.Element = &GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int"}
		}},
		{"unexported Go field", "Go field", func(c *ContractIR) { c.GraphQLTypes[1].Fields[0].GoName = "kind" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := objectContractFixture()
			tc.mutate(&c)
			_, err := CanonicalContract(c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want %q", err, tc.want)
			}
		})
	}
}

func TestEmptyGraphQLDefinitionsDoNotChangeExistingContractEncoding(t *testing.T) {
	c := ContractIR{FormatVersion: ContractFormatVersion}
	first, err := CanonicalContract(c)
	if err != nil {
		t.Fatal(err)
	}
	c.GraphQLTypes = []GraphQLTypeDefinitionIR{}
	second, err := CanonicalContract(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || bytes.Contains(first, []byte("graphqlTypes")) {
		t.Fatalf("empty definitions changed encoding: %s / %s", first, second)
	}
}

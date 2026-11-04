package clientmanifest

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const shapeSDL = `
type Post {
  id: ID!
}
type Query {
  post(id: ID!): Post
  posts: [Post!]!
  aggregatePost: Int!
  groupByPost: [Int!]!
  relationGroupByPost: [Int!]!
  featured: Post
  trending: [Post!]!
  postCount: Int!
  postIds: [Int!]!
}
type Mutation {
  createPost: Post!
  updatePost: Post!
  upsertPost: Post!
  deletePost: Post!
  updateManyPost: Int!
  deleteManyPost: Int!
}
`

func shapeCompilation() ir.CompilationIR {
	model := ir.ModelDeclIR{ID: "post"}
	contract := ir.ModelContractIR{
		ModelID:     "post",
		GraphQLName: "Post",
		Exposed:     true,
		Roots: ir.GraphQLRootNamesIR{
			FindOne:         "post",
			FindMany:        "posts",
			Create:          "createPost",
			Update:          "updatePost",
			Upsert:          "upsertPost",
			Delete:          "deletePost",
			UpdateMany:      "updateManyPost",
			DeleteMany:      "deleteManyPost",
			Aggregate:       "aggregatePost",
			GroupBy:         "groupByPost",
			RelationGroupBy: "relationGroupByPost",
		},
		Fields:    []ir.FieldContractIR{{FieldID: "id", GraphQLName: "id"}},
		Selectors: []ir.SelectorContractIR{{Kind: ir.KeyPrimary, Name: "id", Fields: []ir.FieldID{"id"}}},
	}
	postType := ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post"}
	intType := ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int"}
	var compilation ir.CompilationIR
	compilation.Model.Models = []ir.ModelDeclIR{model}
	compilation.Contract.Models = []ir.ModelContractIR{contract}
	compilation.Contract.CustomOperations = []ir.CustomOperationContractIR{
		{Name: "featured", Result: postType},
		{Name: "trending", Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Element: &postType}},
		{Name: "postCount", Result: intType},
		{Name: "postIds", Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeList, Element: &intType}},
	}
	return compilation
}

func TestRootShapes(t *testing.T) {
	manifest, err := Build(shapeCompilation(), "contract", shapeSDL)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	expected := map[string]string{
		"post":                ShapeRow,
		"createPost":          ShapeRow,
		"updatePost":          ShapeRow,
		"upsertPost":          ShapeRow,
		"deletePost":          ShapeRow,
		"posts":               ShapeRows,
		"aggregatePost":       ShapeAggregate,
		"groupByPost":         ShapeGroupBy,
		"relationGroupByPost": ShapeGroupBy,
		"updateManyPost":      ShapeUpdateMany,
		"deleteManyPost":      ShapeDeleteMany,
		"featured":            ShapeRow,
		"trending":            ShapeRows,
		"postCount":           ShapeValue,
		"postIds":             ShapeValue,
	}
	if len(manifest.Roots) != len(expected) {
		t.Fatalf("roots = %d, want %d", len(manifest.Roots), len(expected))
	}
	for name, want := range expected {
		if got := manifest.Roots[name].Shape; got != want {
			t.Errorf("root %s shape = %q, want %q", name, got, want)
		}
	}
}

func TestShapeValuesAreTheClientVocabulary(t *testing.T) {
	want := map[string]string{
		ShapeRows: "rows", ShapeRow: "row", ShapeAggregate: "aggregate", ShapeGroupBy: "groupBy",
		ShapeUpdateMany: "updateMany", ShapeDeleteMany: "deleteMany", ShapeValue: "value",
	}
	for got, literal := range want {
		if got != literal {
			t.Errorf("shape constant %q, want %q", got, literal)
		}
	}
}

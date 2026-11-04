package clientmanifest

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const shapeSDL = `
type Post {
  id: ID!
}
input PostWhereUniqueInput {
  id: ID
}
type Query {
  post(where: PostWhereUniqueInput!): Post
  posts(cursor: PostWhereUniqueInput): [Post!]!
  aggregatePost: Int!
  groupByPost: [Int!]!
  relationGroupByPost: [Int!]!
  featured: Post
  pinned: Post!
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
		{Name: "featured", Result: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: "Post", Nullable: true}},
		{Name: "pinned", Result: postType},
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
		"deletePost":          ShapeDeletedRow,
		"posts":               ShapeRows,
		"aggregatePost":       ShapeAggregate,
		"groupByPost":         ShapeGroupBy,
		"relationGroupByPost": ShapeGroupBy,
		"updateManyPost":      ShapeUpdateMany,
		"deleteManyPost":      ShapeDeleteMany,
		"featured":            ShapeRow,
		"pinned":              ShapeRow,
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

func TestUniqueInputIsReadFromTheGeneratedSchema(t *testing.T) {
	manifest, err := Build(shapeCompilation(), "contract", shapeSDL)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := manifest.Models["Post"].UniqueInput
	if got == nil || *got != "PostWhereUniqueInput" {
		t.Fatalf("uniqueInput = %v, want PostWhereUniqueInput", got)
	}
}

func TestUniqueInputFollowsTheSchemaNotAConvention(t *testing.T) {
	sdl := strings.ReplaceAll(shapeSDL, "PostWhereUniqueInput", "PostKey")
	manifest, err := Build(shapeCompilation(), "contract", sdl)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := manifest.Models["Post"].UniqueInput
	if got == nil || *got != "PostKey" {
		t.Fatalf("uniqueInput = %v, want PostKey", got)
	}
}

func TestUniqueInputIsNullWithoutALookupRoot(t *testing.T) {
	manifest, err := Build(relationCompilation(), "contract", relationSDL)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := manifest.Models["Author"].UniqueInput; got != nil {
		t.Fatalf("uniqueInput = %v, want null", *got)
	}
}

func TestShapeValuesAreTheClientVocabulary(t *testing.T) {
	want := map[string]string{
		ShapeRows: "rows", ShapeRow: "row", ShapeDeletedRow: "deletedRow", ShapeAggregate: "aggregate", ShapeGroupBy: "groupBy",
		ShapeUpdateMany: "updateMany", ShapeDeleteMany: "deleteMany", ShapeValue: "value",
	}
	for got, literal := range want {
		if got != literal {
			t.Errorf("shape constant %q, want %q", got, literal)
		}
	}
}

func TestRootNullability(t *testing.T) {
	manifest, err := Build(shapeCompilation(), "contract", shapeSDL)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	expected := map[string]bool{
		"post":       true,
		"createPost": false,
		"updatePost": false,
		"deletePost": false,
		"posts":      false,
		"featured":   true,
		"pinned":     false,
		"trending":   false,
	}
	for name, want := range expected {
		if got := manifest.Roots[name].Nullable; got != want {
			t.Errorf("root %s nullable = %v, want %v", name, got, want)
		}
	}
}

const relationSDL = `
type Author {
  id: ID!
}
type Post {
  id: ID!
  author: Author
  editor: Author!
  reviewers: [Author!]!
}
type Query {
  post(id: ID!): Post
}
`

func relationCompilation() ir.CompilationIR {
	scalar := func(id ir.FieldID) ir.FieldIR { return ir.FieldIR{ID: id, Kind: ir.FieldScalar} }
	relation := func(id ir.FieldID, relationID ir.RelationID, kind ir.RelationKind) ir.FieldIR {
		return ir.FieldIR{ID: id, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relationID, Role: ir.RelationSource, Kind: kind}}
	}
	selector := func(id ir.FieldID) []ir.SelectorContractIR {
		return []ir.SelectorContractIR{{Kind: ir.KeyPrimary, Name: "id", Fields: []ir.FieldID{id}}}
	}
	var compilation ir.CompilationIR
	compilation.Model.Models = []ir.ModelDeclIR{
		{ID: "post", Fields: []ir.FieldIR{
			scalar("post.id"),
			relation("post.author", "author", ir.RelationBelongsTo),
			relation("post.editor", "editor", ir.RelationBelongsTo),
			relation("post.reviewers", "reviewers", ir.RelationHasMany),
		}},
		{ID: "author", Fields: []ir.FieldIR{scalar("author.id")}},
	}
	compilation.Model.Relations = []ir.RelationIR{
		{ID: "author", SourceModel: "post", TargetModel: "author", SourceField: "post.author"},
		{ID: "editor", SourceModel: "post", TargetModel: "author", SourceField: "post.editor"},
		{ID: "reviewers", SourceModel: "post", TargetModel: "author", SourceField: "post.reviewers"},
	}
	compilation.Contract.Models = []ir.ModelContractIR{
		{
			ModelID: "post", GraphQLName: "Post", Exposed: true,
			Roots: ir.GraphQLRootNamesIR{FindOne: "post"},
			Fields: []ir.FieldContractIR{
				{FieldID: "post.id", GraphQLName: "id"},
				{FieldID: "post.author", GraphQLName: "author"},
				{FieldID: "post.editor", GraphQLName: "editor"},
				{FieldID: "post.reviewers", GraphQLName: "reviewers"},
			},
			Selectors: selector("post.id"),
		},
		{
			ModelID: "author", GraphQLName: "Author", Exposed: true,
			Fields:    []ir.FieldContractIR{{FieldID: "author.id", GraphQLName: "id"}},
			Selectors: selector("author.id"),
		},
	}
	return compilation
}

func TestRelationNullability(t *testing.T) {
	manifest, err := Build(relationCompilation(), "contract", relationSDL)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	expected := map[string]bool{"author": true, "editor": false, "reviewers": false}
	relations := manifest.Models["Post"].Relations
	if len(relations) != len(expected) {
		t.Fatalf("relations = %d, want %d", len(relations), len(expected))
	}
	for name, want := range expected {
		if got := relations[name].Nullable; got != want {
			t.Errorf("relation %s nullable = %v, want %v", name, got, want)
		}
	}
}
